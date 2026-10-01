package locate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"heall/internal/events"
)

// CheckFunc tests one commit. worker identifies the calling worker, in
// [0, Workers), so an implementation can keep one checkout per worker. An
// error means the test could not be run at all and aborts the search.
type CheckFunc func(ctx context.Context, worker int, sha string) (events.Verdict, error)

type Options struct {
	// Workers is how many commits are tested at once.
	Workers int
	// VerifyEnds tests the good and bad commits first and refuses to search
	// if good fails or bad passes. Skip it only when that is already known.
	VerifyEnds bool
	// Emit receives progress events; nil discards them.
	Emit *events.Emitter
	// Diff returns the patch of the culprit for the final event; optional.
	Diff func(ctx context.Context, sha string) (string, error)
}

type Result struct {
	Culprit events.Commit
	// Index is the culprit's position in the commit list given to Run.
	Index int
	Diff  string
	// Suspects are skipped commits directly before the culprit. They could
	// not be tested, so the failure may have started in one of them.
	Suspects []events.Commit
	// Consistent is false when a commit passed after an older one failed.
	Consistent bool
	Rounds     int
	Tested     int
	Duration   time.Duration
	Verdicts   map[string]events.Verdict
}

// EndpointError means the range does not bracket a failure, so there is
// nothing to bisect.
type EndpointError struct {
	Commit  events.Commit
	Good    bool
	Verdict events.Verdict
}

func (e *EndpointError) Error() string {
	if e.Good {
		return fmt.Sprintf("the good commit %.10s does not pass (%s); pick an older good commit", e.Commit.SHA, e.Verdict)
	}
	return fmt.Sprintf("the bad commit %.10s does not fail (%s); the failure does not reproduce", e.Commit.SHA, e.Verdict)
}

// Run searches commits, ordered oldest first with the known-good commit
// first and the known-bad commit last, for the first commit that fails.
func Run(ctx context.Context, commits []events.Commit, check CheckFunc, opts Options) (Result, error) {
	if len(commits) < 2 {
		return Result{}, errors.New("need at least a good and a bad commit")
	}
	workers := max(opts.Workers, 1)
	emit := func(p events.Payload) { _ = opts.Emit.Emit(events.StageLocate, p) }
	start := time.Now()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pool := newPool(ctx, workers, check, emit)
	defer pool.close()

	res := Result{Consistent: true, Verdicts: map[string]events.Verdict{}}
	emit(events.LocateStarted{Commits: commits, Workers: workers})

	// testRound runs one batch of commits and records their verdicts.
	testRound := func(round int, s State, idx []int) (map[int]events.Verdict, error) {
		shas := make([]string, len(idx))
		for i, n := range idx {
			shas[i] = commits[n].SHA
		}
		emit(events.RoundStarted{Round: round, Lo: s.Lo, Hi: s.Hi, Probes: shas})
		verdicts, err := pool.run(round, idx, shas)
		if err != nil {
			cancel()
			return nil, err
		}
		for n, v := range verdicts {
			res.Verdicts[commits[n].SHA] = v
		}
		res.Tested += len(idx)
		return verdicts, nil
	}

	state := State{Lo: 0, Hi: len(commits) - 1, Skipped: map[int]bool{}}

	if opts.VerifyEnds {
		v, err := testRound(0, state, []int{state.Lo, state.Hi})
		if err != nil {
			return res, err
		}
		emit(events.RoundDone{Round: 0, Lo: state.Lo, Hi: state.Hi})
		if v[state.Lo] != events.Pass {
			return res, &EndpointError{Commit: commits[state.Lo], Good: true, Verdict: v[state.Lo]}
		}
		if v[state.Hi] != events.Fail {
			return res, &EndpointError{Commit: commits[state.Hi], Good: false, Verdict: v[state.Hi]}
		}
	}

	for !state.Done() {
		res.Rounds++
		verdicts, err := testRound(res.Rounds, state, Plan(state, workers))
		if err != nil {
			return res, err
		}
		next, consistent := state.Apply(verdicts)
		if !consistent {
			res.Consistent = false
			emit(events.Log{Level: "warn", Message: "a commit passed after an older commit failed; the failure may be flaky"})
		}
		state = next
		emit(events.RoundDone{Round: res.Rounds, Lo: state.Lo, Hi: state.Hi})
	}

	res.Index = state.Hi
	res.Culprit = commits[state.Hi]
	for _, n := range state.Suspects() {
		res.Suspects = append(res.Suspects, commits[n])
	}
	if len(res.Suspects) > 0 {
		emit(events.Log{Level: "warn", Message: fmt.Sprintf(
			"%d commit(s) directly before the culprit could not be built, so the failure may have started in one of them",
			len(res.Suspects))})
	}
	if opts.Diff != nil {
		diff, err := opts.Diff(ctx, res.Culprit.SHA)
		if err != nil {
			return res, err
		}
		res.Diff = diff
	}
	res.Duration = time.Since(start)
	emit(events.CulpritFound{
		Commit:     res.Culprit,
		Diff:       res.Diff,
		Rounds:     res.Rounds,
		Tested:     res.Tested,
		DurationMS: res.Duration.Milliseconds(),
	})
	return res, nil
}

type job struct {
	round int
	index int
	sha   string
}

type outcome struct {
	index   int
	verdict events.Verdict
	err     error
}

// pool is a fixed set of workers that pull commits from a channel. Each
// worker keeps its number for life, so it can own one checkout.
type pool struct {
	jobs    chan job
	results chan outcome
	wg      sync.WaitGroup
}

func newPool(ctx context.Context, workers int, check CheckFunc, emit func(events.Payload)) *pool {
	p := &pool{jobs: make(chan job), results: make(chan outcome)}
	for w := range workers {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for j := range p.jobs {
				emit(events.CommitTesting{SHA: j.sha, Round: j.round, Worker: w})
				started := time.Now()
				verdict, err := check(ctx, w, j.sha)
				if err == nil {
					emit(events.CommitTested{
						SHA:        j.sha,
						Round:      j.round,
						Worker:     w,
						Verdict:    verdict,
						DurationMS: time.Since(started).Milliseconds(),
					})
				}
				p.results <- outcome{index: j.index, verdict: verdict, err: err}
			}
		}()
	}
	return p
}

// run tests one round and waits for every commit in it. It returns the
// first error, after all workers have finished their current commit.
func (p *pool) run(round int, idx []int, shas []string) (map[int]events.Verdict, error) {
	go func() {
		for i, n := range idx {
			p.jobs <- job{round: round, index: n, sha: shas[i]}
		}
	}()
	verdicts := make(map[int]events.Verdict, len(idx))
	var first error
	for range idx {
		o := <-p.results
		if o.err != nil {
			if first == nil {
				first = o.err
			}
			continue
		}
		verdicts[o.index] = o.verdict
	}
	return verdicts, first
}

func (p *pool) close() {
	close(p.jobs)
	p.wg.Wait()
}
