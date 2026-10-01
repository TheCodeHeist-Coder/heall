package guard

import (
	"regexp"
	"strings"
	"testing"

	"heall/internal/config"
	"heall/internal/events"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"src/**", "src/a.js", true},
		{"src/**", "src/deep/er/a.js", true},
		{"src/**", "src", false},
		{"src/**", "srcs/a.js", false},
		{"src/**", "lib/src/a.js", false},
		{"**/*.test.js", "a.test.js", true},
		{"**/*.test.js", "test/unit/a.test.js", true},
		{"**/*.test.js", "test/a.test.jsx", false},
		{"test/**", "test/a.js", true},
		{"test/**", "tests/a.js", false},
		{"package.json", "package.json", true},
		{"package.json", "sub/package.json", false},
		{"src/*.js", "src/a.js", true},
		{"src/*.js", "src/sub/a.js", false},
		{"src/?.js", "src/a.js", true},
		{"src/?.js", "src/ab.js", false},
		{".heall.yaml", "xheall.yaml", false},
		{"src/**/gen/*.js", "src/gen/a.js", true},
		{"src/**/gen/*.js", "src/x/y/gen/a.js", true},
	}
	for _, tc := range cases {
		if got := Match(tc.pattern, tc.path); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

const gitPatch = `diff --git a/src/a.js b/src/a.js
index 1111111..2222222 100644
--- a/src/a.js
+++ b/src/a.js
@@ -1,3 +1,3 @@
 keep
-old line
+new line
 keep
diff --git a/src/new.js b/src/new.js
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/src/new.js
@@ -0,0 +1,2 @@
+first
+second
diff --git a/src/gone.js b/src/gone.js
deleted file mode 100644
index 4444444..0000000
--- a/src/gone.js
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/src/old name.js b/src/new name.js
similarity index 100%
rename from src/old name.js
rename to src/new name.js
`

func TestParsePatch(t *testing.T) {
	files, err := ParsePatch(gitPatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("got %d files, want 4: %+v", len(files), files)
	}
	if f := files[0]; f.Path != "src/a.js" || f.OldPath != "" || f.Added || f.Deleted || strings.Join(f.AddedLines, "|") != "new line" {
		t.Errorf("modified file: %+v", f)
	}
	if f := files[1]; f.Path != "src/new.js" || !f.Added || f.Mode != "100644" || len(f.AddedLines) != 2 {
		t.Errorf("added file: %+v", f)
	}
	if f := files[2]; f.Path != "src/gone.js" || !f.Deleted {
		t.Errorf("deleted file: %+v", f)
	}
	if f := files[3]; f.Path != "src/new name.js" || f.OldPath != "src/old name.js" || len(f.Paths()) != 2 {
		t.Errorf("renamed file: %+v", f)
	}
}

func TestParsePlainUnifiedDiff(t *testing.T) {
	// No "diff --git" line, wrong line counts, and a removed line that looks
	// like a file header: all things a model produces.
	files, err := ParsePatch("--- a/src/a.sql\n+++ b/src/a.sql\n@@ -1,9 +1,9 @@\n--- an old comment\n+-- a new comment\n select 1;\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "src/a.sql" || strings.Join(files[0].AddedLines, "|") != "-- a new comment" {
		t.Errorf("got %+v", files)
	}
}

func TestNormalizeSplitsPlainMultiFileDiffs(t *testing.T) {
	plain := "--- a/src/a.js\n+++ b/src/a.js\n@@ -1 +1 @@\n-a\n+b\n--- /dev/null\n+++ b/test/new.test.js\n@@ -0,0 +1 @@\n+x\n--- a/src/gone.js\n+++ /dev/null\n@@ -1 +0,0 @@\n-y\n"
	norm := Normalize(plain)
	for _, want := range []string{
		"diff --git a/src/a.js b/src/a.js\n--- a/src/a.js",
		"diff --git a/test/new.test.js b/test/new.test.js\nnew file mode 100644\n--- /dev/null",
		"diff --git a/src/gone.js b/src/gone.js\ndeleted file mode 100644\n--- a/src/gone.js",
	} {
		if !strings.Contains(norm, want) {
			t.Errorf("normalized patch lacks %q:\n%s", want, norm)
		}
	}
	files, err := ParsePatch(plain)
	if err != nil || len(files) != 3 {
		t.Fatalf("got %d files, %v; the second and third file must not hide inside the first one's hunk", len(files), err)
	}
	if files[1].Path != "test/new.test.js" || !files[1].Added || files[2].Path != "src/gone.js" || !files[2].Deleted {
		t.Errorf("files: %+v", files)
	}
	// A patch that already has its "diff" lines is left alone.
	if Normalize(gitPatch) != gitPatch || Normalize(norm) != norm {
		t.Error("Normalize changed a patch that was already normalized")
	}
}

func TestParsePatchRejectsWhatItCannotVouchFor(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"prose only":        "Here is the fix you asked for.\n",
		"hunk without file": "@@ -1 +1 @@\n-a\n+b\n",
		"junk in a hunk":    "--- a/src/a.js\n+++ b/src/a.js\n@@ -1 +1 @@\n-a\n+b\nand then some prose\n",
		"parent directory":  "--- a/../outside.js\n+++ b/../outside.js\n@@ -1 +1 @@\n-a\n+b\n",
		"hidden traversal":  "--- a/src/../../outside.js\n+++ b/src/../../outside.js\n@@ -1 +1 @@\n-a\n+b\n",
		"absolute path":     "--- /etc/passwd\n+++ /etc/passwd\n@@ -1 +1 @@\n-a\n+b\n",
		"git directory":     "--- a/.git/hooks/pre-commit\n+++ b/.git/hooks/pre-commit\n@@ -1 +1 @@\n-a\n+b\n",
		"quoted path":       "--- \"a/src/\\303\\251.js\"\n+++ \"b/src/\\303\\251.js\"\n@@ -1 +1 @@\n-a\n+b\n",
	}
	for name, patch := range cases {
		if files, err := ParsePatch(patch); err == nil {
			t.Errorf("%s: accepted as %+v", name, files)
		}
	}
}

