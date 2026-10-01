// Package cmd holds the Cobra commands. Commands only parse flags and call
// into internal/; all real logic lives there.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var (
	repoDir    string
	configPath string
)

var rootCmd = &cobra.Command{
	Use:           "heall",
	Short:         "Self-healing CI agent: find the culprit, prove the fix, or escalate",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// ExitCode maps the error from Execute to the process exit code.
func ExitCode(err error) int {
	var esc *Escalation
	switch {
	case err == nil:
		return 0
	case errors.As(err, &esc):
		return ExitEscalated
	}
	return 1
}

// Execute runs the CLI. Ctrl-C cancels the command's context, so stages stop
// and clean up their worktrees and containers instead of being killed.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := rootCmd.ExecuteContext(ctx)
	var esc *Escalation
	switch {
	case err == nil:
	case errors.As(err, &esc):
		// Already explained on the event stream.
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, "heall: interrupted; worktrees and containers were cleaned up")
	default:
		fmt.Fprintln(os.Stderr, "heall:", err)
	}
	return err
}

func init() {
	rootCmd.PersistentFlags().StringVar(&repoDir, "repo", ".", "path to the repository to heal")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "path to .heall.yaml (default: <repo>/.heall.yaml)")

	rootCmd.AddCommand(
		stub("run", "Run the full pipeline: triage, reproduce, locate, heal, deliver", 7, false),
		newTriageCmd(),
		newReproduceCmd(),
		newLocateCmd(),
		newHealCmd(),
		stub("pr", "Open a draft pull request with the patch and evidence", 7, false),
		// Called by the Python agent, not by people.
		newRunTestCmd(),
		newVerifyCmd(),
	)
}

// stub registers a command whose implementation lands in a later build step.
func stub(use, short string, step int, hidden bool) *cobra.Command {
	return &cobra.Command{
		Use:    use,
		Short:  short,
		Hidden: hidden,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%s is not implemented yet (build step %d)", cmd.Name(), step)
		},
	}
}
