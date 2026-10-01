package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExample(t *testing.T) {
	cfg, err := Load("", "../../../contracts/examples/heall.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Image != "node:24-alpine" || cfg.Locate.Workers != 6 || len(cfg.Protect) != 5 {
		t.Errorf("unexpected config: %+v", cfg)
	}
	got := cfg.TestOne("sum adds (a+b)")
	if want := `--test-name-pattern=^sum adds \(a\+b\)$`; got[len(got)-1] != want {
		t.Errorf("TestOne = %q, want last arg %q", got, want)
	}
}

func TestLoadAppliesDefaultsAndValidates(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("version: 1\ntest_cmd: [node, --test]\ntest_one_cmd: [node, --test, \"{{test}}\"]\nallow: [\"src/**\"]\n")
	cfg, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Heal.MaxAttempts != 3 || cfg.Sandbox.Mode != "docker" || cfg.Reproduce.Runs != 5 {
		t.Errorf("defaults not applied: %+v", cfg)
	}

	write("version: 1\ntest_cmd: [node, --test]\ntest_one_cmd: [node, --test]\nallow: [\"src/**\"]\n")
	if _, err := Load(dir, ""); err == nil || !strings.Contains(err.Error(), "must contain {{test}}") {
		t.Errorf("test_one_cmd without a placeholder: got %v", err)
	}

	write("version: 1\nallow: [\"src/**\"]\nsandbox: {mode: vm}\n")
	_, err = Load(dir, "")
	if err == nil {
		t.Fatal("invalid config accepted")
	}
	for _, want := range []string{"test_cmd is required", "sandbox.mode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
