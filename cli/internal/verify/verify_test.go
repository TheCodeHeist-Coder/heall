package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heall/internal/config"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/gitx/gitxtest"
	"heall/internal/guard"
	"heall/internal/sandbox"
)

// A tiny test runner that prints TAP, so the suite needs neither Node nor
// Docker. "always broken" fails on every commit: a failure that was there
// before any patch and must not be blamed on one.
const runner = `only=$1
fail=0
n=0
check() {
  name=$1; shift
  n=$((n + 1))
  if [ -n "$only" ] && [ "$only" != "$name" ]; then return; fi
  if "$@"; then echo "ok $n - $name"; else echo "not ok $n - $name"; fail=1; fi
}
v=$(cat src/value)
check "value is small" test "$v" -lt 10
check "value is even" test $((v % 2)) -eq 0
check "always broken" false
touch test-leftover
exit $fail
`

type fixture struct {
	t   *testing.T
	fx  *gitxtest.Repo
	v   Verifier
	bad string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	fx := gitxtest.New(t)
	fx.Write("test/run.sh", runner)
	fx.Write("src/value", "12\n")
	fx.Write("README.md", "docs\n")
	fx.Write(".gitignore", "node_modules/\n")
	bad := fx.Commit("value is 12")

	repo, err := gitx.Open(context.Background(), fx.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		BuildCmd:   []string{"sh", "-c", "! test -e src/broken"},
		TestCmd:    []string{"sh", "test/run.sh"},
		TestOneCmd: []string{"sh", "test/run.sh", "{{test}}"},
		Allow:      []string{"src/**"},
		Protect:    []string{"test/**"},
	}
	return &fixture{t: t, fx: fx, bad: bad,
		v: Verifier{Repo: repo, Cfg: cfg, Runner: &sandbox.Local{Timeout: 20 * time.Second}}}
}

