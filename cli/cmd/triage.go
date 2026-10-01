package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"heall/internal/events"
	"heall/internal/triage"
	"heall/internal/workspace"
)

func newTriageCmd() *cobra.Command {
	var (
		flags commonFlags
		bad   string
	)
	cmd := &cobra.Command{
		Use:   "triage [log]",
		Short: "Parse a test log into structured failure info",
		Long: `Find the failing test in a test log: its name, file, error and the source
files to look at first.

The log can be a file, or "-" for standard input. It may be raw test output
or a GitHub Actions log. With no log, the test suite is run on --bad in the
sandbox and its output is used.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openSession(cmd, flags)
			if err != nil {
				return err
			}
			defer s.close()

			badSHA, err := s.repo.Resolve(ctx, bad)
			if err != nil {
				return err
			}
			snapshot, err := s.repo.Snapshot(ctx, badSHA)
			if err != nil {
				return err
			}

			var report triage.Report
			err = s.stage(events.StageTriage, func() error {
				var log string
				switch {
				case len(args) == 0:
					if log, err = s.runSuite(cmd, badSHA); err != nil {
						return err
					}
				case args[0] == "-":
					raw, err := io.ReadAll(cmd.InOrStdin())
					if err != nil {
						return err
					}
					log = string(raw)
				default:
					raw, err := os.ReadFile(args[0])
					if err != nil {
						return err
					}
					log = string(raw)
				}

				report, err = triage.Analyze(log, snapshot)
				if errors.Is(err, triage.ErrNoFailure) {
					return &Escalation{
						Stage:     events.StageTriage,
						Reason:    "no failing test found",
						Diagnosis: "The log does not contain a failing test in a format heall understands (Node test runner TAP or spec output).",
					}
				}
				if err != nil {
					return err
				}
				p := report.Primary
				return s.emitter.Emit(events.StageTriage, events.TriageDone{
					TestName:     p.Test,
					TestFile:     p.File,
					SuspectFiles: nonNil(report.SuspectFiles),
					Excerpt:      p.Excerpt,
				})
			})
			if err != nil {
				return err
			}
			if !flags.asJSON && len(report.Failures) > 1 {
				fmt.Fprintf(cmd.OutOrStdout(), "  %d tests failed in this log; heall works on the first:\n", len(report.Failures))
				for _, f := range report.Failures {
					fmt.Fprintf(cmd.OutOrStdout(), "    %s (%s:%d)\n", f.Test, f.File, f.Line)
				}
			}
			return s.printJSON(report)
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&bad, "bad", "HEAD", "the failing commit; file paths in the log are resolved against it")
	return cmd
}

// runSuite runs the whole test suite on sha in the sandbox and returns its
// output, for when there is no CI log to read.
func (s *session) runSuite(cmd *cobra.Command, sha string) (string, error) {
	ctx := cmd.Context()
	if err := s.runner.Prepare(ctx); err != nil {
		return "", err
	}
	pool, err := workspace.New(s.repo, s.tester(), "", 1)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := pool.Close(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree cleanup: %v\n", err)
		}
	}()
	out, err := pool.Outcome(ctx, 0, sha)
	if err != nil {
		return "", err
	}
	switch out.Verdict {
	case events.Pass:
		return "", &Escalation{
			Stage:     events.StageTriage,
			Reason:    "the test suite passes",
			Diagnosis: fmt.Sprintf("Every test passes on %.10s in the sandbox, so there is no failure to work on.", sha),
		}
	case events.Skipped:
		return "", &Escalation{
			Stage:     events.StageTriage,
			Reason:    "the failing commit cannot be tested",
			Diagnosis: fmt.Sprintf("%.10s does not build in the sandbox, or the %s step timed out:\n%s", sha, out.Phase, out.Result.Output),
		}
	}
	return out.Result.Output, nil
}

// nonNil keeps empty lists as [] rather than null in the event stream.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
