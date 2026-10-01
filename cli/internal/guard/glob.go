package guard

import (
	"regexp"
	"strings"
	"sync"
)

var globs sync.Map // pattern -> *regexp.Regexp

// Match reports whether path matches the glob pattern. Paths are relative
// to the repository root and use forward slashes. A single star matches any
// run of characters within one path segment, a question mark one character
// within a segment, and a double star any number of segments, including
// none. So "src/**" covers everything under src, "**/*.test.js" covers test
// files at any depth, and "package.json" covers only the one at the root.
func Match(pattern, path string) bool {
	re, ok := globs.Load(pattern)
	if !ok {
		re, _ = globs.LoadOrStore(pattern, compile(pattern))
	}
	return re.(*regexp.Regexp).MatchString(path)
}

func compile(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case pattern[i] == '*':
			b.WriteString("[^/]*")
		case pattern[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func matchAny(patterns []string, path string) (string, bool) {
	for _, p := range patterns {
		if Match(p, path) {
			return p, true
		}
	}
	return "", false
}
