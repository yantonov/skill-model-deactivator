package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newHome points os.UserHomeDir at a temp directory, so a test needs no hook in the package.
func newHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// writeFile returns the path it wrote.
func writeFile(t *testing.T, home, content string) string {
	t.Helper()
	path := filepath.Join(home, FileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the config fixture: %v", err)
	}
	return path
}

func loadFile(t *testing.T, content string) Loaded {
	t.Helper()
	writeFile(t, newHome(t), content)
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	return loaded
}

func levelCount(findings []Finding, level Level) int {
	count := 0
	for _, finding := range findings {
		if finding.Level == level {
			count++
		}
	}
	return count
}

func firstMessage(findings []Finding, level Level) string {
	for _, finding := range findings {
		if finding.Level == level {
			return finding.Message
		}
	}
	return ""
}

func TestLoadReadsEveryFieldFromTheFile(t *testing.T) {
	loaded := loadFile(t, `{
		"patterns": ["~/one/SKILL.md", "~/two/*/SKILL.md"],
		"debounce": "1s",
		"rescan_interval": "5m"
	}`)

	if !loaded.Exists {
		t.Error("Exists = false, want true")
	}
	if loaded.Bytes == 0 {
		t.Error("Bytes = 0, want the file size")
	}
	if len(loaded.Findings) != 0 {
		t.Errorf("findings = %v, want none", loaded.Findings)
	}
	want := Config{
		Patterns:       []string{"~/one/SKILL.md", "~/two/*/SKILL.md"},
		Debounce:       time.Second,
		RescanInterval: 5 * time.Minute,
	}
	if !equalPatterns(loaded.Config.Patterns, want.Patterns) {
		t.Errorf("Patterns = %v, want %v", loaded.Config.Patterns, want.Patterns)
	}
	if loaded.Config.Debounce != want.Debounce {
		t.Errorf("Debounce = %v, want %v", loaded.Config.Debounce, want.Debounce)
	}
	if loaded.Config.RescanInterval != want.RescanInterval {
		t.Errorf("RescanInterval = %v, want %v", loaded.Config.RescanInterval, want.RescanInterval)
	}
	for _, field := range knownKeys {
		if got := loaded.Source(field); got != SourceFile {
			t.Errorf("Source(%q) = %q, want %q", field, got, SourceFile)
		}
	}
}

func TestLoadMissingFileGivesDefaultsAndOneWarning(t *testing.T) {
	home := newHome(t)
	path := filepath.Join(home, FileName)

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if loaded.Exists {
		t.Error("Exists = true, want false")
	}
	if loaded.Bytes != 0 {
		t.Errorf("Bytes = %d, want 0", loaded.Bytes)
	}
	if len(loaded.Findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", loaded.Findings)
	}
	if got := loaded.Findings[0].Level; got != LevelWarn {
		t.Errorf("finding level = %v, want %v", got, LevelWarn)
	}
	if levelCount(loaded.Findings, LevelFail) != 0 {
		t.Errorf("findings = %v, want no failure", loaded.Findings)
	}

	want := Config{
		Debounce:       250 * time.Millisecond,
		RescanInterval: 30 * time.Minute,
	}
	// An installed pattern would make the daemon act on files the user never named.
	if len(loaded.Config.Patterns) != 0 {
		t.Errorf("Patterns = %v, want an empty list", loaded.Config.Patterns)
	}
	if loaded.Config.Debounce != want.Debounce {
		t.Errorf("Debounce = %v, want %v", loaded.Config.Debounce, want.Debounce)
	}
	if loaded.Config.RescanInterval != want.RescanInterval {
		t.Errorf("RescanInterval = %v, want %v", loaded.Config.RescanInterval, want.RescanInterval)
	}
	for _, field := range knownKeys {
		if got := loaded.Source(field); got != SourceDefault {
			t.Errorf("Source(%q) = %q, want %q", field, got, SourceDefault)
		}
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Load created the config file, want a missing file untouched")
	}
}

