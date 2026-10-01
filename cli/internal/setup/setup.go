// Package setup connects a repository to heall: it looks at what the
// repository is, writes a .heall.yaml to match, and checks that the machine
// has what a run needs.
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Plan is the configuration heall proposes for a repository.
type Plan struct {
	// Kind is what the repository was recognised as: "node" or "unknown".
	Kind string
	// YAML is the content for .heall.yaml.
	YAML string
	// Notes are things the user should know before the first run.
	Notes []string
	// Ready is false when the config has placeholders that must be filled in
	// by hand before heall can run.
	Ready bool
}

type packageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Engines         map[string]string `json:"engines"`
}

var firstNumber = regexp.MustCompile(`\d+`)

func exists(dir string, names ...string) []string {
	var found []string
	for _, name := range names {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.IsDir() {
			found = append(found, name)
		}
	}
	return found
}

// Detect looks at the repository in dir and proposes a configuration.
func Detect(dir string) Plan {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return unknown(dir)
	}
	var pkg packageJSON
	if json.Unmarshal(raw, &pkg) != nil {
		return unknown(dir)
	}
	plan := Plan{Kind: "node", Ready: true}

	// heall reads the output of Node's own test runner. Other runners can
	// be configured, but their failures will not be recognised yet.
	script := pkg.Scripts["test"]
	for _, runner := range []string{"jest", "vitest", "mocha", "ava", "jasmine", "playwright"} {
		_, dep := pkg.DevDependencies[runner]
		if dep || strings.Contains(script, runner) {
			plan.Notes = append(plan.Notes, fmt.Sprintf(
				"This project tests with %s. heall only understands the output of Node's built-in runner (node --test) so far, "+
					"so it will not find the failing test in %s output. The config below assumes node --test; adjust it if some of your tests use that.", runner, runner))
			break
		}
	}
	if len(pkg.Dependencies)+len(pkg.DevDependencies) > 0 {
		plan.Notes = append(plan.Notes,
			"This project has npm dependencies. heall tests each commit in a fresh checkout inside a container with no network, "+
				"so installed packages are not there and tests that need them will fail. Projects without dependencies work today; "+
				"support for an install step is not built yet.")
	}

	major := 0
	if nvm, err := os.ReadFile(filepath.Join(dir, ".nvmrc")); err == nil {
		major, _ = strconv.Atoi(firstNumber.FindString(string(nvm)))
	}
	if major == 0 {
		major, _ = strconv.Atoi(firstNumber.FindString(pkg.Engines["node"]))
	}
	if major == 0 {
		if out, err := exec.Command("node", "--version").Output(); err == nil {
			major, _ = strconv.Atoi(firstNumber.FindString(string(out)))
		}
	}
	// The built-in runner's TAP reporter and name patterns need Node 20.
	if major < 20 {
		major = 22
	}

	allow := exists(dir, "src", "lib", "app")
	if len(allow) == 0 {
		allow = []string{"**"}
		plan.Notes = append(plan.Notes, "No src/, lib/ or app/ directory was found, so the agent may edit any file that is not protected. Narrow `allow` if you can.")
	} else {
		for i, d := range allow {
			allow[i] = d + "/**"
		}
	}
	protect := []string{}
	for _, d := range exists(dir, "test", "tests", "__tests__", "spec") {
		protect = append(protect, d+"/**")
	}
	protect = append(protect, "**/*.test.*", "**/*.spec.*", "package.json", "package-lock.json", ".heall.yaml", ".github/**")

	plan.YAML = render(
		"",
		`["node", "--test", "--test-reporter=tap"]`,
		`["node", "--test", "--test-reporter=tap", "--test-name-pattern=^{{test_re}}$"]`,
		allow, protect, fmt.Sprintf("node:%d-alpine", major),
	)
	return plan
}

func unknown(dir string) Plan {
	allow := exists(dir, "src", "lib", "app")
	for i, d := range allow {
		allow[i] = d + "/**"
	}
	if len(allow) == 0 {
		allow = []string{"src/**"}
	}
	return Plan{
		Kind:  "unknown",
		Ready: false,
		Notes: []string{
			"heall did not recognise this project (it looks for a package.json). The config has placeholders marked FILL IN: " +
				"give the commands that run your tests and a Docker image that has your toolchain.",
			"heall reads test failures in TAP format (\"not ok 3 - name\"). If your test runner cannot print TAP, heall will not find the failing test yet.",
		},
		YAML: render(
			"",
			`["FILL IN: the command that runs all tests and prints TAP"]`,
			`["FILL IN: the command that runs one test; {{test}} is its name"]`,
			allow, []string{"test/**", "tests/**", ".heall.yaml", ".github/**"}, `"FILL IN: a Docker image with your toolchain"`,
		),
	}
}

