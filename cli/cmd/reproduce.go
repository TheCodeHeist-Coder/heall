package cmd

import (
	"github.com/spf13/cobra"
)

func newReproduceCmd() *cobra.Command {
	var (
		flags                     commonFlags
		good, bad, test, testFile string
		runs, workers             int
	)
	cmd := &cobra.Command{
		Use:   "reproduce",
		Short: "Confirm the failure reproduces in the sandbox and is not flaky",
		Long: `Run the failing test several times on --bad and once on --good.

The failure is real only if the test fails on --bad every time and passes on
--good. A test that both passes and fails on the same commit is flaky; heall
then stops with exit code 3 and explains why, instead of blaming a commit.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openSession(cmd, flags)
			if err != nil {
				return err
			}
			defer s.close()
			if runs > 0 {
				s.Cfg.Reproduce.Runs = runs
			}
			if workers > 0 {
				s.Cfg.Locate.Workers = workers
			}
			goodSHA, badSHA, err := s.resolve(cmd, good, bad)
			if err != nil {
				return err
			}
			if err := s.Runner.Prepare(ctx); err != nil {
				return err
			}
			s.printer.SetEnds(goodSHA, badSHA)

			n := min(s.Cfg.Locate.Workers, s.Cfg.Reproduce.Runs+1)
			pool, closePool, err := s.Pool(test, n)
			if err != nil {
				return err
			}
			defer closePool()
			go pool.Warm(ctx, badSHA)

			res, err := s.Reproduce(ctx, pool, n, goodSHA, badSHA, test, testFile)
			if jsonErr := s.printJSON(map[string]any{
				"outcome":     res.Outcome,
				"runs":        res.Runs,
				"failures":    res.Failures,
				"good":        res.Good,
				"duration_ms": res.Duration.Milliseconds(),
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
	f.StringVar(&testFile, "test-file", "", "file that holds the test (default: found by searching for the test name)")
	f.IntVar(&runs, "runs", 0, "times to run the test on --bad (default: reproduce.runs in .heall.yaml)")
	f.IntVar(&workers, "workers", 0, "runs to do at once (default: locate.workers in .heall.yaml)")
	_ = cmd.MarkFlagRequired("good")
	_ = cmd.MarkFlagRequired("test")
	return cmd
}
