package tester

import (
	"context"
	"errors"
	"strings"
	"testing"

	"heall/internal/config"
	"heall/internal/events"
	"heall/internal/sandbox"
)

// script is a Box that answers each command from a table keyed by the
// command's first word.
type script struct {
	results map[string]sandbox.Result
	err     error
	calls   []string
}

func (s *script) Close() error { return nil }
func (s *script) Run(_ context.Context, argv []string) (sandbox.Result, error) {
	s.calls = append(s.calls, strings.Join(argv, " "))
	return s.results[argv[0]], s.err
}

func TestCheck(t *testing.T) {
	cfg := config.Config{
		BuildCmd:   []string{"build"},
		TestCmd:    []string{"suite"},
		TestOneCmd: []string{"one", "--name=^{{test_re}}$"},
	}
	ok := sandbox.Result{}
	failed := sandbox.Result{ExitCode: 1}
	hung := sandbox.Result{ExitCode: -1, TimedOut: true}

	cases := []struct {
		name    string
		test    string
		results map[string]sandbox.Result
		verdict events.Verdict
		phase   string
		calls   []string
	}{
		{"single test passes", "adds (a+b)", map[string]sandbox.Result{"build": ok, "one": ok},
			events.Pass, "test", []string{"build", `one --name=^adds \(a\+b\)$`}},
		{"single test fails", "adds", map[string]sandbox.Result{"build": ok, "one": failed},
			events.Fail, "test", []string{"build", "one --name=^adds$"}},
		{"no name runs the suite", "", map[string]sandbox.Result{"build": ok, "suite": failed},
			events.Fail, "test", []string{"build", "suite"}},
		{"broken build is skipped without testing", "adds", map[string]sandbox.Result{"build": failed, "one": ok},
			events.Skipped, "build", []string{"build"}},
		{"hung build is skipped", "adds", map[string]sandbox.Result{"build": hung},
			events.Skipped, "build", []string{"build"}},
		{"hung test is skipped, not failed", "adds", map[string]sandbox.Result{"build": ok, "one": hung},
			events.Skipped, "test", []string{"build", "one --name=^adds$"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &script{results: tc.results}
			out, err := Tester{Cfg: cfg}.Check(context.Background(), r, tc.test)
			if err != nil {
				t.Fatal(err)
			}
			if out.Verdict != tc.verdict || out.Phase != tc.phase {
				t.Errorf("verdict=%s phase=%s, want %s in %s", out.Verdict, out.Phase, tc.verdict, tc.phase)
			}
			if strings.Join(r.calls, "|") != strings.Join(tc.calls, "|") {
				t.Errorf("ran %q, want %q", r.calls, tc.calls)
			}
		})
	}
}

func TestCheckWithoutBuildStep(t *testing.T) {
	r := &script{results: map[string]sandbox.Result{"suite": {}}}
	out, err := Tester{Cfg: config.Config{TestCmd: []string{"suite"}}}.Check(context.Background(), r, "")
	if err != nil || out.Verdict != events.Pass || len(r.calls) != 1 {
		t.Errorf("verdict=%s err=%v calls=%q, want one passing suite run", out.Verdict, err, r.calls)
	}
}

func TestCheckPassesSandboxErrorsThrough(t *testing.T) {
	boom := errors.New("docker is gone")
	_, err := Tester{Cfg: config.Config{TestCmd: []string{"suite"}}}.Check(context.Background(), &script{err: boom}, "")
	if !errors.Is(err, boom) {
		t.Errorf("got %v: a sandbox failure must not be turned into a verdict", err)
	}
}
