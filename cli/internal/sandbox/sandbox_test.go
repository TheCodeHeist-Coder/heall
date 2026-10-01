package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalReportsOutputAndExitCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l := &Local{Timeout: 10 * time.Second}

	res, err := l.Run(context.Background(), dir, []string{"sh", "-c", "ls; echo oops >&2; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.TimedOut {
		t.Errorf("exit=%d timedOut=%v, want 3 and false", res.ExitCode, res.TimedOut)
	}
	if !strings.Contains(res.Output, "marker") || !strings.Contains(res.Output, "oops") {
		t.Errorf("output should hold stdout from the commit's directory and stderr, got %q", res.Output)
	}

	if _, err := l.Run(context.Background(), dir, []string{"no-such-binary-heall"}); err == nil {
		t.Error("a command that cannot start should be an error, not a verdict")
	}
}

func TestLocalTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	l := &Local{Timeout: 300 * time.Millisecond}

	// The child would write the file after the timeout if it survived.
	start := time.Now()
	res, err := l.Run(context.Background(), dir, []string{"sh", "-c", "(sleep 2; touch survived) & sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Errorf("timedOut=%v exit=%d, want true and -1", res.TimedOut, res.ExitCode)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %v to take effect", elapsed)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "survived")); err == nil {
		t.Error("a background child outlived the timeout")
	}
}

func TestCancelIsAnErrorNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	l := &Local{Timeout: 30 * time.Second}
	_, err := l.Run(ctx, t.TempDir(), []string{"sleep", "30"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled so a Ctrl-C is not read as a verdict", err)
	}
}

func TestCaptureKeepsStartAndEnd(t *testing.T) {
	c := &capture{limit: 100}
	c.Write([]byte("START-"))
	for range 100 {
		c.Write([]byte("0123456789"))
	}
	c.Write([]byte("-END"))
	out := c.String()
	if !c.truncated || !strings.HasPrefix(out, "START-") || !strings.HasSuffix(out, "-END") {
		t.Errorf("truncated=%v, output %q", c.truncated, out)
	}
	if !strings.Contains(out, "output truncated") || len(out) > 150 {
		t.Errorf("output is %d bytes and should be about 100 with a truncation note", len(out))
	}

	small := &capture{limit: 100}
	small.Write([]byte("short"))
	if small.truncated || small.String() != "short" {
		t.Errorf("short output was altered: %q", small.String())
	}
}

const testImage = "node:24-alpine"

func dockerOrSkip(t *testing.T) *Docker {
	t.Helper()
	if testing.Short() {
		t.Skip("docker tests are skipped in -short mode")
	}
	if exec.Command("docker", "image", "inspect", testImage).Run() != nil {
		t.Skipf("docker or the %s image is not available", testImage)
	}
	d := NewDocker(testImage, 30*time.Second)
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}

