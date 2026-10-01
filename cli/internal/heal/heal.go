// Package heal runs the agent and decides what its answer is worth. The
// agent is a separate Python process; this package starts it, relays its
// events, and checks any fix it claims with heall's own verifier before
// believing it.
package heal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"heall/internal/agentio"
	"heall/internal/config"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/sandbox"
	"heall/internal/verify"
)

// Outcomes of the heal stage.
const (
	Fixed     = "fixed"
	Escalated = "escalated"
)

type Healer struct {
	Repo   *gitx.Repo
	Cfg    config.Config
	Runner sandbox.Runner
	Emit   *events.Emitter
	// ConfigPath is passed on to the agent's callbacks; empty means the
	// .heall.yaml in the repository.
	ConfigPath string
	// Command starts the agent; nil means AgentCommand().
	Command []string
	// Env is added to the agent's environment.
	Env []string
	// HeallBin is the CLI the agent calls back into; empty means this program.
	HeallBin string
}

type Input struct {
	RunID        string
	Good, Bad    string
	Failure      agentio.Failure
	SuspectFiles []string
	Culprit      agentio.Culprit
	// InjectBadPatch makes the agent first submit a patch that cheats, to
	// show the guardrails rejecting it. It gets an extra attempt for that.
	InjectBadPatch bool
}

type Result struct {
	// Outcome is Fixed or Escalated.
	Outcome  string
	Attempts int
	// RootCause is the agent's explanation; for an escalation, its diagnosis.
	RootCause string
	// Reason says, in one line, why the stage escalated.
	Reason string
	// Patch is the verified fix as git sees it. Empty unless Outcome is Fixed.
	Patch string
}

// AgentCommand works out how to start the agent: $HEALL_AGENT_CMD if set,
// otherwise the Python package in the agent directory next to this program.
func AgentCommand() ([]string, []string, error) {
	if custom := strings.Fields(os.Getenv("HEALL_AGENT_CMD")); len(custom) > 0 {
		return custom, nil, nil
	}
	var candidates []string
	if dir := os.Getenv("HEALL_AGENT_DIR"); dir != "" {
		candidates = append(candidates, dir)
	}
	if exe, err := os.Executable(); err == nil {
		// bin/heall sits beside agent/ in the project.
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", "agent"))
	}
	candidates = append(candidates, "agent")
	for _, dir := range candidates {
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(abs, "heall_agent", "__main__.py")); err == nil {
			env := []string{"PYTHONPATH=" + abs + string(os.PathListSeparator) + os.Getenv("PYTHONPATH")}
			return []string{"python3", "-m", "heall_agent"}, env, nil
		}
	}
	return nil, nil, errors.New("cannot find the heal agent; set HEALL_AGENT_DIR to the directory that holds heall_agent")
}

