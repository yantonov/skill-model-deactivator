package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newHome points both the config path and the pattern expansion at the fixture.
func newHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", home)
	return home
}

func writeConfig(t *testing.T, home, content string) string {
	t.Helper()
	path := filepath.Join(home, ".skill-model-deactivator.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the config fixture: %v", err)
	}
	return path
}

// writeTree gives every file the canonical frontmatter, so the tree is clean.
func writeTree(t *testing.T, home string, names ...string) {
	t.Helper()
	for _, name := range names {
		content := "---\nname: " + strings.TrimSuffix(filepath.Base(filepath.Dir(name)), "/") +
			"\ndisable-model-invocation: true\n---\nbody\n"
		writeSkillFile(t, home, name, content)
	}
}

func validateHome(t *testing.T, home string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runCLI(t, append([]string{"config", "validate"}, args...)...)
}

func TestConfigValidateOnACleanTree(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md", ".agents/skills/bl/SKILL.md")
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/*/SKILL.md"]}`)

	stdout, stderr, code := validateHome(t, home)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty: a finding never goes to stdout", stdout)
	}
	for _, want := range []string{
		"config       ~/.skill-model-deactivator.json (exists,",
		"valid JSON)",
		"patterns     1 pattern, 2 files",
		"log          ",
		"ok           config parses",
		"ok           patterns yield files",
		"ok           frontmatter parses in all matched files",
		"0 warnings, 0 failures",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("validate output does not contain %q:\n%s", want, stderr)
		}
	}
	// Two clean files still print one ok line.
	if got := strings.Count(stderr, okFrontmatterRead); got != 1 {
		t.Errorf("frontmatter ok lines = %d, want 1 for a tree with two clean files:\n%s", got, stderr)
	}
}

// TestConfigValidateFrontmatterFindings: one case per frontmatter finding, each with a fixture
// that must produce exactly one warn.
func TestConfigValidateFrontmatterFindings(t *testing.T) {
	const skill = ".agents/skills/kibana/SKILL.md"

	tests := []struct {
		name    string
		content string
		want    string // the message the warn line must hold
	}{
		{
			name:    "no frontmatter",
			content: "name: kibana\ndescription: a skill\n",
			want:    "no frontmatter",
		},
		{
			name:    "frontmatter does not parse",
			content: "---\nname: a\nname: b\n---\nbody\n",
			want:    "frontmatter does not parse",
		},
		{
			name:    "no name key",
			content: "---\ndescription: a skill\ndisable-model-invocation: true\n---\n",
			want:    "no name key in frontmatter",
		},
		{
			name:    "key absent needs an insert",
			content: "---\nname: kibana\n---\nbody\n",
			want:    "needs disable-model-invocation: true (insert)",
		},
		{
			name:    "truthy value needs a rewrite",
			content: "---\nname: kibana\ndisable-model-invocation: yes\n---\n",
			want:    "needs disable-model-invocation: true (rewrite)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := newHome(t)
			writeSkillFile(t, home, skill, test.content)
			writeConfig(t, home, `{"patterns": ["~/.agents/skills/*/SKILL.md"]}`)

			_, stderr, code := validateHome(t, home)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0: every one of these is a warn\n%s", code, stderr)
			}
			if got := strings.Count(stderr, "\nwarn         "); got != 1 {
				t.Fatalf("warn lines = %d, want 1:\n%s", got, stderr)
			}
			// The message follows the subject on the same line.
			if want := "~/.agents/skills/kibana/SKILL.md: " + test.want; !strings.Contains(stderr, want) {
				t.Errorf("validate output does not contain %q:\n%s", want, stderr)
			}
			if !strings.Contains(stderr, "1 warning, 0 failures") {
				t.Errorf("summary line is missing the warn count:\n%s", stderr)
			}
		})
	}
}