func leftovers(t *testing.T, d *Docker) string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "--all", "--format", "{{.Names}} {{.Status}}",
		"--filter", "label="+sessionLabel+"="+d.session).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestDockerIsolatesTheCommand(t *testing.T) {
	d := dockerOrSkip(t)
	ctx := context.Background()
	if err := d.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := d.Run(ctx, dir, []string{"sh", "-c", "ls; touch written; exit 7"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 || !strings.Contains(res.Output, "marker") {
		t.Errorf("exit=%d output=%q, want 7 and the mounted directory listing", res.ExitCode, res.Output)
	}
	// Files written by the container must be removable by the caller, or
	// worktrees could not be cleaned up.
	if err := os.Remove(filepath.Join(dir, "written")); err != nil {
		t.Errorf("file written in the container cannot be removed: %v", err)
	}

	res, err = d.Run(ctx, dir, []string{"node", "-e",
		`fetch("http://example.com").then(() => process.exit(0), () => process.exit(9))`})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 9 {
		t.Errorf("exit=%d: the container reached the network", res.ExitCode)
	}

	home, _ := os.UserHomeDir()
	res, err = d.Run(ctx, dir, []string{"sh", "-c", "ls " + home})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Errorf("the container can see the host home directory:\n%s", res.Output)
	}
}

func TestDockerTimeoutRemovesTheContainer(t *testing.T) {
	d := dockerOrSkip(t)
	d.Timeout = 1500 * time.Millisecond
	res, err := d.Run(context.Background(), t.TempDir(), []string{"sleep", "60"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Errorf("timedOut=false after %v", res.Duration)
	}
	if left := leftovers(t, d); left != "" {
		t.Errorf("a container was left after the timeout: %s", left)
	}
}

// Cancelling many runs at once, as Reproduce does when it has its answer,
// can stop the docker client before the container it created has started.
func TestDockerCloseSweepsCancelledRuns(t *testing.T) {
	d := dockerOrSkip(t)
	dir := t.TempDir()
	for _, delay := range []time.Duration{20, 60, 120, 200, 300, 450} {
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = d.Run(ctx, dir, []string{"sleep", "30"})
			}()
		}
		time.Sleep(delay * time.Millisecond)
		cancel()
		wg.Wait()
	}
	t.Logf("before Close: %d container(s) left by cancelled runs", len(strings.Split(leftovers(t, d), "\n")))
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if left := leftovers(t, d); left != "" {
		t.Errorf("containers left after Close:\n%s", left)
	}
}

func TestDockerPrepareExplainsMissingImage(t *testing.T) {
	if testing.Short() || exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	d := NewDocker("heall.invalid/no-such-image:none", time.Second)
	err := d.Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pull") {
		t.Errorf("got %v, want a pull error naming the image", err)
	}
}

func TestDockerBoxReusesOneContainer(t *testing.T) {
	d := dockerOrSkip(t)
	ctx := context.Background()
	dir := t.TempDir()
	shared := d.Share(dir)
	box := shared.At("")

	if left := leftovers(t, d); left != "" {
		t.Fatalf("Share started a container before it was needed: %s", left)
	}
	// /tmp is outside the mounted directory, so a file there survives only
	// if the second command runs in the same container.
	res, err := box.Run(ctx, []string{"sh", "-c", "touch /tmp/kept; pwd; id -u"})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("first command: exit=%d err=%v output=%q", res.ExitCode, err, res.Output)
	}
	if !strings.HasPrefix(res.Output, "/work\n") || strings.TrimSpace(strings.TrimPrefix(res.Output, "/work\n")) == "0" {
		t.Errorf("commands should run in /work as the calling user, not root: %q", res.Output)
	}
	res, err = box.Run(ctx, []string{"sh", "-c", "test -e /tmp/kept; exit $?"})
	if err != nil || res.ExitCode != 0 {
		t.Errorf("the second command did not see the first one's container: exit=%d err=%v", res.ExitCode, err)
	}
	res, err = box.Run(ctx, []string{"sh", "-c", "exit 4"})
	if err != nil || res.ExitCode != 4 {
		t.Errorf("exit=%d err=%v, want the command's own exit code 4", res.ExitCode, err)
	}
	if n := len(strings.Split(leftovers(t, d), "\n")); n != 1 {
		t.Errorf("%d containers running for one box, want 1", n)
	}

	// The same isolation as a fresh container.
	res, err = box.Run(ctx, []string{"node", "-e",
		`fetch("http://example.com").then(() => process.exit(0), () => process.exit(9))`})
	if err != nil || res.ExitCode != 9 {
		t.Errorf("exit=%d err=%v: the reused container reached the network", res.ExitCode, err)
	}

	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if left := leftovers(t, d); left != "" {
		t.Errorf("container left after the sandbox was closed: %s", left)
	}
}

