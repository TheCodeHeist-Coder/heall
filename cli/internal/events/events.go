// Package events defines the event stream shared by the CLI, the Python agent
// and the web dashboard. The wire format is one JSON object per line (JSONL).
// contracts/README.md is the source of truth; keep this file, the Python
// mirror (agent/heall_agent/contracts.py) and the TypeScript mirror
// (web/lib/events.ts) in sync with it.
package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Version is the contract version stamped on every event.
const Version = 1

type Verdict string

const (
	Pass    Verdict = "pass"
	Fail    Verdict = "fail"
	Flaky   Verdict = "flaky"
	Skipped Verdict = "skipped" // did not build or timed out
)

type Stage string

const (
	StageRun       Stage = "run"
	StageTriage    Stage = "triage"
	StageReproduce Stage = "reproduce"
	StageLocate    Stage = "locate"
	StageHeal      Stage = "heal"
	StageDeliver   Stage = "deliver"
)

type Kind string

const (
	KindRunStarted       Kind = "run_started"
	KindStageStarted     Kind = "stage_started"
	KindStageDone        Kind = "stage_done"
	KindTriageDone       Kind = "triage_done"
	KindReproduceRun     Kind = "reproduce_run"
	KindReproduceDone    Kind = "reproduce_done"
	KindLocateStarted    Kind = "locate_started"
	KindRoundStarted     Kind = "round_started"
	KindCommitTesting    Kind = "commit_testing"
	KindCommitTested     Kind = "commit_tested"
	KindRoundDone        Kind = "round_done"
	KindCulpritFound     Kind = "culprit_found"
	KindAgentStarted     Kind = "agent_started"
	KindAgentThought     Kind = "agent_thought"
	KindToolCall         Kind = "tool_call"
	KindToolResult       Kind = "tool_result"
	KindPatchSubmitted   Kind = "patch_submitted"
	KindGuardrailChecked Kind = "guardrail_checked"
	KindVerifyDone       Kind = "verify_done"
	KindAgentDone        Kind = "agent_done"
	KindDeliverDone      Kind = "deliver_done"
	KindEscalated        Kind = "escalated"
	KindRunDone          Kind = "run_done"
	KindLog              Kind = "log"
)

// Event is the envelope written on the stream.
type Event struct {
	V     int             `json:"v"`
	Seq   int64           `json:"seq"`
	TS    time.Time       `json:"ts"`
	RunID string          `json:"run_id"`
	Stage Stage           `json:"stage"`
	Kind  Kind            `json:"kind"`
	Data  json.RawMessage `json:"data"`
}

// Payload is the typed body of an event. Each payload names its own kind so
// an event can never be emitted with a mismatched body.
type Payload interface {
	Kind() Kind
}

type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

type RunStarted struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Good   string `json:"good"`
	Bad    string `json:"bad"`
	DryRun bool   `json:"dry_run"`
}

type StageStarted struct{}

// StageDone status is "ok", "escalated" or "error".
type StageDone struct {
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

type TriageDone struct {
	TestName     string   `json:"test_name"`
	TestFile     string   `json:"test_file"`
	SuspectFiles []string `json:"suspect_files"`
	Excerpt      string   `json:"excerpt"`
}

type ReproduceRun struct {
	SHA        string  `json:"sha"`
	Attempt    int     `json:"attempt"`
	Verdict    Verdict `json:"verdict"`
	DurationMS int64   `json:"duration_ms"`
}

type ReproduceDone struct {
	Reproduced bool `json:"reproduced"`
	Flaky      bool `json:"flaky"`
	Runs       int  `json:"runs"`
	Failures   int  `json:"failures"`
	GoodPasses bool `json:"good_passes"`
}

// LocateStarted lists the candidate commits oldest first; the first is the
// known-good commit and the last is the bad one.
type LocateStarted struct {
	Commits []Commit `json:"commits"`
	Workers int      `json:"workers"`
}

// RoundStarted: Lo and Hi are indexes into LocateStarted.Commits. Lo is the
// newest commit known good, Hi the oldest known bad.
type RoundStarted struct {
	Round  int      `json:"round"`
	Lo     int      `json:"lo"`
	Hi     int      `json:"hi"`
	Probes []string `json:"probes"`
}

type CommitTesting struct {
	SHA    string `json:"sha"`
	Round  int    `json:"round"`
	Worker int    `json:"worker"`
}

type CommitTested struct {
	SHA        string  `json:"sha"`
	Round      int     `json:"round"`
	Worker     int     `json:"worker"`
	Verdict    Verdict `json:"verdict"`
	DurationMS int64   `json:"duration_ms"`
}

type RoundDone struct {
	Round int `json:"round"`
	Lo    int `json:"lo"`
	Hi    int `json:"hi"`
}

type CulpritFound struct {
	Commit     Commit `json:"commit"`
	Diff       string `json:"diff"`
	Rounds     int    `json:"rounds"`
	Tested     int    `json:"tested"`
	DurationMS int64  `json:"duration_ms"`
}

type AgentStarted struct {
	Model       string `json:"model"`
	MaxAttempts int    `json:"max_attempts"`
}

type AgentThought struct {
	Text string `json:"text"`
}

type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
}

type PatchSubmitted struct {
	Attempt int    `json:"attempt"`
	Diff    string `json:"diff"`
}

type GuardrailCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type GuardrailChecked struct {
	Attempt int              `json:"attempt"`
	Passed  bool             `json:"passed"`
	Checks  []GuardrailCheck `json:"checks"`
}

