// Package verify decides whether a patch is a proven fix. It checks the
// patch against the guardrails, applies it to a fresh checkout of the
// failing commit, and runs the tests in a brand-new sandbox. Nothing the
// agent says about its own patch is taken into account.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"heall/internal/agentio"
	"heall/internal/config"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/guard"
	"heall/internal/sandbox"
	"heall/internal/tester"
	"heall/internal/triage"
)

// Statuses of a verification, as in the contract.
const (
	Verified = "verified"
	Rejected = "rejected"
	Failed   = "failed"
)

// maxOutput caps the test output handed back to the agent.
const maxOutput = 8 * 1024

type Verifier struct {
	Repo   *gitx.Repo
	Cfg    config.Config
	Runner sandbox.Runner
}

type Request struct {
	// RunID scopes the attempt counter.
	RunID string
	// Bad is the failing commit the patch is applied to.
	Bad string
	// Test is the name of the test the patch must fix.
	Test string
	// MaxAttempts is how many patches the agent may submit in this run.
	MaxAttempts int
	// Final marks heall's own check before delivery. It is not counted as
	// an attempt and is never refused for the attempt limit.
	Final bool
}

// Result adds to the contract's VerifyResult what only the CLI needs.
type Result struct {
	agentio.VerifyResult
	// Patch is the change as git sees it after applying, set when the patch
	// applied. It is what should be delivered.
	Patch string
}

// Run verifies one patch.
func (v Verifier) Run(ctx context.Context, req Request, patch string) (Result, error) {
	rules, err := guard.FromConfig(v.Cfg)
	if err != nil {
		return Result{}, err
	}
	res := Result{VerifyResult: agentio.VerifyResult{Status: Rejected, NewFailures: []string{}}}
	// Checked and applied in the same form, so heall and git see the same
	// files in it.
	patch = guard.Normalize(patch)

	// The attempt is counted before anything else, so a patch that is
	// rejected outright still uses one up.
	var limit events.GuardrailCheck
	if !req.Final {
		n, err := v.countAttempt(ctx, req.RunID)
		if err != nil {
			return Result{}, err
		}
		res.Attempt = n
		limit = events.GuardrailCheck{Name: guard.NameAttempts, Passed: n <= req.MaxAttempts}
		if !limit.Passed {
			limit.Detail = fmt.Sprintf("this is attempt %d and the limit is %d", n, req.MaxAttempts)
			res.Guardrails = []events.GuardrailCheck{limit}
			res.Output = "No attempts left. Escalate instead of submitting another patch."
			return res, nil
		}
	}

	res.Guardrails = guard.CheckPatch(patch, rules)
	if !req.Final {
		res.Guardrails = append(res.Guardrails, limit)
	}
	if !guard.Passed(res.Guardrails) {
		res.Output = "The patch was rejected before it was applied:\n" + failures(res.Guardrails)
		return res, nil
	}

	dir, remove, err := v.Repo.TempWorktree(ctx, req.Bad)
	if err != nil {
		return Result{}, err
	}
	defer remove()

	// What already fails on the bad commit, so the patch is only blamed for
	// failures it adds.
	baseline, err := v.baseline(ctx, req.Bad, dir)
	if err != nil {
		return Result{}, err
	}

	if err := gitx.ApplyPatch(ctx, dir, patch); err != nil {
		var apply *gitx.ApplyError
		if !errors.As(err, &apply) {
			return Result{}, err
		}
		res.Status = Failed
		res.Output = "The patch does not apply to the failing commit:\n" + apply.Output
		return res, nil
	}

	// The files that really changed are checked again: this does not depend
	// on heall having read the patch the same way git did.
	changed, err := gitx.ChangedFiles(ctx, dir)
	if err != nil {
		return Result{}, err
	}
	applied := guard.CheckApplied(changed, rules)
	res.Guardrails = append(res.Guardrails, applied)
	if !applied.Passed {
		res.Output = "The patch was rejected after it was applied:\n" + failures(res.Guardrails)
		return res, nil
	}
	if res.Patch, err = gitx.WorktreeDiff(ctx, dir); err != nil {
		return Result{}, err
	}

	// From here the patch runs, always in a sandbox of its own.
	res.Status = Failed
	box := sandbox.Fresh(v.Runner, dir)
	t := tester.Tester{Cfg: v.Cfg}

	target, err := t.Check(ctx, box, req.Test)
	if err != nil {
		return Result{}, err
	}
	switch {
	case target.Verdict == events.Skipped && target.Phase == "build":
		res.Output = "With the patch applied the code no longer builds:\n" + tail(target.Result.Output)
		return res, nil
	case target.Verdict == events.Skipped:
		res.Output = "With the patch applied the test timed out."
		return res, nil
	case target.Verdict == events.Fail:
		res.Output = fmt.Sprintf("The test %q still fails with the patch applied:\n%s", req.Test, describe(target.Result.Output, nil))
		return res, nil
	}
	res.TargetPassed = true

	suite, err := box.Run(ctx, v.Cfg.TestCmd)
	if err != nil {
		return Result{}, err
	}
	if suite.TimedOut {
		res.Output = "With the patch applied the full test suite timed out."
		return res, nil
	}
	now := failing(suite)
	for _, name := range now.names() {
		if name == req.Test {
			// It passed alone but fails with the rest of the suite.
			res.TargetPassed = false
		}
		if !baseline.Failures[name] {
			res.NewFailures = append(res.NewFailures, name)
		}
	}
	if !res.TargetPassed {
		res.Output = fmt.Sprintf("The test %q passes on its own but fails when the full suite runs:\n%s",
			req.Test, describe(suite.Output, map[string]bool{req.Test: true}))
		return res, nil
	}
	if len(res.NewFailures) > 0 {
		broke := map[string]bool{}
		for _, name := range res.NewFailures {
			broke[name] = true
		}
		res.Output = fmt.Sprintf("The patch fixes %q but breaks %d test(s) that passed before:\n%s",
			req.Test, len(res.NewFailures), describe(suite.Output, broke))
		return res, nil
	}

	res.SuitePassed = true
	res.Status = Verified
	return res, nil
}

