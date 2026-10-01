// Package console prints the event stream as a readable log for people
// running heall in a terminal.
package console

import (
	"fmt"
	"io"
	"os"
	"time"

	"heall/internal/events"
)

type Printer struct {
	w     io.Writer
	color bool
	// index maps a commit hash to its position in the range being searched.
	index   map[string]int
	commits []events.Commit
}

// New prints to w. Colour is used when w is a terminal and NO_COLOR is unset.
func New(w io.Writer) *Printer {
	color := false
	if f, ok := w.(*os.File); ok && os.Getenv("NO_COLOR") == "" {
		if info, err := f.Stat(); err == nil {
			color = info.Mode()&os.ModeCharDevice != 0
		}
	}
	return &Printer{w: w, color: color, index: map[string]int{}}
}

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
