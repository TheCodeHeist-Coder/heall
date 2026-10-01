package locate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"heall/internal/events"
)

func history(n int) []events.Commit {
	commits := make([]events.Commit, n)
	for i := range commits {
		commits[i] = events.Commit{SHA: fmt.Sprintf("sha%03d", i), Subject: fmt.Sprintf("commit %d", i)}
	}
	return commits
}

// fake is a CheckFunc over history(n) that also checks how it is called.
type fake struct {
	t       *testing.T
	workers int
	verdict func(i int) events.Verdict

	mu      sync.Mutex
	seen    map[string]int
	busy    map[int]bool
	running atomic.Int32
	peak    atomic.Int32
}

func (f *fake) check(ctx context.Context, worker int, sha string) (events.Verdict, error) {
	now := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		peak := f.peak.Load()
		if now <= peak || f.peak.CompareAndSwap(peak, now) {
			break
		}
	}

	f.mu.Lock()
	if f.seen == nil {
		f.seen, f.busy = map[string]int{}, map[int]bool{}
	}
	f.seen[sha]++
	if worker < 0 || worker >= f.workers {
		f.t.Errorf("worker id %d out of range", worker)
	}
	if f.busy[worker] {
		f.t.Errorf("worker %d was given two commits at once", worker)
	}
	f.busy[worker] = true
	f.mu.Unlock()

	time.Sleep(2 * time.Millisecond)

	f.mu.Lock()
	f.busy[worker] = false
	f.mu.Unlock()

	var i int
	fmt.Sscanf(sha, "sha%d", &i)
	return f.verdict(i), nil
}

func failFrom(culprit int, skipped ...int) func(int) events.Verdict {
	skip := map[int]bool{}
	for _, i := range skipped {
		skip[i] = true
	}
	return func(i int) events.Verdict {
		switch {
		case skip[i]:
			return events.Skipped
		case i >= culprit:
			return events.Fail
		}
		return events.Pass
	}
}

func TestRunFindsCulpritInParallel(t *testing.T) {
	commits := history(121)
	f := &fake{t: t, workers: 6, verdict: failFrom(53, 17, 68)}
	var stream bytes.Buffer

	res, err := Run(context.Background(), commits, f.check, Options{
		Workers:    6,
		VerifyEnds: true,
		Emit:       events.NewEmitter("r1", &stream),
		Diff:       func(_ context.Context, sha string) (string, error) { return "diff of " + sha, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Index != 53 || res.Culprit.SHA != "sha053" || res.Diff != "diff of sha053" {
		t.Errorf("culprit = #%d %s (diff %q), want #53", res.Index, res.Culprit.SHA, res.Diff)
	}
	if res.Rounds != 3 || !res.Consistent || len(res.Suspects) != 0 {
		t.Errorf("rounds=%d consistent=%v suspects=%v, want 3 rounds, consistent, none", res.Rounds, res.Consistent, res.Suspects)
	}
	if res.Verdicts["sha017"] != events.Skipped || res.Verdicts["sha068"] != events.Skipped {
		t.Errorf("the unbuildable commits were not recorded as skipped: %v", res.Verdicts)
	}
	if peak := f.peak.Load(); peak < 2 || peak > 6 {
		t.Errorf("peak concurrency %d, want between 2 and 6", peak)
	}
	for sha, n := range f.seen {
		if n != 1 {
			t.Errorf("%s was tested %d times", sha, n)
		}
	}
	if res.Tested != len(f.seen) {
		t.Errorf("Tested = %d, but %d commits were checked", res.Tested, len(f.seen))
	}

	// The stream must be valid against the contract and tell the whole story.
	var kinds []events.Kind
	started, finished := 0, 0
	sc := bufio.NewScanner(&stream)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var ev events.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		if _, err := events.Decode(ev.Kind, ev.Data); err != nil {
			t.Fatalf("seq %d: %v", ev.Seq, err)
		}
		switch ev.Kind {
		case events.KindCommitTesting:
			started++
		case events.KindCommitTested:
			finished++
		default:
			kinds = append(kinds, ev.Kind)
		}
	}
	want := []events.Kind{
		events.KindLocateStarted,
		events.KindRoundStarted, events.KindRoundDone, // the two ends
		events.KindRoundStarted, events.KindRoundDone,
		events.KindRoundStarted, events.KindRoundDone,
		events.KindRoundStarted, events.KindRoundDone,
		events.KindCulpritFound,
	}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Errorf("event order:\n got %v\nwant %v", kinds, want)
	}
	if started != res.Tested || finished != res.Tested {
		t.Errorf("%d commit_testing and %d commit_tested events for %d commits", started, finished, res.Tested)
	}
}

func TestRunSequentialMatchesParallel(t *testing.T) {
	commits := history(121)
	f := &fake{t: t, workers: 1, verdict: failFrom(53)}
	res, err := Run(context.Background(), commits, f.check, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Index != 53 || res.Rounds != 7 || f.peak.Load() != 1 {
		t.Errorf("index=%d rounds=%d peak=%d, want 53, 7, 1", res.Index, res.Rounds, f.peak.Load())
	}
}

func TestRunReportsSuspectsBeforeCulprit(t *testing.T) {
	// 9 and 10 do not build and 11 is the first commit seen failing, so the
	// failure may really have started in 9 or 10.
	f := &fake{t: t, workers: 4, verdict: failFrom(9, 9, 10)}
	res, err := Run(context.Background(), history(30), f.check, Options{Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	if res.Index != 11 || len(res.Suspects) != 2 || res.Suspects[0].SHA != "sha009" {
		t.Errorf("index=%d suspects=%v, want 11 with suspects 9 and 10", res.Index, res.Suspects)
	}
}

func TestRunRefusesBadEndpoints(t *testing.T) {
	cases := []struct {
		name    string
		verdict func(int) events.Verdict
		good    bool
	}{
		{"good fails", func(int) events.Verdict { return events.Fail }, true},
		{"bad passes", func(int) events.Verdict { return events.Pass }, false},
		{"good does not build", failFrom(5, 0), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{t: t, workers: 3, verdict: tc.verdict}
			_, err := Run(context.Background(), history(10), f.check, Options{Workers: 3, VerifyEnds: true})
			var end *EndpointError
			if !errors.As(err, &end) || end.Good != tc.good {
				t.Fatalf("got %v, want an EndpointError with Good=%v", err, tc.good)
			}
			if len(f.seen) != 2 {
				t.Errorf("tested %d commits, want only the two ends", len(f.seen))
			}
		})
	}
}

func TestRunStopsOnSandboxError(t *testing.T) {
	boom := errors.New("docker is gone")
	var calls atomic.Int32
	check := func(ctx context.Context, worker int, sha string) (events.Verdict, error) {
		calls.Add(1)
		return "", boom
	}
	_, err := Run(context.Background(), history(200), check, Options{Workers: 4})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the sandbox error", err)
	}
	if n := calls.Load(); n != 4 {
		t.Errorf("checked %d commits after the failure, want the search to stop after one round of 4", n)
	}
}
