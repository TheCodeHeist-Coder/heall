// Package pipeline chains the stages: triage, reproduce, locate, heal,
// deliver. Each stage either produces evidence for the next or stops the
// pipeline with an Escalation that says why.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"heall/internal/agentio"
	"heall/internal/config"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/heal"
	"heall/internal/locate"
	"heall/internal/reproduce"
	"heall/internal/sandbox"
	"heall/internal/tester"
	"heall/internal/triage"
	"heall/internal/verify"
	"heall/internal/workspace"
)

// Escalation means heall stopped on purpose because it could not prove
// something. It is not a failure of the tool.
type Escalation struct {
	Stage     events.Stage
	Reason    string
	Diagnosis string
}

func (e *Escalation) Error() string {
	return fmt.Sprintf("escalated at %s: %s", e.Stage, e.Reason)
}

type Pipeline struct {
	Repo   *gitx.Repo
	Cfg    config.Config
	Runner sandbox.Runner
	Emit   *events.Emitter
	RunID  string
	// ConfigPath is the --config in use, absolute, or empty.
	ConfigPath string
	// Warn reports problems that do not change the outcome, such as cleanup
	// that failed.
	Warn func(message string)

	// The fields below replace external programs in tests.

	// AgentCommand starts the heal agent; nil means the bundled one.
	AgentCommand []string
	// HeallBin is the CLI the agent calls back into; empty means this program.
	HeallBin string
	// Gh is the GitHub CLI; nil means "gh".
	Gh []string
}

func (p *Pipeline) warn(format string, args ...any) {
	if p.Warn != nil {
		p.Warn(fmt.Sprintf(format, args...))
	}
}

func (p *Pipeline) tester() tester.Tester { return tester.Tester{Cfg: p.Cfg} }

func (p *Pipeline) verifier() verify.Verifier {
	return verify.Verifier{Repo: p.Repo, Cfg: p.Cfg, Runner: p.Runner}
}

// Stage wraps fn in stage_started and stage_done events. An Escalation from
// fn is reported on the stream before it is returned.
func (p *Pipeline) Stage(stage events.Stage, fn func() error) error {
	start := time.Now()
	_ = p.Emit.Emit(stage, events.StageStarted{})
	err := fn()
	status := "ok"
	var esc *Escalation
	if errors.As(err, &esc) {
		status = "escalated"
		_ = p.Emit.Emit(stage, events.Escalated{Reason: esc.Reason, Diagnosis: esc.Diagnosis})
	} else if err != nil {
		status = "error"
	}
	_ = p.Emit.Emit(stage, events.StageDone{Status: status, DurationMS: time.Since(start).Milliseconds()})
	return err
}

// Pool makes the worktrees commits are tested in. The returned function
// removes them.
func (p *Pipeline) Pool(test string, workers int) (*workspace.Pool, func(), error) {
	pool, err := workspace.New(p.Repo, p.Runner, p.tester(), test, workers)
	if err != nil {
		return nil, nil, err
	}
	return pool, func() {
		if err := pool.Close(); err != nil {
			p.warn("worktree cleanup: %v", err)
		}
	}, nil
}

// Triage finds the failing test. log is the test output to read; nil means
// run the suite on bad in the sandbox and read that.
func (p *Pipeline) Triage(ctx context.Context, bad string, log *string) (triage.Report, error) {
	var report triage.Report
	err := p.Stage(events.StageTriage, func() error {
		snapshot, err := p.Repo.Snapshot(ctx, bad)
		if err != nil {
			return err
		}
		text := ""
		if log != nil {
			text = *log
		} else if text, err = p.suiteOutput(ctx, bad); err != nil {
			return err
		}
		report, err = triage.Analyze(text, snapshot)
		if errors.Is(err, triage.ErrNoFailure) {
			return &Escalation{
				Stage:     events.StageTriage,
				Reason:    "no failing test found",
				Diagnosis: "The log does not contain a failing test in a format heall understands (Node test runner TAP or spec output).",
			}
		}
		if err != nil {
			return err
		}
		if report.SuspectFiles == nil {
			report.SuspectFiles = []string{}
		}
		return p.Emit.Emit(events.StageTriage, events.TriageDone{
			TestName:     report.Primary.Test,
			TestFile:     report.Primary.File,
			SuspectFiles: report.SuspectFiles,
			Excerpt:      report.Primary.Excerpt,
		})
	})
	return report, err
}

// suiteOutput runs the whole suite on sha, for when there is no log.
func (p *Pipeline) suiteOutput(ctx context.Context, sha string) (string, error) {
	pool, closePool, err := p.Pool("", 1)
	if err != nil {
		return "", err
	}
	defer closePool()
	out, err := pool.Outcome(ctx, 0, sha)
	if err != nil {
		return "", err
	}
	switch out.Verdict {
	case events.Pass:
		return "", &Escalation{
			Stage:     events.StageTriage,
			Reason:    "the test suite passes",
			Diagnosis: fmt.Sprintf("Every test passes on %.10s in the sandbox, so there is no failure to work on.", sha),
		}
	case events.Skipped:
		return "", &Escalation{
			Stage:     events.StageTriage,
			Reason:    "the failing commit cannot be tested",
			Diagnosis: fmt.Sprintf("%.10s does not build in the sandbox, or the %s step timed out:\n%s", sha, out.Phase, out.Result.Output),
		}
	}
	return out.Result.Output, nil
}