func (f *fixture) verify(runID, patch string) Result {
	f.t.Helper()
	res, err := f.v.Run(context.Background(), Request{RunID: runID, Bad: f.bad, Test: "value is small", MaxAttempts: 3}, patch)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func set(value string) string {
	return "--- a/src/value\n+++ b/src/value\n@@ -1 +1 @@\n-12\n+" + value + "\n"
}

func failedChecks(res Result) string {
	var names []string
	for _, c := range res.Guardrails {
		if !c.Passed {
			names = append(names, c.Name)
		}
	}
	return strings.Join(names, ",")
}

func TestVerifiedFix(t *testing.T) {
	f := setup(t)
	res := f.verify("r1", set("4"))
	if res.Status != Verified || !res.TargetPassed || !res.SuitePassed || len(res.NewFailures) != 0 {
		t.Fatalf("status=%s target=%v suite=%v new=%v\n%s", res.Status, res.TargetPassed, res.SuitePassed, res.NewFailures, res.Output)
	}
	if res.Attempt != 1 || failedChecks(res) != "" || len(res.Guardrails) != 7 {
		t.Errorf("attempt=%d failed=%q checks=%d, want attempt 1 and 7 passing checks", res.Attempt, failedChecks(res), len(res.Guardrails))
	}
	if !strings.Contains(res.Patch, "+4") || !strings.Contains(res.Patch, "diff --git a/src/value b/src/value") {
		t.Errorf("Patch should be the change as git sees it:\n%s", res.Patch)
	}
	if strings.Contains(res.Patch, "test-leftover") {
		t.Error("files written by the tests leaked into the patch")
	}
	// The user's checkout and branch are never touched.
	if f.fx.Git("status", "--porcelain") != "" || f.fx.Git("rev-parse", "HEAD") != f.bad {
		t.Error("verifying changed the user's checkout")
	}
	if list := f.fx.Git("worktree", "list"); strings.Count(list, "\n") != 0 {
		t.Errorf("worktree left behind:\n%s", list)
	}
}

func TestFixThatBreaksAnotherTest(t *testing.T) {
	f := setup(t)
	res := f.verify("r1", set("5"))
	if res.Status != Failed || !res.TargetPassed || res.SuitePassed {
		t.Fatalf("status=%s target=%v suite=%v, want failed with the target passing", res.Status, res.TargetPassed, res.SuitePassed)
	}
	// "always broken" failed before the patch too, so it is not the patch's
	// fault; "value is even" is.
	if len(res.NewFailures) != 1 || res.NewFailures[0] != "value is even" {
		t.Errorf("new failures = %v, want only \"value is even\"", res.NewFailures)
	}
	if !strings.Contains(res.Output, "value is even") || strings.Contains(res.Output, "always broken") {
		t.Errorf("output should describe the new failure only:\n%s", res.Output)
	}
}

func TestPatchThatDoesNotFix(t *testing.T) {
	f := setup(t)
	res := f.verify("r1", set("20"))
	if res.Status != Failed || res.TargetPassed || !strings.Contains(res.Output, "still fails") {
		t.Errorf("status=%s target=%v output=%q", res.Status, res.TargetPassed, res.Output)
	}
}

func TestPatchThatBreaksTheBuild(t *testing.T) {
	f := setup(t)
	res := f.verify("r1", set("4")+"--- /dev/null\n+++ b/src/broken\n@@ -0,0 +1 @@\n+x\n")
	if res.Status != Failed || !strings.Contains(res.Output, "no longer builds") {
		t.Errorf("status=%s output=%q", res.Status, res.Output)
	}
}

func TestPatchThatDoesNotApply(t *testing.T) {
	f := setup(t)
	res := f.verify("r1", "--- a/src/value\n+++ b/src/value\n@@ -1 +1 @@\n-999\n+4\n")
	if res.Status != Failed || !strings.Contains(res.Output, "does not apply") || res.Patch != "" {
		t.Errorf("status=%s output=%q", res.Status, res.Output)
	}
}

func TestGuardrailsRejectBeforeRunningAnything(t *testing.T) {
	f := setup(t)
	// A marker the test runner would leave if the patched code were run.
	cheat := "--- a/test/run.sh\n+++ b/test/run.sh\n@@ -1 +1,2 @@\n only=$1\n+touch " + filepath.Join(f.fx.Dir, "ran-the-cheat") + "; exit 0\n"
	cases := map[string]struct{ patch, check string }{
		"edits the test":        {cheat, guard.NameProtected},
		"edits outside allow":   {"--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-docs\n+new\n", guard.NameAllowlist},
		"fix plus a test edit":  {set("4") + cheat, guard.NameProtected},
		"not a patch":           {"I changed the value to 4.", guard.NameWellFormed},
		"escapes the checkout":  {"--- a/../x\n+++ b/../x\n@@ -1 +1 @@\n-a\n+b\n", guard.NameWellFormed},
		"writes an ignored dir": {set("4") + "--- /dev/null\n+++ b/node_modules/x.js\n@@ -0,0 +1 @@\n+x\n", guard.NameAllowlist},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := f.verify("r-"+strings.ReplaceAll(name, " ", "-"), tc.patch)
			if res.Status != Rejected || failedChecks(res) != tc.check {
				t.Errorf("status=%s failed=%q, want rejected by %s", res.Status, failedChecks(res), tc.check)
			}
			if res.TargetPassed || res.SuitePassed || !strings.Contains(res.Output, tc.check) {
				t.Errorf("target=%v suite=%v output=%q", res.TargetPassed, res.SuitePassed, res.Output)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(f.fx.Dir, "ran-the-cheat")); err == nil {
		t.Error("a rejected patch was executed")
	}
}

func TestAttemptLimitIsKeptByTheCLI(t *testing.T) {
	f := setup(t)
	for want := 1; want <= 3; want++ {
		if res := f.verify("r1", set("20")); res.Attempt != want || res.Status != Failed {
			t.Fatalf("attempt %d: got attempt=%d status=%s", want, res.Attempt, res.Status)
		}
	}
	// A fourth patch is refused even though it is the right one.
	res := f.verify("r1", set("4"))
	if res.Status != Rejected || failedChecks(res) != guard.NameAttempts || res.Attempt != 4 {
		t.Errorf("status=%s failed=%q attempt=%d, want rejected by the attempt limit", res.Status, failedChecks(res), res.Attempt)
	}
	// Another run has its own count.
	if res := f.verify("r2", set("4")); res.Status != Verified || res.Attempt != 1 {
		t.Errorf("a new run: status=%s attempt=%d", res.Status, res.Attempt)
	}
	// heall's own final check is not an attempt and is never refused.
	final, err := f.v.Run(context.Background(), Request{RunID: "r1", Bad: f.bad, Test: "value is small", MaxAttempts: 3, Final: true}, set("4"))
	if err != nil || final.Status != Verified || final.Attempt != 0 {
		t.Errorf("final check: status=%s attempt=%d err=%v", final.Status, final.Attempt, err)
	}
	if _, err := f.v.Run(context.Background(), Request{RunID: "../x", Bad: f.bad, Test: "t", MaxAttempts: 3}, set("4")); err == nil {
		t.Error("a run id that is a path was accepted")
	}
}

func TestBaselineIsCachedInsideGit(t *testing.T) {
	f := setup(t)
	f.verify("r1", set("4"))
	state, err := f.v.Repo.StateDir(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cached, _ := filepath.Glob(filepath.Join(state, "baseline", f.bad+"-*.json"))
	if len(cached) != 1 {
		t.Fatalf("baseline files: %v", cached)
	}
	raw, _ := os.ReadFile(cached[0])
	if !strings.Contains(string(raw), "always broken") || !strings.Contains(string(raw), "value is small") || strings.Contains(string(raw), "value is even") {
		t.Errorf("baseline should hold the two tests failing before the patch: %s", raw)
	}
	// A cached baseline is used as it is: plant one that blames nothing new.
	if err := os.WriteFile(cached[0], []byte(`{"failures":{"value is even":true,"always broken":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := f.verify("r2", set("5")); res.Status != Verified {
		t.Errorf("status=%s: the cached baseline was not used", res.Status)
	}
	if f.fx.Git("status", "--porcelain") != "" {
		t.Error("heall's state shows up as a change in the repository")
	}
}

func TestRunTest(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	one, err := f.v.RunTest(ctx, f.bad, "value is small")
	if err != nil {
		t.Fatal(err)
	}
	if one.Verdict != events.Fail || one.ExitCode != 1 || !strings.Contains(one.Output, "not ok 1 - value is small") || strings.Contains(one.Output, "always broken") {
		t.Errorf("single test: %+v", one)
	}
	pass, err := f.v.RunTest(ctx, f.bad, "value is even")
	if err != nil || pass.Verdict != events.Pass {
		t.Errorf("passing test: %+v, %v", pass, err)
	}
	suite, err := f.v.RunTest(ctx, f.bad, "")
	if err != nil || suite.Verdict != events.Fail || !strings.Contains(suite.Output, "always broken") {
		t.Errorf("suite: %+v, %v", suite, err)
	}
}
