package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"heall/internal/events"
	"heall/internal/locate"
	"heall/internal/workspace"
)

func newLocateCmd() *cobra.Command {
	var (
		flags           commonFlags
		good, bad, test string
		workers         int
		skipEndCheck    bool
	)
	cmd := &cobra.Command{
		Use:   "locate",
		Short: "Find the culprit commit with a parallel bisect",
		Long: `Find the first commit between --good and --bad where the test fails.

Several commits are tested at once, each in its own git worktree, so the
range shrinks by a factor of workers+1 per round instead of 2. Commits that
do not build are skipped and the search routes around them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openSession(cmd, flags)
			if err != nil {
				return err
			}
			defer s.close()
			if workers > 0 {
				s.cfg.Locate.Workers = workers
			}

			goodSHA, err := s.repo.Resolve(ctx, good)
			if err != nil {
				return err
			}
			badSHA, err := s.repo.Resolve(ctx, bad)
			if err != nil {
				return err
			}
			commits, err := s.repo.Range(ctx, goodSHA, badSHA)
			if err != nil {
				return err
			}
			if len(commits) < 2 {
				return errors.New("--good and --bad are the same commit")
			}
			if err := s.runner.Prepare(ctx); err != nil {
				return err
			}

			pool, err := workspace.New(s.repo, s.tester(), test, s.cfg.Locate.Workers)
			if err != nil {
				return err
			}
			defer func() {
				if err := pool.Close(); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree cleanup: %v\n", err)
				}
			}()

			var res locate.Result
			err = s.stage(events.StageLocate, func() error {
				res, err = locate.Run(ctx, commits, pool.Check, locate.Options{
					Workers:    s.cfg.Locate.Workers,
					VerifyEnds: !skipEndCheck,
					Emit:       s.emitter,
					Diff:       s.repo.Diff,
				})
				return err
			})
			if err != nil {
				return err
			}
			return s.printJSON(map[string]any{
				"culprit":     res.Culprit,
				"index":       res.Index,
				"suspects":    res.Suspects,
				"consistent":  res.Consistent,
				"rounds":      res.Rounds,
				"tested":      res.Tested,
				"duration_ms": res.Duration.Milliseconds(),
				"sandbox":     s.runner.Name(),
				"workers":     s.cfg.Locate.Workers,
			})
		},
	}
	flags.register(cmd)
	f := cmd.Flags()
	f.StringVar(&good, "good", "", "a commit where the test passes (required)")
	f.StringVar(&bad, "bad", "HEAD", "a commit where the test fails")
	f.StringVar(&test, "test", "", "name of the failing test (default: run the whole suite)")
	f.IntVar(&workers, "workers", 0, "commits to test at once (default: locate.workers in .heall.yaml)")
	f.BoolVar(&skipEndCheck, "skip-end-check", false, "do not test the good and bad commits first")
	_ = cmd.MarkFlagRequired("good")
	return cmd
}
