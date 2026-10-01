package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"heall/internal/config"
	"heall/internal/console"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/sandbox"
	"heall/internal/tester"
)

// ExitEscalated is the exit code when heall stopped on purpose because it
// could not prove something, as opposed to failing with an error (1).
const ExitEscalated = 3

// Escalation is returned by a command that stopped the pipeline with an
// explanation. It has already been reported on the event stream.
type Escalation struct {
	Stage     events.Stage
	Reason    string
	Diagnosis string
}

func (e *Escalation) Error() string {
	return fmt.Sprintf("escalated at %s: %s", e.Stage, e.Reason)
}

// commonFlags are shared by the commands that run a stage.
type commonFlags struct {
	sandboxMode string
	eventsPath  string
	asJSON      bool
}

func (f *commonFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.sandboxMode, "sandbox", "", "override sandbox.mode: docker or local")
	cmd.Flags().StringVar(&f.eventsPath, "events", "", "also write the event stream to this file as JSONL")
	cmd.Flags().BoolVar(&f.asJSON, "json", false, "print the result as JSON on stdout")
}

// session is what every stage command needs: the repository, its config, a
// sandbox, and an event stream that is also printed for people.
type session struct {
	repo    *gitx.Repo
	cfg     config.Config
	runner  sandbox.Runner
	emitter *events.Emitter
	printer *console.Printer
	flags   commonFlags
	out     io.Writer
	closers []func()
}

func openSession(cmd *cobra.Command, flags commonFlags) (*session, error) {
	ctx := cmd.Context()
	repo, err := gitx.Open(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(repo.Dir, configPath)
	if err != nil {
		return nil, err
	}
	if flags.sandboxMode != "" {
		cfg.Sandbox.Mode = flags.sandboxMode
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	runner, err := sandbox.New(cfg.Sandbox)
	if err != nil {
		return nil, err
	}

	s := &session{repo: repo, cfg: cfg, runner: runner, flags: flags, out: cmd.OutOrStdout()}
	s.closers = append(s.closers, func() {
		if err := runner.Close(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: sandbox cleanup: %v\n", err)
		}
	})
	// With --json, stdout carries only the result.
	human := cmd.OutOrStdout()
	if flags.asJSON {
		human = cmd.ErrOrStderr()
	}
	var sinks []io.Writer
	if flags.eventsPath != "" {
		f, err := os.Create(flags.eventsPath)
		if err != nil {
			return nil, err
		}
		s.closers = append(s.closers, func() { f.Close() })
		sinks = append(sinks, f)
	}
	s.emitter = events.NewEmitter(newRunID(), sinks...)
	s.printer = console.New(human)
	s.emitter.Listen(s.printer.Handle)
	return s, nil
}

// close runs after the command's own deferred cleanup, so the sandbox is
// swept only once every run has stopped.
func (s *session) close() {
	for _, fn := range s.closers {
		fn()
	}
}

func (s *session) tester() tester.Tester {
	return tester.Tester{Cfg: s.cfg, Runner: s.runner}
}

// stage wraps fn in stage_started and stage_done events. An Escalation from
// fn is reported on the stream before it is returned.
func (s *session) stage(stage events.Stage, fn func() error) error {
	start := time.Now()
	_ = s.emitter.Emit(stage, events.StageStarted{})
	err := fn()
	status := "ok"
	if esc, ok := err.(*Escalation); ok {
		status = "escalated"
		_ = s.emitter.Emit(stage, events.Escalated{Reason: esc.Reason, Diagnosis: esc.Diagnosis})
	} else if err != nil {
		status = "error"
	}
	_ = s.emitter.Emit(stage, events.StageDone{Status: status, DurationMS: time.Since(start).Milliseconds()})
	return err
}

// printJSON writes the result on stdout when --json was given.
func (s *session) printJSON(v any) error {
	if !s.flags.asJSON {
		return nil
	}
	enc := json.NewEncoder(s.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newRunID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "r-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}
