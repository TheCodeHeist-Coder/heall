// Package reproduce confirms that a reported failure is real before any
// time is spent on it: the test must fail on the bad commit every time, and
// pass on the good commit. Anything else stops the pipeline with a reason.
package reproduce

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"heall/internal/events"
	"heall/internal/tester"
)

// Outcome says what the runs showed.
type Outcome string

const (
	// Reproduced: the test fails on bad every time and passes on good.
	Reproduced Outcome = "reproduced"
	// Flaky: the test both passed and failed on the same commit.
	Flaky Outcome = "flaky"
	// NotReproduced: the test passes on the bad commit in the sandbox.
	NotReproduced Outcome = "not_reproduced"
	// BadUnbuildable: the bad commit does not build or the test hangs.
	BadUnbuildable Outcome = "bad_unbuildable"
	// GoodFails: the good commit is not good, so there is no range to search.
	GoodFails Outcome = "good_fails"
)

// CheckFunc builds and tests sha in the given worker's workspace.
type CheckFunc func(ctx context.Context, worker int, sha string) (tester.Outcome, error)

type Options struct {
	// Runs is how many times the test is run on the bad commit.
	Runs int
	// Workers is how many runs happen at once.
	Workers int
	Emit    *events.Emitter
}

type Result struct {
	Outcome Outcome
	// Runs and Failures count completed runs on the bad commit. Runs can be
	// lower than requested: the stage stops as soon as the answer is known.
	Runs     int
	Failures int
	Good     events.Verdict
	// FailureOutput is the test output of one failing run on the bad commit.
	FailureOutput string
	Duration      time.Duration
}

// Proceed reports whether the pipeline should carry on to find the culprit.
func (r Result) Proceed() bool { return r.Outcome == Reproduced }

type job struct {
	sha     string
	attempt int
	good    bool
}

type done struct {
	job job
	out tester.Outcome
	err error
}

