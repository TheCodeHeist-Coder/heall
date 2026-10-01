// Package gitx wraps the git commands heall needs: reading history and
// managing the worktrees that commits are tested in.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"heall/internal/events"
)

type Repo struct {
	Dir string
	// Adding and removing worktrees edits shared files under .git, so those
	// calls are serialised. Work inside separate worktrees is not.
	worktrees sync.Mutex
}

// Open checks that dir is inside a git repository.
func Open(ctx context.Context, dir string) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	top, err := run(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not a git repository: %w", dir, err)
	}
	return &Repo{Dir: top}, nil
}

func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "advice.detachedHead=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &Error{Args: args, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// Error is a failed git command.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	return run(ctx, r.Dir, args...)
}

// Resolve turns a branch, tag or abbreviated hash into a full commit hash.
func (r *Repo) Resolve(ctx context.Context, ref string) (string, error) {
	sha, err := r.git(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("unknown commit %q", ref)
	}
	return sha, nil
}

// IsAncestor reports whether older is an ancestor of newer.
func (r *Repo) IsAncestor(ctx context.Context, older, newer string) (bool, error) {
	_, err := r.git(ctx, "merge-base", "--is-ancestor", older, newer)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

const logFormat = "%H%x1f%s%x1f%an%x1f%aI"

func parseCommit(line string) (events.Commit, error) {
	f := strings.Split(line, "\x1f")
	if len(f) != 4 {
		return events.Commit{}, fmt.Errorf("unexpected git log line %q", line)
	}
	return events.Commit{SHA: f[0], Subject: f[1], Author: f[2], Date: f[3]}, nil
}

// Commit returns the metadata of one commit.
func (r *Repo) Commit(ctx context.Context, sha string) (events.Commit, error) {
	out, err := r.git(ctx, "log", "-1", "--format="+logFormat, sha)
	if err != nil {
		return events.Commit{}, err
	}
	return parseCommit(out)
}

// Range lists the commits from good to bad, oldest first, with both ends
// included. It follows first parents only, so the result is a single line of
// history even when the range contains merges.
func (r *Repo) Range(ctx context.Context, good, bad string) ([]events.Commit, error) {
	ok, err := r.IsAncestor(ctx, good, bad)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("good commit %.10s is not an ancestor of bad commit %.10s", good, bad)
	}
	first, err := r.Commit(ctx, good)
	if err != nil {
		return nil, err
	}
	out, err := r.git(ctx, "log", "--first-parent", "--reverse", "--format="+logFormat, good+".."+bad)
	if err != nil {
		return nil, err
	}
	commits := []events.Commit{first}
	if out == "" {
		return commits, nil
	}
	for _, line := range strings.Split(out, "\n") {
		c, err := parseCommit(line)
		if err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// Diff returns the patch a commit introduced.
func (r *Repo) Diff(ctx context.Context, sha string) (string, error) {
	return r.git(ctx, "show", "--format=", "--patch", "--no-color", sha)
}

// AddWorktree checks sha out, detached, into a new directory at path.
func (r *Repo) AddWorktree(ctx context.Context, path, sha string) error {
	r.worktrees.Lock()
	defer r.worktrees.Unlock()
	_, err := r.git(ctx, "worktree", "add", "--quiet", "--detach", path, sha)
	return err
}

// Checkout moves an existing worktree to sha and discards anything a
// previous test run left behind.
func (r *Repo) Checkout(ctx context.Context, path, sha string) error {
	if _, err := run(ctx, path, "checkout", "--quiet", "--force", "--detach", sha); err != nil {
		return err
	}
	_, err := run(ctx, path, "clean", "-fdxq")
	return err
}

// RemoveWorktree deletes a worktree made by AddWorktree.
func (r *Repo) RemoveWorktree(ctx context.Context, path string) error {
	r.worktrees.Lock()
	defer r.worktrees.Unlock()
	_, err := r.git(ctx, "worktree", "remove", "--force", path)
	return err
}

// PruneWorktrees drops records of worktrees whose directories are gone.
func (r *Repo) PruneWorktrees(ctx context.Context) error {
	r.worktrees.Lock()
	defer r.worktrees.Unlock()
	_, err := r.git(ctx, "worktree", "prune")
	return err
}
