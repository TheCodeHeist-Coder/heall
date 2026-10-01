package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"heall/internal/agentio"
	"heall/internal/events"
	"heall/internal/heal"
	"heall/internal/triage"
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
				s.cfg.Heal.Model = model
			}

			goodSHA, err := s.repo.Resolve(ctx, good)
			if err != nil {
				return err
			}
			badSHA, err := s.repo.Resolve(ctx, bad)
			if err != nil {
				return err
			}
			culpritSHA, err := s.repo.Resolve(ctx, culprit)
			if err != nil {
				return err
			}
			if err := s.runner.Prepare(ctx); err != nil {
				return err
			}

			var res heal.Result
			err = s.stage(events.StageHeal, func() error {
				in, err := s.healInput(cmd, goodSHA, badSHA, culpritSHA, test)
				if err != nil {
					return err
				}
				in.InjectBadPatch = inject
				res, err = heal.Healer{
					Repo: s.repo, Cfg: s.cfg, Runner: s.runner, Emit: s.emitter, ConfigPath: absConfigPath(),
				}.Run(ctx, in)
				if err != nil {
					return err
				}
				if res.Outcome != heal.Fixed {
					return &Escalation{Stage: events.StageHeal, Reason: res.Reason, Diagnosis: res.RootCause}
				}
				return nil
			})
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

// healInput gathers what the agent is told: the failure as it looks in the
// sandbox, and the culprit commit with its message and diff.
func (s *session) healInput(cmd *cobra.Command, good, bad, culprit, test string) (heal.Input, error) {
	ctx := cmd.Context()
	in := heal.Input{RunID: s.runID, Good: good, Bad: bad}

	run, err := s.verifier().RunTest(ctx, bad, test)
	if err != nil {
		return in, err
	}
	if run.Verdict != events.Fail {
		return in, &Escalation{
			Stage:     events.StageHeal,
			Reason:    "the test does not fail on the bad commit",
			Diagnosis: fmt.Sprintf("%q gave %q on %.10s in the sandbox, so there is nothing to fix. Run `heall reproduce` first.", test, run.Verdict, bad),
		}
	}
	in.Failure = agentio.Failure{TestName: test, Output: run.Output}
	snapshot, err := s.repo.Snapshot(ctx, bad)
	if err != nil {
		return in, err
	}
	if report, err := triage.Analyze(run.Output, snapshot); err == nil {
		in.Failure.TestFile = report.Primary.File
		in.Failure.Output = report.Primary.Excerpt
		in.SuspectFiles = report.SuspectFiles
	}

	commit, err := s.repo.Commit(ctx, culprit)
	if err != nil {
		return in, err
	}
	message, err := s.repo.Message(ctx, culprit)
	if err != nil {
		return in, err
	}
	diff, err := s.repo.Diff(ctx, culprit)
	if err != nil {
		return in, err
	}
	in.Culprit = agentio.Culprit{Commit: commit, Message: message, Diff: diff}
	return in, nil
}
