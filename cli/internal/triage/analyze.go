package triage

import (
	"errors"
	"path"
	"regexp"
	"strings"
)

// Source reads files of the repository at the failing commit. Paths are
// relative to the repository root and use forward slashes.
type Source interface {
	Exists(path string) bool
	Read(path string) (string, error)
}

// Report is what triage hands to the rest of the pipeline.
type Report struct {
	// Primary is the failure heall will work on: the first one in the log.
	Primary Failure `json:"primary"`
	// Failures holds every failure in the log, Primary included.
	Failures []Failure `json:"failures"`
	// SuspectFiles are source files worth reading first for Primary: files
	// in its stack trace, then what its test file imports.
	SuspectFiles []string `json:"suspect_files"`
}

// ErrNoFailure means the log holds no failing test that triage recognises.
var ErrNoFailure = errors.New("no failing test found in the log")

// Analyze parses a log and resolves what it finds against the repository.
// src may be nil, in which case paths are left as the log printed them and
// no suspect files are worked out.
func Analyze(log string, src Source) (Report, error) {
	failures := Parse(log)
	if len(failures) == 0 {
		return Report{}, ErrNoFailure
	}
	if src != nil {
		for i := range failures {
			f := &failures[i]
			f.File = relative(f.File, src)
			kept := f.StackFiles[:0]
			for _, file := range f.StackFiles {
				// Frames outside the repository (dependencies, the runtime)
				// are not something a patch can touch.
				if rel := relative(file, src); src.Exists(rel) {
					kept = append(kept, rel)
				}
			}
			f.StackFiles = kept
		}
	}
	rep := Report{Primary: failures[0], Failures: failures}
	if src != nil {
		rep.SuspectFiles = Suspects(rep.Primary, src)
	}
	return rep, nil
}

// relative maps a path from a log to a path in the repository. Logs carry
// absolute paths from the machine that ran the tests (a CI runner, a
// container), so the longest trailing part that exists in the repository is
// taken to be the file.
func relative(file string, src Source) string {
	clean := strings.TrimPrefix(strings.ReplaceAll(file, `\`, "/"), "file://")
	parts := strings.Split(strings.TrimPrefix(path.Clean(clean), "/"), "/")
	for i := range parts {
		if candidate := strings.Join(parts[i:], "/"); src.Exists(candidate) {
			return candidate
		}
	}
	return file
}

const (
	maxSuspects    = 12
	maxImportDepth = 2
)

// A relative import or require in JavaScript or TypeScript.
var importPath = regexp.MustCompile(`(?:\bfrom\s*|\bimport\s*\(\s*|\brequire\s*\(\s*|\bimport\s+)["'](\.{1,2}/[^"']+)["']`)

// Suspects lists source files to read first: those in the stack trace, then
// what the test and those files import, then what those import in turn. The
// test file itself is left out; it is evidence, not a suspect.
func Suspects(f Failure, src Source) []string {
	var out []string
	seen := map[string]bool{f.File: true}
	add := func(file string) bool {
		if seen[file] || len(out) >= maxSuspects {
			return false
		}
		seen[file] = true
		out = append(out, file)
		return true
	}
	for _, file := range f.StackFiles {
		add(file)
	}

	level := append([]string{f.File}, out...)
	for depth := 0; depth < maxImportDepth && len(level) > 0; depth++ {
		var next []string
		for _, file := range level {
			for _, imp := range imports(file, src) {
				if add(imp) {
					next = append(next, imp)
				}
			}
		}
		level = next
	}
	return out
}

// imports returns the repository files that file imports by relative path.
func imports(file string, src Source) []string {
	if file == "" || !src.Exists(file) {
		return nil
	}
	content, err := src.Read(file)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range importPath.FindAllStringSubmatch(content, -1) {
		target := path.Join(path.Dir(file), m[1])
		// Imports may leave the extension off.
		for _, candidate := range []string{target, target + ".js", target + ".ts", target + "/index.js"} {
			if src.Exists(candidate) {
				out = append(out, candidate)
				break
			}
		}
	}
	return out
}
