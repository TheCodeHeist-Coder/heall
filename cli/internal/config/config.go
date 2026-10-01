// Package config loads .heall.yaml, the per-repo configuration that keeps the
// CLI independent of the language of the repository being healed.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const FileName = ".heall.yaml"

// Placeholders in TestOneCmd. TestPlaceholder becomes the test name as is;
// TestRePlaceholder becomes the name escaped for use inside a regular
// expression, for runners that select tests by pattern.
const (
	TestPlaceholder   = "{{test}}"
	TestRePlaceholder = "{{test_re}}"
)

type Sandbox struct {
	// Mode is "docker" or "local".
	Mode           string `yaml:"mode"`
	Image          string `yaml:"image"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type Locate struct {
	Workers int `yaml:"workers"`
}

type Reproduce struct {
	Runs int `yaml:"runs"`
}

type Heal struct {
	MaxAttempts int    `yaml:"max_attempts"`
	Model       string `yaml:"model"`
}

type Config struct {
	Version int `yaml:"version"`
	// BuildCmd must exit non-zero when a commit cannot be built; such commits
	// are skipped by bisect. Empty means there is no build step.
	BuildCmd []string `yaml:"build_cmd"`
	// TestCmd runs the full suite.
	TestCmd []string `yaml:"test_cmd"`
	// TestOneCmd runs a single test; see TestPlaceholder and TestRePlaceholder.
	TestOneCmd []string `yaml:"test_one_cmd"`
	// Allow lists the globs a patch may touch.
	Allow []string `yaml:"allow"`
	// Protect lists globs a patch may never touch, even if allowed.
	Protect   []string  `yaml:"protect"`
	Sandbox   Sandbox   `yaml:"sandbox"`
	Locate    Locate    `yaml:"locate"`
	Reproduce Reproduce `yaml:"reproduce"`
	Heal      Heal      `yaml:"heal"`
}

func Default() Config {
	return Config{
		Version: 1,
		Sandbox: Sandbox{Mode: "docker", Image: "node:24-alpine", TimeoutSeconds: 60},
		Locate:  Locate{Workers: 6},
		Reproduce: Reproduce{
			Runs: 5,
		},
		Heal: Heal{MaxAttempts: 3, Model: "claude-opus-5-5"},
	}
}

// Load reads .heall.yaml from repoDir, or from path when it is not empty.
// Values missing from the file keep their defaults.
func Load(repoDir, path string) (Config, error) {
	if path == "" {
		path = filepath.Join(repoDir, FileName)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// TestOne returns the command that runs the single test called name.
func (c Config) TestOne(name string) []string {
	r := strings.NewReplacer(TestRePlaceholder, regexp.QuoteMeta(name), TestPlaceholder, name)
	out := make([]string, len(c.TestOneCmd))
	for i, arg := range c.TestOneCmd {
		out[i] = r.Replace(arg)
	}
	return out
}

func (c Config) Validate() error {
	var errs []error
	if c.Version != 1 {
		errs = append(errs, fmt.Errorf("version must be 1, got %d", c.Version))
	}
	if len(c.TestCmd) == 0 {
		errs = append(errs, errors.New("test_cmd is required"))
	}
	if len(c.TestOneCmd) == 0 {
		errs = append(errs, errors.New("test_one_cmd is required"))
	} else if !slices.ContainsFunc(c.TestOneCmd, func(arg string) bool {
		return strings.Contains(arg, TestPlaceholder) || strings.Contains(arg, TestRePlaceholder)
	}) {
		errs = append(errs, fmt.Errorf("test_one_cmd must contain %s or %s", TestPlaceholder, TestRePlaceholder))
	}
	if len(c.Allow) == 0 {
		errs = append(errs, errors.New("allow must list at least one path"))
	}
	if c.Sandbox.Mode != "docker" && c.Sandbox.Mode != "local" {
		errs = append(errs, fmt.Errorf("sandbox.mode must be docker or local, got %q", c.Sandbox.Mode))
	}
	if c.Sandbox.Mode == "docker" && c.Sandbox.Image == "" {
		errs = append(errs, errors.New("sandbox.image is required in docker mode"))
	}
	if c.Sandbox.TimeoutSeconds < 1 {
		errs = append(errs, errors.New("sandbox.timeout_seconds must be at least 1"))
	}
	if c.Locate.Workers < 1 {
		errs = append(errs, errors.New("locate.workers must be at least 1"))
	}
	if c.Reproduce.Runs < 1 {
		errs = append(errs, errors.New("reproduce.runs must be at least 1"))
	}
	if c.Heal.MaxAttempts < 1 {
		errs = append(errs, errors.New("heal.max_attempts must be at least 1"))
	}
	return errors.Join(errs...)
}
