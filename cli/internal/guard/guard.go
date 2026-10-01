// Package guard is the trust layer: the rules a patch must meet before heall
// will run it, and again before heall will deliver it. The rules are plain
// code with no model involved, so the agent cannot talk its way past them.
package guard

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"heall/internal/config"
	"heall/internal/events"
)

// Rules come from the repository's .heall.yaml.
type Rules struct {
	// Allow lists the globs a patch may touch.
	Allow []string
	// Protect lists globs a patch may never touch, even if allowed.
	Protect []string
	// ForbidAdded are patterns no added line may match, for code that would
	// make a test pass without fixing anything.
	ForbidAdded []*regexp.Regexp
}

// FromConfig builds the rules for a repository. Git's own directory is
// always protected.
func FromConfig(cfg config.Config) (Rules, error) {
	r := Rules{Allow: cfg.Allow, Protect: append([]string{".git/**"}, cfg.Protect...)}
	for _, pattern := range cfg.ForbidAdded {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return Rules{}, fmt.Errorf("forbid_added: %w", err)
		}
		r.ForbidAdded = append(r.ForbidAdded, re)
	}
	return r, nil
}

// Names of the checks, as they appear in events and results.
const (
	NameWellFormed   = "well_formed"
	NameProtected    = "protected_paths"
	NameAllowlist    = "allowlist"
	NameFileTypes    = "file_types"
	NameForbidden    = "forbidden_code"
	NameApplied      = "applied_changes"
	NameAttempts     = "attempt_limit"
	maxListedInCheck = 5
)

func pass(name string) events.GuardrailCheck {
	return events.GuardrailCheck{Name: name, Passed: true}
}

func fail(name, detail string) events.GuardrailCheck {
	return events.GuardrailCheck{Name: name, Passed: false, Detail: detail}
}

// Passed reports whether every check passed.
func Passed(checks []events.GuardrailCheck) bool {
	for _, c := range checks {
		if !c.Passed {
			return false
		}
	}
	return true
}

// CheckPatch judges a patch from its text, before it is applied. Every check
// is always reported, so a reader sees what was looked at, not only what
// failed.
func CheckPatch(diff string, rules Rules) []events.GuardrailCheck {
	files, err := ParsePatch(diff)
	if err != nil {
		// Nothing else can be said about a patch that cannot be read.
		return []events.GuardrailCheck{fail(NameWellFormed, err.Error())}
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Paths()...)
	}
	checks := []events.GuardrailCheck{pass(NameWellFormed)}
	checks = append(checks, pathChecks(paths, rules)...)
	checks = append(checks, fileTypes(files), forbidden(files, rules))
	return checks
}

// CheckApplied judges the files a patch really changed once it was applied.
// It is the authoritative path check: it does not depend on heall reading
// the patch the same way git did.
func CheckApplied(changed []string, rules Rules) events.GuardrailCheck {
	var bad []string
	for _, c := range pathChecks(changed, rules) {
		if !c.Passed {
			bad = append(bad, c.Detail)
		}
	}
	if len(bad) > 0 {
		return fail(NameApplied, strings.Join(bad, "; "))
	}
	return pass(NameApplied)
}

func pathChecks(paths []string, rules Rules) []events.GuardrailCheck {
	sort.Strings(paths)
	var protected, outside []string
	for _, p := range paths {
		if pattern, ok := matchAny(rules.Protect, p); ok {
			protected = append(protected, fmt.Sprintf("%s (protected by %q)", p, pattern))
			continue
		}
		if _, ok := matchAny(rules.Allow, p); !ok {
			outside = append(outside, p)
		}
	}
	out := []events.GuardrailCheck{pass(NameProtected), pass(NameAllowlist)}
	if len(protected) > 0 {
		out[0] = fail(NameProtected, "the patch touches "+list(protected))
	}
	if len(outside) > 0 {
		out[1] = fail(NameAllowlist, fmt.Sprintf("the patch touches %s, outside the allowed paths %v", list(outside), rules.Allow))
	}
	return out
}

// fileTypes rejects changes that are not ordinary text edits.
func fileTypes(files []File) events.GuardrailCheck {
	var bad []string
	for _, f := range files {
		switch {
		case f.Binary:
			bad = append(bad, f.Path+" (binary)")
		case strings.HasPrefix(f.Mode, "120"):
			bad = append(bad, f.Path+" (symbolic link)")
		case strings.HasPrefix(f.Mode, "160"):
			bad = append(bad, f.Path+" (submodule)")
		}
	}
	if len(bad) > 0 {
		return fail(NameFileTypes, "the patch adds "+list(bad))
	}
	return pass(NameFileTypes)
}

func forbidden(files []File, rules Rules) events.GuardrailCheck {
	var bad []string
	for _, f := range files {
		for _, line := range f.AddedLines {
			for _, re := range rules.ForbidAdded {
				if re.MatchString(line) {
					bad = append(bad, fmt.Sprintf("%s adds %q (matches %s)", f.Path, strings.TrimSpace(line), re))
					break
				}
			}
		}
	}
	if len(bad) > 0 {
		return fail(NameForbidden, list(bad))
	}
	return pass(NameForbidden)
}

func list(items []string) string {
	if len(items) > maxListedInCheck {
		return strings.Join(items[:maxListedInCheck], ", ") + fmt.Sprintf(" and %d more", len(items)-maxListedInCheck)
	}
	return strings.Join(items, ", ")
}
