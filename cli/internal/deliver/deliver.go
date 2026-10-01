// Package deliver hands the result of a run to people: a draft pull request
// with the evidence for a verified fix, or a written diagnosis when heall
// escalated. Whatever happens, the patch or diagnosis is also saved as a
// file, so a run is never lost to a GitHub or network problem.
package deliver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"heall/internal/events"
	"heall/internal/gitx"
)

// Outcomes, as in the deliver_done event.
const (
	PR        = "pr"
	PatchFile = "patch_file"
	Diagnosis = "diagnosis"
)

type Deliverer struct {
	Repo *gitx.Repo
	// OutDir is where the patch, report and diagnosis files are written.
	OutDir string
	// DryRun writes the files and stops there: nothing is pushed or posted.
	DryRun bool
	// Remote is the git remote the fix branch is pushed to.
	Remote string
	// Gh is the GitHub CLI command; nil means "gh".
	Gh []string
	// Warn reports why a pull request or comment could not be made.
	Warn func(message string)
}

// EvidenceFile is the name of the file a run's evidence is saved in.
const EvidenceFile = "evidence.json"

// LoadEvidence reads the evidence a previous run saved in dir.
func LoadEvidence(dir string) (Evidence, error) {
	var ev Evidence
	raw, err := os.ReadFile(filepath.Join(dir, EvidenceFile))
	if err != nil {
		return ev, fmt.Errorf("%s does not hold a fix from a heall run: %w", dir, err)
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return ev, fmt.Errorf("read %s: %w", EvidenceFile, err)
	}
	if ev.Fix == nil || ev.Fix.Patch == "" {
		return ev, fmt.Errorf("%s holds no verified fix", dir)
	}
	return ev, nil
}

type Result struct {
	Outcome string
	// URL of the pull request or comment, when one was made.
	URL string
	// Path of the patch or diagnosis file.
	Path string
	// Branch holding the fix commit, when one was made.
	Branch string
}

// Evidence is everything a run found out, for the report.
type Evidence struct {
	RunID string
	// Base is the branch the failure is on; a pull request targets it.
	Base      string
	Good, Bad string
	Test      string
	TestFile  string
	// Failure is the test's output on the bad commit.
	Failure string

	// Reproduce: nil when the stage did not run.
	Reproduce *Reproduced
	// Locate: nil when no culprit was found.
	Culprit *Culprit
	// Fix: nil unless a fix was verified.
	Fix *Fix

	Sandbox string
	Model   string
}

type Reproduced struct {
	Runs, Failures int
}

type Culprit struct {
	Commit   events.Commit
	Message  string
	Index    int
	Commits  int
	Rounds   int
	Tested   int
	Workers  int
	Seconds  float64
	Suspects []events.Commit
}

type Fix struct {
	Patch       string
	RootCause   string
	Attempts    int
	MaxAttempts int
}

func short(sha string) string { return fmt.Sprintf("%.10s", sha) }

func (d Deliverer) warn(format string, args ...any) {
	if d.Warn != nil {
		d.Warn(fmt.Sprintf(format, args...))
	}
}

