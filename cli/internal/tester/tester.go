// Package tester turns "this directory holds a commit" into a verdict by
// running the repository's build and test commands in the sandbox.
package tester

import (
	"context"

	"heall/internal/config"
	"heall/internal/events"
	"heall/internal/sandbox"
)

type Tester struct {
	Cfg    config.Config
	Runner sandbox.Runner
}

type Outcome struct {
	Verdict events.Verdict
	// Phase is the command that decided the verdict: "build" or "test".
	Phase  string
	Result sandbox.Result
}

// Check builds the commit in dir and runs the test called name, or the whole
// suite when name is empty. A commit that does not build, or whose build or
// test times out, is Skipped: it says nothing about where the failure began.
func (t Tester) Check(ctx context.Context, dir, name string) (Outcome, error) {
	if len(t.Cfg.BuildCmd) > 0 {
		res, err := t.Runner.Run(ctx, dir, t.Cfg.BuildCmd)
		if err != nil {
			return Outcome{}, err
		}
		if res.TimedOut || res.ExitCode != 0 {
			return Outcome{Verdict: events.Skipped, Phase: "build", Result: res}, nil
		}
	}

	argv := t.Cfg.TestCmd
	if name != "" {
		argv = t.Cfg.TestOne(name)
	}
	res, err := t.Runner.Run(ctx, dir, argv)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Phase: "test", Result: res}
	switch {
	case res.TimedOut:
		out.Verdict = events.Skipped
	case res.ExitCode == 0:
		out.Verdict = events.Pass
	default:
		out.Verdict = events.Fail
	}
	return out, nil
}
