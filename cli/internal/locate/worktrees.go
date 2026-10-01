package locate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/tester"
)

// Worktrees tests commits in git worktrees: separate checkout directories
// that share the repository's objects. Each worker owns one, so workers
// never see each other's files and the user's own checkout is untouched.
type Worktrees struct {
	repo   *gitx.Repo
	tester tester.Tester
	test   string
	root   string
	// dirs[w] is worker w's worktree, empty until its first commit. Each
	// entry is only touched by its own worker.
	dirs []string
}

// NewWorktrees prepares a checker for up to workers workers. test names the
// test to run; empty runs the whole suite. Call Close when done.
func NewWorktrees(repo *gitx.Repo, t tester.Tester, test string, workers int) (*Worktrees, error) {
	root, err := os.MkdirTemp("", "heall-worktrees-")
	if err != nil {
		return nil, err
	}
	return &Worktrees{repo: repo, tester: t, test: test, root: root, dirs: make([]string, workers)}, nil
}

// Check implements CheckFunc.
func (w *Worktrees) Check(ctx context.Context, worker int, sha string) (events.Verdict, error) {
	if worker < 0 || worker >= len(w.dirs) {
		return "", fmt.Errorf("worker %d out of range", worker)
	}
	dir := w.dirs[worker]
	if dir == "" {
		dir = filepath.Join(w.root, fmt.Sprintf("w%d", worker))
		if err := w.repo.AddWorktree(ctx, dir, sha); err != nil {
			return "", err
		}
		w.dirs[worker] = dir
	} else if err := w.repo.Checkout(ctx, dir, sha); err != nil {
		return "", err
	}
	out, err := w.tester.Check(ctx, dir, w.test)
	if err != nil {
		return "", err
	}
	return out.Verdict, nil
}

// Close removes every worktree. It uses its own context so that cleanup
// still happens after the search was cancelled with Ctrl-C.
func (w *Worktrees) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var errs []error
	for _, dir := range w.dirs {
		if dir != "" {
			errs = append(errs, w.repo.RemoveWorktree(ctx, dir))
		}
	}
	errs = append(errs, os.RemoveAll(w.root), w.repo.PruneWorktrees(ctx))
	return errors.Join(errs...)
}