var rules = Rules{
	Allow:       []string{"src/**"},
	Protect:     []string{".git/**", "test/**", "**/*.test.js", "package.json"},
	ForbidAdded: []*regexp.Regexp{regexp.MustCompile(`NODE_TEST_CONTEXT`), regexp.MustCompile(`process\.exit\(`)},
}

func edit(path string) string {
	return "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1 @@\n-a\n+b\n"
}

func failed(checks []events.GuardrailCheck) string {
	var names []string
	for _, c := range checks {
		if !c.Passed {
			names = append(names, c.Name)
		}
	}
	return strings.Join(names, ",")
}

func TestCheckPatch(t *testing.T) {
	cases := []struct {
		name, patch, want string
	}{
		{"source edit", edit("src/paginate.js"), ""},
		{"test edit", edit("test/paginate.test.js"), NameProtected},
		{"test file kept beside the source", edit("src/paginate.test.js"), NameProtected},
		{"config that picks the test command", edit("package.json"), NameProtected},
		{"file outside the allowlist", edit("README.md"), NameAllowlist},
		{"good and bad file together", edit("src/a.js") + edit("test/a.test.js"), NameProtected},
		{"deleting a test",
			"diff --git a/test/a.test.js b/test/a.test.js\ndeleted file mode 100644\n--- a/test/a.test.js\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n",
			NameProtected},
		{"renaming a test out of the way",
			"diff --git a/test/a.test.js b/src/moved.js\nrename from test/a.test.js\nrename to src/moved.js\n",
			NameProtected},
		{"renaming a source file over a test",
			"diff --git a/src/a.js b/test/a.test.js\nrename from src/a.js\nrename to test/a.test.js\n",
			NameProtected},
		{"symbolic link",
			"diff --git a/src/link.js b/src/link.js\nnew file mode 120000\n--- /dev/null\n+++ b/src/link.js\n@@ -0,0 +1 @@\n+../test/a.test.js\n",
			NameFileTypes},
		{"binary file",
			"diff --git a/src/blob.bin b/src/blob.bin\nnew file mode 100644\nBinary files /dev/null and b/src/blob.bin differ\n",
			NameFileTypes},
		{"detects the test runner",
			"--- a/src/a.js\n+++ b/src/a.js\n@@ -1 +1,2 @@\n a\n+if (process.env.NODE_TEST_CONTEXT) return 6;\n",
			NameForbidden},
		{"unreadable", "not a patch", NameWellFormed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checks := CheckPatch(tc.patch, rules)
			if got := failed(checks); got != tc.want {
				t.Errorf("failed checks = %q, want %q\n%+v", got, tc.want, checks)
			}
			if Passed(checks) != (tc.want == "") {
				t.Errorf("Passed = %v", Passed(checks))
			}
			for _, c := range checks {
				if !c.Passed && c.Detail == "" {
					t.Errorf("%s failed without saying why", c.Name)
				}
			}
			// Every check is reported unless the patch could not be read.
			if tc.want != NameWellFormed && len(checks) != 5 {
				t.Errorf("got %d checks, want all 5", len(checks))
			}
		})
	}
}

func TestForbiddenCodeOnlyLooksAtAddedLines(t *testing.T) {
	// Removing or keeping such a line is fine; only adding one is not.
	patch := "--- a/src/a.js\n+++ b/src/a.js\n@@ -1,2 +1,2 @@\n process.exit(1);\n-process.exit(2);\n+return;\n"
	if got := failed(CheckPatch(patch, rules)); got != "" {
		t.Errorf("failed %q on a patch that adds no forbidden line", got)
	}
}

func TestCheckApplied(t *testing.T) {
	if c := CheckApplied([]string{"src/a.js", "src/deep/b.js"}, rules); !c.Passed {
		t.Errorf("allowed files rejected: %s", c.Detail)
	}
	for _, changed := range [][]string{
		{"src/a.js", "test/a.test.js"},
		{"node_modules/lib/index.js"},
		{".git/config"},
	} {
		c := CheckApplied(changed, rules)
		if c.Passed || c.Name != NameApplied || !strings.Contains(c.Detail, changed[len(changed)-1]) {
			t.Errorf("%v: passed=%v detail=%q", changed, c.Passed, c.Detail)
		}
	}
}

func TestFromConfigAlwaysProtectsGit(t *testing.T) {
	r, err := FromConfig(config.Config{Allow: []string{"**"}, ForbidAdded: []string{`\beval\(`}})
	if err != nil {
		t.Fatal(err)
	}
	// Even a config that allows everything cannot open up .git; the parser
	// refuses such paths first, and the rules refuse them again after apply.
	if c := CheckApplied([]string{".git/hooks/pre-commit"}, r); c.Passed {
		t.Error("a change inside .git was allowed")
	}
	if len(r.ForbidAdded) != 1 {
		t.Errorf("forbid_added not compiled: %v", r.ForbidAdded)
	}
	if _, err := FromConfig(config.Config{ForbidAdded: []string{"("}}); err == nil {
		t.Error("an invalid pattern was accepted")
	}
}