// Run starts the agent on one failure and returns a fix heall has verified
// itself, or an escalation.
func (h Healer) Run(ctx context.Context, in Input) (Result, error) {
	command, env := h.Command, h.Env
	if command == nil {
		c, e, err := AgentCommand()
		if err != nil {
			return Result{}, err
		}
		command, env = c, append(e, env...)
	}
	self := h.HeallBin
	if self == "" {
		exe, err := os.Executable()
		if err != nil {
			return Result{}, err
		}
		self = exe
	}

	// The agent reads the failing commit from a checkout of its own.
	worktree, remove, err := h.Repo.TempWorktree(ctx, in.Bad)
	if err != nil {
		return Result{}, err
	}
	defer remove()

	attempts := h.Cfg.Heal.MaxAttempts
	if in.InjectBadPatch {
		attempts++
	}
	req := agentio.HealRequest{
		V:              events.Version,
		RunID:          in.RunID,
		RepoDir:        h.Repo.Dir,
		Worktree:       worktree,
		HeallBin:       self,
		ConfigPath:     h.ConfigPath,
		Good:           in.Good,
		Bad:            in.Bad,
		Failure:        in.Failure,
		SuspectFiles:   append([]string{}, in.SuspectFiles...),
		Culprit:        in.Culprit,
		Allow:          h.Cfg.Allow,
		Protect:        h.Cfg.Protect,
		MaxAttempts:    attempts,
		Model:          h.Cfg.Heal.Model,
		InjectBadPatch: in.InjectBadPatch,
	}
	state, err := h.Repo.StateDir(ctx)
	if err != nil {
		return Result{}, err
	}
	requestPath := filepath.Join(state, "runs", in.RunID, "request.json")
	raw, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(requestPath), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(requestPath, raw, 0o644); err != nil {
		return Result{}, err
	}

	done, err := h.runAgent(ctx, append(append([]string{}, command...), "heal", "--request", requestPath), env)
	if err != nil {
		return Result{}, err
	}
	res := Result{Outcome: done.Outcome, Attempts: done.Attempts, RootCause: done.RootCause, Reason: done.Reason}
	if done.Outcome != Fixed {
		res.Outcome = Escalated
		if res.Reason == "" {
			res.Reason = "the agent could not find a fix"
		}
		return res, nil
	}

	// The agent says it fixed it. Check that claim from scratch.
	final, err := verify.Verifier{Repo: h.Repo, Cfg: h.Cfg, Runner: h.Runner}.Run(ctx, verify.Request{
		RunID: in.RunID, Bad: in.Bad, Test: in.Failure.TestName, Final: true,
	}, done.Patch)
	if err != nil {
		return Result{}, err
	}
	if final.Status != verify.Verified {
		_ = h.Emit.Emit(events.StageHeal, events.Log{Level: "error", Message: "the agent's fix did not pass heall's final check: " + final.Output})
		res.Outcome = Escalated
		res.Reason = "the agent's fix did not pass heall's own final check"
		res.RootCause = strings.TrimSpace(res.RootCause + "\n\nFinal check: " + final.Output)
		return res, nil
	}
	_ = h.Emit.Emit(events.StageHeal, events.Log{Level: "info", Message: "final check passed: the fix was verified again in a fresh sandbox"})
	res.Patch = final.Patch
	return res, nil
}

// runAgent runs the agent to completion, relaying its events, and returns
// its closing agent_done payload.
func (h Healer) runAgent(parent context.Context, argv, env []string) (events.AgentDone, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(h.Cfg.Heal.TimeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(append(os.Environ(), "PYTHONUNBUFFERED=1"), env...)
	// Its own process group, so stopping the agent also stops a callback it
	// is waiting on.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 20 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return events.AgentDone{}, err
	}
	if err := cmd.Start(); err != nil {
		return events.AgentDone{}, fmt.Errorf("start the heal agent (%s): %w", argv[0], err)
	}

	var done *events.AgentDone
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg agentio.AgentLine
		if err := json.Unmarshal(line, &msg); err != nil {
			_ = h.Emit.Emit(events.StageHeal, events.Log{Level: "warn", Message: "the agent printed a line that is not an event"})
			continue
		}
		// Only the kinds the contract gives the agent, and only well-formed.
		if !agentKinds[msg.Kind] {
			_ = h.Emit.Emit(events.StageHeal, events.Log{Level: "warn", Message: fmt.Sprintf("the agent sent an event it may not send: %s", msg.Kind)})
			continue
		}
		if err := h.Emit.EmitRaw(events.StageHeal, msg.Kind, msg.Data); err != nil {
			_ = h.Emit.Emit(events.StageHeal, events.Log{Level: "warn", Message: "the agent sent a malformed event: " + err.Error()})
			continue
		}
		if msg.Kind == events.KindAgentDone {
			var d events.AgentDone
			if json.Unmarshal(msg.Data, &d) == nil {
				done = &d
			}
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if parent.Err() != nil {
		return events.AgentDone{}, parent.Err()
	}
	if ctx.Err() != nil {
		return events.AgentDone{}, fmt.Errorf("the heal agent did not finish within %d seconds", h.Cfg.Heal.TimeoutSeconds)
	}
	if waitErr != nil {
		return events.AgentDone{}, fmt.Errorf("the heal agent failed: %s", lastLines(stderr.String(), waitErr))
	}
	if done == nil {
		return events.AgentDone{}, errors.New("the heal agent exited without reporting a result")
	}
	return *done, nil
}

var agentKinds = map[events.Kind]bool{
	events.KindAgentStarted:     true,
	events.KindAgentThought:     true,
	events.KindToolCall:         true,
	events.KindToolResult:       true,
	events.KindPatchSubmitted:   true,
	events.KindGuardrailChecked: true,
	events.KindVerifyDone:       true,
	events.KindAgentDone:        true,
	events.KindLog:              true,
}

func lastLines(stderr string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return err.Error()
	}
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n")
}
