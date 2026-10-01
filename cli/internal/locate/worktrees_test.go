package locate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heall/internal/config"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/gitx/gitxtest"
	"heall/internal/sandbox"
	"heall/internal/tester"
)

// End to end over real git worktrees, with shell commands standing in for a
// build and a test suite so the test needs neither Docker nor Node.
func TestLocateOnARealRepository(t *testing.T) {
	const (
		commits = 60
		culprit = 41
	)
	broken := map[int]bool{12: true, 13: true, 44: true}

	fx := gitxtest.New(t)
	fx.Write("value", "0\n")
	fx.Commit("good")
	for i := 1; i <= commits; i++ {
		fx.Write("value", fmt.Sprintf("%d\n", i))
		if broken[i] {
			fx.Write("broken", "x\n")
		} else if broken[i-1] {
			fx.Git("rm", "-q", "broken")
		}
		fx.Commit(fmt.Sprintf("set value to %d", i))
	}

	ctx := context.Background()
	repo, err := gitx.Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}
	good, _ := repo.Resolve(ctx, fmt.Sprintf("HEAD~%d", commits))
	bad, _ := repo.Resolve(ctx, "HEAD")
	history, err := repo.Range(ctx, good, bad)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		BuildCmd: []string{"sh", "-c", "! test -e broken"},
		TestCmd:  []string{"false"},
		// The test leaves a file behind, which the next checkout must remove.
		TestOneCmd: []string{"sh", "-c", fmt.Sprintf(`touch "ran-{{test}}"; test "$(cat value)" -lt %d`, culprit)},
	}
	const workers = 5
	trees, err := NewWorktrees(repo, tester.Tester{Cfg: cfg, Runner: &sandbox.Local{Timeout: 20 * time.Second}}, "value", workers)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Run(ctx, history, trees.Check, Options{Workers: workers, VerifyEnds: true, Diff: repo.Diff})
	if err != nil {
		t.Fatal(err)
	}
	if res.Index != culprit || res.Culprit.Subject != fmt.Sprintf("set value to %d", culprit) {
		t.Errorf("culprit = #%d %q, want #%d", res.Index, res.Culprit.Subject, culprit)
	}
	if !strings.Contains(res.Diff, fmt.Sprintf("+%d", culprit)) {
		t.Errorf("diff is not the culprit's patch:\n%s", res.Diff)
	}
	for sha, v := range res.Verdicts {
		var n int
		for i, c := range history {
			if c.SHA == sha {
				n = i
			}
		}
		want := events.Pass
		switch {
		case broken[n]:
			want = events.Skipped
		case n >= culprit:
			want = events.Fail
		}
		if v != want {
			t.Errorf("commit #%d judged %s, want %s", n, v, want)
		}
	}

	// A worktree reused for a second commit must not keep the first's files.
	leftovers, _ := filepath.Glob(filepath.Join(trees.root, "*", "ran-*"))
	if len(leftovers) > workers {
		t.Errorf("%d test leftovers in %d worktrees: checkouts are not cleaned", len(leftovers), workers)
	}

	if err := trees.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(trees.root); !os.IsNotExist(err) {
		t.Error("the worktree directory was not removed")
	}
	if list := fx.Git("worktree", "list"); strings.Count(list, "\n") != 0 {
		t.Errorf("worktrees left registered:\n%s", list)
	}
	if fx.Git("status", "--porcelain") != "" || fx.Git("rev-parse", "HEAD") != bad {
		t.Error("the user's checkout was disturbed")
	}
}