// Reproduce confirms the failure is real and steady. pool must run the
// failing test; testFile may be empty.
func (p *Pipeline) Reproduce(ctx context.Context, pool *workspace.Pool, workers int, good, bad, test, testFile string) (reproduce.Result, error) {
	var res reproduce.Result
	err := p.Stage(events.StageReproduce, func() error {
		var err error
		res, err = reproduce.Run(ctx, good, bad, pool.Outcome, reproduce.Options{
			Runs:    p.Cfg.Reproduce.Runs,
			Workers: workers,
			Emit:    p.Emit,
		})
		if err != nil || res.Proceed() {
			return err
		}
		var hints []string
		if res.Outcome == reproduce.Flaky {
			hints = p.flakyHints(ctx, bad, test, testFile)
		}
		reason, diagnosis := res.Explain(test, good, bad, hints)
		return &Escalation{Stage: events.StageReproduce, Reason: reason, Diagnosis: diagnosis}
	})
	return res, err
}

// flakyHints looks through the test and the files it imports for code whose
// result changes from run to run. Hints are a courtesy: any problem finding
// them just means there are none.
func (p *Pipeline) flakyHints(ctx context.Context, sha, test, testFile string) []string {
	snapshot, err := p.Repo.Snapshot(ctx, sha)
	if err != nil {
		return nil
	}
	if testFile == "" {
		files, err := snapshot.Find(test)
		if err != nil || len(files) == 0 {
			return nil
		}
		testFile = files[0]
	}
	sources := map[string]string{}
	for _, file := range append([]string{testFile}, triage.Suspects(triage.Failure{File: testFile}, snapshot)...) {
		if content, err := snapshot.Read(file); err == nil {
			sources[file] = content
		}
	}
	return reproduce.Hints(sources)
}

// Locate finds the culprit among commits, oldest first. verifyEnds can be
// false when Reproduce has already tested both ends.
func (p *Pipeline) Locate(ctx context.Context, pool *workspace.Pool, commits []events.Commit, verifyEnds bool) (locate.Result, error) {
	var res locate.Result
	err := p.Stage(events.StageLocate, func() error {
		var err error
		res, err = locate.Run(ctx, commits, pool.Check, locate.Options{
			Workers:    p.Cfg.Locate.Workers,
			VerifyEnds: verifyEnds,
			Emit:       p.Emit,
			Diff:       p.Repo.Diff,
		})
		var end *locate.EndpointError
		if errors.As(err, &end) {
			return &Escalation{Stage: events.StageLocate, Reason: "the range does not bracket the failure", Diagnosis: end.Error()}
		}
		return err
	})
	return res, err
}

// HealInput gathers what the agent is told. failure is the test's output on
// the bad commit if it is already known; empty means run the test to get it.
func (p *Pipeline) HealInput(ctx context.Context, good, bad, culprit, test, failure string) (heal.Input, error) {
	in := heal.Input{RunID: p.RunID, Good: good, Bad: bad}
	if failure == "" {
		run, err := p.verifier().RunTest(ctx, bad, test)
		if err != nil {
			return in, err
		}
		if run.Verdict != events.Fail {
			return in, &Escalation{
				Stage:     events.StageHeal,
				Reason:    "the test does not fail on the bad commit",
				Diagnosis: fmt.Sprintf("%q gave %q on %.10s in the sandbox, so there is nothing to fix. Run `heall reproduce` first.", test, run.Verdict, bad),
			}
		}
		failure = run.Output
	}
	in.Failure = agentio.Failure{TestName: test, Output: failure}
	snapshot, err := p.Repo.Snapshot(ctx, bad)
	if err != nil {
		return in, err
	}
	if report, err := triage.Analyze(failure, snapshot); err == nil {
		// The output may hold other failures; use the one being healed.
		for _, f := range report.Failures {
			if f.Test == test {
				in.Failure.TestFile = f.File
				in.Failure.Output = f.Excerpt
				in.SuspectFiles = triage.Suspects(f, snapshot)
				break
			}
		}
	}

	commit, err := p.Repo.Commit(ctx, culprit)
	if err != nil {
		return in, err
	}
	message, err := p.Repo.Message(ctx, culprit)
	if err != nil {
		return in, err
	}
	diff, err := p.Repo.Diff(ctx, culprit)
	if err != nil {
		return in, err
	}
	in.Culprit = agentio.Culprit{Commit: commit, Message: message, Diff: diff}
	return in, nil
}

// Heal runs the agent. The result is a fix heall verified itself; anything
// else is an Escalation carrying the agent's diagnosis.
func (p *Pipeline) Heal(ctx context.Context, in heal.Input) (heal.Result, error) {
	var res heal.Result
	err := p.Stage(events.StageHeal, func() error {
		var err error
		res, err = heal.Healer{
			Repo:       p.Repo,
			Cfg:        p.Cfg,
			Runner:     p.Runner,
			Emit:       p.Emit,
			ConfigPath: p.ConfigPath,
			Command:    p.AgentCommand,
			HeallBin:   p.HeallBin,
		}.Run(ctx, in)
		if err != nil {
			return err
		}
		if res.Outcome != heal.Fixed {
			return &Escalation{Stage: events.StageHeal, Reason: res.Reason, Diagnosis: res.RootCause}
		}
		return nil
	})
	return res, err
}