// TestConfigValidateCleanTreePrintsOneOkLinePerCategory: many clean files give exactly three ok
// lines.
func TestConfigValidateCleanTreePrintsOneOkLinePerCategory(t *testing.T) {
	home := newHome(t)
	for i := 0; i < 6; i++ {
		writeSkillFile(t, home, fmt.Sprintf(".agents/skills/s%d/SKILL.md", i),
			"---\nname: s\ndisable-model-invocation: true\n---\nbody\n")
	}
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/*/SKILL.md"]}`)

	_, stderr, code := validateHome(t, home)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, stderr)
	}
	if got := strings.Count(stderr, "\nok           "); got != 3 {
		t.Errorf("ok lines = %d, want 3:\n%s", got, stderr)
	}
}

func TestConfigValidateOkLinesComeBeforePerFileWarnings(t *testing.T) {
	home := newHome(t)
	writeSkillFile(t, home, ".agents/skills/noname/SKILL.md",
		"---\ndescription: x\ndisable-model-invocation: true\n---\n")
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/*/SKILL.md"]}`)

	_, stderr, code := validateHome(t, home)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, stderr)
	}
	okAt := strings.Index(stderr, okFrontmatterRead)
	warnAt := strings.Index(stderr, noNameText)
	if okAt < 0 || warnAt < 0 {
		t.Fatalf("the report is missing a finding:\n%s", stderr)
	}
	if okAt > warnAt {
		t.Errorf("the frontmatter ok line comes after the per-file warning:\n%s", stderr)
	}
	if strings.Count(stderr, okFrontmatterRead) != 1 {
		t.Errorf("frontmatter ok line is not printed exactly once:\n%s", stderr)
	}
}

func TestConfigValidateCanonicalFileHasNoWarn(t *testing.T) {
	home := newHome(t)
	writeSkillFile(t, home, ".agents/skills/kibana/SKILL.md",
		"---\nname: kibana\ndisable-model-invocation: true\n---\nbody\n")
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/*/SKILL.md"]}`)

	_, stderr, code := validateHome(t, home)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, stderr)
	}
	if strings.Contains(stderr, "\nwarn         ") {
		t.Errorf("validate reported a warn for a canonical file:\n%s", stderr)
	}
	if !strings.Contains(stderr, "0 warnings, 0 failures") {
		t.Errorf("summary line does not count zero warnings:\n%s", stderr)
	}
}

func TestConfigValidateStalePatternIsAWarn(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/stale-skill-*/SKILL.md"]}`)

	_, stderr, code := validateHome(t, home)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: a stale pattern is not a failure", code)
	}
	if !strings.Contains(stderr, "warn") || !strings.Contains(stderr, "stale") {
		t.Errorf("validate did not report the stale pattern as a warn:\n%s", stderr)
	}
	if !strings.Contains(stderr, "1 warning, 0 failures") {
		t.Errorf("summary line is missing the warn count:\n%s", stderr)
	}
	// The home directory prints as ~.
	if !strings.Contains(stderr, `"~/.agents/skills/stale-skill-*/SKILL.md"`) {
		t.Errorf("the finding does not show the pattern with ~ and forward slashes:\n%s", stderr)
	}
}

func TestConfigValidateStrictTurnsAWarnIntoExitOne(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/stale-skill-*/SKILL.md"]}`)

	_, stderr, code := validateHome(t, home, "--strict")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 with --strict: %s", code, stderr)
	}
	// --strict changes the exit code only.
	if !strings.Contains(stderr, "warn") {
		t.Errorf("--strict changed the level of the stale finding:\n%s", stderr)
	}
	if strings.Contains(stderr, "fail         ") {
		t.Errorf("--strict reported a fail finding, want the warn kept:\n%s", stderr)
	}
}

func TestConfigValidateMissingWatchRoot(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/*/SKILL.md", "~/absent/*/SKILL.md"]}`)

	_, stderr, code := validateHome(t, home)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: a missing root is a warn", code)
	}
	if !strings.Contains(stderr, `watch root "~/absent" does not exist`) {
		t.Errorf("validate did not report the missing watch root:\n%s", stderr)
	}
}

func TestConfigValidateMissingWatchRootIsReportedOnce(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, "other/SKILL.md")
	writeConfig(t, home, `{"patterns": ["~/absent/*/SKILL.md", "~/absent/**/SKILL.md"]}`)

	_, stderr, _ := validateHome(t, home)
	if got := strings.Count(stderr, "watch root"); got != 1 {
		t.Errorf("watch root findings = %d, want 1: two patterns share one root\n%s", got, stderr)
	}
}

