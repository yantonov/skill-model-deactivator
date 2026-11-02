package display

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/config"
)

func withFakeHome(t *testing.T, home string) {
	t.Helper()
	home = filepath.FromSlash(home)
	previous := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = previous })
}

// fixture names all three keys, so every field comes from the file.
const fixture = `{
  "patterns": ["~/.claude/skills/*/SKILL.md", "~/.agents/skills/*/SKILL.md"],
  "debounce": "250ms",
  "rescan_interval": "30m"
}
`

// loadFrom goes through config.Load, which is the only path that records where each value came
// from. It sets both home variables, so config.Path resolves inside the temp directory.
func loadFrom(t *testing.T, home string, content string) config.Loaded {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if content != "" {
		path := filepath.Join(home, config.FileName)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write the config fixture: %v", err)
		}
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v, want nil", err)
	}
	return loaded
}

func TestTildeAbbreviatesTheHomeDirectory(t *testing.T) {
	tests := []struct {
		name string
		home string
		path string
		want string
	}{
		{
			name: "linux home",
			home: "/home/user",
			path: "/home/user/.local/state/skill-model-deactivator/smd.log",
			want: "~/.local/state/skill-model-deactivator/smd.log",
		},
		{
			name: "windows home",
			home: `C:\Users\user`,
			path: `C:\Users\user\AppData\Local\skill-model-deactivator\smd.log`,
			want: "~/AppData/Local/skill-model-deactivator/smd.log",
		},
		{
			name: "the home directory itself",
			home: "/home/user",
			path: "/home/user",
			want: "~",
		},
		{
			name: "outside the home directory",
			home: "/home/user",
			path: "/etc/smd.log",
			want: "/etc/smd.log",
		},
		{
			name: "a sibling that starts with the home path",
			home: "/home/user",
			path: "/home/username/smd.log",
			want: "/home/username/smd.log",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withFakeHome(t, test.home)
			if got := Tilde(test.path); got != test.want {
				t.Errorf("Tilde(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestTildeWithoutAHomeDirectory(t *testing.T) {
	previous := homeDir
	homeDir = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { homeDir = previous })

	if got, want := Tilde(`C:\a\b`), "C:/a/b"; got != want {
		t.Errorf("Tilde() = %q, want %q", got, want)
	}
}

func TestSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1 KiB"},
		{23 * 1024, "23 KiB"},
		{23*1024 + 1, "24 KiB"},
	}
	for _, test := range tests {
		if got := Size(test.bytes); got != test.want {
			t.Errorf("Size(%d) = %q, want %q", test.bytes, got, test.want)
		}
	}
}

func TestConfigStatus(t *testing.T) {
	tests := []struct {
		name string
		l    config.Loaded
		want string
	}{
		{"missing", config.Loaded{Path: "/x"}, "(missing)"},
		{
			"valid",
			config.Loaded{Path: "/x", Exists: true, Bytes: 412},
			"(exists, 412 bytes, valid JSON)",
		},
		{
			"invalid",
			config.Loaded{
				Path:     "/x",
				Exists:   true,
				Bytes:    12,
				Findings: []config.Finding{{Level: config.LevelFail, Message: "unknown key \"paterns\""}},
			},
			`(exists, 12 bytes, invalid: unknown key "paterns")`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ConfigStatus(test.l); got != test.want {
				t.Errorf("ConfigStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLogStatus(t *testing.T) {
	if got, want := LogStatus(false, 0), "(missing)"; got != want {
		t.Errorf("LogStatus(absent) = %q, want %q", got, want)
	}
	if got, want := LogStatus(true, 900), "(900 B)"; got != want {
		t.Errorf("LogStatus(900) = %q, want %q", got, want)
	}
	if got, want := LogStatus(true, 23*1024), "(23 KiB)"; got != want {
		t.Errorf("LogStatus(23 KiB) = %q, want %q", got, want)
	}
	if got := LogStatus(true, 1); got == "(0 KiB)" {
		t.Error("LogStatus(1) printed 0 KiB for a non-empty file")
	}
}

func TestShowFromTheFile(t *testing.T) {
	home := t.TempDir()
	withFakeHome(t, home)
	loaded := loadFrom(t, home, fixture)

	var out bytes.Buffer
	err := Show(&out, Report{
		Loaded:    loaded,
		LogPath:   filepath.Join(home, ".local", "state", "skill-model-deactivator", "smd.log"),
		LogExists: true,
		LogBytes:  23 * 1024,
	})
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	want := fmt.Sprintf("CONFIG   ~/%s   (exists, %d bytes, valid JSON)\n",
		config.FileName, len(fixture)) +
		"LOG      ~/.local/state/skill-model-deactivator/smd.log   (23 KiB)\n" +
		"\n" +
		"patterns          ~/.claude/skills/*/SKILL.md\n" +
		"                  ~/.agents/skills/*/SKILL.md\n" +
		"debounce          250ms\n" +
		"rescan_interval   30m\n"
	if got := out.String(); got != want {
		t.Errorf("Show() =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(out.String(), "(default)") {
		t.Error("Show() marked a value from the file as a default")
	}
}

func TestShowMarksDefaults(t *testing.T) {
	home := t.TempDir()
	withFakeHome(t, home)
	loaded := loadFrom(t, home, "")

	var out bytes.Buffer
	if err := Show(&out, Report{Loaded: loaded, LogPath: filepath.Join(home, "smd.log")}); err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}
	printed := out.String()

	labels := []string{
		"patterns          (empty)\n",
		"debounce          250ms (default)\n",
		"rescan_interval   30m (default)\n",
	}
	for _, label := range labels {
		if !strings.Contains(printed, label) {
			t.Errorf("Show() has no label line %q:\n%s", label, printed)
		}
	}
	for _, line := range strings.Split(printed, "\n") {
		if strings.Contains(line, "~/") && strings.Contains(line, "(default)") &&
			strings.HasPrefix(line, strings.Repeat(" ", 18)) {
			t.Errorf("the default marker sits on an element line: %q", line)
		}
	}
	if want := "CONFIG   ~/" + config.FileName + "   (missing)\n"; !strings.Contains(printed, want) {
		t.Errorf("Show() has no line %q:\n%s", want, printed)
	}
	if want := "LOG      ~/smd.log   (missing)\n"; !strings.Contains(printed, want) {
		t.Errorf("Show() has no line %q:\n%s", want, printed)
	}
}

func TestShowJSON(t *testing.T) {
	home := t.TempDir()
	withFakeHome(t, home)
	loaded := loadFrom(t, home, fixture)

	var out bytes.Buffer
	err := ShowJSON(&out, Report{Loaded: loaded, LogPath: filepath.Join(home, "state", "smd.log")})
	if err != nil {
		t.Fatalf("ShowJSON() error = %v, want nil", err)
	}

	raw := out.String()
	if !strings.HasSuffix(raw, "\n") || strings.Count(raw, "\n") != 1 {
		t.Fatalf("ShowJSON() output has %d newlines, want exactly one at the end: %q",
			strings.Count(raw, "\n"), raw)
	}
	if strings.Contains(raw, "\n") && strings.Contains(strings.TrimSuffix(raw, "\n"), "\n") {
		t.Errorf("ShowJSON() printed more than one line: %q", raw)
	}

	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("ShowJSON() output does not unmarshal: %v", err)
	}
	for _, key := range []string{"config_path", "config_exists", "config_valid", "log_path",
		"patterns", "debounce", "rescan_interval"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("ShowJSON() has no key %q: %s", key, raw)
		}
	}
	if decoded["config_valid"] != true {
		t.Errorf("config_valid = %v, want true", decoded["config_valid"])
	}
	if got, want := decoded["config_path"], "~/"+config.FileName; got != want {
		t.Errorf("config_path = %v, want %v", got, want)
	}

	for _, field := range []string{"patterns", "debounce", "rescan_interval"} {
		value, ok := decoded[field].(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object: %v", field, decoded[field])
		}
		if got := value["source"]; got != config.SourceFile {
			t.Errorf("%s.source = %v, want %q", field, got, config.SourceFile)
		}
	}
	if got, want := decoded["debounce"].(map[string]any)["value"], "250ms"; got != want {
		t.Errorf("debounce.value = %v, want %v", got, want)
	}
	if got, want := decoded["rescan_interval"].(map[string]any)["value"], "30m"; got != want {
		t.Errorf("rescan_interval.value = %v, want %v", got, want)
	}
}

func TestShowJSONWithDefaults(t *testing.T) {
	home := t.TempDir()
	withFakeHome(t, home)
	loaded := loadFrom(t, home, "")

	var out bytes.Buffer
	if err := ShowJSON(&out, Report{Loaded: loaded, LogPath: filepath.Join(home, "smd.log")}); err != nil {
		t.Fatalf("ShowJSON() error = %v, want nil", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("ShowJSON() output does not unmarshal: %v", err)
	}
	if decoded["config_exists"] != false {
		t.Errorf("config_exists = %v, want false", decoded["config_exists"])
	}
	for _, field := range []string{"patterns", "debounce", "rescan_interval"} {
		if got := decoded[field].(map[string]any)["source"]; got != config.SourceDefault {
			t.Errorf("%s.source = %v, want %q", field, got, config.SourceDefault)
		}
	}
	patterns, ok := decoded["patterns"].(map[string]any)["value"]
	if !ok || patterns != nil {
		t.Errorf("patterns.value = %v, want a null value with no default", decoded["patterns"])
	}
}

func TestDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{250 * time.Millisecond, "250ms"},
		{time.Second, "1s"},
		{90 * time.Second, "1m30s"},
		{30 * time.Minute, "30m"},
		{time.Hour, "1h"},
		{2*time.Hour + 30*time.Minute, "2h30m"},
		{25 * time.Hour, "25h"},
	}
	for _, test := range tests {
		if got := Duration(test.in); got != test.want {
			t.Errorf("Duration(%v) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestShowInvalidConfig(t *testing.T) {
	home := t.TempDir()
	withFakeHome(t, home)
	loaded := loadFrom(t, home, `{"paterns": ["~/a/SKILL.md"]}`)

	var out bytes.Buffer
	if err := Show(&out, Report{Loaded: loaded, LogPath: filepath.Join(home, "smd.log")}); err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}
	printed := out.String()

	lines := strings.Split(printed, "\n")
	if !strings.Contains(lines[0], "(exists,") || !strings.Contains(lines[0], "invalid:") {
		t.Errorf("CONFIG line = %q, want an invalid status", lines[0])
	}
	if !strings.Contains(printed, "patterns          (empty)\n") {
		t.Errorf("Show() did not report the empty pattern list:\n%s", printed)
	}
}

// TestHelpConfigStatus: the same status as config show, but the short `valid` instead of
// `valid JSON`.
func TestHelpConfigStatus(t *testing.T) {
	tests := []struct {
		name string
		l    config.Loaded
		want string
	}{
		{"missing", config.Loaded{Path: "/x"}, "(missing)"},
		{
			"valid",
			config.Loaded{Path: "/x", Exists: true, Bytes: 412},
			"(exists, 412 bytes, valid)",
		},
		{
			"invalid",
			config.Loaded{
				Path:     "/x",
				Exists:   true,
				Bytes:    12,
				Findings: []config.Finding{{Level: config.LevelFail, Message: "unknown key \"paterns\""}},
			},
			`(exists, 12 bytes, invalid: unknown key "paterns")`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HelpConfigStatus(test.l); got != test.want {
				t.Errorf("HelpConfigStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestHelpPaths: the config path with its status, then the log path with its size, both
// abbreviated.
func TestHelpPaths(t *testing.T) {
	home := t.TempDir()
	withFakeHome(t, home)
	loaded := loadFrom(t, home, fixture)
	logPath := filepath.Join(home, ".local", "state", "skill-model-deactivator", "smd.log")

	var out bytes.Buffer
	if err := HelpPaths(&out, loaded, logPath, true, 23*1024); err != nil {
		t.Fatalf("HelpPaths() error = %v, want nil", err)
	}

	want := fmt.Sprintf("CONFIG   ~/%s   (exists, %d bytes, valid)\n", config.FileName, len(fixture)) +
		"LOG      ~/.local/state/skill-model-deactivator/smd.log   (23 KiB)\n"
	if out.String() != want {
		t.Errorf("HelpPaths() = %q, want %q", out.String(), want)
	}
}