func render(build, test, testOne string, allow, protect []string, image string) string {
	list := func(items []string) string {
		var b strings.Builder
		for _, item := range items {
			fmt.Fprintf(&b, "  - %q\n", item)
		}
		return b.String()
	}
	buildLine := "# build_cmd: [\"npm\", \"run\", \"build\"]"
	if build != "" {
		buildLine = "build_cmd: " + build
	}
	return fmt.Sprintf(`# heall configuration. See https://github.com/TheCodeHeist-Coder/heall
version: 1

# Optional. A command that exits non-zero when a commit cannot be built, so
# the bisect can skip such commits instead of blaming them.
%s

# The whole test suite, and one test by name. {{test_re}} is the test's name
# escaped for a regular expression; {{test}} is the name as it is.
test_cmd: %s
test_one_cmd: %s

# A fix may only touch files matching allow, and never one matching protect.
# Tests are protected: heall will not make a test pass by editing it.
allow:
%sprotect:
%s
# Lines a fix may not add: code that would make a test pass without fixing
# anything, by noticing it is under test or by quitting early.
forbid_added:
  - "NODE_TEST_CONTEXT"
  - "process\\.exit\\("

sandbox:
  mode: docker # "local" runs on this machine with no isolation
  image: %s
  timeout_seconds: 120

locate:
  workers: 6 # commits tested at once while bisecting

reproduce:
  runs: 5 # times the failing test is run to rule out flakiness

heal:
  max_attempts: 3
  model: openai/gpt-oss-120b
`, buildLine, test, testOne, list(allow), list(protect), image)
}

// Check is one thing a run needs from the machine.
type Check struct {
	Name string
	OK   bool
	// Optional checks do not stop heall from running.
	Optional bool
	// Detail says what was found, or what to do about a problem.
	Detail string
}

var pythonVersion = regexp.MustCompile(`Python (\d+)\.(\d+)`)

func output(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Doctor checks the machine. dir is the repository; image is the sandbox
// image from its config, or empty when there is no config yet.
func Doctor(ctx context.Context, dir, image string) []Check {
	var checks []Check

	if out, err := output(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel"); err == nil {
		checks = append(checks, Check{Name: "git repository", OK: true, Detail: out})
	} else {
		checks = append(checks, Check{Name: "git repository", Detail: "this directory is not inside a git repository"})
	}

	if out, err := output(ctx, "docker", "version", "--format", "{{.Server.Version}}"); err == nil {
		checks = append(checks, Check{Name: "Docker", OK: true, Detail: "version " + out})
		if image != "" && !strings.HasPrefix(image, "FILL IN") {
			if _, err := output(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image); err == nil {
				checks = append(checks, Check{Name: "sandbox image", OK: true, Detail: image})
			} else {
				checks = append(checks, Check{Name: "sandbox image", OK: true, Optional: true, Detail: image + " is not on this machine yet; the first run will pull it"})
			}
		}
	} else {
		checks = append(checks, Check{Name: "Docker", Detail: "not running. Start Docker, or set sandbox.mode to local to run without isolation"})
	}

	if out, err := output(ctx, "python3", "--version"); err == nil {
		m := pythonVersion.FindStringSubmatch(out)
		major, minor := 0, 0
		if m != nil {
			major, _ = strconv.Atoi(m[1])
			minor, _ = strconv.Atoi(m[2])
		}
		ok := major > 3 || (major == 3 && minor >= 10)
		detail := out
		if !ok {
			detail = out + " is too old; the agent needs Python 3.10 or newer"
		}
		checks = append(checks, Check{Name: "Python", OK: ok, Detail: detail})
	} else {
		checks = append(checks, Check{Name: "Python", Detail: "python3 was not found; the agent needs Python 3.10 or newer"})
	}

	if where := groqKey(dir); where != "" {
		checks = append(checks, Check{Name: "Groq API key", OK: true, Detail: "found in " + where})
	} else {
		checks = append(checks, Check{Name: "Groq API key", Detail: "not set. Get one at https://console.groq.com, then put GROQ_API_KEY=... in " + KeyFile()})
	}

	if _, err := output(ctx, "gh", "auth", "status"); err == nil {
		checks = append(checks, Check{Name: "GitHub CLI", OK: true, Optional: true, Detail: "logged in"})
	} else {
		checks = append(checks, Check{Name: "GitHub CLI", Optional: true, Detail: "not logged in; needed only to open pull requests (gh auth login). Runs with --dry-run work without it"})
	}
	return checks
}

// KeyFile is where a user-wide Groq key is kept.
func KeyFile() string {
	config, err := os.UserConfigDir()
	if err != nil {
		config = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(config, "heall", "env")
}

var keyLine = regexp.MustCompile(`(?m)^\s*(?:export\s+)?GROQ_API_KEYS?=\s*["']?\S`)

// groqKey says where a Groq key is set, without reading its value out.
func groqKey(dir string) string {
	if os.Getenv("GROQ_API_KEY") != "" || os.Getenv("GROQ_API_KEYS") != "" {
		return "the environment"
	}
	for _, path := range []string{filepath.Join(dir, ".env"), ".env", KeyFile()} {
		if raw, err := os.ReadFile(path); err == nil && keyLine.Match(raw) {
			return path
		}
	}
	return ""
}

// Usable reports whether every required check passed.
func Usable(checks []Check) bool {
	for _, c := range checks {
		if !c.OK && !c.Optional {
			return false
		}
	}
	return true
}
