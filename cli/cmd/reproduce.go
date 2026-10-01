package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/reproduce"
	"heall/internal/triage"
	"heall/internal/workspace"
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
				s.cfg.Reproduce.Runs = runs
			}
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
			if err := s.runner.Prepare(ctx); err != nil {
				return err
			}
			s.printer.SetEnds(goodSHA, badSHA)

			n := min(s.cfg.Locate.Workers, s.cfg.Reproduce.Runs+1)
			pool, err := workspace.New(s.repo, s.tester(), test, n)
			if err != nil {
				return err
			}
			defer func() {
				if err := pool.Close(); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree cleanup: %v\n", err)
				}
			}()

			var res reproduce.Result
			err = s.stage(events.StageReproduce, func() error {
				res, err = reproduce.Run(ctx, goodSHA, badSHA, pool.Outcome, reproduce.Options{
					Runs:    s.cfg.Reproduce.Runs,
					Workers: n,
					Emit:    s.emitter,
				})
				if err != nil || res.Proceed() {
					return err
				}
				var hints []string
				if res.Outcome == reproduce.Flaky {
					hints = flakyHints(cmd, s.repo, badSHA, test, testFile)
				}
				reason, diagnosis := res.Explain(test, goodSHA, badSHA, hints)
				return &Escalation{Stage: events.StageReproduce, Reason: reason, Diagnosis: diagnosis}
			})
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

// flakyHints looks through the test and the files it imports for code whose
// result changes from run to run. Hints are a courtesy: any problem finding
// them just means there are none.
func flakyHints(cmd *cobra.Command, repo *gitx.Repo, sha, test, testFile string) []string {
	snapshot, err := repo.Snapshot(cmd.Context(), sha)
	if err != nil {
		return nil
	}
	if testFile == "" {
		files, err := snapshot.Find(test)
		if err != nil || len(files) == 0 {
			return nil
		}
		testFile = files[0]
	}
	sources := map[string]string{}
	for _, file := range append([]string{testFile}, triage.Suspects(triage.Failure{File: testFile}, snapshot)...) {
		if content, err := snapshot.Read(file); err == nil {
			sources[file] = content
		}
	}
	return reproduce.Hints(sources)
}
