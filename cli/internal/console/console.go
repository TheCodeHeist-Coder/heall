// Package console prints the event stream as a readable log for people
// running heall in a terminal.
package console

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"heall/internal/events"
)

type Printer struct {
	w     io.Writer
	color bool
	// index maps a commit hash to its position in the range being searched.
	index   map[string]int
	commits []events.Commit
	// good and bad label the two ends in the reproduce stage.
	good, bad string
	reproduce bool
	// calls remembers each tool call until its result arrives.
	calls map[string]string
}

// SetEnds tells the printer which commits are the known-good and the bad
// one, so runs on them can be labelled.
func (p *Printer) SetEnds(good, bad string) {
	p.good, p.bad = good, bad
}

// New prints to w. Colour is used when w is a terminal and NO_COLOR is unset.
func New(w io.Writer) *Printer {
	color := false
	if f, ok := w.(*os.File); ok && os.Getenv("NO_COLOR") == "" {
		if info, err := f.Stat(); err == nil {
			color = info.Mode()&os.ModeCharDevice != 0
		}
	}
	return &Printer{w: w, color: color, index: map[string]int{}, calls: map[string]string{}}
}

const maxThoughtLines = 4

const (
	green  = "32"
	red    = "31"
	yellow = "33"
	gray   = "90"
	bold   = "1"
)

func (p *Printer) paint(code, s string) string {
	if !p.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p *Printer) verdict(v events.Verdict) string {
	switch v {
	case events.Pass:
		return p.paint(green, "✓ pass   ")
	case events.Fail:
		return p.paint(red, "✗ fail   ")
	case events.Flaky:
		return p.paint(yellow, "~ flaky  ")
	default:
		return p.paint(gray, "– skipped")
	}
}

