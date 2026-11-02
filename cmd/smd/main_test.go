package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// firstLine is what a one-line error must be. The flag package writes its own message, so that
// message is the first line for a parse error.
func firstLine(s string) string {
	if s == "" {
		return ""
	}
	return strings.SplitN(strings.TrimSuffix(s, "\n"), "\n", 2)[0]
}

func TestRunRejectsBadArguments(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		first string
	}{
		{"unknown command", []string{"bogus"}, `smd: unknown command "bogus"`},
		{"unknown config subcommand", []string{"config", "bogus"}, `smd: unknown command "config bogus"`},
		{"config without a subcommand", []string{"config"}, `smd: missing subcommand for "config"`},
		{"create takes no flag", []string{"config", "create", "--json"}, "flag provided but not defined: -json"},
		{"create takes no argument", []string{"config", "create", "extra"}, `smd: unknown argument "extra"`},
		{
			"config flag is rejected",
			[]string{"config", "show", "--config", "x"},
			`smd: unknown flag "--config": the config path is always ~/.skill-model-deactivator.json`,
		},
		{"lines without a value", []string{"log", "--lines"}, "flag needs an argument: -lines"},
		{"lines zero", []string{"log", "--lines", "0"}, `invalid value "0" for flag -lines: must be a number greater than 0`},
		{"lines negative", []string{"log", "--lines", "-1"}, `invalid value "-1" for flag -lines: must be a number greater than 0`},
		{"lines not a number", []string{"log", "--lines", "x"}, `invalid value "x" for flag -lines: must be a number greater than 0`},
		{"config flag before the command", []string{"--config", "x", "serve"}, `smd: unknown flag "--config": the config path is always ~/.skill-model-deactivator.json`},
		{"config flag if set to an empty value", []string{"serve", "--config="}, `smd: unknown flag "--config": the config path is always ~/.skill-model-deactivator.json`},
		{"strict is not a log flag", []string{"log", "--strict"}, "flag provided but not defined: -strict"},
		{"json is not a serve flag", []string{"serve", "--json"}, "flag provided but not defined: -json"},
		{"help takes no flags", []string{"help", "--json"}, "flag provided but not defined: -json"},
		{"version takes no flags", []string{"version", "--json"}, "flag provided but not defined: -json"},
		{"show takes no strict flag", []string{"config", "show", "--strict"}, "flag provided but not defined: -strict"},
		{"stray argument", []string{"serve", "extra"}, `smd: unknown argument "extra"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, test.args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if got := firstLine(stderr); got != test.first {
				t.Errorf("first stderr line = %q, want %q", got, test.first)
			}
		})
	}
}

func TestHelp(t *testing.T) {
	home := newHome(t)

	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	want := []string{
		"smd - keep skill frontmatter in sync with a declared list of patterns\n",
		"CONFIG   ~/.skill-model-deactivator.json   (missing)\n",
		"LOG      " + display.Tilde(filepath.Join(stateDirOf(t, home), platform.LogFileName)) + "   (missing)\n",
		"COMMANDS\n",
		"  config show       Show the effective config and both paths.\n",
		"  config validate   Check the config and the matched files. No writes.\n",
		"  config create     Write the default config. Refuses an existing file.\n",
		"  serve             Start the daemon: reconcile now, then watch for changes.\n",
		"  log               Print the last log lines. --lines N, --follow, --all.\n",
		"  help              Show this text.\n",
		"  version           Show the build version.\n",
		"OPTIONS\n",
		"  --dry-run   Report the planned changes and write nothing.\n",
		"  --once      Run one reconcile and exit.\n",
		"  --json      Emit machine-readable output (config show, config validate).\n",
		"  --strict    Treat a warning as a failure (config validate).\n",
		"  --lines N   Number of log lines to print (log). Default 10.\n",
		"  --follow    Keep printing new log lines (log).\n",
		"  --all       Print the whole log, including the rotated file (log).\n",
		"The config path is always ~/.skill-model-deactivator.json. There is no --config flag.\n",
	}
	for _, line := range want {
		if !strings.Contains(stdout, line) {
			t.Errorf("help output does not contain %q", line)
		}
	}

	// The two paths come before the command list.
	configIndex := strings.Index(stdout, "CONFIG   ")
	logIndex := strings.Index(stdout, "LOG      ")
	commandsIndex := strings.Index(stdout, "COMMANDS\n")
	if configIndex < 0 || logIndex < 0 || commandsIndex < 0 {
		t.Fatalf("help output lacks a path or the command list:\n%s", stdout)
	}
	if configIndex > logIndex || logIndex > commandsIndex {
		t.Errorf("help output puts the paths after the command list:\n%s", stdout)
	}
}

// TestBareInvocationPrintsHelp: the help body on stdout, no failure on stderr, and still exit 1
// so a script sees the usage error.
func TestBareInvocationPrintsHelp(t *testing.T) {
	newHome(t)

	helpOut, helpErr, helpCode := runCLI(t, "help")
	if helpCode != 0 {
		t.Fatalf("smd help exit code = %d, want 0", helpCode)
	}
	if helpErr != "" {
		t.Fatalf("smd help stderr = %q, want empty", helpErr)
	}

	stdout, stderr, code := runCLI(t)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != helpOut {
		t.Errorf("stdout differs from smd help:\ngot:\n%s\nwant:\n%s", stdout, helpOut)
	}
}

// TestHelpReportsBothStatuses: the config path carries its state and the log path its size,
// both computed from what is on disk.
func TestHelpReportsBothStatuses(t *testing.T) {
	home := newHome(t)
	content := `{"patterns": ["~/skills/*/SKILL.md"]}`
	writeConfig(t, home, content)

	stateDir := stateDirOf(t, home)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, platform.LogFileName), []byte(strings.Repeat("x\n", 2048)), 0o644); err != nil {
		t.Fatalf("write the log fixture: %v", err)
	}

	stdout, _, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	for _, want := range []string{
		fmt.Sprintf("CONFIG   ~/.skill-model-deactivator.json   (exists, %d bytes, valid)\n", len(content)),
		"LOG      " + display.Tilde(filepath.Join(stateDir, platform.LogFileName)) + "   (4 KiB)\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help output does not contain %q:\n%s", want, stdout)
		}
	}
}

// TestHelpInvalidConfigStatus: a broken config is visible without a second command.
func TestHelpInvalidConfigStatus(t *testing.T) {
	home := newHome(t)
	writeConfig(t, home, `not json`)

	stdout, _, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "(exists, 8 bytes, invalid: ") {
		t.Errorf("help output does not carry the invalid status:\n%s", stdout)
	}
}

// TestNeitherCommandWrites: no byte changes and no temp file appears.
func TestNeitherCommandWrites(t *testing.T) {
	home := newHome(t)
	writeConfig(t, home, `{"patterns": ["~/skills/*/SKILL.md"]}`)

	stateDir := stateDirOf(t, home)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, platform.LogFileName), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write the log fixture: %v", err)
	}
	before := snapshot(t, home)

	for _, args := range [][]string{{"log"}, {"log", "--all"}, {"help"}} {
		if _, _, code := runCLI(t, args...); code != 0 {
			t.Fatalf("smd %s exit code = %d, want 0", strings.Join(args, " "), code)
		}
	}

	after := snapshot(t, home)
	if len(before) != len(after) {
		t.Fatalf("the file count changed from %d to %d: %v", len(before), len(after), after)
	}
	for name, hash := range before {
		if after[name] != hash {
			t.Errorf("%s changed: %s became %q", name, hash, after[name])
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("%s appeared during smd log or smd help", name)
		}
		if strings.Contains(filepath.Base(name), ".smd-tmp") {
			t.Errorf("smd log or smd help left a temp file: %s", name)
		}
	}
}

func TestConfigShow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	stdout, stderr, code := runCLI(t, "config", "show")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	for _, want := range []string{"CONFIG   ~/.skill-model-deactivator.json", "LOG      ",
		"patterns          (empty)",
		"debounce          250ms (default)", "rescan_interval   30m (default)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("config show output does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestConfigShowWithAValidFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path := filepath.Join(home, ".skill-model-deactivator.json")
	content := `{"patterns": ["~/skills/*/SKILL.md"], "debounce": "1s", "rescan_interval": "0"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the config fixture: %v", err)
	}

	stdout, stderr, code := runCLI(t, "config", "show")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	for _, want := range []string{
		fmt.Sprintf("CONFIG   ~/.skill-model-deactivator.json   (exists, %d bytes, valid JSON)\n", len(content)),
		"patterns          ~/skills/*/SKILL.md\n",
		"debounce          1s\n",
		"rescan_interval   0s\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("config show output does not contain %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "(default)") {
		t.Errorf("config show marked a value from the file as a default:\n%s", stdout)
	}
}

func TestConfigShowExitsZeroOnAnInvalidConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path := filepath.Join(home, ".skill-model-deactivator.json")
	if err := os.WriteFile(path, []byte(`not json`), 0o600); err != nil {
		t.Fatalf("write the config fixture: %v", err)
	}

	stdout, stderr, code := runCLI(t, "config", "show")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: config show judges nothing", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "invalid:") {
		t.Errorf("config show did not report the invalid file:\n%s", stdout)
	}
}

func TestConfigShowJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	stdout, stderr, code := runCLI(t, "config", "show", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "\n") {
		t.Errorf("config show --json printed %d lines, want one: %q",
			strings.Count(stdout, "\n"), stdout)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("config show --json output does not unmarshal: %v", err)
	}
	for _, key := range []string{"config_path", "config_exists", "config_valid", "log_path",
		"patterns", "debounce", "rescan_interval"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("config show --json has no key %q: %s", key, stdout)
		}
	}
	patterns, ok := decoded["patterns"].(map[string]any)["value"]
	if !ok || patterns != nil {
		t.Errorf("patterns.value = %v, want null with no default", decoded["patterns"])
	}
}

func TestConfigShowOnThisMachine(t *testing.T) {
	// A real home may or may not carry a config file, so this asserts only the shape.
	stdout, _, code := runCLI(t, "config", "show")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "CONFIG") || !strings.Contains(stdout, "LOG") ||
		!strings.Contains(stdout, "patterns") || !strings.Contains(stdout, "debounce") ||
		!strings.Contains(stdout, "rescan_interval") {
		t.Errorf("config show output is missing a path or a field:\n%s", stdout)
	}
}

func TestVersion(t *testing.T) {
	stdout, stderr, code := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("version printed %d lines, want 1: %q", len(lines), stdout)
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 4 {
		t.Fatalf("version line = %q, want four fields", lines[0])
	}
	if fields[0] != "smd" || fields[1] != "dev" {
		t.Errorf("version line = %q, want %q", lines[0], "smd dev ...")
	}
	if !strings.HasPrefix(fields[2], "go1.") {
		t.Errorf("version line = %q, third field is not a Go version", lines[0])
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; fields[3] != want {
		t.Errorf("version line = %q, want platform %q", lines[0], want)
	}
}
