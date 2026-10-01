package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"heall/internal/heal"
)

func newHealCmd() *cobra.Command {
	var (
		flags                    commonFlags
		good, bad, test, culprit string
		model, patchOut          string
		inject                   bool
	)
	cmd := &cobra.Command{
		Use:   "heal",
		Short: "Ask the agent for a fix and verify it against the guardrails",
		Long: `Run the heal agent on one failing test whose culprit commit is known.

The agent reads the code, then submits patches to heall's verifier. heall
accepts a fix only after checking it again itself in a fresh sandbox. If the
agent cannot produce a fix that passes, heall escalates with the agent's
diagnosis and exits with code 3.

The agent needs a Groq API key in GROQ_API_KEY (or several, comma separated,
in GROQ_API_KEYS). A .env file in the working directory is read.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openSession(cmd, flags)
			if err != nil {
				return err
			}
			defer s.close()
			if model != "" {
				s.Cfg.Heal.Model = model
			}
			goodSHA, badSHA, err := s.resolve(cmd, good, bad)
			if err != nil {
				return err
			}
			culpritSHA, err := s.Repo.Resolve(ctx, culprit)
			if err != nil {
				return err
			}
			if err := s.Runner.Prepare(ctx); err != nil {
				return err
			}

			in, err := s.HealInput(ctx, goodSHA, badSHA, culpritSHA, test, "")
			if err != nil {
				return err
			}
			in.InjectBadPatch = inject
			res, err := s.Heal(ctx, in)
			if res.Outcome == heal.Fixed && patchOut != "" {
				if werr := os.WriteFile(patchOut, []byte(res.Patch), 0o644); werr != nil && err == nil {
					err = werr
				}
			}
			if jsonErr := s.printJSON(map[string]any{
				"outcome":    res.Outcome,
				"attempts":   res.Attempts,
				"root_cause": res.RootCause,
				"reason":     res.Reason,
				"patch":      res.Patch,
			}); jsonErr != nil && err == nil {
				err = jsonErr
			}
			return err
		},
	}
	flags.register(cmd)
	f := cmd.Flags()
	f.StringVar(&good, "good", "", "a commit where the test passes (required)")
	f.StringVar(&bad, "bad", "HEAD", "the commit where the test fails")
	f.StringVar(&test, "test", "", "name of the failing test (required)")
	f.StringVar(&culprit, "culprit", "", "the commit that introduced the failure, from `heall locate` (required)")
	f.StringVar(&model, "model", "", "Groq model to use (default: heal.model in .heall.yaml)")
	f.StringVar(&patchOut, "patch-out", "", "write the verified patch to this file")
	f.BoolVar(&inject, "inject-bad-patch", false, "first submit a patch that skips the test, to show the guardrails rejecting it")
	for _, name := range []string{"good", "test", "culprit"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
