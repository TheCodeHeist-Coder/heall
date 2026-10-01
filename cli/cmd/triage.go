package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// readLog reads a test log from a file, or from standard input for "-".
func readLog(cmd *cobra.Command, path string) (string, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(cmd.InOrStdin())
	} else {
		raw, err = os.ReadFile(path)
	}
	return string(raw), err
}

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
			badSHA, err := s.Repo.Resolve(ctx, bad)
			if err != nil {
				return err
			}
			var log *string
			if len(args) == 1 {
				text, err := readLog(cmd, args[0])
				if err != nil {
					return err
				}
				log = &text
			} else if err := s.Runner.Prepare(ctx); err != nil {
				return err
			}

			report, err := s.Triage(ctx, badSHA, log)
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
