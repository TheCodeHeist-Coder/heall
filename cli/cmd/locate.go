package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"heall/internal/config"
	"heall/internal/console"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/locate"
	"heall/internal/sandbox"
	"heall/internal/tester"
)

func newRunID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "r-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func newLocateCmd() *cobra.Command {
	var (
		good, bad, test string
		workers         int
		sandboxMode     string
		eventsPath      string
		asJSON          bool
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

			repo, err := gitx.Open(ctx, repoDir)
			if err != nil {
				return err
			}
			cfg, err := config.Load(repo.Dir, configPath)
			if err != nil {
				return err
			}
			if workers > 0 {
				cfg.Locate.Workers = workers
			}
			if sandboxMode != "" {
				cfg.Sandbox.Mode = sandboxMode
			}
			if err := cfg.Validate(); err != nil {
				return err
			}

			goodSHA, err := repo.Resolve(ctx, good)
			if err != nil {
				return err
			}
			badSHA, err := repo.Resolve(ctx, bad)
			if err != nil {
				return err
			}
			commits, err := repo.Range(ctx, goodSHA, badSHA)
			if err != nil {
				return err
			}
			if len(commits) < 2 {
				return errors.New("--good and --bad are the same commit")
			}

			runner, err := sandbox.New(cfg.Sandbox)
			if err != nil {
				return err
			}
			if err := runner.Prepare(ctx); err != nil {
				return err
			}

			// With --json, stdout carries only the result.
			var human io.Writer = cmd.OutOrStdout()
			if asJSON {
				human = cmd.ErrOrStderr()
			}
			var sinks []io.Writer
			if eventsPath != "" {
				f, err := os.Create(eventsPath)
				if err != nil {
					return err
				}
				defer f.Close()
				sinks = append(sinks, f)
			}
			emitter := events.NewEmitter(newRunID(), sinks...)
			emitter.Listen(console.New(human).Handle)

			trees, err := locate.NewWorktrees(repo, tester.Tester{Cfg: cfg, Runner: runner}, test, cfg.Locate.Workers)
			if err != nil {
				return err
			}
			defer func() {
				if err := trees.Close(); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree cleanup: %v\n", err)
				}
			}()

			res, err := locate.Run(ctx, commits, trees.Check, locate.Options{
				Workers:    cfg.Locate.Workers,
				VerifyEnds: !skipEndCheck,
				Emit:       emitter,
				Diff:       repo.Diff,
			})
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"culprit":     res.Culprit,
					"index":       res.Index,
					"suspects":    res.Suspects,
					"consistent":  res.Consistent,
					"rounds":      res.Rounds,
					"tested":      res.Tested,
					"duration_ms": res.Duration.Milliseconds(),
					"sandbox":     runner.Name(),
					"workers":     cfg.Locate.Workers,
				})
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&good, "good", "", "a commit where the test passes (required)")
	f.StringVar(&bad, "bad", "HEAD", "a commit where the test fails")
	f.StringVar(&test, "test", "", "name of the failing test (default: run the whole suite)")
	f.IntVar(&workers, "workers", 0, "commits to test at once (default: locate.workers in .heall.yaml)")
	f.StringVar(&sandboxMode, "sandbox", "", "override sandbox.mode: docker or local")
	f.StringVar(&eventsPath, "events", "", "also write the event stream to this file as JSONL")
	f.BoolVar(&asJSON, "json", false, "print the result as JSON on stdout")
	f.BoolVar(&skipEndCheck, "skip-end-check", false, "do not test the good and bad commits first")
	_ = cmd.MarkFlagRequired("good")
	return cmd
}
