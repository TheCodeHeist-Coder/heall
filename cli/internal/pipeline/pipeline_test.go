package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"heall/internal/config"
	"heall/internal/deliver"
	"heall/internal/events"
	"heall/internal/gitx"
	"heall/internal/gitx/gitxtest"
	"heall/internal/sandbox"
)

// A test runner that prints TAP, so the pipeline can run without Node or
// Docker.
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

const (
	commits = 12
	culprit = 7
	fixIt   = "--- a/src/value\n+++ b/src/value\n@@ -1 +1 @@\n-12\n+4\n"
)

type fixture struct {
	t         *testing.T
	fx        *gitxtest.Repo
	p         *Pipeline
	stream    *bytes.Buffer
	dir       string
	remote    string
	good, bad string
	culprit   string
}

func agentScript(done events.AgentDone) string {
	raw, _ := json.Marshal(map[string]any{"kind": "agent_done", "data": done})
	return "printf '%s\\n' '" + string(raw) + "'\n"
}

// setup builds a repository whose commit number 7 breaks a test, a bare
// repository standing in for GitHub, a fake agent and a fake gh.
func setup(t *testing.T, agent, gh string) *fixture {
	t.Helper()
	ctx := context.Background()
	fx := gitxtest.New(t)
	fx.Write("test/run.sh", runner)
	fx.Write("src/value", "4\n")
	f := &fixture{t: t, fx: fx, dir: t.TempDir(), stream: &bytes.Buffer{}}
	f.good = fx.Commit("start")
	for i := 1; i <= commits; i++ {
		fx.Write("notes", fmt.Sprintf("%d\n", i))
		if i == culprit {
			fx.Write("src/value", "12\n")
		}
		sha := fx.Commit(fmt.Sprintf("change %d", i))
		if i == culprit {
			f.culprit = sha
		}
		f.bad = sha
	}
	f.remote = filepath.Join(f.dir, "remote.git")
	fx.Git("init", "-q", "--bare", f.remote)
	fx.Git("remote", "add", "origin", f.remote)
	fx.Git("push", "-q", "origin", "main")

	write := func(name, body string) string {
		path := filepath.Join(f.dir, name)
		if err := os.WriteFile(path, []byte("out="+f.dir+"\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	repo, err := gitx.Open(ctx, fx.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.TestCmd = []string{"sh", "test/run.sh"}
	cfg.TestOneCmd = []string{"sh", "test/run.sh", "{{test}}"}
	cfg.Allow = []string{"src/**"}
	cfg.Protect = []string{"test/**"}
	cfg.Locate.Workers = 3
	cfg.Reproduce.Runs = 3
	cfg.Heal.TimeoutSeconds = 30
	f.p = &Pipeline{
		Repo: repo, Cfg: cfg, Runner: &sandbox.Local{Timeout: 20 * time.Second},
		Emit: events.NewEmitter("r-test", f.stream), RunID: "r-test",
		AgentCommand: []string{"sh", write("agent.sh", agent)},
		HeallBin:     "/path/to/heall",
		Gh:           []string{"sh", write("gh.sh", `echo "$@" >> "$out/gh-calls"`+"\n"+gh)},
		Warn:         func(m string) { t.Log("warning:", m) },
	}
	return f
}

func (f *fixture) run(opts RunOptions) (Summary, error) {
	f.t.Helper()
	opts.Good, opts.Bad, opts.Base = f.good, f.bad, "main"
	opts.OutDir, opts.Remote = filepath.Join(f.dir, "out"), "origin"
	return f.p.Run(context.Background(), opts)
}

// events returns "stage:kind" for every event except per-commit detail.
func (f *fixture) events() []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(f.stream.String()), "\n") {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			f.t.Fatalf("not an event: %s", line)
		}
		if _, err := events.Decode(ev.Kind, ev.Data); err != nil {
			f.t.Errorf("event %d breaks the contract: %v", ev.Seq, err)
		}
		switch ev.Kind {
		case events.KindReproduceRun, events.KindRoundStarted, events.KindRoundDone,
			events.KindCommitTesting, events.KindCommitTested, events.KindLog:
			continue
		}
		out = append(out, string(ev.Stage)+":"+string(ev.Kind))
	}
	return out
}

func (f *fixture) ghCalls() string {
	raw, _ := os.ReadFile(filepath.Join(f.dir, "gh-calls"))
	return string(raw)
}

