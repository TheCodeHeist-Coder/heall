// Package embedded carries the parts of heall that are not Go, so that the
// installed program is a single file: the Python agent and the built
// dashboard. `make build` copies them into this directory before compiling.
// A plain `go build` without that step still works; the directories then
// hold only their placeholder and the callers fall back to the source tree.
package embedded

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:agent all:web
var files embed.FS

// Version is set at build time for releases.
var Version = "dev"

// Web returns the built dashboard, or false when this build has none.
func Web() (fs.FS, bool) {
	web, err := fs.Sub(files, "web")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(web, "index.html"); err != nil {
		return nil, false
	}
	return web, true
}

// HasAgent reports whether this build carries the agent.
func HasAgent() bool {
	_, err := fs.Stat(files, "agent/heall_agent/__main__.py")
	return err == nil
}

// ExtractAgent writes the agent to the user's cache directory and returns
// the directory to put on PYTHONPATH. The directory is named after the
// content, so every version of heall gets its own copy and an extraction
// that already happened is reused.
func ExtractAgent() (string, error) {
	if !HasAgent() {
		return "", errors.New("this build of heall does not include the agent")
	}
	agent, err := fs.Sub(files, "agent")
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	err = fs.WalkDir(agent, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(agent, path)
		if err != nil {
			return err
		}
		sum.Write([]byte(path))
		sum.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	dir := filepath.Join(cache, "heall", "agent-"+hex.EncodeToString(sum.Sum(nil))[:16])
	if _, err := os.Stat(filepath.Join(dir, "heall_agent", "__main__.py")); err == nil {
		return dir, nil
	}

	// Written beside the final place and renamed, so a second heall started
	// at the same moment never sees half an agent.
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "agent-tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	err = fs.WalkDir(agent, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(agent, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Another heall got there first.
		if _, statErr := os.Stat(filepath.Join(dir, "heall_agent", "__main__.py")); statErr == nil {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}