func (d Deliverer) write(name, content string) (string, error) {
	if err := os.MkdirAll(d.OutDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(d.OutDir, name)
	return path, os.WriteFile(path, []byte(content), 0o644)
}

func (d Deliverer) gh(ctx context.Context, args ...string) (string, error) {
	argv := d.Gh
	if len(argv) == 0 {
		argv = []string{"gh"}
	}
	cmd := exec.CommandContext(ctx, argv[0], append(append([]string{}, argv[1:]...), args...)...)
	cmd.Dir = d.Repo.Dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", errors.New(strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var url = regexp.MustCompile(`https?://\S+`)

// Fix delivers a verified fix. It opens a draft pull request when it can and
// otherwise leaves the patch and the report as files.
func (d Deliverer) Fix(ctx context.Context, ev Evidence) (Result, error) {
	if ev.Fix == nil {
		return Result{}, errors.New("no verified fix to deliver")
	}
	patchPath, err := d.write("fix.patch", ev.Fix.Patch)
	if err != nil {
		return Result{}, err
	}
	body := FixReport(ev)
	if _, err := d.write("report.md", body); err != nil {
		return Result{}, err
	}
	// Kept so that a dry run can be published later with `heall pr`.
	if raw, err := json.MarshalIndent(ev, "", "  "); err == nil {
		if _, err := d.write(EvidenceFile, string(raw)+"\n"); err != nil {
			return Result{}, err
		}
	}
	res := Result{Outcome: PatchFile, Path: patchPath}
	if d.DryRun {
		return res, nil
	}

	// From here on, a failure is not an error: the fix is proven and saved,
	// it just could not be put on GitHub.
	if ev.Base == "" {
		d.warn("no pull request: --bad is not a branch, so there is nothing to target; pass --base")
		return res, nil
	}
	if !d.Repo.HasRemote(ctx, d.Remote) {
		d.warn("no pull request: the repository has no remote called %q", d.Remote)
		return res, nil
	}
	branch := "heall/fix-" + short(ev.Bad) + "-" + strings.TrimPrefix(ev.RunID, "r-")
	if _, err := d.Repo.CommitPatch(ctx, ev.Bad, branch, ev.Fix.Patch, commitMessage(ev), "heall", "heall@users.noreply.github.com"); err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		d.warn("no pull request: could not commit the fix: %v", err)
		return res, nil
	}
	res.Branch = branch
	if err := d.Repo.Push(ctx, d.Remote, branch); err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		d.warn("no pull request: could not push %s to %s: %v", branch, d.Remote, err)
		return res, nil
	}
	out, err := d.gh(ctx, "pr", "create", "--draft",
		"--base", ev.Base, "--head", branch,
		"--title", fmt.Sprintf("heall: fix %q", ev.Test),
		"--body-file", filepath.Join(d.OutDir, "report.md"))
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		d.warn("the fix was pushed to %s/%s, but the pull request could not be opened: %v", d.Remote, branch, err)
		return res, nil
	}
	res.Outcome = PR
	res.URL = url.FindString(out)
	return res, nil
}

// Escalation delivers a diagnosis: a file, and when possible a comment on
// the failing commit.
func (d Deliverer) Escalation(ctx context.Context, ev Evidence, stage events.Stage, reason, diagnosis string) (Result, error) {
	path, err := d.write("diagnosis.md", DiagnosisReport(ev, stage, reason, diagnosis))
	if err != nil {
		return Result{}, err
	}
	res := Result{Outcome: Diagnosis, Path: path}
	if d.DryRun || !d.Repo.HasRemote(ctx, d.Remote) {
		return res, nil
	}
	out, err := d.gh(ctx, "api", "repos/{owner}/{repo}/commits/"+ev.Bad+"/comments",
		"--method", "POST", "--field", "body=@"+path, "--jq", ".html_url")
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		d.warn("the diagnosis was saved, but could not be posted on the commit: %v", err)
		return res, nil
	}
	res.URL = url.FindString(out)
	return res, nil
}

func commitMessage(ev Evidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "fix: %s\n\n", ev.Test)
	if ev.Fix.RootCause != "" {
		fmt.Fprintf(&b, "%s\n\n", ev.Fix.RootCause)
	}
	if ev.Culprit != nil {
		fmt.Fprintf(&b, "The failure was introduced by %s (%s).\n", short(ev.Culprit.Commit.SHA), ev.Culprit.Commit.Subject)
	}
	b.WriteString("Found, fixed and verified by heall; see the pull request for the evidence.\n")
	return b.String()
}

