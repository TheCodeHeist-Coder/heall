// Package sandbox runs build and test commands against a checked-out commit,
// either in a Docker container or directly on the host.
package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"heall/internal/config"
)

// maxOutput caps how much command output is kept; see capture.
const maxOutput = 512 * 1024

type Result struct {
	// ExitCode is -1 when the command timed out.
	ExitCode  int
	Output    string
	Truncated bool
	Duration  time.Duration
	TimedOut  bool
}

// Runner runs a command with dir as its working directory. A non-zero exit
// or a timeout is reported in Result; the error is for failures of the
// sandbox itself, such as Docker not running.
type Runner interface {
	Name() string
	// Prepare does one-off setup, such as pulling the image.
	Prepare(ctx context.Context) error
	Run(ctx context.Context, dir string, argv []string) (Result, error)
}

func New(cfg config.Sandbox) (Runner, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	switch cfg.Mode {
	case "docker":
		return &Docker{Image: cfg.Image, Timeout: timeout}, nil
	case "local":
		return &Local{Timeout: timeout}, nil
	}
	return nil, fmt.Errorf("unknown sandbox mode %q", cfg.Mode)
}

// execute runs the command built by build under a timeout. onStop is called
// when the command was cut short, to clean up what killing it left behind.
func execute(parent context.Context, timeout time.Duration, build func(context.Context) *exec.Cmd, onStop func()) (Result, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := build(ctx)
	out := &capture{limit: maxOutput}
	cmd.Stdout, cmd.Stderr = out, out
	// Do not wait forever on output pipes held open by orphaned children.
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	err := cmd.Run()
	res := Result{Output: out.String(), Truncated: out.truncated, Duration: time.Since(start)}

	if ctx.Err() != nil {
		if onStop != nil {
			onStop()
		}
		if parent.Err() != nil {
			return res, parent.Err()
		}
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
	default:
		return res, fmt.Errorf("run %s: %w", cmd.Path, err)
	}
	return res, nil
}

// Local runs commands directly on the host. It gives no isolation and exists
// as a fallback for machines without Docker.
type Local struct {
	Timeout time.Duration
}

func (l *Local) Name() string { return "local" }

func (l *Local) Prepare(context.Context) error { return nil }

func (l *Local) Run(ctx context.Context, dir string, argv []string) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("empty command")
	}
	return execute(ctx, l.Timeout, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		// Own process group, so a timeout kills the test's children too.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		return cmd
	}, nil)
}

// Docker runs each command in a fresh container with no network, no
// capabilities and only the commit's directory mounted.
type Docker struct {
	Image   string
	Timeout time.Duration
}

func (d *Docker) Name() string { return "docker (" + d.Image + ")" }

func (d *Docker) Prepare(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput(); err != nil {
		return fmt.Errorf("docker is not available (%s); start Docker or set sandbox.mode to local", firstLine(out, err))
	}
	if exec.CommandContext(ctx, "docker", "image", "inspect", d.Image).Run() == nil {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "docker", "pull", "--quiet", d.Image).CombinedOutput(); err != nil {
		return fmt.Errorf("pull %s: %s", d.Image, firstLine(out, err))
	}
	return nil
}

func (d *Docker) Run(ctx context.Context, dir string, argv []string) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("empty command")
	}
	name := "heall-" + randomHex(6)
	args := []string{
		"run", "--rm", "--name", name,
		"--network", "none",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--memory", "1g",
		"--pids-limit", "512",
		// Run as the calling user so files the tests write can be cleaned up.
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--env", "HOME=/tmp",
		"--volume", dir + ":/work",
		"--workdir", "/work",
		d.Image,
	}
	args = append(args, argv...)

	res, err := execute(ctx, d.Timeout, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "docker", args...)
	}, func() {
		// Killing the docker client leaves the container running.
		stop, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(stop, "docker", "rm", "--force", name).Run()
	})
	if err != nil {
		return res, err
	}
	// 125 is docker's own failure, not the command's.
	if res.ExitCode == 125 {
		return res, fmt.Errorf("docker run failed: %s", strings.TrimSpace(res.Output))
	}
	return res, nil
}

func firstLine(out []byte, err error) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return err.Error()
	}
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// capture keeps the start and the end of a command's output and drops the
// middle once limit is exceeded: the start shows what ran, the end shows how
// it failed.
type capture struct {
	limit     int
	head      []byte
	tail      []byte
	truncated bool
}

func (c *capture) Write(p []byte) (int, error) {
	n := len(p)
	half := c.limit / 2
	if room := half - len(c.head); room > 0 {
		take := min(room, len(p))
		c.head = append(c.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		c.tail = append(c.tail, p...)
		if over := len(c.tail) - half; over > 0 {
			c.tail = append(c.tail[:0], c.tail[over:]...)
			c.truncated = true
		}
	}
	return n, nil
}

func (c *capture) String() string {
	if !c.truncated {
		return string(c.head) + string(c.tail)
	}
	return string(c.head) + "\n... output truncated ...\n" + string(c.tail)
}
