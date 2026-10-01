package cmd

import (
	"errors"

	"github.com/spf13/cobra"
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
				s.Cfg.Locate.Workers = workers
			}
			goodSHA, badSHA, err := s.resolve(cmd, good, bad)
			if err != nil {
				return err
			}
			commits, err := s.Repo.Range(ctx, goodSHA, badSHA)
			if err != nil {
				return err
			}
			if len(commits) < 2 {
				return errors.New("--good and --bad are the same commit")
			}
			if err := s.Runner.Prepare(ctx); err != nil {
				return err
			}
			pool, closePool, err := s.Pool(test, s.Cfg.Locate.Workers)
			if err != nil {
				return err
			}
			defer closePool()
			// Start every worker's sandbox while the first commits are tested.
			go pool.Warm(ctx, badSHA)

			res, err := s.Locate(ctx, pool, commits, !skipEndCheck)
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
				"sandbox":     s.Runner.Name(),
				"workers":     s.Cfg.Locate.Workers,
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