func TestLoadRejectsBadContent(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantMessage string
		wantSuggest string
	}{
		{
			name: "YAML content",
			content: "patterns:\n  - ~/.claude/skills/*/SKILL.md\n" +
				"debounce: 250ms\nrescan_interval: 30m\n",
			wantMessage: "JSON",
		},
		{
			name:        "unknown key paterns",
			content:     `{"patterns": ["~/a/SKILL.md"], "paterns": ["~/b/SKILL.md"]}`,
			wantMessage: "patterns",
		},
		{
			name:        "unknown key patternz",
			content:     `{"patterns": ["~/a/SKILL.md"], "patternz": ["~/b/SKILL.md"]}`,
			wantMessage: "patterns",
		},
		{
			name:        "unknown key zzzzzzzz",
			content:     `{"patterns": ["~/a/SKILL.md"], "zzzzzzzz": 1}`,
			wantMessage: "closest known key",
		},
		{
			name:        "patterns missing",
			content:     `{"debounce": "250ms", "rescan_interval": "30m"}`,
			wantMessage: "missing",
		},
		{
			name:        "patterns empty",
			content:     `{"patterns": []}`,
			wantMessage: "empty",
		},
		{
			name:        "debounce without a unit",
			content:     `{"patterns": ["~/a/SKILL.md"], "debounce": "300"}`,
			wantMessage: "300ms",
			wantSuggest: "300ms",
		},
		{
			name:        "debounce as a number",
			content:     `{"patterns": ["~/a/SKILL.md"], "debounce": 300}`,
			wantMessage: "debounce",
		},
		{
			name:        "debounce negative",
			content:     `{"patterns": ["~/a/SKILL.md"], "debounce": "-1s"}`,
			wantMessage: "greater than zero",
		},
		{
			name:        "rescan_interval without a unit",
			content:     `{"patterns": ["~/a/SKILL.md"], "rescan_interval": "30"}`,
			wantMessage: "30ms",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loaded := loadFile(t, test.content)

			if levelCount(loaded.Findings, LevelFail) == 0 {
				t.Fatalf("findings = %v, want at least one failure", loaded.Findings)
			}
			if message := firstMessage(loaded.Findings, LevelFail); !strings.Contains(message, test.wantMessage) {
				t.Errorf("failure message = %q, want it to contain %q", message, test.wantMessage)
			}
			if test.wantSuggest != "" {
				got := suggestFor(loaded.Findings, test.wantSuggest)
				if got == "" {
					t.Errorf("no suggestion contains %q, findings = %v", test.wantSuggest, loaded.Findings)
				}
			}
		})
	}
}

// suggestFor returns "" when no suggestion contains want.
func suggestFor(findings []Finding, want string) string {
	for _, finding := range findings {
		if strings.Contains(finding.Suggest, want) {
			return finding.Suggest
		}
	}
	return ""
}

func TestLoadAcceptsValidDurations(t *testing.T) {
	tests := []struct {
		name         string
		debounce     string
		rescan       string
		wantDebounce time.Duration
		wantRescan   time.Duration
	}{
		{
			name:         "both with a unit",
			debounce:     `"250ms"`,
			rescan:       `"30m"`,
			wantDebounce: 250 * time.Millisecond,
			wantRescan:   30 * time.Minute,
		},
		{
			name:         "rescan off",
			debounce:     `"1s"`,
			rescan:       `"0"`,
			wantDebounce: time.Second,
			wantRescan:   0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loaded := loadFile(t, `{"patterns": ["~/a/SKILL.md"], "debounce": `+
				test.debounce+`, "rescan_interval": `+test.rescan+`}`)

			if len(loaded.Findings) != 0 {
				t.Fatalf("findings = %v, want none", loaded.Findings)
			}
			if loaded.Config.Debounce != test.wantDebounce {
				t.Errorf("Debounce = %v, want %v", loaded.Config.Debounce, test.wantDebounce)
			}
			if loaded.Config.RescanInterval != test.wantRescan {
				t.Errorf("RescanInterval = %v, want %v", loaded.Config.RescanInterval, test.wantRescan)
			}
		})
	}
}

