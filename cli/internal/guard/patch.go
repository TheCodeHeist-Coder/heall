package guard

import (
	"fmt"
	"path"
	"strings"
)

// File is one file a patch touches.
type File struct {
	// Path is the file after the patch, or the deleted file.
	Path string
	// OldPath is set when the patch renames or copies the file.
	OldPath string
	Added   bool
	Deleted bool
	Binary  bool
	// Mode is the file mode the patch sets, when it sets one.
	Mode string
	// AddedLines are the lines the patch adds, without the leading "+".
	AddedLines []string
}

// Paths returns every path the change touches.
func (f File) Paths() []string {
	if f.OldPath != "" && f.OldPath != f.Path {
		return []string{f.OldPath, f.Path}
	}
	return []string{f.Path}
}

// section collects the header lines of one file in a patch. A path can be
// named by up to three kinds of header, which resolve() reconciles.
type section struct {
	file              File
	gitOld, gitNew    string
	renameFrom        string
	renameTo          string
	minus, plus       string
	hasMinus, hasPlus bool
}

func (s *section) resolve() (File, error) {
	f := s.file
	oldPath := firstNonEmpty(s.renameFrom, s.minus, s.gitOld)
	f.Path = firstNonEmpty(s.renameTo, s.plus, s.gitNew, s.minus)
	if s.hasMinus && s.minus == "" {
		f.Added = true
		oldPath = ""
	}
	if s.hasPlus && s.plus == "" {
		f.Deleted = true
		f.Path = oldPath
	}
	if f.Path == "" {
		return File{}, fmt.Errorf("a file section has no path")
	}
	if oldPath != f.Path {
		f.OldPath = oldPath
	}
	return f, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Normalize rewrites a patch into the form git expects, without changing
// what it does. A plain unified diff separates files only by their "---"
// and "+++" lines, which "git apply --recount" would read as part of the
// previous file's hunk, and it marks added and deleted files only with
// /dev/null, which git ignores without a "file mode" line. Normalize adds
// the "diff --git" and mode lines that are missing, so heall and git split
// the patch into the same files. Apply the normalized patch, not the
// original.
func Normalize(diff string) string {
	lines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines)+4)
	// headerOpen is true between a "diff" line and its first hunk, where a
	// "---" line belongs to the section already open.
	headerOpen, hasMode := false, false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff "):
			headerOpen, hasMode = true, false
		case strings.HasPrefix(line, "@@ "):
			headerOpen = false
		case strings.HasPrefix(line, "new file mode "), strings.HasPrefix(line, "deleted file mode "):
			hasMode = headerOpen
		case strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ "):
			oldPath, err1 := headerPath(line[4:], "a/")
			newPath, err2 := headerPath(lines[i+1][4:], "b/")
			name := firstNonEmpty(newPath, oldPath)
			if err1 != nil || err2 != nil || name == "" {
				break
			}
			if !headerOpen {
				out = append(out, "diff --git a/"+name+" b/"+name)
				headerOpen, hasMode = true, false
			}
			if !hasMode && oldPath == "" {
				out = append(out, "new file mode 100644")
			}
			if !hasMode && newPath == "" {
				out = append(out, "deleted file mode 100644")
			}
			hasMode = true
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ParsePatch reads a normalized unified diff as "git apply --recount" would: inside a
// hunk every line is content until the next "@@" or "diff" line. It returns
// an error for anything it cannot account for, because a patch the
// guardrails cannot read is a patch they cannot vouch for.
func ParsePatch(diff string) ([]File, error) {
	var files []File
	var cur *section
	inHunk := false

	flush := func() error {
		if cur == nil {
			return nil
		}
		f, err := cur.resolve()
		if err != nil {
			return err
		}
		files = append(files, f)
		cur = nil
		return nil
	}

	lines := strings.Split(Normalize(diff), "\n")
	for n, line := range lines {
		if strings.HasPrefix(line, "diff ") {
			if err := flush(); err != nil {
				return nil, err
			}
			cur, inHunk = &section{}, false
			// The "---", "+++" and rename lines name the paths; the "diff"
			// line is only a fallback, since it is ambiguous for paths with
			// spaces.
			cur.gitOld, cur.gitNew, _ = gitHeaderPaths(line)
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			if cur == nil {
				return nil, fmt.Errorf("line %d: hunk before any file header", n+1)
			}
			inHunk = true
			continue
		}
		if inHunk {
			switch {
			case strings.HasPrefix(line, "+"):
				cur.file.AddedLines = append(cur.file.AddedLines, line[1:])
			case line == "", strings.HasPrefix(line, " "), strings.HasPrefix(line, "-"), strings.HasPrefix(line, `\`):
			default:
				return nil, fmt.Errorf("line %d: unexpected text inside a hunk: %q", n+1, line)
			}
			continue
		}

		// File headers, outside any hunk. A plain unified diff has no "diff"
		// line, so "---" may open the section.
		if cur == nil {
			if !strings.HasPrefix(line, "--- ") {
				continue
			}
			cur = &section{}
		}
		var err error
		switch {
		case strings.HasPrefix(line, "--- "):
			cur.minus, err = headerPath(line[4:], "a/")
			cur.hasMinus = true
		case strings.HasPrefix(line, "+++ "):
			cur.plus, err = headerPath(line[4:], "b/")
			cur.hasPlus = true
		case strings.HasPrefix(line, "rename from "):
			cur.renameFrom = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "copy from "):
			cur.renameFrom = strings.TrimPrefix(line, "copy from ")
		case strings.HasPrefix(line, "rename to "):
			cur.renameTo = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "copy to "):
			cur.renameTo = strings.TrimPrefix(line, "copy to ")
		case strings.HasPrefix(line, "new file mode "):
			cur.file.Added, cur.file.Mode = true, strings.TrimPrefix(line, "new file mode ")
		case strings.HasPrefix(line, "deleted file mode "):
			cur.file.Deleted = true
		case strings.HasPrefix(line, "new mode "):
			cur.file.Mode = strings.TrimPrefix(line, "new mode ")
		case strings.HasPrefix(line, "Binary files "), line == "GIT binary patch":
			cur.file.Binary = true
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n+1, err)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("the patch does not change any file")
	}
	for _, f := range files {
		for _, p := range f.Paths() {
			if err := safePath(p); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

// gitHeaderPaths reads the two paths from "diff --git a/x b/y".
func gitHeaderPaths(line string) (oldPath, newPath string, ok bool) {
	rest, found := strings.CutPrefix(line, "diff --git a/")
	if !found {
		return "", "", false
	}
	// Without quoting, the only ambiguity is a path containing " b/". The
	// "---" and "+++" lines settle it when they are present.
	i := strings.LastIndex(rest, " b/")
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+3:], true
}

// headerPath reads the path from a "---" or "+++" line. It returns "" for
// /dev/null.
func headerPath(s, prefix string) (string, error) {
	// A timestamp may follow the path after a tab.
	s, _, _ = strings.Cut(s, "\t")
	s = strings.TrimRight(s, " ")
	if s == "/dev/null" {
		return "", nil
	}
	if strings.HasPrefix(s, `"`) {
		return "", fmt.Errorf("quoted paths are not supported: %s", s)
	}
	return strings.TrimPrefix(s, prefix), nil
}

// safePath rejects paths that could reach outside the repository or into
// git's own files.
func safePath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("empty path")
	case strings.HasPrefix(p, "/"), strings.Contains(p, `\`):
		return fmt.Errorf("%q is not a relative path", p)
	case path.Clean(p) != p:
		return fmt.Errorf("%q is not a clean relative path", p)
	case p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Errorf("%q leaves the repository", p)
	case p == ".git" || strings.HasPrefix(p, ".git/"):
		return fmt.Errorf("%q is inside .git", p)
	}
	return nil
}
