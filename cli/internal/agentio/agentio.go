// Package agentio defines the JSON exchanged between the Go CLI and the
// Python agent. See contracts/README.md.
package agentio

import (
	"encoding/json"

	"heall/internal/events"
)

type Failure struct {
	TestName string `json:"test_name"`
	TestFile string `json:"test_file"`
	Output   string `json:"output"`
}

type Culprit struct {
	Commit events.Commit `json:"commit"`
	// Message is the full commit message: it often says whether a change
	// was intended.
	Message string `json:"message"`
	Diff    string `json:"diff"`
}

// HealRequest is written to a file and passed to the agent with --request.
type HealRequest struct {
	V     int    `json:"v"`
	RunID string `json:"run_id"`
	// RepoDir is the repository being healed.
	RepoDir string `json:"repo_dir"`
	// Worktree is a checkout of the bad commit for the agent to read. Edits
	// made here are ignored; changes only count when sent through _verify.
	Worktree string `json:"worktree"`
	// HeallBin is the CLI binary the agent calls back for _runtest and _verify.
	HeallBin string `json:"heall_bin"`
	// ConfigPath is the .heall.yaml in use; empty means the one in RepoDir.
	ConfigPath string  `json:"config_path"`
	Good       string  `json:"good"`
	Bad        string  `json:"bad"`
	Failure    Failure `json:"failure"`
	// SuspectFiles are the source files triage picked out to read first.
	SuspectFiles   []string `json:"suspect_files"`
	Culprit        Culprit  `json:"culprit"`
	Allow          []string `json:"allow"`
	Protect        []string `json:"protect"`
	MaxAttempts    int      `json:"max_attempts"`
	Model          string   `json:"model"`
	InjectBadPatch bool     `json:"inject_bad_patch"`
}

// RunTestResult is printed by `heall _runtest`.
type RunTestResult struct {
	Verdict    events.Verdict `json:"verdict"`
	ExitCode   int            `json:"exit_code"`
	DurationMS int64          `json:"duration_ms"`
	Output     string         `json:"output"`
	Truncated  bool           `json:"truncated"`
}

// VerifyResult is printed by `heall _verify`. Status is "verified",
// "rejected" or "failed", matching events.VerifyDone.
type VerifyResult struct {
	Attempt      int                     `json:"attempt"`
	Status       string                  `json:"status"`
	Guardrails   []events.GuardrailCheck `json:"guardrails"`
	TargetPassed bool                    `json:"target_passed"`
	SuitePassed  bool                    `json:"suite_passed"`
	NewFailures  []string                `json:"new_failures"`
	Output       string                  `json:"output"`
}

// AgentLine is one line of the agent's stdout: an event without the envelope
// fields, which the CLI adds before relaying it.
type AgentLine struct {
	Kind events.Kind     `json:"kind"`
	Data json.RawMessage `json:"data"`
}
