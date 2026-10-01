// Package locate finds the commit that introduced a failure with a parallel
// k-ary bisect: each round tests several commits at once and keeps the one
// segment of the range where the failure begins.
package locate

import (
	"sort"

	"heall/internal/events"
)

// State is the search window over commits indexed oldest first. Lo is the
// newest commit known good and Hi the oldest known bad, so the culprit lies
// in (Lo, Hi]. Skipped holds commits that could not be judged.
//
// The search here is pure: it never touches git or runs a test, which is
// what lets it be tested exhaustively.
type State struct {
	Lo, Hi  int
	Skipped map[int]bool
}

// untested returns the commits strictly inside the window that can still
// tell us something.
func (s State) untested() []int {
	var out []int
	for i := s.Lo + 1; i < s.Hi; i++ {
		if !s.Skipped[i] {
			out = append(out, i)
		}
	}
	return out
}

// Done reports whether no test can narrow the window further.
func (s State) Done() bool {
	return len(s.untested()) == 0
}

// Suspects returns the skipped commits left inside the window. The search
// could not rule them out, so any of them may be the real culprit instead
// of Hi.
func (s State) Suspects() []int {
	var out []int
	for i := s.Lo + 1; i < s.Hi; i++ {
		if s.Skipped[i] {
			out = append(out, i)
		}
	}
	return out
}

// Plan picks up to k commits to test next. They split the remaining
// possibilities into k+1 groups of near-equal size, so each round shrinks
// the window by a factor of k+1 instead of 2. Skipped commits are never
// picked, which is how the search routes around commits that do not build.
func Plan(s State, k int) []int {
	c := s.untested()
	if k < 1 {
		k = 1
	}
	if len(c) <= k {
		return c
	}
	// The culprit is one of len(c)+1 possibilities: a candidate or Hi.
	// Probe i sits at the end of the i-th of k+1 equal groups.
	n := len(c) + 1
	out := make([]int, 0, k)
	for i := 1; i <= k; i++ {
		out = append(out, c[i*n/(k+1)-1])
	}
	return out
}

// Apply narrows the window with the verdicts of one round. consistent is
// false when a commit passed after an older one failed, which means the
// failure does not have a single starting point (for example, a flaky test).
func (s State) Apply(verdicts map[int]events.Verdict) (next State, consistent bool) {
	next = State{Lo: s.Lo, Hi: s.Hi, Skipped: make(map[int]bool, len(s.Skipped))}
	for i := range s.Skipped {
		next.Skipped[i] = true
	}

	idx := make([]int, 0, len(verdicts))
	for i := range verdicts {
		if i > s.Lo && i < s.Hi {
			idx = append(idx, i)
		}
	}
	sort.Ints(idx)

	consistent = true
	for _, i := range idx {
		switch verdicts[i] {
		case events.Fail:
			// The oldest failure bounds the window; nothing newer matters.
			if i < next.Hi {
				next.Hi = i
			}
		case events.Pass:
			if i < next.Hi {
				next.Lo = i
			} else {
				consistent = false
			}
		default:
			next.Skipped[i] = true
		}
	}
	return next, consistent
}