func TestConfigValidateBackslashPattern(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")
	// The backslash is escaped in the JSON, so the config holds the characters the user typed.
	writeConfig(t, home, `{"patterns": ["~\\.agents\\skills\\*\\SKILL.md"]}`)

	_, stderr, code := validateHome(t, home)
	if filepath.Separator == '\\' {
		// Windows: ToSlash turns every backslash into a separator, so the pattern resolves.
		if code != 0 {
			t.Errorf("exit code = %d, want 0: a backslash pattern is normalized on Windows\n%s", code, stderr)
		}
		if strings.Contains(stderr, "backslash") {
			t.Errorf("validate reported a backslash on Windows, where it is a separator:\n%s", stderr)
		}
		return
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1: a surviving backslash is a fail\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "backslash") {
		t.Errorf("validate did not report the backslash:\n%s", stderr)
	}
	if strings.Contains(stderr, `\.agents`) {
		t.Errorf("the finding still shows a backslash, want forward slashes:\n%s", stderr)
	}
}

func TestConfigValidateUnbalancedBracketIsAFail(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/[a-/SKILL.md"]}`)

	_, stderr, code := validateHome(t, home)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1: an unbalanced bracket is a fail\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "fail") || !strings.Contains(stderr, "unbalanced bracket or brace") {
		t.Errorf("validate did not report the unbalanced bracket:\n%s", stderr)
	}
	if !strings.Contains(stderr, "1 failure") {
		t.Errorf("summary line is missing the failure count:\n%s", stderr)
	}
}

func TestConfigValidateNoConfigPrintsAnExample(t *testing.T) {
	newHome(t)

	stdout, stderr, code := validateHome(t, "")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 with no config file", code)
	}
	if !strings.Contains(stdout, "No config file at ~/.skill-model-deactivator.json") {
		t.Errorf("stdout does not name the missing config path:\n%s", stdout)
	}
	// The example must paste as valid JSON with the required key and one concrete skill path, so
	// it never reads as "disable everything".
	start := strings.Index(stdout, "{")
	var decoded map[string]any
	if start < 0 {
		t.Fatalf("stdout holds no example config:\n%s", stdout)
	}
	if err := json.Unmarshal([]byte(stdout[start:]), &decoded); err != nil {
		t.Fatalf("the example config does not parse: %v\n%s", err, stdout)
	}
	if _, ok := decoded["patterns"]; !ok {
		t.Errorf("the example config has no %q key:\n%s", "patterns", stdout)
	}
	if !strings.Contains(stdout, "skills/foo-bar/SKILL.md") {
		t.Errorf("the example config names no skill:\n%s", stdout)
	}
	if !strings.Contains(stderr, "warn") {
		t.Errorf("validate did not report the missing config as a warn:\n%s", stderr)
	}
}

func TestConfigValidateJSON(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/*/SKILL.md", "~/absent/*/SKILL.md"]}`)

	stdout, stderr, code := validateHome(t, home, "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if strings.Count(stdout, "\n") != 1 {
		t.Errorf("--json printed %d lines, want one: %q", strings.Count(stdout, "\n"), stdout)
	}

	var decoded struct {
		Findings []struct {
			Level   string `json:"level"`
			Subject string `json:"subject"`
			Message string `json:"message"`
			Suggest string `json:"suggest"`
		} `json:"findings"`
		Summary struct {
			OK   int `json:"ok"`
			Warn int `json:"warn"`
			Fail int `json:"fail"`
		} `json:"summary"`
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("--json output does not unmarshal: %v\n%s", err, stdout)
	}
	if len(decoded.Findings) == 0 {
		t.Fatalf("--json reported no findings: %s", stdout)
	}
	if decoded.Summary.Warn == 0 {
		t.Errorf("summary.warn = 0, want the missing root and stale pattern counted: %s", stdout)
	}
	if decoded.Summary.Fail != 0 {
		t.Errorf("summary.fail = %d, want 0: %s", decoded.Summary.Fail, stdout)
	}
	if decoded.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0: %s", decoded.ExitCode, stdout)
	}
	for _, finding := range decoded.Findings {
		switch finding.Level {
		case "ok", "warn", "fail":
		default:
			t.Errorf("finding level = %q, want ok, warn, or fail", finding.Level)
		}
		if finding.Message == "" {
			t.Errorf("finding %+v has no message", finding)
		}
	}
}

