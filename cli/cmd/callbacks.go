package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"heall/internal/agentio"
	"heall/internal/verify"
)

// The commands in this file are called by the Python agent, not by people.
// They are how the agent runs tests and submits patches: both happen here,
// in the CLI, so the agent never judges its own work. Each prints one JSON
// object on stdout.

// openCallback loads the heal request the agent was started with and opens
// a session on the repository it names.
func openCallback(cmd *cobra.Command, requestPath string) (*session, agentio.HealRequest, error) {
	var req agentio.HealRequest
	raw, err := os.ReadFile(requestPath)
	if err != nil {
		return nil, req, err
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, req, fmt.Errorf("parse %s: %w", requestPath, err)
	}
	repoDir, configPath = req.RepoDir, req.ConfigPath
	s, err := openSession(cmd, commonFlags{asJSON: true})
	if err != nil {
		return nil, req, err
	}
	if err := s.runner.Prepare(cmd.Context()); err != nil {
		s.close()
		return nil, req, err
	}
	return s, req, nil
}

func (s *session) verifier() verify.Verifier {
	return verify.Verifier{Repo: s.repo, Cfg: s.cfg, Runner: s.runner}
}

func newRunTestCmd() *cobra.Command {
	var requestPath, test string
	cmd := &cobra.Command{
		Use:    "_runtest",
		Short:  "Run a test on the failing commit in the sandbox and print the result as JSON",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, req, err := openCallback(cmd, requestPath)
			if err != nil {
				return err
			}
			defer s.close()
			res, err := s.verifier().RunTest(cmd.Context(), req.Bad, test)
			if err != nil {
				return err
			}
			return s.printJSON(res)
		},
	}
	cmd.Flags().StringVar(&requestPath, "request", "", "the HealRequest file the agent was started with (required)")
	cmd.Flags().StringVar(&test, "test", "", "name of the test to run (default: the whole suite)")
	_ = cmd.MarkFlagRequired("request")
	return cmd
}

func newVerifyCmd() *cobra.Command {
	var requestPath, patchPath string
	cmd := &cobra.Command{
		Use:    "_verify",
		Short:  "Check a patch against the guardrails and tests and print the result as JSON",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var patch []byte
			var err error
			if patchPath == "-" {
				patch, err = io.ReadAll(cmd.InOrStdin())
			} else {
				patch, err = os.ReadFile(patchPath)
			}
			if err != nil {
				return err
			}
			s, req, err := openCallback(cmd, requestPath)
			if err != nil {
				return err
			}
			defer s.close()
			res, err := s.verifier().Run(cmd.Context(), verify.Request{
				RunID:       req.RunID,
				Bad:         req.Bad,
				Test:        req.Failure.TestName,
				MaxAttempts: req.MaxAttempts,
			}, string(patch))
			if err != nil {
				return err
			}
			return s.printJSON(res.VerifyResult)
		},
	}
	cmd.Flags().StringVar(&requestPath, "request", "", "the HealRequest file the agent was started with (required)")
	cmd.Flags().StringVar(&patchPath, "patch", "", "the patch to verify, as a unified diff; \"-\" reads standard input (required)")
	_ = cmd.MarkFlagRequired("request")
	_ = cmd.MarkFlagRequired("patch")
	return cmd
}
