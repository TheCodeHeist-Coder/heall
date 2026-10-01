// Package workspace tests commits in git worktrees: separate checkout
// directories that share the repository's objects. Each worker owns one, so
// workers never see each other's files and the user's own checkout is
// untouched.
package workspace

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

type Pool struct {
	repo   *gitx.Repo
	tester tester.Tester
	test   string
	root   string
	// dirs[w] is worker w's worktree, empty until its first commit. Each
	// entry is only touched by its own worker.
	dirs []string
}

// New prepares a pool for up to workers workers. test names the test to
// run; empty runs the whole suite. Call Close when done.
func New(repo *gitx.Repo, t tester.Tester, test string, workers int) (*Pool, error) {
	root, err := os.MkdirTemp("", "heall-worktrees-")
	if err != nil {
		return nil, err
	}
	return &Pool{repo: repo, tester: t, test: test, root: root, dirs: make([]string, workers)}, nil
}

// Root is the directory that holds the worktrees.
func (p *Pool) Root() string { return p.root }

// Outcome checks sha out in the worker's worktree, then builds and tests it.
// A worker must not be used by two callers at once.
func (p *Pool) Outcome(ctx context.Context, worker int, sha string) (tester.Outcome, error) {
	if worker < 0 || worker >= len(p.dirs) {
		return tester.Outcome{}, fmt.Errorf("worker %d out of range", worker)
	}
	dir := p.dirs[worker]
	if dir == "" {
		dir = filepath.Join(p.root, fmt.Sprintf("w%d", worker))
		if err := p.repo.AddWorktree(ctx, dir, sha); err != nil {
			return tester.Outcome{}, err
		}
		p.dirs[worker] = dir
	} else if err := p.repo.Checkout(ctx, dir, sha); err != nil {
		return tester.Outcome{}, err
	}
	return p.tester.Check(ctx, dir, p.test)
}

// Check is Outcome reduced to its verdict; it has the shape the bisect
// engine expects.
func (p *Pool) Check(ctx context.Context, worker int, sha string) (events.Verdict, error) {
	out, err := p.Outcome(ctx, worker, sha)
	if err != nil {
		return "", err
	}
	return out.Verdict, nil
}

// Close removes every worktree. It uses its own context so that cleanup
// still happens after the run was cancelled with Ctrl-C.
func (p *Pool) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var errs []error
	for _, dir := range p.dirs {
		if dir != "" {
			errs = append(errs, p.repo.RemoveWorktree(ctx, dir))
		}
	}
	errs = append(errs, os.RemoveAll(p.root), p.repo.PruneWorktrees(ctx))
	return errors.Join(errs...)
}
