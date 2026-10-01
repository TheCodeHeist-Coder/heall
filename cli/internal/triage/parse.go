// Package triage turns a test log into structured failure information: which
// test failed, where, with what error, and which source files to look at
// first. It is deterministic parsing; no model is involved.
package triage

import (
	"regexp"
	"strconv"
	"strings"
)

// Failure is one failing test found in a log.
type Failure struct {
	// Test is the name of the failing test, as the runner selects it.
	Test string `json:"test"`
	// Suite lists the enclosing suites, outermost first.
	Suite []string `json:"suite,omitempty"`
	// File and Line locate the test. File is as written in the log until
	// Analyze makes it relative to the repository.
	File string `json:"file"`
	Line int    `json:"line"`
	// ErrorName is the error type, such as AssertionError.
	ErrorName string `json:"error_name,omitempty"`
	Message   string `json:"message"`
	// Excerpt is the part of the log that describes this failure.
	Excerpt string `json:"excerpt"`
	// StackFiles are the files in the stack trace, innermost first, without
	// runtime internals.
	StackFiles []string `json:"stack_files,omitempty"`
}

var (
	// GitHub Actions prefixes each line with a timestamp, and `gh run view
	// --log` adds the job and step names before that.
	actionsPrefix = regexp.MustCompile(`^(?:[^\t]*\t[^\t]*\t)?\x{FEFF}?\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z ?`)
	ansi          = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
)

// clean strips what CI adds around the test runner's own output.
func clean(log string) []string {
	log = strings.ReplaceAll(log, "\r\n", "\n")
	lines := strings.Split(ansi.ReplaceAllString(log, ""), "\n")
	for i, line := range lines {
		lines[i] = actionsPrefix.ReplaceAllString(line, "")
	}
	return lines
}

// Parse finds the failing tests in a log. It understands the two formats the
// Node test runner prints: TAP (--test-reporter=tap) and the default "spec"
// report. It returns nil when the log holds no failure it can recognise.
func Parse(log string) []Failure {
	lines := clean(log)
	if f := parseTAP(lines); len(f) > 0 {
		return f
	}
	return parseSpec(lines)
}

const maxExcerptLines = 40

// excerpt joins the lines that describe a failure, without the stack frames
// inside the runtime: they are the same for every failure and say nothing
// about the code under test.
func excerpt(lines []string) string {
	kept := lines[:0:0]
	for _, line := range lines {
		t := strings.TrimPrefix(strings.TrimSpace(line), "at ")
		if strings.Contains(t, "(node:") || strings.HasPrefix(t, "node:") || strings.HasPrefix(t, "async node:") {
			continue
		}
		kept = append(kept, line)
	}
	lines = kept
	if len(lines) > maxExcerptLines {
		lines = append(append([]string{}, lines[:maxExcerptLines]...), "...")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

var (
	tapResult  = regexp.MustCompile(`^(\s*)(not ok|ok) \d+ - (.*?)(?: # (SKIP|TODO)\b.*)?$`)
	tapSubtest = regexp.MustCompile(`^(\s*)# Subtest: (.*)$`)
	tapKey     = regexp.MustCompile(`^([A-Za-z_]+):(?: (.*))?$`)
	location   = regexp.MustCompile(`^(.*):(\d+):\d+$`)
	// A stack frame's file, with or without a function name before it.
	// The path must start the frame or follow a space or "(", so that
	// "node:internal/test_runner/test:1:2" is not mistaken for a file.
	frameFile = regexp.MustCompile(`(?:^|[\s(])((?:file://)?/[^\s()]+?):\d+:\d+\)?$`)
)

func parseTAP(lines []string) []Failure {
	var out []Failure
	// suites[d] is the latest "# Subtest:" seen at nesting depth d.
	var suites []string

	for i := 0; i < len(lines); i++ {
		if m := tapSubtest.FindStringSubmatch(lines[i]); m != nil {
			depth := len(m[1]) / 4
			suites = append(suites[:min(depth, len(suites))], m[2])
			continue
		}
		m := tapResult.FindStringSubmatch(lines[i])
		if m == nil || m[2] == "ok" || m[4] != "" {
			continue
		}
		indent, name := m[1], m[3]
		i0 := i

		// The YAML block that follows holds the details.
		start := i + 1
		if start >= len(lines) || strings.TrimSpace(lines[start]) != "---" {
			out = append(out, Failure{Test: name, Excerpt: lines[i]})
			continue
		}
		end := start + 1
		for end < len(lines) && lines[end] != indent+"  ..." {
			end++
		}
		fields := tapFields(lines[start+1:end], len(indent)+2)
		i = end

		// A suite fails because a test inside it failed; report the test.
		if unquote(fields["failureType"]) == "subtestsFailed" {
			continue
		}
		f := Failure{
			Test:      name,
			ErrorName: unquote(fields["name"]),
			Message:   firstLine(unquote(fields["error"])),
		}
		if depth := len(indent) / 4; depth > 0 && depth <= len(suites) {
			f.Suite = append([]string{}, suites[:depth]...)
		}
		if loc := location.FindStringSubmatch(unquote(fields["location"])); loc != nil {
			f.File = loc[1]
			f.Line, _ = strconv.Atoi(loc[2])
		}
		f.StackFiles = stackFiles(strings.Split(fields["stack"], "\n"))

		msg := unquote(fields["error"])
		// A test file that fails to load is reported as a test named after
		// the file. When each file runs in its own process, the real error
		// is only in the comment lines printed before it.
		if msg == "test failed" && strings.HasSuffix(f.File, name) {
			f.ErrorName, msg = loadError(lines[:i0])
			f.Message = firstLine(msg)
		}

		block := []string{"not ok - " + name}
		if msg != "" {
			label := f.ErrorName
			if label == "" {
				label = "error"
			}
			block = append(block, label+": "+msg)
		}
		if s := fields["stack"]; s != "" {
			block = append(block, "stack:", s)
		}
		f.Excerpt = excerpt(strings.Split(strings.Join(block, "\n"), "\n"))
		out = append(out, f)
	}
	return out
}

var tapCommentError = regexp.MustCompile(`^# ([A-Za-z_$][\w$]*Error): (.*)$`)

// loadError looks back through the TAP comment lines above a result for the
// error the runtime printed, such as "# SyntaxError: ...".
func loadError(before []string) (name, message string) {
	for i := len(before) - 1; i >= 0 && strings.HasPrefix(before[i], "#"); i-- {
		if m := tapCommentError.FindStringSubmatch(before[i]); m != nil {
			return m[1], m[2]
		}
	}
	return "", "the test file failed to load"
}

// tapFields reads the top-level keys of a TAP YAML block indented by indent
// spaces. Block scalars ("key: |-") are joined into one multi-line value.
func tapFields(lines []string, indent int) map[string]string {
	fields := map[string]string{}
	pad := strings.Repeat(" ", indent)
	for i := 0; i < len(lines); i++ {
		line, ok := strings.CutPrefix(lines[i], pad)
		if !ok {
			continue
		}
		m := tapKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key, value := m[1], m[2]
		if strings.HasPrefix(value, "|") || value == "" {
			var body []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], pad+"  ") || strings.TrimSpace(lines[i+1]) == "") {
				i++
				body = append(body, strings.TrimPrefix(lines[i], pad+"  "))
			}
			value = strings.TrimRight(strings.Join(body, "\n"), "\n ")
		}
		fields[key] = value
	}
	return fields
}

