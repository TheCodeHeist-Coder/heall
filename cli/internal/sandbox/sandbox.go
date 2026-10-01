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
	"path"
	"path/filepath"
	"strings"
	"sync"
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
	// Share returns one long-lived sandbox for everything under root. Nothing
	// is started until it is first used.
	Share(root string) Shared
	// Close removes anything the runner left behind. Call it once, after
	// the last Run has returned.
	Close() error
}

// Box runs commands against one directory.
type Box interface {
	Run(ctx context.Context, argv []string) (Result, error)
	Close() error
}

// Shared is one sandbox that many workers use at once, each in its own
// subdirectory of the root it was made for.
//
// Reusing a sandbox saves the cost of starting one per command, which is
// most of the time when a test run is short. The price is that a command
// can see what earlier ones left outside its directory. That is fine for
// bisecting a repository's own history. Code that has not been reviewed,
// such as a patch from the agent, must run in a box from Fresh instead.
type Shared interface {
	// At returns a Box that runs commands in root/sub. Closing that box does
	// nothing; close the Shared.
	At(sub string) Box
	Close() error
}

// Fresh returns a Box that runs every command in a brand-new sandbox.
func Fresh(r Runner, dir string) Box { return freshBox{r, dir} }

type freshBox struct {
	runner Runner
	dir    string
}

func (b freshBox) Run(ctx context.Context, argv []string) (Result, error) {
	return b.runner.Run(ctx, b.dir, argv)
}

func (b freshBox) Close() error { return nil }

func New(cfg config.Sandbox) (Runner, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	switch cfg.Mode {
	case "docker":
		return NewDocker(cfg.Image, timeout), nil
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

func (l *Local) Close() error { return nil }

// Share has nothing to keep alive for local runs.
func (l *Local) Share(root string) Shared { return localShared{l, root} }

type localShared struct {
	local *Local
	root  string
}

func (s localShared) At(sub string) Box { return Fresh(s.local, filepath.Join(s.root, sub)) }
func (s localShared) Close() error      { return nil }

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
	// session labels every container this runner starts, so Close can find
	// the ones a cancelled run left behind.
	session string
}

func NewDocker(image string, timeout time.Duration) *Docker {
	return &Docker{Image: image, Timeout: timeout, session: randomHex(8)}
}

const sessionLabel = "heall.session"

// Close removes containers that outlived their run. Stopping the docker
// client between "create" and "start" leaves a container that never ran and
// that --rm never removes; nothing but a sweep by label catches those.
func (d *Docker) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "--all", "--quiet",
		"--filter", "label="+sessionLabel+"="+d.session).Output()
	if err != nil {
		return fmt.Errorf("list leftover containers: %w", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "docker", append([]string{"rm", "--force"}, ids...)...).CombinedOutput(); err != nil {
		return fmt.Errorf("remove leftover containers: %s", firstLine(out, err))
	}
	return nil
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
	args := append(d.runArgs(name, dir, "1g", "512", "--rm"), d.Image)
	args = append(args, argv...)

	res, err := execute(ctx, d.Timeout, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "docker", args...)
	}, func() {
		// Killing the docker client leaves the container running.
		remove(name)
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

// runArgs is "docker run" with the isolation every heall container gets:
// no network, no capabilities, limited memory and processes, and only dir
// mounted.
func (d *Docker) runArgs(name, dir, memory, pids string, extra ...string) []string {
	args := []string{
		"run", "--name", name,
		"--label", sessionLabel + "=" + d.session,
		"--network", "none",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--memory", memory,
		"--pids-limit", pids,
		// Run as the calling user so files the tests write can be cleaned up.
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--env", "HOME=/tmp",
		"--volume", dir + ":/work",
		"--workdir", "/work",
	}
	return append(args, extra...)
}

func remove(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "rm", "--force", name).Run()
}

func (d *Docker) Share(root string) Shared { return &dockerShared{docker: d, root: root} }

// dockerShared keeps one container running, with root mounted, and sends
// each command to it with "docker exec". Starting one container and reusing
// it is several times cheaper than starting one per command, and cheaper
// than one per worker.
type dockerShared struct {
	docker *Docker
	root   string

	mu sync.Mutex
	// name is the running container, or empty when there is none.
	name string
	// generation counts the containers this sandbox has gone through, so a
	// command can tell that its container was replaced under it.
	generation int
}

// container returns the running container, starting one if needed.
func (s *dockerShared) container(ctx context.Context) (string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.name != "" {
		return s.name, s.generation, nil
	}
	name := "heall-" + randomHex(6)
	// --init gives the container a real init, so "docker rm" stops it at
	// once and processes orphaned by a test are reaped.
	args := append(s.docker.runArgs(name, s.root, "3g", "2048", "--rm", "--detach", "--init"), s.docker.Image, "sleep", "2147483647")
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		remove(name)
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, fmt.Errorf("start sandbox container: %s", firstLine(out, err))
	}
	s.name = name
	return name, s.generation, nil
}

// discard removes the container of the given generation, unless another
// command already did.
func (s *dockerShared) discard(generation int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation || s.name == "" {
		return
	}
	remove(s.name)
	s.name = ""
	s.generation++
}

func (s *dockerShared) current() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

func (s *dockerShared) run(ctx context.Context, sub string, argv []string) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("empty command")
	}
	var res Result
	// A second try covers a container that was taken away mid-command.
	for range 3 {
		name, generation, err := s.container(ctx)
		if err != nil {
			return Result{}, err
		}
		args := append([]string{"exec", "--workdir", path.Join("/work", sub), name}, argv...)
		// A command that is cut short keeps running inside the container, so
		// the container goes with it; the next command starts a new one.
		res, err = execute(ctx, s.docker.Timeout, func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "docker", args...)
		}, func() { s.discard(generation) })
		if err != nil || res.TimedOut {
			return res, err
		}
		if res.ExitCode == 0 {
			return res, nil
		}
		// A failure in a container that has since been replaced says nothing
		// about the command: another worker's timeout removed the container
		// while this command was running in it. Run it again.
		if s.current() != generation {
			continue
		}
		// The container itself is gone or broken: docker's failure, not the
		// command's.
		if strings.HasPrefix(res.Output, "Error response from daemon:") {
			s.discard(generation)
			continue
		}
		return res, nil
	}
	return res, fmt.Errorf("the sandbox container keeps going away: %s", strings.TrimSpace(res.Output))
}

func (s *dockerShared) At(sub string) Box { return sharedBox{s, sub} }

func (s *dockerShared) Close() error {
	s.discard(s.current())
	return nil
}

type sharedBox struct {
	shared *dockerShared
	sub    string
}

func (b sharedBox) Run(ctx context.Context, argv []string) (Result, error) {
	return b.shared.run(ctx, b.sub, argv)
}

func (b sharedBox) Close() error { return nil }

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
