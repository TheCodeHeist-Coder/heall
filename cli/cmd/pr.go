package cmd

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"heall/internal/deliver"
	"heall/internal/events"
	"heall/internal/pipeline"
	"heall/internal/verify"
)

func newPRCmd() *cobra.Command {
	var (
		flags        commonFlags
		base, remote string
	)
	cmd := &cobra.Command{
		Use:   "pr <run directory>",
		Short: "Open a draft pull request for a fix found by an earlier dry run",
		Long: `Publish the fix a previous "heall run --dry-run" saved.

The run directory is the one that run printed (.heall/<run id>). The fix is
verified once more against the failing commit before anything is pushed, so
a patch file that was edited by hand, or that no longer fits, is refused.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			ev, err := deliver.LoadEvidence(dir)
			if err != nil {
				return err
			}
			s, err := openSession(cmd, flags)
			if err != nil {
				return err
			}
			defer s.close()
			if base != "" {
				ev.Base = base
			}
			if err := s.Runner.Prepare(ctx); err != nil {
				return err
			}

			var res deliver.Result
			err = s.Stage(events.StageDeliver, func() error {
				check, err := s.verifier().Run(ctx, verify.Request{RunID: s.RunID, Bad: ev.Bad, Test: ev.Test, Final: true}, ev.Fix.Patch)
				if err != nil {
					return err
				}
				if check.Status != verify.Verified {
					return &pipeline.Escalation{
						Stage:     events.StageDeliver,
						Reason:    "the saved fix no longer verifies",
						Diagnosis: check.Output,
					}
				}
				ev.Fix.Patch = check.Patch
				warned := ""
				res, err = deliver.Deliverer{
					Repo: s.Repo, OutDir: dir, Remote: remote,
					Warn: func(m string) { warned = m },
				}.Fix(ctx, ev)
				if err != nil {
					return err
				}
				if res.Outcome != deliver.PR {
					return errors.New(warned)
				}
				return s.Emit.Emit(events.StageDeliver, events.DeliverDone{Outcome: res.Outcome, URL: res.URL, Path: res.Path})
			})
			if err != nil {
				return err
			}
			if flags.asJSON {
				return s.printJSON(map[string]any{"url": res.URL, "branch": res.Branch})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  branch: %s\n", res.Branch)
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&base, "base", "", "branch the pull request targets (default: the one recorded by the run)")
	cmd.Flags().StringVar(&remote, "remote", "origin", "git remote the fix branch is pushed to")
	return cmd
}
