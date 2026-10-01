package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"heall/internal/config"
	"heall/internal/console"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/pipeline"
	"heall/internal/sandbox"
	"heall/internal/verify"
)

// ExitEscalated is the exit code when heall stopped on purpose because it
// could not prove something, as opposed to failing with an error (1).
const ExitEscalated = 3

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
	*pipeline.Pipeline
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

	stderr := cmd.ErrOrStderr()
	warn := func(message string) { fmt.Fprintf(stderr, "warning: %s\n", message) }
	s := &session{flags: flags, out: cmd.OutOrStdout()}
	s.closers = append(s.closers, func() {
		if err := runner.Close(); err != nil {
			warn("sandbox cleanup: " + err.Error())
		}
	})
	// With --json, stdout carries only the result.
	human := cmd.OutOrStdout()
	if flags.asJSON {
		human = stderr
	}
	runID := newRunID()
	emitter := events.NewEmitter(runID)
	if flags.eventsPath != "" {
		if err := s.record(emitter, flags.eventsPath); err != nil {
			return nil, err
		}
	}
	s.printer = console.New(human)
	emitter.Listen(s.printer.Handle)
	s.Pipeline = &pipeline.Pipeline{
		Repo: repo, Cfg: cfg, Runner: runner, Emit: emitter, RunID: runID,
		ConfigPath: absConfigPath(), Warn: warn,
	}
	return s, nil
}

// record also writes the event stream to a file, as JSONL.
func (s *session) record(emitter *events.Emitter, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	s.closers = append(s.closers, func() { f.Close() })
	emitter.Listen(func(ev events.Event) {
		if line, err := json.Marshal(ev); err == nil {
			_, _ = f.Write(append(line, '\n'))
		}
	})
	return nil
}

// close runs after the command's own deferred cleanup, so the sandbox is
// swept only once every run has stopped.
func (s *session) close() {
	for _, fn := range s.closers {
		fn()
	}
}

func (s *session) verifier() verify.Verifier {
	return verify.Verifier{Repo: s.Repo, Cfg: s.Cfg, Runner: s.Runner}
}

// resolve turns the --good and --bad flags into commit hashes.
func (s *session) resolve(cmd *cobra.Command, good, bad string) (string, string, error) {
	goodSHA, err := s.Repo.Resolve(cmd.Context(), good)
	if err != nil {
		return "", "", err
	}
	badSHA, err := s.Repo.Resolve(cmd.Context(), bad)
	return goodSHA, badSHA, err
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

// absConfigPath is the --config flag as an absolute path, for handing to
// the agent, whose callbacks may run in another directory.
func absConfigPath() string {
	if configPath == "" {
		return ""
	}
	if abs, err := filepath.Abs(configPath); err == nil {
		return abs
	}
	return configPath
}

func newRunID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "r-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}
