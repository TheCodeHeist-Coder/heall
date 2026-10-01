package pipeline

import (
	"context"
	"errors"
	"time"

	"heall/internal/deliver"
	"heall/internal/events"
	"heall/internal/reproduce"
	"heall/internal/triage"
)

// Outcomes of a whole run, as in the run_done event.
const (
	Fixed     = "fixed"
	Escalated = "escalated"
	Failed    = "error"
)

type RunOptions struct {
	// Good and Bad are resolved commit hashes.
	Good, Bad string
	// Base is the branch the failure is on, for the pull request; may be
	// empty.
	Base string
	// Log is the CI log to triage; nil means run the suite to get one.
	Log *string
	// DryRun delivers files only: nothing is pushed or posted.
	DryRun bool
	// InjectBadPatch shows a guardrail rejection before the agent starts.
	InjectBadPatch bool
	// OutDir receives the patch, the report or the diagnosis.
	OutDir string
	// Remote is the git remote to push the fix branch to.
	Remote string
}

// Summary is what a run came to.
type Summary struct {
	Outcome string `json:"outcome"`
	RunID   string `json:"run_id"`
	Test    string `json:"test,omitempty"`
	// Stage is where the run stopped, when it escalated.
	Stage     events.Stage `json:"stage,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Diagnosis string       `json:"diagnosis,omitempty"`

	Culprit   *events.Commit `json:"culprit,omitempty"`
	Rounds    int            `json:"rounds,omitempty"`
	Tested    int            `json:"tested,omitempty"`
	RootCause string         `json:"root_cause,omitempty"`
	Attempts  int            `json:"attempts,omitempty"`

	// Delivered is "pr", "patch_file" or "diagnosis".
	Delivered  string `json:"delivered,omitempty"`
	URL        string `json:"url,omitempty"`
	Path       string `json:"path,omitempty"`
	Branch     string `json:"branch,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// Run takes one failing build from log to delivery. It returns an
// *Escalation when heall stopped on purpose; by then the diagnosis has been
// delivered and Summary describes it.
func (p *Pipeline) Run(ctx context.Context, opts RunOptions) (Summary, error) {
	start := time.Now()
	sum := Summary{RunID: p.RunID}
	_ = p.Emit.Emit(events.StageRun, events.RunStarted{
		Repo: p.Repo.Dir, Branch: opts.Base, Good: opts.Good, Bad: opts.Bad, DryRun: opts.DryRun,
	})

	ev := deliver.Evidence{
		RunID: p.RunID, Base: opts.Base, Good: opts.Good, Bad: opts.Bad,
		Sandbox: p.Runner.Name(), Model: p.Cfg.Heal.Model,
	}
	deliverer := deliver.Deliverer{
		Repo: p.Repo, OutDir: opts.OutDir, DryRun: opts.DryRun, Remote: opts.Remote, Gh: p.Gh,
		Warn: func(m string) { _ = p.Emit.Emit(events.StageDeliver, events.Log{Level: "warn", Message: m}) },
	}

	err := p.stages(ctx, opts, &ev, &sum, deliverer)

	var esc *Escalation
	switch {
	case err == nil:
		sum.Outcome = Fixed
	case errors.As(err, &esc):
		sum.Outcome, sum.Stage, sum.Reason, sum.Diagnosis = Escalated, esc.Stage, esc.Reason, esc.Diagnosis
		// A diagnosis is a deliverable too. If writing it fails, that is the
		// error to report.
		if derr := p.Stage(events.StageDeliver, func() error {
			res, derr := deliverer.Escalation(ctx, ev, esc.Stage, esc.Reason, esc.Diagnosis)
			if derr != nil {
				return derr
			}
			sum.Delivered, sum.URL, sum.Path = res.Outcome, res.URL, res.Path
			return p.Emit.Emit(events.StageDeliver, events.DeliverDone{Outcome: res.Outcome, URL: res.URL, Path: res.Path})
		}); derr != nil {
			sum.Outcome, err = Failed, derr
		}
	default:
		sum.Outcome = Failed
	}
	sum.DurationMS = time.Since(start).Milliseconds()
	_ = p.Emit.Emit(events.StageRun, events.RunDone{Outcome: sum.Outcome, DurationMS: sum.DurationMS})
	return sum, err
}

func (p *Pipeline) stages(ctx context.Context, opts RunOptions, ev *deliver.Evidence, sum *Summary, deliverer deliver.Deliverer) error {
	if err := p.Runner.Prepare(ctx); err != nil {
		return err
	}
	commits, err := p.Repo.Range(ctx, opts.Good, opts.Bad)
	if err != nil {
		return err
	}
	if len(commits) < 2 {
		return errors.New("the good and bad commits are the same")
	}

	// 1. Triage: which test failed?
	report, err := p.Triage(ctx, opts.Bad, opts.Log)
	if err != nil {
		return err
	}
	failure := report.Primary
	ev.Test, ev.TestFile, ev.Failure = failure.Test, failure.File, failure.Excerpt
	sum.Test = failure.Test

	// One set of worktrees and sandboxes serves both stages that test
	// commits.
	workers := p.Cfg.Locate.Workers
	pool, closePool, err := p.Pool(failure.Test, max(workers, 2))
	if err != nil {
		return err
	}
	defer closePool()
	go pool.Warm(ctx, opts.Bad)

	// 2. Reproduce: is the failure real, and steady?
	repro, err := p.Reproduce(ctx, pool, min(max(workers, 2), p.Cfg.Reproduce.Runs+1), opts.Good, opts.Bad, failure.Test, failure.File)
	if repro.Runs > 0 {
		ev.Reproduce = &deliver.Reproduced{Runs: repro.Runs, Failures: repro.Failures}
	}
	if err != nil {
		return err
	}

	// 3. Locate: which commit introduced it? Reproduce has just tested both
	// ends of the range, so the search starts straight away.
	found, err := p.Locate(ctx, pool, commits, false)
	if err != nil {
		return err
	}
	message, err := p.Repo.Message(ctx, found.Culprit.SHA)
	if err != nil {
		return err
	}
	ev.Culprit = &deliver.Culprit{
		Commit: found.Culprit, Message: message, Index: found.Index, Commits: len(commits) - 1,
		Rounds: found.Rounds, Tested: found.Tested, Workers: workers,
		Seconds: found.Duration.Seconds(), Suspects: found.Suspects,
	}
	sum.Culprit, sum.Rounds, sum.Tested = &found.Culprit, found.Rounds, found.Tested
	if !found.Consistent {
		return &Escalation{
			Stage:  events.StageLocate,
			Reason: "the failure does not have a single starting point",
			Diagnosis: "While searching, a commit passed after an older commit had failed. A failure that comes and goes " +
				"across the history cannot be pinned on one commit, so any culprit heall named would be a guess.",
		}
	}

	// 4. Heal: can the agent produce a fix heall can prove?
	in, err := p.HealInput(ctx, opts.Good, opts.Bad, found.Culprit.SHA, failure.Test, healOutput(repro, failure))
	if err != nil {
		return err
	}
	in.InjectBadPatch = opts.InjectBadPatch
	if len(in.SuspectFiles) == 0 {
		in.SuspectFiles = report.SuspectFiles
	}
	if in.Failure.TestFile == "" {
		in.Failure.TestFile = failure.File
	}
	fixed, err := p.Heal(ctx, in)
	sum.RootCause, sum.Attempts = fixed.RootCause, fixed.Attempts
	if err != nil {
		return err
	}
	limit := p.Cfg.Heal.MaxAttempts
	if opts.InjectBadPatch {
		limit++
	}
	ev.Fix = &deliver.Fix{Patch: fixed.Patch, RootCause: fixed.RootCause, Attempts: fixed.Attempts, MaxAttempts: limit}

	// 5. Deliver.
	return p.Stage(events.StageDeliver, func() error {
		res, err := deliverer.Fix(ctx, *ev)
		if err != nil {
			return err
		}
		sum.Delivered, sum.URL, sum.Path, sum.Branch = res.Outcome, res.URL, res.Path, res.Branch
		return p.Emit.Emit(events.StageDeliver, events.DeliverDone{Outcome: res.Outcome, URL: res.URL, Path: res.Path})
	})
}

// healOutput picks the test output the agent is shown: what the sandbox
// printed when the failure was reproduced, which is what the agent will see
// again when it runs the test, rather than the CI log.
func healOutput(repro reproduce.Result, failure triage.Failure) string {
	if repro.FailureOutput != "" {
		return repro.FailureOutput
	}
	return failure.Excerpt
}
