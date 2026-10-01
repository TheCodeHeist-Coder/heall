package heal

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heall/internal/agentio"
	"heall/internal/config"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/gitx/gitxtest"
	"heall/internal/sandbox"
)

// A test runner that prints TAP; see the verify package's tests.
const runner = `only=$1
fail=0
n=0
check() {
  name=$1; shift
  n=$((n + 1))
  if [ -n "$only" ] && [ "$only" != "$name" ]; then return; fi
  if "$@"; then echo "ok $n - $name"; else echo "not ok $n - $name"; fail=1; fi
}
v=$(cat src/value)
check "value is small" test "$v" -lt 10
check "value is even" test $((v % 2)) -eq 0
exit $fail
`

const goodPatch = "--- a/src/value\n+++ b/src/value\n@@ -1 +1 @@\n-12\n+4\n"

type fixture struct {
	t      *testing.T
	fx     *gitxtest.Repo
	healer Healer
	stream *bytes.Buffer
	bad    string
	dir    string
}

// setup makes a repository with one failing test and a fake agent: a shell
// script that prints the given lines as its event stream.
func setup(t *testing.T, script string) *fixture {
	t.Helper()
	fx := gitxtest.New(t)
	fx.Write("test/run.sh", runner)
	fx.Write("src/value", "12\n")
	bad := fx.Commit("value is 12")

	repo, err := gitx.Open(context.Background(), fx.Dir)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent.sh")
	// The agent is started as: agent.sh heal --request <file>
	if err := os.WriteFile(agent, []byte("request=$3\nout="+dir+"\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.TestCmd = []string{"sh", "test/run.sh"}
	cfg.TestOneCmd = []string{"sh", "test/run.sh", "{{test}}"}
	cfg.Allow = []string{"src/**"}
	cfg.Protect = []string{"test/**"}
	cfg.Heal.TimeoutSeconds = 30

	var stream bytes.Buffer
	return &fixture{t: t, fx: fx, bad: bad, dir: dir, stream: &stream, healer: Healer{
		Repo:     repo,
		Cfg:      cfg,
		Runner:   &sandbox.Local{Timeout: 20 * time.Second},
		Emit:     events.NewEmitter("r1", &stream),
		Command:  []string{"sh", agent},
		HeallBin: "/path/to/heall",
	}}
}

func (f *fixture) run(in Input) (Result, error) {
	f.t.Helper()
	in.RunID, in.Bad, in.Good = "r1", f.bad, f.bad
	in.Failure = agentio.Failure{TestName: "value is small", TestFile: "test/run.sh", Output: "not ok 1 - value is small"}
	return f.healer.Run(context.Background(), in)
}

func (f *fixture) kinds() []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(f.stream.String()), "\n") {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			f.t.Fatalf("stream line is not an event: %s", line)
		}
		if ev.Stage != events.StageHeal {
			f.t.Errorf("event %s has stage %s", ev.Kind, ev.Stage)
		}
		out = append(out, string(ev.Kind))
	}
	return out
}

func line(kind string, data any) string {
	raw, _ := json.Marshal(map[string]any{"kind": kind, "data": data})
	return "printf '%s\\n' '" + string(raw) + "'\n"
}

func agentDone(outcome, patch, reason string) string {
	return line("agent_done", events.AgentDone{Outcome: outcome, Attempts: 1, RootCause: "the value is too big", Patch: patch, Reason: reason})
}

