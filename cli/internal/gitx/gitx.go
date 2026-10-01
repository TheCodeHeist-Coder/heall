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
	"time"

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

// Snapshot is the set of files in one commit. It lets stages read the
// repository as it was at that commit without checking it out.
type Snapshot struct {
	repo  *Repo
	ctx   context.Context
	ref   string
	files map[string]bool
}

// Snapshot lists the files of ref.
func (r *Repo) Snapshot(ctx context.Context, ref string) (*Snapshot, error) {
	out, err := r.git(ctx, "ls-tree", "-r", "--name-only", "-z", ref)
	if err != nil {
		return nil, err
	}
	files := map[string]bool{}
	for _, name := range strings.Split(out, "\x00") {
		if name != "" {
			files[name] = true
		}
	}
	return &Snapshot{repo: r, ctx: ctx, ref: ref, files: files}, nil
}

func (s *Snapshot) Exists(path string) bool { return s.files[path] }

func (s *Snapshot) Read(path string) (string, error) {
	if !s.files[path] {
		return "", fmt.Errorf("%s does not exist at %.10s", path, s.ref)
	}
	return s.repo.git(s.ctx, "show", s.ref+":"+path)
}

// Find returns the files whose content includes the literal text.
func (s *Snapshot) Find(text string) ([]string, error) {
	out, err := s.repo.git(s.ctx, "grep", "-l", "-z", "--fixed-strings", "-e", text, s.ref)
	if err != nil {
		// git grep exits 1 when nothing matches.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, name := range strings.Split(out, "\x00") {
		if name = strings.TrimPrefix(name, s.ref+":"); name != "" {
			files = append(files, name)
		}
	}
	return files, nil
}

// StateDir is where heall keeps its own files for this repository. It is
// inside git's directory, so it never shows up as a change in a checkout.
func (r *Repo) StateDir(ctx context.Context) (string, error) {
	dir, err := r.git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "heall"), nil
}

// TempWorktree checks sha out into a new temporary directory. The returned
// function removes it and works even after ctx was cancelled.
func (r *Repo) TempWorktree(ctx context.Context, sha string) (dir string, remove func() error, err error) {
	root, err := os.MkdirTemp("", "heall-worktree-")
	if err != nil {
		return "", nil, err
	}
	dir = filepath.Join(root, "w")
	if err := r.AddWorktree(ctx, dir, sha); err != nil {
		os.RemoveAll(root)
		return "", nil, err
	}
	return dir, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return errors.Join(r.RemoveWorktree(ctx, dir), os.RemoveAll(root), r.PruneWorktrees(ctx))
	}, nil
}

// ApplyPatch applies a unified diff to the worktree at dir. It changes
// nothing unless the whole patch applies. Hunk line counts are recomputed,
// since patches written by a model often get them wrong.
func ApplyPatch(ctx context.Context, dir, patch string) error {
	if !strings.HasSuffix(patch, "\n") {
		patch += "\n"
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "--recount", "--whitespace=nowarn", "-")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Stdin = strings.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ApplyError{Output: strings.TrimSpace(string(out))}
	}
	return nil
}

// ApplyError means git could not apply a patch; Output says why.
type ApplyError struct {
	Output string
}

func (e *ApplyError) Error() string { return "patch does not apply: " + e.Output }

// ChangedFiles lists every path in the worktree at dir that differs from
// its commit: modified, added, deleted, renamed (both names), untracked and
// ignored files alike.
func ChangedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := run(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored")
	if err != nil {
		return nil, err
	}
	var paths []string
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, strings.TrimSuffix(e[3:], "/"))
		// A rename or copy is followed by the path it came from.
		if (e[0] == 'R' || e[0] == 'C' || e[1] == 'R' || e[1] == 'C') && i+1 < len(entries) {
			i++
			paths = append(paths, entries[i])
		}
	}
	return paths, nil
}

// WorktreeDiff returns everything that differs from the commit in the
// worktree at dir, as one patch. Unlike the patch that was applied, it is
// what git itself says changed.
func WorktreeDiff(ctx context.Context, dir string) (string, error) {
	if _, err := run(ctx, dir, "add", "--all", "--force"); err != nil {
		return "", err
	}
	diff, err := run(ctx, dir, "diff", "--cached", "--no-color", "HEAD")
	if err != nil {
		return "", err
	}
	return diff + "\n", nil
}

// Message returns the full commit message of sha.
func (r *Repo) Message(ctx context.Context, sha string) (string, error) {
	return r.git(ctx, "log", "-1", "--format=%B", sha)
}