func failures(checks []events.GuardrailCheck) string {
	var lines []string
	for _, c := range checks {
		if !c.Passed {
			lines = append(lines, "- "+c.Name+": "+c.Detail)
		}
	}
	return strings.Join(lines, "\n")
}

// failingSet is the set of tests failing in one run of the suite.
type failingSet struct {
	Failures map[string]bool `json:"failures"`
}

func (f failingSet) names() []string {
	out := make([]string, 0, len(f.Failures))
	for name := range f.Failures {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// unreadable stands in for failures heall can see happened (the suite
// exited non-zero) but cannot name.
const unreadable = "(the test suite failed in a way heall could not read)"

func failing(suite sandbox.Result) failingSet {
	set := failingSet{Failures: map[string]bool{}}
	if suite.ExitCode == 0 {
		return set
	}
	for _, f := range triage.Parse(suite.Output) {
		set.Failures[f.Test] = true
	}
	if len(set.Failures) == 0 {
		set.Failures[unreadable] = true
	}
	return set
}

// describe summarises the failures in test output for the agent. only, when
// not nil, limits it to those tests.
func describe(output string, only map[string]bool) string {
	var parts []string
	for _, f := range triage.Parse(output) {
		if only == nil || only[f.Test] {
			parts = append(parts, f.Excerpt)
		}
	}
	if len(parts) == 0 {
		return tail(output)
	}
	return tail(strings.Join(parts, "\n\n"))
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxOutput {
		return "...\n" + s[len(s)-maxOutput:]
	}
	return s
}

// baseline returns the tests that fail on the bad commit without any patch.
// dir must be a clean checkout of it. The result is cached per commit and
// test command, since every attempt needs it.
func (v Verifier) baseline(ctx context.Context, sha, dir string) (failingSet, error) {
	state, err := v.Repo.StateDir(ctx)
	if err != nil {
		return failingSet{}, err
	}
	key := sha + "-" + shortHash(strings.Join(append(append([]string{}, v.Cfg.BuildCmd...), v.Cfg.TestCmd...), "\x00"))
	file := filepath.Join(state, "baseline", key+".json")

	var set failingSet
	if raw, err := os.ReadFile(file); err == nil && json.Unmarshal(raw, &set) == nil && set.Failures != nil {
		return set, nil
	}

	suite, err := sandbox.Fresh(v.Runner, dir).Run(ctx, v.Cfg.TestCmd)
	if err != nil {
		return failingSet{}, err
	}
	if suite.TimedOut {
		return failingSet{}, errors.New("the test suite timed out on the failing commit, so there is no baseline to compare a patch against")
	}
	set = failing(suite)
	// The suite may have left files behind; the patch must meet a clean tree.
	if err := v.Repo.Checkout(ctx, dir, sha); err != nil {
		return failingSet{}, err
	}
	if raw, err := json.Marshal(set); err == nil && os.MkdirAll(filepath.Dir(file), 0o755) == nil {
		_ = os.WriteFile(file, raw, 0o644)
	}
	return set, nil
}

// countAttempt records one more submitted patch for the run and returns how
// many there have been. The count lives with the CLI, so the agent cannot
// reset it.
func (v Verifier) countAttempt(ctx context.Context, runID string) (int, error) {
	if runID == "" || strings.ContainsAny(runID, `/\.`) {
		return 0, fmt.Errorf("invalid run id %q", runID)
	}
	state, err := v.Repo.StateDir(ctx)
	if err != nil {
		return 0, err
	}
	file := filepath.Join(state, "runs", runID, "attempts")
	n := 0
	if raw, err := os.ReadFile(file); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	n++
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return 0, err
	}
	return n, os.WriteFile(file, []byte(strconv.Itoa(n)+"\n"), 0o644)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// maxTestOutput caps the output of a test run requested by the agent.
const maxTestOutput = 16 * 1024

// RunTest runs one test, or the whole suite when test is empty, on the
// failing commit as it is, without any patch. It is how the agent looks at
// the failure for itself.
func (v Verifier) RunTest(ctx context.Context, bad, test string) (agentio.RunTestResult, error) {
	dir, remove, err := v.Repo.TempWorktree(ctx, bad)
	if err != nil {
		return agentio.RunTestResult{}, err
	}
	defer remove()

	out, err := tester.Tester{Cfg: v.Cfg}.Check(ctx, sandbox.Fresh(v.Runner, dir), test)
	if err != nil {
		return agentio.RunTestResult{}, err
	}
	res := agentio.RunTestResult{
		Verdict:    out.Verdict,
		ExitCode:   out.Result.ExitCode,
		DurationMS: out.Result.Duration.Milliseconds(),
		Output:     out.Result.Output,
		Truncated:  out.Result.Truncated,
	}
	if out.Phase == "build" {
		res.Output = "The build step failed:\n" + res.Output
	}
	if len(res.Output) > maxTestOutput {
		half := maxTestOutput / 2
		res.Output = res.Output[:half] + "\n... output truncated ...\n" + res.Output[len(res.Output)-half:]
		res.Truncated = true
	}
	return res, nil
}
