package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"heall/internal/config"
)

func project(t *testing.T, files map[string]string, dirs ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// load checks that the proposed config is one heall itself accepts.
func load(t *testing.T, plan Plan) config.Config {
	t.Helper()
	cfg := config.Default()
	if err := yaml.Unmarshal([]byte(plan.YAML), &cfg); err != nil {
		t.Fatalf("the proposed config is not valid YAML: %v\n%s", err, plan.YAML)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("heall would reject its own proposed config: %v\n%s", err, plan.YAML)
	}
	return cfg
}

func TestDetectNodeProject(t *testing.T) {
	dir := project(t, map[string]string{
		"package.json": `{"scripts": {"test": "node --test"}, "engines": {"node": ">=22.3"}}`,
	}, "src", "test")
	plan := Detect(dir)
	cfg := load(t, plan)
	if plan.Kind != "node" || !plan.Ready || len(plan.Notes) != 0 {
		t.Errorf("kind=%s ready=%v notes=%v", plan.Kind, plan.Ready, plan.Notes)
	}
	if cfg.Sandbox.Image != "node:22-alpine" {
		t.Errorf("image = %q, want the version the project asks for", cfg.Sandbox.Image)
	}
	if len(cfg.Allow) != 1 || cfg.Allow[0] != "src/**" {
		t.Errorf("allow = %v", cfg.Allow)
	}
	protect := strings.Join(cfg.Protect, " ")
	for _, want := range []string{"test/**", "**/*.test.*", "package.json", ".heall.yaml", ".github/**"} {
		if !strings.Contains(protect, want) {
			t.Errorf("protect lacks %s: %v", want, cfg.Protect)
		}
	}
	if got := cfg.TestOne("adds (a+b)"); got[len(got)-1] != `--test-name-pattern=^adds \(a\+b\)$` {
		t.Errorf("test_one_cmd = %q", got)
	}
	if len(cfg.ForbidAdded) == 0 || len(cfg.BuildCmd) != 0 {
		t.Errorf("forbid_added=%v build_cmd=%v", cfg.ForbidAdded, cfg.BuildCmd)
	}
}

func TestDetectWarnsAboutWhatHeallCannotDoYet(t *testing.T) {
	dir := project(t, map[string]string{
		"package.json": `{"scripts": {"test": "jest --ci"}, "devDependencies": {"jest": "^29"}, "dependencies": {"express": "^4"}}`,
		".nvmrc":       "v18.19.0\n",
	})
	plan := Detect(dir)
	cfg := load(t, plan)
	notes := strings.Join(plan.Notes, "\n")
	for _, want := range []string{"tests with jest", "npm dependencies", "No src/, lib/ or app/"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	// Node 18's runner lacks what heall relies on, so a newer image is used.
	if cfg.Sandbox.Image != "node:22-alpine" {
		t.Errorf("image = %q", cfg.Sandbox.Image)
	}
	if len(cfg.Allow) != 1 || cfg.Allow[0] != "**" {
		t.Errorf("allow = %v", cfg.Allow)
	}
}

func TestDetectUnknownProjectLeavesPlaceholders(t *testing.T) {
	plan := Detect(project(t, map[string]string{"main.py": "print(1)\n"}, "src"))
	if plan.Kind != "unknown" || plan.Ready {
		t.Errorf("kind=%s ready=%v", plan.Kind, plan.Ready)
	}
	if strings.Count(plan.YAML, "FILL IN") != 3 {
		t.Errorf("want the test command, the single-test command and the image marked:\n%s", plan.YAML)
	}
	// Still a config heall can load, so the error a user sees is about the
	// placeholder command and not about YAML.
	load(t, plan)
}

func TestGroqKeyIsFoundButNeverRead(t *testing.T) {
	dir := project(t, map[string]string{".env": "# keys\nexport GROQ_API_KEY=\"gsk_secretsecretsecret\"\n"})
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("GROQ_API_KEYS", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	where := groqKey(dir)
	if where != filepath.Join(dir, ".env") || strings.Contains(where, "gsk_") {
		t.Errorf("groqKey = %q", where)
	}
	if got := groqKey(t.TempDir()); got != "" {
		t.Errorf("found a key where there is none: %q", got)
	}
	empty := project(t, map[string]string{".env": "GROQ_API_KEY=\n"})
	if got := groqKey(empty); got != "" {
		t.Errorf("an empty key counted as set: %q", got)
	}
	t.Setenv("GROQ_API_KEYS", "a,b")
	if got := groqKey(t.TempDir()); got != "the environment" {
		t.Errorf("groqKey = %q", got)
	}
}

func TestDoctorReportsEveryRequirement(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("GROQ_API_KEYS", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	checks := Doctor(context.Background(), t.TempDir(), "")
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
		if !c.OK && c.Detail == "" {
			t.Errorf("%s failed without saying what to do", c.Name)
		}
	}
	for _, name := range []string{"git repository", "Docker", "Python", "Groq API key", "GitHub CLI"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("no check for %s", name)
		}
	}
	if byName["git repository"].OK || byName["Groq API key"].OK {
		t.Error("an empty directory with no key passed")
	}
	if Usable(checks) {
		t.Error("a machine with required checks failing was called usable")
	}
	if !byName["GitHub CLI"].Optional {
		t.Error("gh must be optional: dry runs work without it")
	}
}
