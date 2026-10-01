// Package cmd holds the Cobra commands. Commands only parse flags and call
// into internal/; all real logic lives there.
package cmd

import (
	"fmt"

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
	SilenceErrors: false,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&repoDir, "repo", ".", "path to the repository to heal")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "path to .heall.yaml (default: <repo>/.heall.yaml)")

	rootCmd.AddCommand(
		stub("run", "Run the full pipeline: triage, reproduce, locate, heal, deliver", 7, false),
		stub("triage", "Parse a test log into structured failure info", 4, false),
		stub("reproduce", "Confirm the failure reproduces in the sandbox and is not flaky", 4, false),
		stub("locate", "Find the culprit commit with a parallel bisect", 3, false),
		stub("heal", "Ask the agent for a fix and verify it against the guardrails", 6, false),
		stub("pr", "Open a draft pull request with the patch and evidence", 7, false),
		// Called by the Python agent, not by people.
		stub("_runtest", "Run a test in the sandbox and print the result as JSON", 5, true),
		stub("_verify", "Check a patch against the guardrails and tests and print the result as JSON", 5, true),
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
