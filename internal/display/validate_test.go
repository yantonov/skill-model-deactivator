package display

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yantonov/skill-model-deactivator/internal/config"
)

// validateFixture has one finding of each level, so one fixture covers the layout, the
// counters, and the exit code.
func validateFixture(t *testing.T) ValidateReport {
	t.Helper()
	home := t.TempDir()
	withFakeHome(t, home)
	loaded := loadFrom(t, home, fixture)
	return ValidateReport{
		Loaded:   loaded,
		LogPath:  filepath.Join(home, "smd.log"),
		Patterns: len(loaded.Config.Patterns),
		Files:    78,
		Findings: []config.Finding{
			{Level: config.LevelOK, Message: "config parses"},
			{Level: config.LevelWarn, Subject: home + "/.agents/skills/stale-skill-*/SKILL.md",
				Message: `pattern "` + home + `/.agents/skills/stale-skill-*/SKILL.md" matches nothing: stale`},
			{Level: config.LevelFail, Subject: home + "/.agents/skills/[a-/SKILL.md",
				Message: "unbalanced bracket or brace"},
		},
	}
}

func TestValidateReportCounts(t *testing.T) {
	ok, warn, fail := validateFixture(t).Counts()
	if ok != 1 || warn != 1 || fail != 1 {
		t.Errorf("Counts() = %d, %d, %d, want 1, 1, 1", ok, warn, fail)
	}
}

func TestValidateReportExitCode(t *testing.T) {
	tests := []struct {
		name     string
		findings []config.Finding
		strict   bool
		noConfig bool
		want     int
	}{
		{"all ok", []config.Finding{{Level: config.LevelOK}}, false, false, 0},
		{"warn only", []config.Finding{{Level: config.LevelWarn}}, false, false, 0},
		{"warn with strict", []config.Finding{{Level: config.LevelWarn}}, true, false, 1},
		{"fail", []config.Finding{{Level: config.LevelFail}}, false, false, 1},
		{"fail with strict", []config.Finding{{Level: config.LevelFail}}, true, false, 1},
		{"no config file", []config.Finding{{Level: config.LevelWarn}}, false, true, 1},
		{"no findings", nil, false, false, 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := ValidateReport{Findings: test.findings, Strict: test.strict, NoConfig: test.noConfig}
			if got := report.ExitCode(); got != test.want {
				t.Errorf("ExitCode() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestValidateLayout(t *testing.T) {
	report := validateFixture(t)

	var out bytes.Buffer
	if err := Validate(&out, report); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	printed := out.String()

	lines := strings.Split(printed, "\n")
	if len(lines) < 8 {
		t.Fatalf("Validate() printed %d lines, want the header, the findings, and the summary:\n%s",
			len(lines), printed)
	}
	for _, want := range []string{
		"config       ~/.skill-model-deactivator.json (exists,",
		"patterns     2 patterns, 78 files",
		"log          ~/smd.log (missing)",
		"ok           config parses",
		`warn         pattern "~/.agents/skills/stale-skill-*/SKILL.md" matches nothing: stale`,
		"fail         ~/.agents/skills/[a-/SKILL.md: unbalanced bracket or brace",
		"1 warning, 1 failure",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("Validate() output does not contain %q:\n%s", want, printed)
		}
	}
}

func TestValidateFindingLine(t *testing.T) {
	tests := []struct {
		name    string
		finding config.Finding
		want    string
	}{
		{"message only", config.Finding{Message: "config parses"}, "config parses"},
		{"subject only", config.Finding{Subject: "patterns"}, "patterns"},
		{"subject and message", config.Finding{Subject: "debounce", Message: "does not parse"}, "debounce: does not parse"},
		{"message names the subject", config.Finding{Subject: "patterns", Message: "patterns is empty"}, "patterns is empty"},
		{"neither", config.Finding{}, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := findingLine(test.finding); got != test.want {
				t.Errorf("findingLine(%+v) = %q, want %q", test.finding, got, test.want)
			}
		})
	}
}

func TestValidateJSON(t *testing.T) {
	report := validateFixture(t)

	var out bytes.Buffer
	if err := ValidateJSON(&out, report); err != nil {
		t.Fatalf("ValidateJSON() error = %v, want nil", err)
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Errorf("ValidateJSON() printed %d lines, want one: %q", strings.Count(out.String(), "\n"), out.String())
	}

	var decoded struct {
		Findings []map[string]string `json:"findings"`
		Summary  map[string]int      `json:"summary"`
		ExitCode int                 `json:"exit_code"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("ValidateJSON() output does not unmarshal: %v\n%s", err, out.String())
	}
	if len(decoded.Findings) != 3 {
		t.Errorf("findings = %d, want 3: %s", len(decoded.Findings), out.String())
	}
	for _, finding := range decoded.Findings {
		for _, key := range []string{"level", "subject", "message", "suggest"} {
			if _, ok := finding[key]; !ok {
				t.Errorf("finding %v has no %q key", finding, key)
			}
		}
	}
	if decoded.Summary["ok"] != 1 || decoded.Summary["warn"] != 1 || decoded.Summary["fail"] != 1 {
		t.Errorf("summary = %v, want ok 1, warn 1, fail 1", decoded.Summary)
	}
	if decoded.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", decoded.ExitCode)
	}
}

func TestAbbreviateHome(t *testing.T) {
	home := filepath.ToSlash(t.TempDir())
	withFakeHome(t, home)

	tests := []struct {
		text string
		want string
	}{
		{home + "/.agents/skills/bl/SKILL.md", "~/.agents/skills/bl/SKILL.md"},
		{`pattern "` + home + `/a/*/SKILL.md" matches nothing`, `pattern "~/a/*/SKILL.md" matches nothing`},
		{"a message with no path", "a message with no path"},
	}
	for _, test := range tests {
		if got := abbreviateHome(test.text); got != test.want {
			t.Errorf("abbreviateHome(%q) = %q, want %q", test.text, got, test.want)
		}
	}
}