func (f *fixture) unchanged() {
	f.t.Helper()
	if f.fx.Git("status", "--porcelain") != "" || f.fx.Git("rev-parse", "HEAD") != f.bad {
		f.t.Error("the run changed the user's checkout")
	}
	if list := f.fx.Git("worktree", "list"); strings.Count(list, "\n") != 0 {
		f.t.Errorf("worktrees left behind:\n%s", list)
	}
}

var fixed = agentScript(events.AgentDone{Outcome: "fixed", Attempts: 1, RootCause: "the value is too big", Patch: fixIt})

func TestRunDeliversAVerifiedFixAsADraftPullRequest(t *testing.T) {
	f := setup(t, fixed, "echo https://github.com/acme/shop/pull/7\n")
	sum, err := f.run(RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Outcome != Fixed || sum.Delivered != deliver.PR || sum.URL != "https://github.com/acme/shop/pull/7" {
		t.Fatalf("summary: %+v", sum)
	}
	if sum.Test != "value is small" || sum.Culprit == nil || sum.Culprit.SHA != f.culprit || sum.RootCause != "the value is too big" {
		t.Errorf("summary: %+v", sum)
	}

	want := []string{
		"run:run_started",
		"triage:stage_started", "triage:triage_done", "triage:stage_done",
		"reproduce:stage_started", "reproduce:reproduce_done", "reproduce:stage_done",
		"locate:stage_started", "locate:locate_started", "locate:culprit_found", "locate:stage_done",
		"heal:stage_started", "heal:agent_done", "heal:stage_done",
		"deliver:stage_started", "deliver:deliver_done", "deliver:stage_done",
		"run:run_done",
	}
	if got := f.events(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("events:\n got %v\nwant %v", got, want)
	}

	// The fix is one commit on top of the failing commit, on its own branch,
	// pushed to the remote.
	if !strings.HasPrefix(sum.Branch, "heall/fix-") {
		t.Fatalf("branch = %q", sum.Branch)
	}
	remote := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"--git-dir", f.remote}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if parent := remote("rev-parse", sum.Branch+"^"); parent != f.bad {
		t.Errorf("the fix commit's parent is %s, want the failing commit", parent)
	}
	if got := remote("show", sum.Branch+":src/value"); got != "4" {
		t.Errorf("src/value on the fix branch is %q", got)
	}
	if msg := remote("log", "-1", "--format=%an|%B", sum.Branch); !strings.HasPrefix(msg, "heall|fix: value is small") || !strings.Contains(msg, "the value is too big") {
		t.Errorf("commit: %s", msg)
	}
	if remote("rev-parse", "main") != f.bad {
		t.Error("the base branch on the remote was moved")
	}

	calls := f.ghCalls()
	for _, want := range []string{"pr create --draft", "--base main", "--head " + sum.Branch, "--body-file"} {
		if !strings.Contains(calls, want) {
			t.Errorf("gh was called as %q, lacking %q", calls, want)
		}
	}
	report, err := os.ReadFile(filepath.Join(f.dir, "out", "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"the value is too big", "+4", "change 7", "commit 7 of 12", "failed 3 of 3 runs", "**draft**"} {
		if !strings.Contains(string(report), want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	f.unchanged()
}

func TestDryRunWritesFilesAndTouchesNothingElse(t *testing.T) {
	f := setup(t, fixed, "echo should-not-be-called; exit 1\n")
	sum, err := f.run(RunOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Outcome != Fixed || sum.Delivered != deliver.PatchFile || sum.URL != "" || sum.Branch != "" {
		t.Errorf("summary: %+v", sum)
	}
	patch, err := os.ReadFile(sum.Path)
	if err != nil || !strings.Contains(string(patch), "+4") {
		t.Errorf("patch file %s: %v\n%s", sum.Path, err, patch)
	}
	if f.ghCalls() != "" {
		t.Errorf("gh was called in a dry run: %s", f.ghCalls())
	}
	if branches := f.fx.Git("branch", "--list", "heall/*"); branches != "" {
		t.Errorf("a dry run made a branch: %s", branches)
	}

	// The saved run can be published later.
	ev, err := deliver.LoadEvidence(filepath.Dir(sum.Path))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Bad != f.bad || ev.Base != "main" || ev.Test != "value is small" || ev.Fix.Patch != string(patch) || ev.Culprit.Commit.SHA != f.culprit {
		t.Errorf("evidence: %+v", ev)
	}
	f.unchanged()
}

func TestFixIsKeptWhenGitHubIsOutOfReach(t *testing.T) {
	f := setup(t, fixed, "echo 'gh: not logged in' >&2\nexit 4\n")
	var warnings []string
	f.p.Emit.Listen(func(ev events.Event) {
		if ev.Kind == events.KindLog && ev.Stage == events.StageDeliver {
			warnings = append(warnings, string(ev.Data))
		}
	})
	sum, err := f.run(RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A proven fix is not thrown away because a pull request failed.
	if sum.Outcome != Fixed || sum.Delivered != deliver.PatchFile || sum.Path == "" {
		t.Errorf("summary: %+v", sum)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not logged in") {
		t.Errorf("warnings: %v", warnings)
	}
}

func TestEscalationDeliversADiagnosisInsteadOfAFix(t *testing.T) {
	escalate := agentScript(events.AgentDone{Outcome: "escalated", RootCause: "Two tests disagree.", Reason: "the test is outdated"})
	f := setup(t, escalate, "echo https://github.com/acme/shop/commit/abc#commitcomment-1\n")
	sum, err := f.run(RunOptions{})

	var esc *Escalation
	if !errors.As(err, &esc) || esc.Stage != events.StageHeal || esc.Reason != "the test is outdated" {
		t.Fatalf("got %v, want an escalation at heal", err)
	}
	if sum.Outcome != Escalated || sum.Stage != events.StageHeal || sum.Delivered != deliver.Diagnosis {
		t.Errorf("summary: %+v", sum)
	}
	if !strings.Contains(sum.URL, "commitcomment") || !strings.Contains(f.ghCalls(), "commits/"+f.bad+"/comments") {
		t.Errorf("the diagnosis should be posted on the failing commit: url=%q gh=%q", sum.URL, f.ghCalls())
	}
	diagnosis, err := os.ReadFile(sum.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"could not fix `value is small`", "**heal** stage", "Two tests disagree.", "change 7", "did not open a pull request"} {
		if !strings.Contains(string(diagnosis), want) {
			t.Errorf("the diagnosis lacks %q:\n%s", want, diagnosis)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, "out", "fix.patch")); err == nil {
		t.Error("an escalated run left a fix.patch")
	}
	got := f.events()
	tail := strings.Join(got[len(got)-6:], " ")
	if tail != "heal:escalated heal:stage_done deliver:stage_started deliver:deliver_done deliver:stage_done run:run_done" {
		t.Errorf("events end with: %s", tail)
	}
	f.unchanged()
}

func TestRunStopsEarlyWhenThereIsNothingToFix(t *testing.T) {
	f := setup(t, "echo 'the agent must not be started' >&2; exit 9\n", "")
	log := "ok 1 - value is small\nok 2 - value is even\n"
	sum, err := f.run(RunOptions{Log: &log, DryRun: true})
	var esc *Escalation
	if !errors.As(err, &esc) || esc.Stage != events.StageTriage {
		t.Fatalf("got %v, want an escalation at triage", err)
	}
	if sum.Outcome != Escalated || sum.Delivered != deliver.Diagnosis {
		t.Errorf("summary: %+v", sum)
	}
	for _, ev := range f.events() {
		if strings.HasPrefix(ev, "reproduce:") || strings.HasPrefix(ev, "locate:") || strings.HasPrefix(ev, "heal:") {
			t.Errorf("stage ran after triage escalated: %s", ev)
		}
	}
}

func TestAgentCrashIsAnErrorNotAnEscalation(t *testing.T) {
	f := setup(t, "echo 'heall-agent: no Groq API key' >&2; exit 2\n", "")
	sum, err := f.run(RunOptions{DryRun: true})
	var esc *Escalation
	if err == nil || errors.As(err, &esc) || !strings.Contains(err.Error(), "no Groq API key") {
		t.Fatalf("got %v, want a plain error naming the problem", err)
	}
	if sum.Outcome != Failed || sum.Delivered != "" {
		t.Errorf("summary: %+v", sum)
	}
	got := f.events()
	if got[len(got)-1] != "run:run_done" {
		t.Errorf("the stream must still end with run_done: %v", got[len(got)-3:])
	}
	f.unchanged()
}