// toolArgs shows the argument that says what a tool call is about.
func toolArgs(name string, input json.RawMessage) string {
	var args map[string]any
	if json.Unmarshal(input, &args) != nil {
		return ""
	}
	key := map[string]string{
		"read_file": "path", "list_files": "directory", "search": "pattern", "run_test": "name", "escalate": "reason",
	}[name]
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// wrap breaks text into lines of at most width characters, keeping the
// text's own line breaks.
func wrap(text string, width int) []string {
	var out []string
	for _, para := range strings.Split(strings.TrimSpace(text), "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if line != "" && len(line)+1+len(word) > width {
				out = append(out, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		out = append(out, line)
	}
	return out
}

func seconds(ms int64) string {
	return fmt.Sprintf("%.1fs", (time.Duration(ms) * time.Millisecond).Seconds())
}

// Handle prints one event. Register it with events.Emitter.Listen. Events it
// has nothing to say about are ignored.
func (p *Printer) Handle(ev events.Event) {
	payload, err := events.Decode(ev.Kind, ev.Data)
	if err != nil {
		return
	}
	switch d := payload.(type) {
	case *events.RunStarted:
		p.SetEnds(d.Good, d.Bad)
		where := fmt.Sprintf("%.10s", d.Bad)
		if d.Branch != "" {
			where = d.Branch + " (" + where + ")"
		}
		mode := ""
		if d.DryRun {
			mode = ", dry run"
		}
		fmt.Fprintf(p.w, "%s %s: last good %.10s, failing %s%s\n", p.paint(bold, "heall"), ev.RunID, d.Good, where, mode)
	case *events.DeliverDone:
		switch d.Outcome {
		case "pr":
			fmt.Fprintf(p.w, "%s draft pull request opened: %s\n", p.paint(bold, "deliver"), d.URL)
		case "patch_file":
			fmt.Fprintf(p.w, "%s the fix was saved to %s, with report.md beside it\n", p.paint(bold, "deliver"), d.Path)
		default:
			fmt.Fprintf(p.w, "%s the diagnosis was saved to %s\n", p.paint(bold, "deliver"), d.Path)
			if d.URL != "" {
				fmt.Fprintf(p.w, "  and posted on the failing commit: %s\n", d.URL)
			}
		}
	case *events.RunDone:
		label := map[string]string{
			"fixed":     p.paint(bold+";"+green, "fixed"),
			"escalated": p.paint(bold+";"+yellow, "escalated"),
			"error":     p.paint(bold+";"+red, "failed"),
		}[d.Outcome]
		fmt.Fprintf(p.w, "%s in %s\n", label, seconds(d.DurationMS))
	case *events.TriageDone:
		fmt.Fprintf(p.w, "%s %s\n", p.paint(bold, "triage"), d.TestName)
		fmt.Fprintf(p.w, "  in %s\n", d.TestFile)
		if len(d.SuspectFiles) > 0 {
			fmt.Fprintf(p.w, "  files to look at first: %s\n", strings.Join(d.SuspectFiles, ", "))
		}
		for _, line := range strings.Split(d.Excerpt, "\n") {
			fmt.Fprintf(p.w, "    %s\n", p.paint(gray, line))
		}
	case *events.ReproduceRun:
		if !p.reproduce {
			p.reproduce = true
			fmt.Fprintf(p.w, "%s running the test on the bad and the good commit\n", p.paint(bold, "reproduce"))
		}
		label := "bad "
		if d.SHA == p.good {
			label = "good"
		}
		fmt.Fprintf(p.w, "    %s  %s %.10s  run %-3d %6s\n", p.verdict(d.Verdict), label, d.SHA, d.Attempt, seconds(d.DurationMS))
	case *events.ReproduceDone:
		switch {
		case d.Reproduced:
			fmt.Fprintf(p.w, "  %s failed %d of %d runs on the bad commit and passes on the good commit\n",
				p.paint(green, "reproduced:"), d.Failures, d.Runs)
		case d.Flaky:
			fmt.Fprintf(p.w, "  %s failed %d of %d runs on the same commit\n", p.paint(yellow, "flaky:"), d.Failures, d.Runs)
		default:
			fmt.Fprintf(p.w, "  %s failed %d of %d runs on the bad commit\n", p.paint(yellow, "not confirmed:"), d.Failures, d.Runs)
		}
	case *events.Escalated:
		fmt.Fprintf(p.w, "%s at %s: %s\n", p.paint(bold+";"+yellow, "escalated"), ev.Stage, d.Reason)
		for _, line := range strings.Split(d.Diagnosis, "\n") {
			fmt.Fprintf(p.w, "  %s\n", line)
		}
	case *events.AgentStarted:
		fmt.Fprintf(p.w, "%s agent on %s, up to %d patch attempts\n", p.paint(bold, "heal"), d.Model, d.MaxAttempts)
	case *events.AgentThought:
		// The terminal shows the start of a thought; the event stream and
		// the dashboard have all of it.
		lines := wrap(d.Text, 96)
		if len(lines) > maxThoughtLines {
			lines = append(lines[:maxThoughtLines], "…")
		}
		for _, line := range lines {
			fmt.Fprintf(p.w, "  %s\n", p.paint(gray, "│ "+line))
		}
	case *events.ToolCall:
		p.calls[d.ID] = fmt.Sprintf("%s %s", d.Name, toolArgs(d.Name, d.Input))
	case *events.ToolResult:
		// A submitted patch is reported by the events it causes. Only a
		// submission that never reached the verifier needs a line here.
		if d.Name == "submit_patch" && (d.OK || strings.HasPrefix(d.Summary, "failed") || strings.HasPrefix(d.Summary, "rejected")) {
			return
		}
		// An escalation is reported by the stage that receives it.
		if d.Name == "escalate" && d.OK {
			return
		}
		mark := p.paint(green, "✓")
		if !d.OK {
			mark = p.paint(yellow, "!")
		}
		fmt.Fprintf(p.w, "  %s %-46.46s %s\n", mark, p.calls[d.ID], p.paint(gray, d.Summary))
	case *events.PatchSubmitted:
		fmt.Fprintf(p.w, "  %s\n", p.paint(bold, fmt.Sprintf("patch %d", d.Attempt)))
		for _, line := range strings.Split(strings.TrimRight(d.Diff, "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "new file"):
				line = p.paint(bold, line)
			case strings.HasPrefix(line, "+"):
				line = p.paint(green, line)
			case strings.HasPrefix(line, "-"):
				line = p.paint(red, line)
			case strings.HasPrefix(line, "@@"):
				line = p.paint(gray, line)
			}
			fmt.Fprintf(p.w, "    %s\n", line)
		}
	case *events.GuardrailChecked:
		if d.Passed {
			fmt.Fprintf(p.w, "    %s all %d checks passed\n", p.paint(green, "guardrails:"), len(d.Checks))
			return
		}
		for _, c := range d.Checks {
			if !c.Passed {
				fmt.Fprintf(p.w, "    %s %s: %s\n", p.paint(red, "guardrail blocked it:"), c.Name, c.Detail)
			}
		}
	case *events.VerifyDone:
		switch d.Status {
		case "verified":
			fmt.Fprintf(p.w, "    %s the failing test passes and nothing else broke\n", p.paint(green, "✓ verified:"))
		case "rejected":
			fmt.Fprintf(p.w, "    %s the patch was not applied\n", p.paint(red, "✗ rejected:"))
		default:
			why := "the failing test still fails"
			if len(d.NewFailures) > 0 {
				why = fmt.Sprintf("it breaks %d other test(s): %s", len(d.NewFailures), strings.Join(d.NewFailures, "; "))
			} else if d.TargetPassed {
				why = "the full suite did not pass"
			}
			fmt.Fprintf(p.w, "    %s %s\n", p.paint(red, "✗ failed:"), why)
		}
	case *events.AgentDone:
		if d.Outcome == "fixed" {
			fmt.Fprintf(p.w, "  %s after %d attempt(s). Root cause: %s\n", p.paint(bold+";"+green, "fix found"), d.Attempts, d.RootCause)
		}
	case *events.LocateStarted:
		p.commits = d.Commits
		for i, c := range d.Commits {
			p.index[c.SHA] = i
		}
		fmt.Fprintf(p.w, "%s %d commits between good and bad, %d workers\n",
			p.paint(bold, "locate"), len(d.Commits)-1, d.Workers)
	case *events.RoundStarted:
		if d.Round == 0 {
			fmt.Fprintf(p.w, "  %s\n", p.paint(bold, "checking the good and bad commits"))
			return
		}
		fmt.Fprintf(p.w, "  %s testing %d of the %d commits left\n",
			p.paint(bold, fmt.Sprintf("round %d:", d.Round)), len(d.Probes), d.Hi-d.Lo)
	case *events.CommitTested:
		i := p.index[d.SHA]
		subject := ""
		if i < len(p.commits) {
			subject = p.commits[i].Subject
		}
		fmt.Fprintf(p.w, "    %s  #%-4d %.10s  %-52.52s %6s\n", p.verdict(d.Verdict), i, d.SHA, subject, seconds(d.DurationMS))
	case *events.RoundDone:
		if d.Round == 0 {
			return
		}
		fmt.Fprintf(p.w, "    %s\n", p.paint(gray, fmt.Sprintf("the failure starts after #%d and by #%d", d.Lo, d.Hi)))
	case *events.CulpritFound:
		fmt.Fprintf(p.w, "%s #%d %.10s %s\n", p.paint(bold+";"+red, "culprit"), p.index[d.Commit.SHA], d.Commit.SHA, d.Commit.Subject)
		fmt.Fprintf(p.w, "  by %s on %.10s; found in %d rounds, %d commits tested, %s\n",
			d.Commit.Author, d.Commit.Date, d.Rounds, d.Tested, seconds(d.DurationMS))
	case *events.Log:
		code := gray
		switch d.Level {
		case "warn":
			code = yellow
		case "error":
			code = red
		}
		fmt.Fprintf(p.w, "  %s\n", p.paint(code, d.Level+": "+d.Message))
	}
}