func TestFixIsAcceptedOnlyAfterHeallChecksIt(t *testing.T) {
	f := setup(t, `cp "$request" "$out/request.json"
`+line("agent_started", events.AgentStarted{Model: "fake", MaxAttempts: 3})+
		line("agent_thought", events.AgentThought{Text: "lower the value"})+
		agentDone("fixed", goodPatch, ""))

	res, err := f.run(Input{SuspectFiles: []string{"src/value"}, Culprit: agentio.Culprit{Message: "set value", Diff: "d"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Fixed || res.Attempts != 1 || res.RootCause != "the value is too big" {
		t.Errorf("result: %+v", res)
	}
	// The patch handed on is git's own diff of the verified tree, not the
	// text the agent sent.
	if !strings.HasPrefix(res.Patch, "diff --git a/src/value b/src/value") || !strings.Contains(res.Patch, "+4") {
		t.Errorf("patch:\n%s", res.Patch)
	}
	want := "agent_started,agent_thought,agent_done,log"
	if got := strings.Join(f.kinds(), ","); got != want {
		t.Errorf("events = %s, want %s", got, want)
	}

	// What the agent was told.
	raw, err := os.ReadFile(filepath.Join(f.dir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var req agentio.HealRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.RunID != "r1" || req.Bad != f.bad || req.RepoDir != f.fx.Dir || req.HeallBin != "/path/to/heall" {
		t.Errorf("request: %+v", req)
	}
	if req.MaxAttempts != 3 || req.Model == "" || req.Failure.TestName != "value is small" || len(req.Allow) != 1 || len(req.SuspectFiles) != 1 {
		t.Errorf("request: %+v", req)
	}
	if !strings.Contains(req.Worktree, "heall-worktree-") {
		t.Errorf("worktree = %q", req.Worktree)
	}
	if _, err := os.Stat(req.Worktree); !os.IsNotExist(err) {
		t.Error("the agent's worktree was not removed afterwards")
	}
	if f.fx.Git("status", "--porcelain") != "" || strings.Count(f.fx.Git("worktree", "list"), "\n") != 0 {
		t.Error("the repository was left changed")
	}
}

func TestAgentThatClaimsAFixItDidNotMake(t *testing.T) {
	cases := map[string]string{
		"patch does not fix the test": "--- a/src/value\n+++ b/src/value\n@@ -1 +1 @@\n-12\n+20\n",
		"patch edits the test":        goodPatch + "--- a/test/run.sh\n+++ b/test/run.sh\n@@ -1 +1,2 @@\n only=$1\n+exit 0\n",
		"patch is not a patch":        "trust me",
	}
	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t, agentDone("fixed", patch, ""))
			res, err := f.run(Input{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != Escalated || res.Patch != "" || !strings.Contains(res.Reason, "final check") {
				t.Errorf("a fix heall could not verify was accepted: %+v", res)
			}
			if !strings.Contains(res.RootCause, "Final check:") {
				t.Errorf("the diagnosis should say what the final check found: %q", res.RootCause)
			}
		})
	}
}

func TestEscalationIsPassedOn(t *testing.T) {
	f := setup(t, agentDone("escalated", "", "two tests disagree"))
	res, err := f.run(Input{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Escalated || res.Reason != "two tests disagree" || res.RootCause != "the value is too big" || res.Patch != "" {
		t.Errorf("result: %+v", res)
	}
}

func TestInjectedBadPatchGetsAnExtraAttempt(t *testing.T) {
	f := setup(t, `cp "$request" "$out/request.json"
`+agentDone("escalated", "", "r"))
	if _, err := f.run(Input{InjectBadPatch: true}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(f.dir, "request.json"))
	var req agentio.HealRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if !req.InjectBadPatch || req.MaxAttempts != 4 {
		t.Errorf("inject=%v max_attempts=%d, want true and 4", req.InjectBadPatch, req.MaxAttempts)
	}
}

func TestOnlyAgentEventsAreRelayed(t *testing.T) {
	// The agent must not be able to announce a culprit, a delivery or the
	// end of the run, or to slip a malformed event into the stream.
	f := setup(t, line("culprit_found", events.CulpritFound{})+
		line("run_done", events.RunDone{Outcome: "fixed"})+
		"echo 'not json at all'\n"+
		line("agent_thought", map[string]any{"txt": "wrong field"})+
		line("agent_thought", events.AgentThought{Text: "fine"})+
		agentDone("escalated", "", "r"))
	if _, err := f.run(Input{}); err != nil {
		t.Fatal(err)
	}
	want := "log,log,log,log,agent_thought,agent_done"
	if got := strings.Join(f.kinds(), ","); got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
}

func TestAgentFailuresAreErrors(t *testing.T) {
	cases := map[string]struct{ script, want string }{
		"setup problem":     {"echo 'heall-agent: no Groq API key' >&2\nexit 2\n", "no Groq API key"},
		"crash":             {line("agent_started", events.AgentStarted{Model: "m", MaxAttempts: 3}) + "exit 1\n", "the heal agent failed"},
		"silent exit":       {"exit 0\n", "without reporting a result"},
		"cannot be started": {"", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t, tc.script)
			if name == "cannot be started" {
				f.healer.Command = []string{filepath.Join(f.dir, "no-such-agent")}
				tc.want = "start the heal agent"
			}
			res, err := f.run(Input{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got result %+v and error %v, want an error mentioning %q", res, err, tc.want)
			}
		})
	}
}

func TestAgentIsStoppedAtTheTimeout(t *testing.T) {
	f := setup(t, "sleep 60\n")
	f.healer.Cfg.Heal.TimeoutSeconds = 1
	start := time.Now()
	_, err := f.run(Input{})
	if err == nil || !strings.Contains(err.Error(), "did not finish within 1 seconds") {
		t.Errorf("got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("stopping the agent took %v", elapsed)
	}
}