func TestValidateKeepsTheOrder(t *testing.T) {
	loaded := Loaded{Findings: []Finding{{Level: LevelWarn, Subject: "load"}}}
	extra := []Finding{{Level: LevelFail, Subject: "pattern"}}

	got := Validate(loaded, extra)
	if len(got) != 2 {
		t.Fatalf("Validate() returned %d findings, want 2", len(got))
	}
	if got[0].Subject != "load" || got[1].Subject != "pattern" {
		t.Errorf("Validate() order = %q, %q, want the load finding first", got[0].Subject, got[1].Subject)
	}
}

func TestClosestKeyBreaksATieBySortedOrder(t *testing.T) {
	// zzzzzzzz shares no character with any key, so the three distances tie on length. The
	// sorted order puts debounce first among the tied keys.
	if got, want := closestKey("zzzzzzzz"), FieldDebounce; got != want {
		t.Errorf("closestKey(zzzzzzzz) = %q, want %q", got, want)
	}
	if got, want := closestKey("paterns"), FieldPatterns; got != want {
		t.Errorf("closestKey(paterns) = %q, want %q", got, want)
	}
}

func TestPathIsInTheHomeDirectory(t *testing.T) {
	home := newHome(t)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path() error = %v, want nil", err)
	}
	if want := filepath.Join(home, FileName); path != want {
		t.Errorf("Path() = %q, want %q", path, want)
	}
}

func equalPatterns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestLoadReadsTheFileThroughTheSeam: Load goes through the one open function the package
// exposes, so a caller can count the opens.
func TestLoadReadsTheFileThroughTheSeam(t *testing.T) {
	home := newHome(t)
	writeFile(t, home, `{"patterns": ["~/skills/*/SKILL.md"]}`)

	opens := 0
	previous := ReadFile
	ReadFile = func(name string) ([]byte, error) {
		opens++
		return previous(name)
	}
	t.Cleanup(func() { ReadFile = previous })

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if opens != 1 {
		t.Errorf("opens = %d, want 1", opens)
	}
	// The parsed value is what the caller holds: no second load is needed.
	if got := loaded.Config.Patterns; len(got) != 1 || got[0] != "~/skills/*/SKILL.md" {
		t.Errorf("patterns = %v, want the fixture pattern", got)
	}
	if opens != 1 {
		t.Errorf("opens after reading the value = %d, want 1", opens)
	}
}

// TestCreateWritesTheDefaultFile: the created file names two skills and holds no duration, so
// the defaults apply until the user writes a value.
func TestCreateWritesTheDefaultFile(t *testing.T) {
	home := newHome(t)

	path, err := Create()
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if want := filepath.Join(home, FileName); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the created file: %v", err)
	}
	if string(data) != DefaultConfig {
		t.Errorf("file = %q, want %q", data, DefaultConfig)
	}

	// The created file is runnable: it loads with no finding, and both durations are defaults.
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !loaded.Exists {
		t.Error("Load() reports the created file as missing")
	}
	if got := loaded.Source(FieldDebounce); got != SourceDefault {
		t.Errorf("Source(debounce) = %q, want %q: create writes no duration", got, SourceDefault)
	}
	if got := loaded.Source(FieldRescanInterval); got != SourceDefault {
		t.Errorf("Source(rescan_interval) = %q, want %q: create writes no duration", got, SourceDefault)
	}
	if len(loaded.Findings) != 0 {
		t.Errorf("findings = %v, want none: the default config is valid", loaded.Findings)
	}
	wantPatterns := []string{
		"~/.claude/skills/foo-bar/SKILL.md",
		"~/.agents/skills/foo-bar/SKILL.md",
	}
	if got := loaded.Config.Patterns; !equalPatterns(got, wantPatterns) {
		t.Errorf("patterns = %v, want %v", got, wantPatterns)
	}
}

// TestCreateRefusesAnExistingFile: it never overwrites a config the user may have written.
func TestCreateRefusesAnExistingFile(t *testing.T) {
	home := newHome(t)
	content := `{"patterns": ["~/skills/*/SKILL.md"]}`
	path := writeFile(t, home, content)

	if _, err := Create(); !errors.Is(err, ErrExists) {
		t.Fatalf("Create() error = %v, want ErrExists", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the config file: %v", err)
	}
	if string(data) != content {
		t.Errorf("file = %q, want the original %q", data, content)
	}
}
