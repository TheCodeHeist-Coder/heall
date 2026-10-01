package locate

import (
	"math/rand"
	"testing"

	"heall/internal/events"
)

// simulate runs the search against a history of n commits where commit
// `culprit` is the first to fail and the commits in skipped cannot be built.
func simulate(t *testing.T, n, culprit, k int, skipped map[int]bool) (State, int) {
	t.Helper()
	s := State{Lo: 0, Hi: n - 1, Skipped: map[int]bool{}}
	tested := map[int]bool{}
	rounds := 0
	for !s.Done() {
		rounds++
		if rounds > n {
			t.Fatalf("n=%d culprit=%d k=%d: search does not terminate", n, culprit, k)
		}
		probes := Plan(s, k)
		if len(probes) == 0 || len(probes) > k {
			t.Fatalf("n=%d culprit=%d k=%d: planned %d probes", n, culprit, k, len(probes))
		}
		verdicts := map[int]events.Verdict{}
		for _, p := range probes {
			if p <= s.Lo || p >= s.Hi {
				t.Fatalf("probe %d is outside the window (%d, %d)", p, s.Lo, s.Hi)
			}
			if tested[p] {
				t.Fatalf("n=%d culprit=%d k=%d: commit %d tested twice", n, culprit, k, p)
			}
			tested[p] = true
			switch {
			case skipped[p]:
				verdicts[p] = events.Skipped
			case p >= culprit:
				verdicts[p] = events.Fail
			default:
				verdicts[p] = events.Pass
			}
		}
		next, consistent := s.Apply(verdicts)
		if !consistent {
			t.Fatalf("n=%d culprit=%d k=%d: monotonic history reported as inconsistent", n, culprit, k)
		}
		s = next
	}
	return s, rounds
}

// The search must end with the tightest window the buildable commits allow:
// Hi is the first buildable commit at or after the culprit, Lo the last
// buildable commit before it.
func checkWindow(t *testing.T, s State, n, culprit, k int, skipped map[int]bool) {
	t.Helper()
	wantHi := culprit
	for skipped[wantHi] {
		wantHi++
	}
	wantLo := culprit - 1
	for skipped[wantLo] {
		wantLo--
	}
	if s.Lo != wantLo || s.Hi != wantHi {
		t.Fatalf("n=%d culprit=%d k=%d skipped=%v: window (%d, %d], want (%d, %d]",
			n, culprit, k, skipped, s.Lo, s.Hi, wantLo, wantHi)
	}
	if got := len(s.Suspects()); got != wantHi-wantLo-1 {
		t.Fatalf("n=%d culprit=%d: %d suspects, want %d", n, culprit, got, wantHi-wantLo-1)
	}
}

func TestSearchFindsEveryCulprit(t *testing.T) {
	for n := 2; n <= 90; n++ {
		for k := 1; k <= 8; k++ {
			// Smallest r with (k+1)^r >= n-1: the best any k-way search can do.
			bound, reach := 0, 1
			for reach < n-1 {
				reach *= k + 1
				bound++
			}
			for culprit := 1; culprit < n; culprit++ {
				s, rounds := simulate(t, n, culprit, k, nil)
				checkWindow(t, s, n, culprit, k, nil)
				if rounds > bound {
					t.Fatalf("n=%d culprit=%d k=%d: took %d rounds, optimum is %d", n, culprit, k, rounds, bound)
				}
			}
		}
	}
}

func TestSearchRoutesAroundSkippedCommits(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 4000; trial++ {
		n := 2 + rng.Intn(150)
		k := 1 + rng.Intn(8)
		culprit := 1 + rng.Intn(n-1)
		// The two ends are known to build; anything between may not.
		skipped := map[int]bool{}
		density := rng.Float64() * 0.6
		for i := 1; i < n-1; i++ {
			if rng.Float64() < density {
				skipped[i] = true
			}
		}
		s, _ := simulate(t, n, culprit, k, skipped)
		checkWindow(t, s, n, culprit, k, skipped)
	}
}

func TestDemoRangeTakesThreeRounds(t *testing.T) {
	// 121 commits (good + 120) with 6 workers, as in the demo repository.
	for culprit := 1; culprit <= 120; culprit++ {
		if _, rounds := simulate(t, 121, culprit, 6, nil); rounds > 3 {
			t.Fatalf("culprit %d took %d rounds", culprit, rounds)
		}
	}
	if _, rounds := simulate(t, 121, 53, 1, nil); rounds != 7 {
		t.Errorf("sequential bisect took %d rounds, want 7", rounds)
	}
}

func TestApplyReportsNonMonotonicResults(t *testing.T) {
	s := State{Lo: 0, Hi: 10, Skipped: map[int]bool{}}
	next, consistent := s.Apply(map[int]events.Verdict{3: events.Fail, 6: events.Pass, 8: events.Fail})
	if consistent {
		t.Error("a pass after a failure was reported as consistent")
	}
	if next.Lo != 0 || next.Hi != 3 {
		t.Errorf("window (%d, %d], want (0, 3]: the oldest failure wins", next.Lo, next.Hi)
	}
	if len(s.Skipped) != 0 {
		t.Error("Apply modified the state it was called on")
	}
}