// VerifyDone status is "verified", "rejected" (a guardrail blocked the patch
// before it was applied) or "failed" (applied, but tests did not pass).
type VerifyDone struct {
	Attempt      int      `json:"attempt"`
	Status       string   `json:"status"`
	TargetPassed bool     `json:"target_passed"`
	SuitePassed  bool     `json:"suite_passed"`
	NewFailures  []string `json:"new_failures"`
}

// AgentDone outcome is "fixed" or "escalated".
type AgentDone struct {
	Outcome   string `json:"outcome"`
	Attempts  int    `json:"attempts"`
	RootCause string `json:"root_cause"`
	Patch     string `json:"patch"`
	Reason    string `json:"reason"`
}

// DeliverDone outcome is "pr", "patch_file" or "diagnosis".
type DeliverDone struct {
	Outcome string `json:"outcome"`
	URL     string `json:"url"`
	Path    string `json:"path"`
}

type Escalated struct {
	Reason    string `json:"reason"`
	Diagnosis string `json:"diagnosis"`
}

// RunDone outcome is "fixed", "escalated" or "error".
type RunDone struct {
	Outcome    string `json:"outcome"`
	DurationMS int64  `json:"duration_ms"`
}

// Log level is "info", "warn" or "error".
type Log struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (RunStarted) Kind() Kind       { return KindRunStarted }
func (StageStarted) Kind() Kind     { return KindStageStarted }
func (StageDone) Kind() Kind        { return KindStageDone }
func (TriageDone) Kind() Kind       { return KindTriageDone }
func (ReproduceRun) Kind() Kind     { return KindReproduceRun }
func (ReproduceDone) Kind() Kind    { return KindReproduceDone }
func (LocateStarted) Kind() Kind    { return KindLocateStarted }
func (RoundStarted) Kind() Kind     { return KindRoundStarted }
func (CommitTesting) Kind() Kind    { return KindCommitTesting }
func (CommitTested) Kind() Kind     { return KindCommitTested }
func (RoundDone) Kind() Kind        { return KindRoundDone }
func (CulpritFound) Kind() Kind     { return KindCulpritFound }
func (AgentStarted) Kind() Kind     { return KindAgentStarted }
func (AgentThought) Kind() Kind     { return KindAgentThought }
func (ToolCall) Kind() Kind         { return KindToolCall }
func (ToolResult) Kind() Kind       { return KindToolResult }
func (PatchSubmitted) Kind() Kind   { return KindPatchSubmitted }
func (GuardrailChecked) Kind() Kind { return KindGuardrailChecked }
func (VerifyDone) Kind() Kind       { return KindVerifyDone }
func (AgentDone) Kind() Kind        { return KindAgentDone }
func (DeliverDone) Kind() Kind      { return KindDeliverDone }
func (Escalated) Kind() Kind        { return KindEscalated }
func (RunDone) Kind() Kind          { return KindRunDone }
func (Log) Kind() Kind              { return KindLog }

var payloads = map[Kind]func() Payload{
	KindRunStarted:       func() Payload { return &RunStarted{} },
	KindStageStarted:     func() Payload { return &StageStarted{} },
	KindStageDone:        func() Payload { return &StageDone{} },
	KindTriageDone:       func() Payload { return &TriageDone{} },
	KindReproduceRun:     func() Payload { return &ReproduceRun{} },
	KindReproduceDone:    func() Payload { return &ReproduceDone{} },
	KindLocateStarted:    func() Payload { return &LocateStarted{} },
	KindRoundStarted:     func() Payload { return &RoundStarted{} },
	KindCommitTesting:    func() Payload { return &CommitTesting{} },
	KindCommitTested:     func() Payload { return &CommitTested{} },
	KindRoundDone:        func() Payload { return &RoundDone{} },
	KindCulpritFound:     func() Payload { return &CulpritFound{} },
	KindAgentStarted:     func() Payload { return &AgentStarted{} },
	KindAgentThought:     func() Payload { return &AgentThought{} },
	KindToolCall:         func() Payload { return &ToolCall{} },
	KindToolResult:       func() Payload { return &ToolResult{} },
	KindPatchSubmitted:   func() Payload { return &PatchSubmitted{} },
	KindGuardrailChecked: func() Payload { return &GuardrailChecked{} },
	KindVerifyDone:       func() Payload { return &VerifyDone{} },
	KindAgentDone:        func() Payload { return &AgentDone{} },
	KindDeliverDone:      func() Payload { return &DeliverDone{} },
	KindEscalated:        func() Payload { return &Escalated{} },
	KindRunDone:          func() Payload { return &RunDone{} },
	KindLog:              func() Payload { return &Log{} },
}

// Kinds returns every kind in the contract.
func Kinds() []Kind {
	out := make([]Kind, 0, len(payloads))
	for k := range payloads {
		out = append(out, k)
	}
	return out
}

// Decode parses data as the payload for kind. Unknown kinds and unknown
// fields are errors, so drift between the three languages is caught early.
func Decode(kind Kind, data json.RawMessage) (Payload, error) {
	newPayload, ok := payloads[kind]
	if !ok {
		return nil, fmt.Errorf("unknown event kind %q", kind)
	}
	p := newPayload()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(p); err != nil {
		return nil, fmt.Errorf("decode %s: %w", kind, err)
	}
	return p, nil
}
