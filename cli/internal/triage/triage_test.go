package triage

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// files is a Source backed by a map of path to content.
type files map[string]string

func (f files) Exists(path string) bool { _, ok := f[path]; return ok }
func (f files) Read(path string) (string, error) {
	if c, ok := f[path]; ok {
		return c, nil
	}
	return "", os.ErrNotExist
}

func TestParseTAP(t *testing.T) {
	got := Parse(fixture(t, "offbyone.tap"))
	if len(got) != 1 {
		t.Fatalf("found %d failures, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Test != "paginate returns a full page" || f.Line != 11 || f.ErrorName != "AssertionError" {
		t.Errorf("got test=%q line=%d error=%q", f.Test, f.Line, f.ErrorName)
	}
	if !strings.HasSuffix(f.File, "/test/paginate.test.js") {
		t.Errorf("file = %q", f.File)
	}
	if f.Message != "Expected values to be strictly deep-equal:" {
		t.Errorf("message = %q", f.Message)
	}
	for _, want := range []string{"-   6", "paginate.test.js:12:10"} {
		if !strings.Contains(f.Excerpt, want) {
			t.Errorf("excerpt lacks %q:\n%s", want, f.Excerpt)
		}
	}
	if strings.Contains(f.Excerpt, "node:internal") {
		t.Errorf("excerpt keeps runtime frames:\n%s", f.Excerpt)
	}
	if len(f.StackFiles) != 1 || !strings.HasSuffix(f.StackFiles[0], "/test/paginate.test.js") {
		t.Errorf("stack files = %v, want only the test file", f.StackFiles)
	}
}

// Nested suites, a thrown error, a skipped test, a TODO test and a test file
// that fails to load, in each format the Node runner prints.
func TestParseEdgeCases(t *testing.T) {
	for _, name := range []string{"nested.tap", "nested-isolated.tap", "nested.spec"} {
		t.Run(name, func(t *testing.T) {
			got := Parse(fixture(t, name))
			if len(got) != 2 {
				t.Fatalf("found %d failures, want 2 (the suite, skip and TODO are not failures): %+v", len(got), got)
			}
			load, thrown := got[0], got[1]

			if load.Test != "test/broken.test.js" || !strings.HasSuffix(load.File, "test/broken.test.js") || load.Line != 1 {
				t.Errorf("load failure: %+v", load)
			}
			if load.Message == "" || load.Message == "test failed" {
				t.Errorf("load failure has no useful message: %q", load.Message)
			}
			// TAP carries the real error; the spec report prints it far away.
			if strings.HasSuffix(name, ".tap") && (load.ErrorName != "SyntaxError" || !strings.Contains(load.Message, "does not provide an export named 'nope'")) {
				t.Errorf("load failure: name=%q message=%q, want the SyntaxError", load.ErrorName, load.Message)
			}

			if thrown.Test != "handles zero: 'quoted' (parens)" {
				t.Errorf("test name = %q", thrown.Test)
			}
			if thrown.Line != 6 || thrown.ErrorName != "RangeError" || thrown.Message != "division by zero" {
				t.Errorf("thrown error: line=%d name=%q message=%q", thrown.Line, thrown.ErrorName, thrown.Message)
			}
			if len(thrown.StackFiles) != 2 || !strings.HasSuffix(thrown.StackFiles[0], "/src/calc.js") {
				t.Errorf("stack files = %v, want src/calc.js then the test file", thrown.StackFiles)
			}
			// Only TAP says which suite a test is in.
			if strings.HasSuffix(name, ".tap") && !reflect.DeepEqual(thrown.Suite, []string{"div"}) {
				t.Errorf("suite = %v, want [div]", thrown.Suite)
			}
		})
	}
}

func TestParseGitHubActionsLog(t *testing.T) {
	log := fixture(t, "offbyone.actions.log")
	if !strings.HasPrefix(log, "test\tRun node --test\t2026-") {
		t.Fatal("fixture should carry the job, step and timestamp prefix of `gh run view --log`")
	}
	got := Parse(log)
	if len(got) != 1 || got[0].Test != "paginate returns a full page" || got[0].File != "test/paginate.test.js" || got[0].Line != 11 {
		t.Fatalf("got %+v", got)
	}
	if got[0].ErrorName != "AssertionError" || !strings.Contains(got[0].Excerpt, "-   6") {
		t.Errorf("error=%q excerpt:\n%s", got[0].ErrorName, got[0].Excerpt)
	}

	// The web UI's raw log has the timestamp only, and colour codes.
	plain := "2026-10-01T10:00:00.1234567Z \x1b[31mnot ok 3 - sums\x1b[0m\r\n"
	if got := Parse(plain); len(got) != 1 || got[0].Test != "sums" {
		t.Errorf("timestamp and colour were not stripped: %+v", got)
	}
}

func TestParseFindsNothingInAPassingRun(t *testing.T) {
	for _, log := range []string{fixture(t, "passing.tap"), "", "npm ERR! something else broke\n"} {
		if got := Parse(log); len(got) != 0 {
			t.Errorf("found failures in a log without any: %+v", got)
		}
	}
	if _, err := Analyze("all good", files{}); !errors.Is(err, ErrNoFailure) {
		t.Errorf("Analyze: got %v, want ErrNoFailure", err)
	}
}

func TestAnalyzeResolvesPathsAndSuspects(t *testing.T) {
	repo := files{
		"test/cart.test.js": `import { cartTotal } from "../src/cart.js";
import { formatPrice } from "../src/format.js";
import assert from "node:assert/strict";`,
		"src/cart.js":        `import { formatPrice } from "./format.js";\nimport { tax } from "./tax";`,
		"src/format.js":      `export const formatPrice = () => "";`,
		"src/tax.js":         `const rates = require("./rates/index.js");`,
		"src/rates/index.js": ``,
		"src/unrelated.js":   ``,
	}
	// Paths as a CI runner would print them.
	log := `TAP version 13
not ok 1 - priceLabel uses the shared formatter
  ---
  location: '/home/runner/work/shopkit/shopkit/test/cart.test.js:14:1'
  failureType: 'testCodeFailure'
  error: 'boom'
  name: 'TypeError'
  stack: |-
    priceLabel (file:///home/runner/work/shopkit/shopkit/src/cart.js:12:3)
    TestContext.<anonymous> (file:///home/runner/work/shopkit/shopkit/test/cart.test.js:15:10)
    helper (/home/runner/work/shopkit/shopkit/node_modules/lib/index.js:1:1)
    Test.run (node:internal/test_runner/test:1201:25)
  ...
`
	rep, err := Analyze(log, repo)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Primary.File != "test/cart.test.js" || rep.Primary.Line != 14 {
		t.Errorf("file = %q line = %d, want the repository path", rep.Primary.File, rep.Primary.Line)
	}
	if want := []string{"src/cart.js", "test/cart.test.js"}; !reflect.DeepEqual(rep.Primary.StackFiles, want) {
		t.Errorf("stack files = %v, want %v (files outside the repository dropped)", rep.Primary.StackFiles, want)
	}
	// Stack first, then what the test and the stack files import, then what
	// those import; never the test itself, and never an unrelated file.
	want := []string{"src/cart.js", "src/format.js", "src/tax.js", "src/rates/index.js"}
	if !reflect.DeepEqual(rep.SuspectFiles, want) {
		t.Errorf("suspects = %v, want %v", rep.SuspectFiles, want)
	}
}

func TestAnalyzeWithoutARepository(t *testing.T) {
	rep, err := Analyze(fixture(t, "offbyone.tap"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rep.Primary.File, "/") || rep.SuspectFiles != nil {
		t.Errorf("without a repository paths stay as logged and there are no suspects: %+v", rep)
	}
}
