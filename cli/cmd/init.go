package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"heall/internal/config"
	"heall/internal/gitx"
	"heall/internal/setup"
)

func printChecks(w io.Writer, checks []setup.Check) {
	for _, c := range checks {
		mark := "ok  "
		switch {
		case !c.OK && c.Optional:
			mark = "note"
		case !c.OK:
			mark = "FIX "
		}
		fmt.Fprintf(w, "  %s  %-15s %s\n", mark, c.Name, c.Detail)
	}
}

func newInitCmd() *cobra.Command {
	var force, printOnly bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Connect this repository to heall",
		Long: `Write a .heall.yaml for the repository and check that this machine can run
heall on it.

heall looks at the project to choose the test commands, the Docker image, and
which paths a fix may touch. Read the file it writes: it is short, and it
decides what the agent is allowed to change.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			repo, err := gitx.Open(ctx, repoDir)
			if err != nil {
				return err
			}
			plan := setup.Detect(repo.Dir)
			if printOnly {
				fmt.Fprint(out, plan.YAML)
				return nil
			}

			path := filepath.Join(repo.Dir, config.FileName)
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists; pass --force to replace it", path)
			}
			if err := os.WriteFile(path, []byte(plan.YAML), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(out, "wrote %s\n", path)
			if err := excludeRunFiles(ctx, repo); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not hide .heall/ from git status: %v\n", err)
			}
			for _, note := range plan.Notes {
				fmt.Fprintf(out, "\nnote: %s\n", note)
			}

			image := ""
			if cfg, err := config.Load(repo.Dir, ""); err == nil {
				image = cfg.Sandbox.Image
			}
			checks := setup.Doctor(ctx, repo.Dir, image)
			fmt.Fprintln(out, "\nthis machine:")
			printChecks(out, checks)

			fmt.Fprintln(out, "\nnext:")
			if !plan.Ready {
				fmt.Fprintf(out, "  1. fill in the lines marked FILL IN in %s\n", config.FileName)
				fmt.Fprintln(out, "  2. heall run --good <last commit where the tests passed> --dry-run")
			} else {
				fmt.Fprintf(out, "  1. look over %s and commit it\n", config.FileName)
				fmt.Fprintln(out, "  2. heall run --good <last commit where the tests passed> --dry-run")
				fmt.Fprintln(out, "  3. heall serve    (in another terminal, to watch runs in the browser)")
			}
			if !setup.Usable(checks) {
				return errors.New("fix the items marked FIX above before running heall")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing .heall.yaml")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the config instead of writing it")
	return cmd
}

// excludeRunFiles keeps the run directory out of `git status` without
// touching the repository's own .gitignore: it adds .heall/ to the local
// exclude file, which is never committed.
func excludeRunFiles(ctx context.Context, repo *gitx.Repo) error {
	state, err := repo.StateDir(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(state), "info", "exclude")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == ".heall/" {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	entry := ".heall/\n"
	if len(raw) > 0 && !strings.HasSuffix(string(raw), "\n") {
		entry = "\n" + entry
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(entry)
	return err
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine can run heall",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, image := repoDir, ""
			if repo, err := gitx.Open(cmd.Context(), repoDir); err == nil {
				dir = repo.Dir
				if cfg, err := config.Load(repo.Dir, configPath); err == nil {
					image = cfg.Sandbox.Image
				}
			}
			checks := setup.Doctor(cmd.Context(), dir, image)
			printChecks(cmd.OutOrStdout(), checks)
			if _, err := os.Stat(filepath.Join(dir, config.FileName)); err != nil && configPath == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  note  %-15s no %s here; run: heall init\n", "config", config.FileName)
			}
			if !setup.Usable(checks) {
				return errors.New("fix the items marked FIX above")
			}
			return nil
		},
	}
}