func TestConfigValidateJSONExitCodeFollowsStrict(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")
	writeConfig(t, home, `{"patterns": ["~/absent/*/SKILL.md"]}`)

	stdout, _, code := validateHome(t, home, "--json", "--strict")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 with --strict", code)
	}
	if !strings.Contains(stdout, `"exit_code":1`) {
		t.Errorf("--json exit_code does not follow --strict: %s", stdout)
	}
}

func TestConfigValidateWritesNothing(t *testing.T) {
	home := newHome(t)
	// Every finding this build can produce, in one fixture.
	writeSkillFile(t, home, ".agents/skills/nofm/SKILL.md", "name: nofm\n")
	writeSkillFile(t, home, ".agents/skills/broken/SKILL.md", "---\nname: a\nname: b\n---\n")
	writeSkillFile(t, home, ".agents/skills/noname/SKILL.md",
		"---\ndescription: a skill\ndisable-model-invocation: true\n---\n")
	writeSkillFile(t, home, ".agents/skills/absent/SKILL.md", "---\nname: absent\n---\n")
	writeConfig(t, home, `{"patterns": ["~/.agents/skills/*/SKILL.md", "~/absent/*/SKILL.md", "~/.agents/skills/[a-/SKILL.md"], "bogus": 1}`)

	before := snapshot(t, home)

	_, stderr, code := validateHome(t, home)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1: the fixture holds a fail finding", code)
	}
	for _, want := range []string{
		"no frontmatter",
		"frontmatter does not parse",
		"no name key in frontmatter",
		"needs disable-model-invocation: true (insert)",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("validate output does not contain %q:\n%s", want, stderr)
		}
	}

	after := snapshot(t, home)
	if len(before) != len(after) {
		t.Fatalf("the file count changed from %d to %d", len(before), len(after))
	}
	for name, hash := range before {
		if after[name] != hash {
			t.Errorf("%s changed: %s became %s", name, hash, after[name])
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("%s appeared during config validate", name)
		}
		if strings.Contains(filepath.Base(name), ".smd-tmp") {
			t.Errorf("config validate left a temp file: %s", name)
		}
	}
}

// snapshot is keyed by the path relative to root, so a test can assert that nothing changed on
// disk.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("%v:%s", info.Mode(), data)))
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(name)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snapshot
}

func TestConfigValidateFileCount(t *testing.T) {
	home := newHome(t)
	// A plain path, a glob, and a link spelling the same file twice, so the count shows the
	// dedupe, plus a second file reached by **.
	writeTree(t, home, ".agents/skills/kibana/SKILL.md", ".agents/skills/synced/uuid/bl/SKILL.md")
	writeConfig(t, home, `{"patterns": [
		"~/.agents/skills/kibana/SKILL.md",
		"~/.agents/skills/*/SKILL.md",
		"~/.agents/skills/**/SKILL.md"
	]}`)

	_, stderr, code := validateHome(t, home)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	// kibana/SKILL.md is resolved once, and the nested file once: two files.
	if !strings.Contains(stderr, "3 patterns, 2 files") {
		t.Errorf("patterns line does not show the deduplicated file count:\n%s", stderr)
	}
}

func TestConfigValidateReportsAJunkConfigAsAFail(t *testing.T) {
	home := newHome(t)
	writeConfig(t, home, `not json`)

	_, stderr, code := validateHome(t, home)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if strings.Contains(stderr, "ok           config parses") {
		t.Errorf("validate reported ok for a config that does not parse:\n%s", stderr)
	}
	if !strings.Contains(stderr, "not valid JSON") {
		t.Errorf("validate does not name the JSON parse failure:\n%s", stderr)
	}
}

func TestValidateFindingOrderIsStable(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")
	writeConfig(t, home, `{"patterns": ["~/absent/*/SKILL.md", "~/.agents/skills/[a-/SKILL.md"]}`)

	first, _, _ := validateHome(t, home)
	second, _, _ := validateHome(t, home)
	if first != second {
		t.Errorf("two runs printed different reports:\n%s\n---\n%s", first, second)
	}
}
