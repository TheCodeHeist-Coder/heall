package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"heall/internal/gitx/gitxtest"
)

func TestRangeAndDiff(t *testing.T) {
	ctx := context.Background()
	fx := gitxtest.New(t)
	var shas []string
	for _, v := range []string{"one", "two", "three", "four"} {
		fx.Write("value.txt", v+"\n")
		shas = append(shas, fx.Commit("set "+v))
	}

	repo, err := Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}

	good, err := repo.Resolve(ctx, "HEAD~3")
	if err != nil || good != shas[0] {
		t.Fatalf("Resolve(HEAD~3) = %q, %v; want %q", good, err, shas[0])
	}
	if _, err := repo.Resolve(ctx, "no-such-branch"); err == nil {
		t.Error("Resolve accepted an unknown ref")
	}

	commits, err := repo.Range(ctx, shas[0], shas[3])
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 4 {
		t.Fatalf("got %d commits, want 4 (both ends included)", len(commits))
	}
	for i, c := range commits {
		if c.SHA != shas[i] {
			t.Errorf("commit %d is %s, want %s (oldest first)", i, c.SHA, shas[i])
		}
	}
	if c := commits[1]; c.Subject != "set two" || c.Author != "Test" || c.Date == "" {
		t.Errorf("metadata not parsed: %+v", c)
	}

	if _, err := repo.Range(ctx, shas[3], shas[0]); err == nil || !strings.Contains(err.Error(), "not an ancestor") {
		t.Errorf("reversed range: got %v, want a not-an-ancestor error", err)
	}

	diff, err := repo.Diff(ctx, shas[2])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-two") || !strings.Contains(diff, "+three") || strings.Contains(diff, "set three") {
		t.Errorf("Diff should be the patch without the message:\n%s", diff)
	}
}

func TestOpenRejectsNonRepo(t *testing.T) {
	if _, err := Open(context.Background(), t.TempDir()); err == nil {
		t.Error("Open accepted a directory that is not a repository")
	}
}

func TestWorktreesAreIndependent(t *testing.T) {
	ctx := context.Background()
	fx := gitxtest.New(t)
	fx.Write("value.txt", "one\n")
	first := fx.Commit("one")
	fx.Write("value.txt", "two\n")
	second := fx.Commit("two")

	repo, err := Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	read := func(dir string) string {
		b, err := os.ReadFile(filepath.Join(dir, "value.txt"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}

	// Worktrees are created by concurrent workers in the real engine.
	dirs := make([]string, 6)
	var wg sync.WaitGroup
	for i := range dirs {
		dirs[i] = filepath.Join(root, "w"+string(rune('0'+i)))
		wg.Add(1)
		go func() {
			defer wg.Done()
			sha := first
			if i%2 == 1 {
				sha = second
			}
			if err := repo.AddWorktree(ctx, dirs[i], sha); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	if read(dirs[0]) != "one" || read(dirs[1]) != "two" {
		t.Fatalf("worktrees hold %q and %q, want one and two", read(dirs[0]), read(dirs[1]))
	}

	// Checkout must move the worktree and wipe what a test run left behind.
	if err := os.WriteFile(filepath.Join(dirs[0], "leftover.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirs[0], "value.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.Checkout(ctx, dirs[0], second); err != nil {
		t.Fatal(err)
	}
	if read(dirs[0]) != "two" {
		t.Errorf("after Checkout the worktree holds %q, want two", read(dirs[0]))
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "leftover.tmp")); !os.IsNotExist(err) {
		t.Error("Checkout left an untracked file behind")
	}
	if read(fx.Dir) != "two" || fx.Git("status", "--porcelain") != "" {
		t.Error("the main checkout was disturbed")
	}

	for _, dir := range dirs {
		if err := repo.RemoveWorktree(ctx, dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.PruneWorktrees(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(fx.Git("worktree", "list"), "\n"); got != 0 {
		t.Errorf("%d worktrees left after cleanup", got)
	}
}

func TestSnapshotReadsACommitWithoutCheckingItOut(t *testing.T) {
	ctx := context.Background()
	fx := gitxtest.New(t)
	fx.Write("src/a.js", "export const a = 1; // marker-one\n")
	old := fx.Commit("add a")
	fx.Write("src/a.js", "export const a = 2;\n")
	fx.Write("test/a.test.js", "test(\"a is two\", () => {});\n")
	fx.Commit("change a")

	repo, err := Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Snapshot(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Exists("src/a.js") || snap.Exists("test/a.test.js") || snap.Exists("src") {
		t.Error("Exists should reflect the files of that commit only")
	}
	if content, err := snap.Read("src/a.js"); err != nil || !strings.Contains(content, "a = 1") {
		t.Errorf("Read = %q, %v; want the old content", content, err)
	}
	if _, err := snap.Read("test/a.test.js"); err == nil {
		t.Error("Read returned a file that does not exist at that commit")
	}

	head, err := repo.Snapshot(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	found, err := head.Find(`"a is two"`)
	if err != nil || len(found) != 1 || found[0] != "test/a.test.js" {
		t.Errorf("Find = %v, %v; want test/a.test.js", found, err)
	}
	if found, err := head.Find("marker-one"); err != nil || len(found) != 0 {
		t.Errorf("Find = %v, %v; text from an older commit must not match", found, err)
	}
}
