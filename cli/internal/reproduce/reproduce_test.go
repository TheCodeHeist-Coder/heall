package reproduce

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"heall/internal/events"
	"heall/internal/sandbox"
	"heall/internal/tester"
)

// plan answers each run on the bad commit from a list of verdicts, in the
// order the runs start, and the good commit with a fixed verdict.
type plan struct {
	bad  []events.Verdict
	good events.Verdict

	mu    sync.Mutex
	next  int
	calls atomic.Int32
	peak  atomic.Int32
	live  atomic.Int32
}

func (p *plan) check(ctx context.Context, worker int, sha string) (tester.Outcome, error) {
	p.calls.Add(1)
	now := p.live.Add(1)
	defer p.live.Add(-1)
	for {
		peak := p.peak.Load()
		if now <= peak || p.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	select {
	case <-time.After(5 * time.Millisecond):
	case <-ctx.Done():
		return tester.Outcome{}, ctx.Err()
	}
	if sha == "good" {
		return tester.Outcome{Verdict: p.good}, nil
	}
	p.mu.Lock()
	v := p.bad[p.next%len(p.bad)]
	p.next++
	p.mu.Unlock()
	return tester.Outcome{Verdict: v, Result: sandbox.Result{Output: "output of a " + string(v) + " run"}}, nil
}

func repeat(v events.Verdict, n int) []events.Verdict {
	out := make([]events.Verdict, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		bad     []events.Verdict
		good    events.Verdict
		want    Outcome
		proceed bool
	}{
		{"fails every time, good passes", repeat(events.Fail, 8), events.Pass, Reproduced, true},
		{"passes every time", repeat(events.Pass, 8), events.Pass, NotReproduced, false},
		{"mixed results", []events.Verdict{events.Fail, events.Pass}, events.Pass, Flaky, false},
		{"a single pass among failures", append(repeat(events.Fail, 7), events.Pass), events.Pass, Flaky, false},
		{"bad commit does not build", repeat(events.Skipped, 8), events.Pass, BadUnbuildable, false},
		{"good commit fails too", repeat(events.Fail, 8), events.Fail, GoodFails, false},
		{"good commit does not build", repeat(events.Fail, 8), events.Skipped, GoodFails, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &plan{bad: tc.bad, good: tc.good}
			var stream bytes.Buffer
			res, err := Run(context.Background(), "good", "bad", p.check, Options{
				Runs: 8, Workers: 1, Emit: events.NewEmitter("r", &stream),
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != tc.want || res.Proceed() != tc.proceed {
				t.Errorf("outcome=%s proceed=%v, want %s and %v (runs=%d failures=%d good=%s)",
					res.Outcome, res.Proceed(), tc.want, tc.proceed, res.Runs, res.Failures, res.Good)
			}
			if !strings.Contains(stream.String(), `"kind":"reproduce_done"`) {
				t.Error("no reproduce_done event")
			}
			reason, diagnosis := res.Explain("the test", "good", "bad", nil)
			if tc.proceed != (reason == "") || tc.proceed != (diagnosis == "") {
				t.Errorf("an outcome that stops the pipeline must be explained, and only that: reason=%q", reason)
			}
		})
	}
}

func TestReproducedRunsEverythingInParallel(t *testing.T) {
	p := &plan{bad: repeat(events.Fail, 8), good: events.Pass}
	res, err := Run(context.Background(), "good", "bad", p.check, Options{Runs: 8, Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	if res.Runs != 8 || res.Failures != 8 || res.Good != events.Pass || p.calls.Load() != 9 {
		t.Errorf("runs=%d failures=%d good=%s calls=%d, want 8, 8, pass, 9", res.Runs, res.Failures, res.Good, p.calls.Load())
	}
	if peak := p.peak.Load(); peak < 2 || peak > 4 {
		t.Errorf("peak concurrency %d, want between 2 and 4", peak)
	}
	if res.FailureOutput != "output of a fail run" {
		t.Errorf("failure output = %q", res.FailureOutput)
	}
}

func TestStopsAsSoonAsTheAnswerIsKnown(t *testing.T) {
	// The first two bad runs disagree, so the other 18 need not run.
	p := &plan{bad: []events.Verdict{events.Fail, events.Pass}, good: events.Pass}
	res, err := Run(context.Background(), "good", "bad", p.check, Options{Runs: 20, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Flaky {
		t.Fatalf("outcome = %s, want flaky", res.Outcome)
	}
	if calls := p.calls.Load(); calls > 5 {
		t.Errorf("made %d runs after the test was already known to be flaky", calls)
	}
	if res.Runs != 2 || res.Failures != 1 {
		t.Errorf("runs=%d failures=%d, want the 2 completed runs with 1 failure", res.Runs, res.Failures)
	}
}

func TestSandboxErrorAndCancelAreErrors(t *testing.T) {
	boom := errors.New("docker is gone")
	_, err := Run(context.Background(), "good", "bad",
		func(context.Context, int, string) (tester.Outcome, error) { return tester.Outcome{}, boom },
		Options{Runs: 8, Workers: 3})
	if !errors.Is(err, boom) {
		t.Errorf("got %v, want the sandbox error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &plan{bad: repeat(events.Fail, 8), good: events.Pass}
	go func() {
		time.Sleep(8 * time.Millisecond)
		cancel()
	}()
	res, err := Run(ctx, "good", "bad", p.check, Options{Runs: 200, Workers: 2})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got outcome %q and error %v; an interrupted stage must not report a verdict", res.Outcome, err)
	}
}

func TestExplainFlaky(t *testing.T) {
	res := Result{Outcome: Flaky, Runs: 5, Failures: 2, Good: events.Pass}
	reason, diagnosis := res.Explain("jitter stays close", "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", []string{"src/retry.js:10 uses random numbers: x"})
	if reason != "the test is flaky" {
		t.Errorf("reason = %q", reason)
	}
	for _, want := range []string{`"jitter stays close" failed 2 of 5 runs`, "bbbbbbbbbb)", "src/retry.js:10 uses random numbers"} {
		if !strings.Contains(diagnosis, want) {
			t.Errorf("diagnosis lacks %q:\n%s", want, diagnosis)
		}
	}
}

func TestHints(t *testing.T) {
	got := Hints(map[string]string{
		"src/retry.js": "const a = 1;\nexport function jitter(r = Math.random) {\n  return Date.now();\n}\n",
		"src/pure.js":  "export const add = (a, b) => a + b;\n",
		"src/poll.js":  "setTimeout(tick, 100);\n",
	})
	want := []string{
		"src/poll.js:1 uses timers: setTimeout(tick, 100);",
		"src/retry.js:2 uses random numbers: export function jitter(r = Math.random) {",
		"src/retry.js:3 uses the current time: return Date.now();",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("hints:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