// Run tests the bad commit opts.Runs times and the good commit once.
func Run(ctx context.Context, good, bad string, check CheckFunc, opts Options) (Result, error) {
	runs, workers := max(opts.Runs, 1), max(opts.Workers, 1)
	emit := func(p events.Payload) { _ = opts.Emit.Emit(events.StageReproduce, p) }
	start := time.Now()

	// Cancelling stops the remaining runs once the answer is known.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The good commit goes second: the first bad run is what tells us
	// whether there is anything to look at.
	jobs := []job{{sha: bad, attempt: 1}, {sha: good, attempt: 1, good: true}}
	for n := 2; n <= runs; n++ {
		jobs = append(jobs, job{sha: bad, attempt: n})
	}

	queue := make(chan job)
	results := make(chan done)
	var wg sync.WaitGroup
	for w := range min(workers, len(jobs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				out, err := check(ctx, w, j.sha)
				results <- done{job: j, out: out, err: err}
			}
		}()
	}
	go func() {
		defer close(queue)
		for _, j := range jobs {
			select {
			case queue <- j:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	res := Result{}
	passes, skips := 0, 0
	var firstErr error
	decided := false
	for d := range results {
		if d.err != nil {
			// Runs cut short by our own cancel are not failures.
			if !decided && firstErr == nil {
				firstErr = d.err
				cancel()
			}
			continue
		}
		if decided {
			continue
		}
		emit(events.ReproduceRun{
			SHA:        d.job.sha,
			Attempt:    d.job.attempt,
			Verdict:    d.out.Verdict,
			DurationMS: d.out.Result.Duration.Milliseconds(),
		})
		if d.job.good {
			res.Good = d.out.Verdict
		} else {
			res.Runs++
			switch d.out.Verdict {
			case events.Fail:
				res.Failures++
				if res.FailureOutput == "" {
					res.FailureOutput = d.out.Result.Output
				}
			case events.Pass:
				passes++
			default:
				skips++
			}
		}
		// Stop early when more runs cannot change the answer.
		if (res.Failures > 0 && passes > 0) || skips > 0 || (res.Good != "" && res.Good != events.Pass) {
			decided = true
			cancel()
		}
	}
	if firstErr != nil {
		return res, firstErr
	}
	// Cancelled from outside (Ctrl-C) before the answer was known.
	if !decided && ctx.Err() != nil {
		return res, ctx.Err()
	}

	switch {
	case skips > 0:
		res.Outcome = BadUnbuildable
	case res.Failures > 0 && passes > 0:
		res.Outcome = Flaky
	case res.Good != "" && res.Good != events.Pass:
		res.Outcome = GoodFails
	case res.Failures == 0:
		res.Outcome = NotReproduced
	default:
		res.Outcome = Reproduced
	}
	res.Duration = time.Since(start)
	emit(events.ReproduceDone{
		Reproduced: res.Outcome == Reproduced,
		Flaky:      res.Outcome == Flaky,
		Runs:       res.Runs,
		Failures:   res.Failures,
		GoodPasses: res.Good == events.Pass,
	})
	return res, nil
}

// Explain returns a one-line reason and a longer diagnosis for an outcome
// that stops the pipeline. hints come from Hints and may be empty.
func (r Result) Explain(test, good, bad string, hints []string) (reason, diagnosis string) {
	short := func(sha string) string { return fmt.Sprintf("%.10s", sha) }
	switch r.Outcome {
	case Flaky:
		reason = "the test is flaky"
		diagnosis = fmt.Sprintf(
			"%q failed %d of %d runs on the same commit (%s) with no change to the code. "+
				"A failure that comes and goes on one commit was not introduced by a single later commit, "+
				"so there is no culprit to find and no fix that can be proved.",
			test, r.Failures, r.Runs, short(bad))
		if len(hints) > 0 {
			diagnosis += "\n\nLikely sources of the flakiness:\n- " + strings.Join(hints, "\n- ")
		}
		diagnosis += "\n\nSuggested next step: make the test deterministic (for example, inject the random or time source), then run heall again."
	case NotReproduced:
		reason = "the failure does not reproduce"
		diagnosis = fmt.Sprintf(
			"%q passed %d of %d runs on %s in the sandbox. The failure seen in CI depends on something "+
				"the sandbox does not have, such as an environment variable, a network service or leftover state.",
			test, r.Runs, r.Runs, short(bad))
	case BadUnbuildable:
		reason = "the failing commit cannot be tested"
		diagnosis = fmt.Sprintf("%s does not build in the sandbox, or the test timed out, so the failure cannot be confirmed.", short(bad))
	case GoodFails:
		reason = "the good commit is not good"
		diagnosis = fmt.Sprintf(
			"%q does not pass on %s (%s), so the failure is older than the range given. Choose an older good commit.",
			test, short(good), r.Good)
	}
	return reason, diagnosis
}

// Patterns that make a test's result depend on something other than the
// code. They are matched against the test and the files it imports.
var nondeterminism = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`\bMath\.random\b|\bcrypto\.random|\brandom\(\)`), "random numbers"},
	{regexp.MustCompile(`\bDate\.now\b|\bnew Date\(\)|\bperformance\.now\b`), "the current time"},
	{regexp.MustCompile(`\bsetTimeout\b|\bsetInterval\b`), "timers"},
	{regexp.MustCompile(`\bfetch\(|\bhttps?\.request\b|\bnet\.connect\b`), "the network"},
}

// Hints points at code that could make a test flaky. sources maps a file
// path to its content.
func Hints(sources map[string]string) []string {
	var out []string
	files := make([]string, 0, len(sources))
	for f := range sources {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, file := range files {
		for n, line := range strings.Split(sources[file], "\n") {
			for _, p := range nondeterminism {
				if p.re.MatchString(line) {
					out = append(out, fmt.Sprintf("%s:%d uses %s: %s", file, n+1, p.what, strings.TrimSpace(line)))
					break
				}
			}
		}
	}
	return out
}
