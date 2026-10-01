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
	"sync"
	"sync/atomic"
	"time"

	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/sandbox"
	"heall/internal/tester"
)

type Pool struct {
	repo   *gitx.Repo
	runner sandbox.Runner
	tester tester.Tester
	test   string
	root   string
	fresh  bool
	// shared is the one sandbox every worker runs in, unless fresh is set.
	shared sandbox.Shared
	slots  []slot
	// closed stops a slot from being set up after Close has swept it.
	closed atomic.Bool
}

var errClosed = errors.New("workspace pool is closed")

// slot is one worker's worktree and the sandbox bound to it.
type slot struct {
	// mu is held while the slot is set up or used, so warming up cannot
	// race with the worker that owns the slot.
	mu  sync.Mutex
	dir string
	box sandbox.Box
}

// New prepares a pool for up to workers workers. test names the test to
// run; empty runs the whole suite. Call Close when done.
//
// The workers share one sandbox that stays alive for the life of the pool,
// each in its own worktree, unless the config asks for a fresh sandbox per
// command.
func New(repo *gitx.Repo, runner sandbox.Runner, t tester.Tester, test string, workers int) (*Pool, error) {
	root, err := os.MkdirTemp("", "heall-worktrees-")
	if err != nil {
		return nil, err
	}
	return &Pool{
		repo:   repo,
		runner: runner,
		tester: t,
		test:   test,
		root:   root,
		fresh:  t.Cfg.Sandbox.FreshPerRun,
		shared: runner.Share(root),
		slots:  make([]slot, workers),
	}, nil
}

// Root is the directory that holds the worktrees.
func (p *Pool) Root() string { return p.root }

// ready puts sha in the slot's worktree, creating the worktree and its
// sandbox on first use. The caller holds s.mu.
func (p *Pool) ready(ctx context.Context, worker int, s *slot, sha string) error {
	if p.closed.Load() {
		return errClosed
	}
	if s.dir != "" {
		return p.repo.Checkout(ctx, s.dir, sha)
	}
	name := fmt.Sprintf("w%d", worker)
	dir := filepath.Join(p.root, name)
	if err := p.repo.AddWorktree(ctx, dir, sha); err != nil {
		return err
	}
	s.dir = dir
	if p.fresh {
		s.box = sandbox.Fresh(p.runner, dir)
	} else {
		s.box = p.shared.At(name)
	}
	return nil
}

// Warm sets up every worker's worktree and sandbox ahead of time, in
// parallel, so the first round does not pay for it. sha is any commit to
// start on. It is an optimisation: errors are left for the worker that hits
// them when it uses the slot.
func (p *Pool) Warm(ctx context.Context, sha string) {
	var wg sync.WaitGroup
	for w := range p.slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &p.slots[w]
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.dir != "" || p.ready(ctx, w, s, sha) != nil {
				return
			}
			// A trivial command starts the sandbox.
			_, _ = s.box.Run(ctx, []string{"true"})
		}()
	}
	wg.Wait()
}

// Outcome checks sha out in the worker's worktree, then builds and tests it.
func (p *Pool) Outcome(ctx context.Context, worker int, sha string) (tester.Outcome, error) {
	if worker < 0 || worker >= len(p.slots) {
		return tester.Outcome{}, fmt.Errorf("worker %d out of range", worker)
	}
	s := &p.slots[worker]
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := p.ready(ctx, worker, s, sha); err != nil {
		return tester.Outcome{}, err
	}
	return p.tester.Check(ctx, s.box, p.test)
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

// Close stops every sandbox and removes every worktree. It uses its own
// context so that cleanup still happens after the run was cancelled with
// Ctrl-C.
func (p *Pool) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p.closed.Store(true)
	errs := make([]error, len(p.slots))
	var wg sync.WaitGroup
	for w := range p.slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &p.slots[w]
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.dir == "" {
				return
			}
			errs[w] = p.repo.RemoveWorktree(ctx, s.dir)
		}()
	}
	wg.Wait()
	errs = append(errs, p.shared.Close(), os.RemoveAll(p.root), p.repo.PruneWorktrees(ctx))
	return errors.Join(errs...)
}
