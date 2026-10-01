package sandbox

import (
	"context"
	"errors"
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
