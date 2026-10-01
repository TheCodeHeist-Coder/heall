package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"heall/internal/pipeline"
)

func newRunCmd() *cobra.Command {
	var (
		flags                  commonFlags
		good, bad, base        string
		logPath, runID         string
		outDir, remote, model  string
		workers                int
		dryRun, injectBadPatch bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the full pipeline: triage, reproduce, locate, heal, deliver",
		Long: `Take a failing build from its log to a delivered result.

  triage     find the failing test in the log
  reproduce  confirm it fails on --bad every time and passes on --good
  locate     find the commit that introduced it, with a parallel bisect
  heal       let the agent propose a fix, and verify it
  deliver    open a draft pull request with the evidence

Every stage either produces evidence for the next or stops the run with an
explanation. A run that stops this way exits with code 3 and delivers a
written diagnosis instead of a fix.

The failing test is read from --log, or from the failed GitHub Actions run
given with --run-id. With neither, the test suite is run on --bad.

With --dry-run nothing is pushed or posted: the patch and the report are
written to the output directory only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if logPath != "" && runID != "" {
				return errors.New("give --log or --run-id, not both")
			}
			s, err := openSession(cmd, flags)
			if err != nil {
				return err
			}
			defer s.close()
			if workers > 0 {
				s.Cfg.Locate.Workers = workers
			}
			if model != "" {
				s.Cfg.Heal.Model = model
			}
			goodSHA, badSHA, err := s.resolve(cmd, good, bad)
			if err != nil {
				return err
			}
			if base == "" {
				base = s.Repo.BranchOf(ctx, bad)
			}
			if outDir == "" {
				outDir = filepath.Join(".heall", s.RunID)
			}
			if outDir, err = filepath.Abs(outDir); err != nil {
				return err
			}
			// Every run is recorded, so it can be replayed in the dashboard.
			if err := s.record(s.Emit, filepath.Join(outDir, "events.jsonl")); err != nil {
				return err
			}

			var log *string
			switch {
			case logPath != "":
				text, err := readLog(cmd, logPath)
				if err != nil {
					return err
				}
				log = &text
			case runID != "":
				text, err := actionsLog(cmd, s.Repo.Dir, runID)
				if err != nil {
					return err
				}
				log = &text
			}

			sum, err := s.Run(ctx, pipeline.RunOptions{
				Good: goodSHA, Bad: badSHA, Base: base, Log: log,
				DryRun: dryRun, InjectBadPatch: injectBadPatch,
				OutDir: outDir, Remote: remote,
			})
			if !flags.asJSON {
				fmt.Fprintf(cmd.OutOrStdout(), "  run files: %s\n", outDir)
			}
			if jsonErr := s.printJSON(sum); jsonErr != nil && err == nil {
				err = jsonErr
			}
			return err
		},
	}
	flags.register(cmd)
	f := cmd.Flags()
	f.StringVar(&good, "good", "", "a commit where the build was green (required)")
	f.StringVar(&bad, "bad", "HEAD", "the commit or branch where the build fails")
	f.StringVar(&logPath, "log", "", "the failing build's test log; \"-\" reads standard input")
	f.StringVar(&runID, "run-id", "", "a failed GitHub Actions run to read the log from (needs gh)")
	f.StringVar(&base, "base", "", "branch the pull request targets (default: --bad, when it is a branch)")
	f.BoolVar(&dryRun, "dry-run", false, "do not push or post anything; write the patch and report to files")
	f.BoolVar(&injectBadPatch, "inject-bad-patch", false, "first submit a patch that skips the test, to show the guardrails rejecting it")
	f.IntVar(&workers, "workers", 0, "commits to test at once (default: locate.workers in .heall.yaml)")
	f.StringVar(&model, "model", "", "Groq model for the agent (default: heal.model in .heall.yaml)")
	f.StringVar(&outDir, "out", "", "directory for the patch, report and event log (default: .heall/<run id>)")
	f.StringVar(&remote, "remote", "origin", "git remote the fix branch is pushed to")
	_ = cmd.MarkFlagRequired("good")
	return cmd
}

// actionsLog fetches the log of the failed jobs of a GitHub Actions run.
func actionsLog(cmd *cobra.Command, repoDir, runID string) (string, error) {
	gh := exec.CommandContext(cmd.Context(), "gh", "run", "view", runID, "--log-failed")
	gh.Dir = repoDir
	gh.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	out, err := gh.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("fetch the log of run %s: %s", runID, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("fetch the log of run %s: %w", runID, err)
	}
	return string(out), nil
}