// unquote removes YAML quoting from a scalar.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// stackFiles extracts the files from stack frames, dropping runtime
// internals such as "node:internal/..." and keeping each file once.
func stackFiles(frames []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, frame := range frames {
		m := frameFile.FindStringSubmatch(strings.TrimSpace(frame))
		if m == nil {
			continue
		}
		file := strings.TrimPrefix(m[1], "file://")
		if !seen[file] {
			seen[file] = true
			out = append(out, file)
		}
	}
	return out
}

var (
	specHeader = regexp.MustCompile(`^✖ failing tests:\s*$`)
	specAt     = regexp.MustCompile(`^test at (.*):(\d+):\d+$`)
	specName   = regexp.MustCompile(`^(✖|⚠) (.*?) \([\d.]+ms\)(?: # (?:SKIP|TODO)\b.*)?$`)
	specError  = regexp.MustCompile(`^([A-Za-z_$][\w$]*)(?: \[[^\]]*\])?: (.*)$`)
)

// parseSpec reads the "failing tests:" section that the default reporter
// prints at the end of a run.
func parseSpec(lines []string) []Failure {
	start := -1
	for i, line := range lines {
		if specHeader.MatchString(line) {
			start = i + 1
		}
	}
	if start < 0 {
		return nil
	}
	var out []Failure
	for i := start; i < len(lines); i++ {
		at := specAt.FindStringSubmatch(lines[i])
		if at == nil || i+1 >= len(lines) {
			continue
		}
		name := specName.FindStringSubmatch(lines[i+1])
		if name == nil {
			continue
		}
		// The details run up to the next "test at" line.
		end := i + 2
		for end < len(lines) {
			if specAt.MatchString(lines[end]) {
				break
			}
			end++
		}
		header, body := lines[i+1], lines[i+2:end]
		i = end - 1
		if name[1] == "⚠" {
			continue // a TODO test is allowed to fail
		}

		f := Failure{Test: name[2], File: at[1]}
		f.Line, _ = strconv.Atoi(at[2])
		var frames []string
		for _, line := range body {
			t := strings.TrimSpace(line)
			if frame, ok := strings.CutPrefix(t, "at "); ok {
				frames = append(frames, strings.TrimSuffix(frame, " {"))
			}
		}
		if len(body) > 0 {
			first := strings.TrimSpace(body[0])
			if m := specError.FindStringSubmatch(first); m != nil {
				f.ErrorName, f.Message = m[1], m[2]
			} else {
				f.Message = unquote(first)
			}
		}
		// A file that fails to load is reported as a test named after the
		// file, with no useful message of its own.
		if f.Test == f.File && f.Message == "test failed" {
			f.Message = "the test file failed to load"
		}
		f.StackFiles = stackFiles(frames)
		f.Excerpt = excerpt(append([]string{header}, dedent(body)...))
		out = append(out, f)
	}
	return out
}

func dedent(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, strings.TrimPrefix(line, "  "))
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}