func TestDockerBoxTimeoutDiscardsTheContainer(t *testing.T) {
	d := dockerOrSkip(t)
	d.Timeout = 1500 * time.Millisecond
	ctx := context.Background()
	shared := d.Share(t.TempDir())
	defer shared.Close()
	box := shared.At("")

	if _, err := box.Run(ctx, []string{"touch", "/tmp/before"}); err != nil {
		t.Fatal(err)
	}
	res, err := box.Run(ctx, []string{"sleep", "60"})
	if err != nil || !res.TimedOut {
		t.Fatalf("timedOut=%v err=%v, want a timeout", res.TimedOut, err)
	}
	// The hung command must not keep running next to the next commit's
	// tests, so the box starts over with a new container.
	res, err = box.Run(ctx, []string{"sh", "-c", "test ! -e /tmp/before && ! pgrep sleep 60"})
	if err != nil || res.ExitCode != 0 {
		t.Errorf("after a timeout the box reused the old container: exit=%d err=%v output=%q", res.ExitCode, err, res.Output)
	}
}

func TestFreshBoxStartsOverEveryTime(t *testing.T) {
	d := dockerOrSkip(t)
	ctx := context.Background()
	box := Fresh(d, t.TempDir())
	if _, err := box.Run(ctx, []string{"touch", "/tmp/kept"}); err != nil {
		t.Fatal(err)
	}
	res, err := box.Run(ctx, []string{"test", "-e", "/tmp/kept"})
	if err != nil || res.ExitCode == 0 {
		t.Errorf("exit=%d err=%v: a fresh box kept state between commands", res.ExitCode, err)
	}
}

func TestDockerSharedRunsWorkersSideBySide(t *testing.T) {
	d := dockerOrSkip(t)
	ctx := context.Background()
	root := t.TempDir()
	const workers = 6
	for w := range workers {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("w%d", w)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shared := d.Share(root)
	defer shared.Close()

	// Every worker sleeps for a second in its own directory. Side by side
	// that takes about a second; one after another it would take six.
	start := time.Now()
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := shared.At(fmt.Sprintf("w%d", w)).Run(ctx, []string{"sh", "-c", "sleep 1; pwd; touch mine"})
			if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Output) != fmt.Sprintf("/work/w%d", w) {
				t.Errorf("worker %d: exit=%d err=%v output=%q", w, res.ExitCode, err, res.Output)
			}
		}()
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("six workers took %v: they are not running at the same time", elapsed)
	}
	for w := range workers {
		if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("w%d", w), "mine")); err != nil {
			t.Errorf("worker %d did not run in its own directory: %v", w, err)
		}
	}
	if n := len(strings.Split(leftovers(t, d), "\n")); n != 1 {
		t.Errorf("%d containers for six workers, want one shared container", n)
	}
}

func TestDockerSharedSurvivesAnotherWorkersTimeout(t *testing.T) {
	d := dockerOrSkip(t)
	d.Timeout = 2 * time.Second
	ctx := context.Background()
	root := t.TempDir()
	shared := d.Share(root)
	defer shared.Close()
	if _, err := shared.At("").Run(ctx, []string{"true"}); err != nil {
		t.Fatal(err)
	}

	// One worker hangs and is timed out, which removes the container. The
	// other is in the middle of a command at that moment; it must still
	// come back with its own result, not with a failure caused by the
	// container being taken away.
	var wg sync.WaitGroup
	var hung, steady Result
	var hungErr, steadyErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		hung, hungErr = shared.At("").Run(ctx, []string{"sleep", "60"})
	}()
	go func() {
		defer wg.Done()
		// Started a second later, so it is half way through when the
		// container is removed.
		time.Sleep(time.Second)
		steady, steadyErr = shared.At("").Run(ctx, []string{"sh", "-c", "sleep 1.5; echo done; exit 7"})
	}()
	wg.Wait()
	if hungErr != nil || !hung.TimedOut {
		t.Errorf("hung worker: timedOut=%v err=%v", hung.TimedOut, hungErr)
	}
	if steadyErr != nil || steady.ExitCode != 7 || !strings.Contains(steady.Output, "done") {
		t.Errorf("the other worker lost its result: exit=%d timedOut=%v err=%v output=%q", steady.ExitCode, steady.TimedOut, steadyErr, steady.Output)
	}
}