func fence(lang, text string) string {
	// A longer fence than any run of backticks inside the text.
	ticks := "```"
	for strings.Contains(text, ticks) {
		ticks += "`"
	}
	return ticks + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + ticks + "\n"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// trail describes what the run established before its last stage.
func trail(b *strings.Builder, ev Evidence) {
	fmt.Fprintf(b, "| | |\n|---|---|\n")
	fmt.Fprintf(b, "| Failing test | `%s`", ev.Test)
	if ev.TestFile != "" {
		fmt.Fprintf(b, " in `%s`", ev.TestFile)
	}
	b.WriteString(" |\n")
	fmt.Fprintf(b, "| Range | good `%s` … bad `%s` |\n", short(ev.Good), short(ev.Bad))
	if r := ev.Reproduce; r != nil {
		fmt.Fprintf(b, "| Reproduced | failed %d of %s on the bad commit, passes on the good commit |\n", r.Failures, plural(r.Runs, "run", "runs"))
	}
	if c := ev.Culprit; c != nil {
		fmt.Fprintf(b, "| Culprit | `%s` %s, by %s on %.10s |\n", short(c.Commit.SHA), c.Commit.Subject, c.Commit.Author, c.Commit.Date)
		fmt.Fprintf(b, "| Search | commit %d of %d, found in %s (%s tested, %d at a time) in %.1fs |\n",
			c.Index, c.Commits, plural(c.Rounds, "round", "rounds"), plural(c.Tested, "commit", "commits"), c.Workers, c.Seconds)
	}
	if ev.Sandbox != "" {
		fmt.Fprintf(b, "| Sandbox | %s |\n", ev.Sandbox)
	}
	b.WriteString("\n")
	if c := ev.Culprit; c != nil && len(c.Suspects) > 0 {
		fmt.Fprintf(b, "> **Note:** %s directly before the culprit could not be built, so the failure may have started in one of them: ",
			plural(len(c.Suspects), "commit", "commits"))
		for i, s := range c.Suspects {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "`%s`", short(s.SHA))
		}
		b.WriteString(".\n\n")
	}
}

// FixReport is the body of the pull request.
func FixReport(ev Evidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Verified fix for `%s`\n\n", ev.Test)
	b.WriteString("This pull request was opened by heall, a self-healing CI agent. It is a **draft**: the fix has been proven against the tests, and a person still has to review and merge it.\n\n")

	b.WriteString("### Root cause\n\n")
	fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(ev.Fix.RootCause))

	b.WriteString("### How it was found\n\n")
	trail(&b, ev)

	b.WriteString("### The failure\n\n")
	b.WriteString(fence("", ev.Failure))
	b.WriteString("\n### The fix\n\n")
	b.WriteString(fence("diff", ev.Fix.Patch))

	b.WriteString("\n### Evidence\n\n")
	b.WriteString("- The patch only touches files the repository's `.heall.yaml` allows, and no test or test configuration.\n")
	fmt.Fprintf(&b, "- With the patch applied to `%s`, `%s` passes.\n", short(ev.Bad), ev.Test)
	b.WriteString("- The full test suite has no failure that was not already there before the patch.\n")
	b.WriteString("- Each of those checks ran in a fresh sandbox with no network, and heall repeated them itself after the agent reported the fix.\n")
	fmt.Fprintf(&b, "- The agent used %d of %s", ev.Fix.Attempts, plural(ev.Fix.MaxAttempts, "attempt", "attempts"))
	if ev.Model != "" {
		fmt.Fprintf(&b, " (model: `%s`)", ev.Model)
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "\n<sub>heall run `%s`</sub>\n", ev.RunID)
	return b.String()
}

// DiagnosisReport is what heall writes when it stops without a fix.
func DiagnosisReport(ev Evidence, stage events.Stage, reason, diagnosis string) string {
	var b strings.Builder
	title := ev.Test
	if title == "" {
		title = "the failing build at " + short(ev.Bad)
	}
	fmt.Fprintf(&b, "## heall could not fix `%s`\n\n", title)
	fmt.Fprintf(&b, "heall stopped at the **%s** stage: %s.\n\n", stage, strings.TrimRight(reason, "."))
	b.WriteString("It did not open a pull request, because it only delivers fixes it can prove.\n\n")

	b.WriteString("### Diagnosis\n\n")
	fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(diagnosis))

	if ev.Test != "" {
		b.WriteString("### What was established\n\n")
		trail(&b, ev)
	}
	if ev.Failure != "" {
		b.WriteString("### The failure\n\n")
		b.WriteString(fence("", ev.Failure))
	}
	fmt.Fprintf(&b, "\n<sub>heall run `%s`</sub>\n", ev.RunID)
	return b.String()
}
