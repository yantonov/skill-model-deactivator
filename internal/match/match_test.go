package match

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/yantonov/skill-model-deactivator/internal/config"
)

// newHome points ~ at a temp directory, so an assertion on a resolved path does not depend on
// the machine.
func newHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeTree(t *testing.T, home string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(home, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// rel returns names relative to home, so a test compares names and not a machine-specific
// prefix.
func rel(t *testing.T, home string, paths []string) []string {
	t.Helper()
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		name, err := filepath.Rel(home, path)
		if err != nil {
			t.Fatalf("rel %s: %v", path, err)
		}
		names = append(names, filepath.ToSlash(name))
	}
	return names
}

func sameNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// resolveNames returns the matched names relative to home.
func resolveNames(t *testing.T, home, pattern string) []string {
	t.Helper()
	result, err := Resolve([]string{pattern})
	if err != nil {
		t.Fatalf("Resolve(%q) error = %v, want nil", pattern, err)
	}
	return rel(t, home, result.Files)
}

func TestParse(t *testing.T) {
	home := filepath.ToSlash(newHome(t))

	tests := []struct {
		name      string
		raw       string
		wantSlash string
		wantWild  bool
		wantRoot  string
	}{
		{"plain path", "~/skills/kibana/SKILL.md", home + "/skills/kibana/SKILL.md", false, home + "/skills/kibana/SKILL.md"},
		{"backslash separators", `~\skills\kibana\SKILL.md`, home + "/skills/kibana/SKILL.md", false, home + "/skills/kibana/SKILL.md"},
		{"one star", "~/skills/*/SKILL.md", home + "/skills/*/SKILL.md", true, home + "/skills"},
		{"double star", "~/skills/**/SKILL.md", home + "/skills/**/SKILL.md", true, home + "/skills"},
		{"brace", "~/skills/{a,b}/SKILL.md", home + "/skills/{a,b}/SKILL.md", true, home + "/skills"},
		{"bracket", "~/skills/[ab]/SKILL.md", home + "/skills/[ab]/SKILL.md", true, home + "/skills"},
		{"question", "~/skills/?.md", home + "/skills/?.md", true, home + "/skills"},
		{"no tilde", "/etc/skills/*/SKILL.md", "/etc/skills/*/SKILL.md", true, "/etc/skills"},
		{"relative", "skills/*/SKILL.md", "skills/*/SKILL.md", true, "skills"},
		{"bare tilde", "~", home, false, home},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pattern, err := Parse(test.raw)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v, want nil", test.raw, err)
			}
			if pattern.Slash != test.wantSlash {
				t.Errorf("Slash = %q, want %q", pattern.Slash, test.wantSlash)
			}
			if pattern.Wildcard != test.wantWild {
				t.Errorf("Wildcard = %v, want %v", pattern.Wildcard, test.wantWild)
			}
			if pattern.Root != test.wantRoot {
				t.Errorf("Root = %q, want %q", pattern.Root, test.wantRoot)
			}
		})
	}
}

func TestParseKeepsRawBeforeNormalization(t *testing.T) {
	home := newHome(t)

	pattern, err := Parse(`~\skills\kibana\SKILL.md`)
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}
	if want := home + `\skills\kibana\SKILL.md`; pattern.Raw != want {
		t.Errorf("Raw = %q, want %q", pattern.Raw, want)
	}
	if strings.Contains(pattern.Slash, `\`) {
		t.Errorf("Slash = %q, want no backslash", pattern.Slash)
	}
}

func TestParseLeavesUserTildeLiteral(t *testing.T) {
	newHome(t)

	for _, raw := range []string{"~user/skills/*/SKILL.md", "~user"} {
		pattern, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v, want nil", raw, err)
		}
		if pattern.Slash != raw {
			t.Errorf("Parse(%q).Slash = %q, want the literal pattern", raw, pattern.Slash)
		}
	}
}

func TestRoot(t *testing.T) {
	home := filepath.ToSlash(newHome(t))

	tests := []struct {
		raw  string
		want string
	}{
		// doublestar.SplitPattern splits this exactly here.
		{"~/tree/skills/*/SKILL.md", home + "/tree/skills"},
		{"~/tree/skills/**/SKILL.md", home + "/tree/skills"},
		{"~/tree/*/deep/**/SKILL.md", home + "/tree"},
		{"~/tree/skills/SKILL.md", home + "/tree/skills/SKILL.md"},
		{"~/tree/skills/*", home + "/tree/skills"},
	}

	for _, test := range tests {
		pattern, err := Parse(test.raw)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v, want nil", test.raw, err)
		}
		if pattern.Root != test.want {
			t.Errorf("Parse(%q).Root = %q, want %q", test.raw, pattern.Root, test.want)
		}
	}
}

func TestResolveStarMatchesOneSegment(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, "skills/SKILL.md", "skills/kibana/SKILL.md", "skills/a/b/SKILL.md")

	got := resolveNames(t, home, "~/skills/*/SKILL.md")
	want := []string{"skills/kibana/SKILL.md"}
	if !sameNames(got, want) {
		t.Errorf("*/SKILL.md matched %v, want %v", got, want)
	}
}

func TestResolveDoubleStarMatchesZeroOrMoreSegments(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, "SKILL.md", "skills/SKILL.md", "skills/kibana/SKILL.md", "skills/a/b/SKILL.md")

	tests := []struct {
		pattern string
		want    []string
	}{
		// ** matches zero segments, so it reaches the file at the root.
		{"~/**/SKILL.md", []string{"SKILL.md", "skills/SKILL.md", "skills/a/b/SKILL.md", "skills/kibana/SKILL.md"}},
		{"~/skills/**/SKILL.md", []string{"skills/SKILL.md", "skills/a/b/SKILL.md", "skills/kibana/SKILL.md"}},
	}

	for _, test := range tests {
		got := resolveNames(t, home, test.pattern)
		if !sameNames(got, test.want) {
			t.Errorf("%s matched %v, want %v", test.pattern, got, test.want)
		}
	}
}

func TestBackupNameIsNotASKill(t *testing.T) {
	home := newHome(t)
	backup := "skills/SKILL.md.bak.2026-10-26_17-17-25"
	writeTree(t, home, backup)

	// The measured behaviour of doublestar v4.10.4: the pattern ends at the file name, so a
	// pattern that names SKILL.md rejects the backup.
	for _, pattern := range []string{"~/skills/*/SKILL.md", "~/skills/*.md"} {
		if got := resolveNames(t, home, pattern); len(got) != 0 {
			t.Errorf("%s matched %v, want nothing", pattern, got)
		}
	}
	// A bare * accepts every name, the backup included.
	if got := resolveNames(t, home, "~/skills/*"); !sameNames(got, []string{backup}) {
		t.Errorf("~/skills/* matched %v, want %v", got, []string{backup})
	}
}

func TestForwardSlashPatternResolvesARealTree(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md", ".agents/skills/bl/SKILL.md")

	// A count of zero on Windows is the defect where doublestar reads a backslash as an escape
	// and reports no error.
	result, err := Resolve([]string{"~/.agents/skills/*/SKILL.md"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("Resolve() found %d files, want 2: %v", len(result.Files), result.Files)
	}
	if len(result.Findings) != 0 {
		t.Errorf("Resolve() findings = %v, want none: a forward-slash pattern is clean", result.Findings)
	}
}

func TestUnconvertedPathDoesNotMatch(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")

	result, err := Resolve([]string{"~/.agents/skills/*/SKILL.md"})
	if err != nil || len(result.Files) != 1 {
		t.Fatalf("Resolve() = %v, %v, want one file", result.Files, err)
	}
	path := result.Files[0]
	pattern, err := Parse("~/.agents/skills/*/SKILL.md")
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}

	// The guard against deleting the conversion: doublestar reads a backslash as an escape, so
	// the raw OS path is not a match even though the slashed one is.
	matched, err := doublestar.Match(pattern.Slash, path)
	if err != nil {
		t.Fatalf("doublestar.Match() error = %v, want nil", err)
	}
	if filepath.Separator == '\\' {
		if matched {
			t.Errorf("doublestar.Match(%q, %q) = true, want false: an unconverted Windows path is not a match", pattern.Slash, path)
		}
	} else if !matched {
		t.Errorf("doublestar.Match(%q, %q) = false, want true", pattern.Slash, path)
	}

	matched, err = doublestar.Match(filepath.ToSlash(pattern.Slash), filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("doublestar.Match() error = %v, want nil", err)
	}
	if !matched {
		t.Errorf("doublestar.Match on both converted sides = false, want true")
	}
}

func TestBackslashPattern(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, ".agents/skills/kibana/SKILL.md")

	result, err := Resolve([]string{`~\.agents\skills\kibana\SKILL.md`})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil: a backslash is a finding, not an error", err)
	}

	if filepath.Separator == '\\' {
		// On Windows the normalization turns every backslash into a separator, so the pattern
		// is accepted and matches.
		if got := rel(t, home, result.Files); !sameNames(got, []string{".agents/skills/kibana/SKILL.md"}) {
			t.Errorf("a backslash pattern matched %v, want .agents/skills/kibana/SKILL.md", got)
		}
		if len(result.Findings) != 0 {
			t.Errorf("findings = %v, want none: the pattern was normalized", result.Findings)
		}
		return
	}

	// On the other systems ToSlash converts nothing, so the backslash survives and the matcher
	// reads it as an escape: one fail finding, whose message shows the pattern with forward
	// slashes.
	if len(result.Findings) != 1 || result.Findings[0].Level != config.LevelFail {
		t.Fatalf("findings = %v, want one fail", result.Findings)
	}
	finding := result.Findings[0]
	if strings.Contains(finding.Message, `\`) {
		t.Errorf("message %q still contains a backslash", finding.Message)
	}
	if want := home + "/.agents/skills/kibana/SKILL.md"; finding.Subject != want {
		t.Errorf("subject = %q, want %q", finding.Subject, want)
	}
}

func TestPlainPathResolvesWithOneStatAndNoWalk(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, "skills/kibana/SKILL.md")

	stats, globs := 0, 0
	statFile, globFiles = func(name string) (os.FileInfo, error) {
		stats++
		return os.Stat(name)
	}, func(pattern string, opts ...doublestar.GlobOption) ([]string, error) {
		globs++
		return doublestar.FilepathGlob(pattern, opts...)
	}
	t.Cleanup(func() {
		statFile, globFiles = os.Stat, doublestar.FilepathGlob
	})

	got := resolveNames(t, home, "~/skills/kibana/SKILL.md")
	if !sameNames(got, []string{"skills/kibana/SKILL.md"}) {
		t.Errorf("plain path matched %v, want skills/kibana/SKILL.md", got)
	}
	if stats != 1 {
		t.Errorf("stat calls = %d, want 1", stats)
	}
	if globs != 0 {
		t.Errorf("glob calls = %d, want 0: a plain path needs no directory walk", globs)
	}
}

func TestResolveSkipsDirectories(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, "skills/loose.md", "skills/kibana/SKILL.md")

	// A directory is not a skill file, so it is filtered out.
	if got := resolveNames(t, home, "~/skills/*"); !sameNames(got, []string{"skills/loose.md"}) {
		t.Errorf("~/skills/* matched %v, want skills/loose.md only", got)
	}
}

func TestResolveDedupesHardLinks(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, "one/SKILL.md")
	if err := os.Link(filepath.Join(home, "one", "SKILL.md"), filepath.Join(home, "two", "SKILL.md")); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}

	result, err := Resolve([]string{"~/one/SKILL.md", "~/two/SKILL.md"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	// The first spelling is the one kept.
	if got := rel(t, home, result.Files); !sameNames(got, []string{"one/SKILL.md"}) {
		t.Errorf("hard link resolved to %v, want one/SKILL.md only", got)
	}
}

func TestResolveDedupesASymlinkAndItsTarget(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, "real/SKILL.md")
	if err := os.MkdirAll(filepath.Join(home, "linked"), 0o755); err != nil {
		t.Fatalf("create the directory: %v", err)
	}
	link := filepath.Join(home, "linked", "SKILL.md")
	if err := os.Symlink(filepath.Join(home, "real", "SKILL.md"), link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	result, err := Resolve([]string{"~/real/SKILL.md", "~/linked/SKILL.md"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	// os.SameFile decides: a link and its target are one file, and the identity is the
	// target's.
	if got := rel(t, home, result.Files); !sameNames(got, []string{"real/SKILL.md"}) {
		t.Errorf("symlink pair resolved to %v, want real/SKILL.md only", got)
	}
}

func TestResolveKeepsTwoFilesWithEqualContent(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, "one/SKILL.md", "two/SKILL.md")

	result, err := Resolve([]string{"~/one/SKILL.md", "~/two/SKILL.md"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	// Equal content is not equal identity: both are targets.
	if got := rel(t, home, result.Files); !sameNames(got, []string{"one/SKILL.md", "two/SKILL.md"}) {
		t.Errorf("two files with equal content resolved to %v, want both", got)
	}
}

func TestStalePatternIsAWarnAndNotAnError(t *testing.T) {
	newHome(t)

	result, err := Resolve([]string{"~/skills/stale-skill-*/SKILL.md"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil: a stale pattern is a warning", err)
	}
	if len(result.Files) != 0 {
		t.Errorf("Files = %v, want none", result.Files)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("findings = %v, want one", result.Findings)
	}
	finding := result.Findings[0]
	if finding.Level != config.LevelWarn {
		t.Errorf("level = %v, want warn", finding.Level)
	}
	if !IsStale(finding) {
		t.Errorf("IsStale(%v) = false, want true", finding)
	}
	if !strings.HasSuffix(finding.Message, "stale") {
		t.Errorf("message = %q, want it to end with stale", finding.Message)
	}
}

func TestMalformedPatternIsAWarnAndNotAnError(t *testing.T) {
	newHome(t)

	result, err := Resolve([]string{"~/skills/[a-/SKILL.md"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil: a malformed pattern is a finding", err)
	}
	if len(result.Files) != 0 {
		t.Errorf("Files = %v, want none", result.Files)
	}
	if len(result.Findings) != 1 || result.Findings[0].Level != config.LevelWarn {
		t.Errorf("findings = %v, want one warn", result.Findings)
	}
}

func TestResolveReturnsAbsolutePaths(t *testing.T) {
	home := newHome(t)
	writeTree(t, home, "skills/SKILL.md")

	result, err := Resolve([]string{"~/skills/SKILL.md"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("Files = %v, want one", result.Files)
	}
	if !filepath.IsAbs(result.Files[0]) {
		t.Errorf("file %q is not absolute", result.Files[0])
	}
}

func TestWildcardIsCaseSensitive(t *testing.T) {
	// The tool never folds case: on Linux /home/me/Skills and /home/me/skills are different
	// directories, so a lowercase pattern must not reach a mixed-case name. The string-level
	// check holds even where the filesystem would fold it for us.
	pattern, err := Parse("~/Skills/*/SKILL.md")
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}
	matched, err := doublestar.Match(pattern.Slash, filepath.ToSlash(pattern.Raw[:len(pattern.Raw)-len("/*/SKILL.md")])+"/skills/kibana/SKILL.md")
	if err != nil {
		t.Fatalf("doublestar.Match() error = %v, want nil", err)
	}
	if matched {
		t.Error("a lowercase path segment matched a mixed-case one, so case was folded")
	}
}

func TestResolveDoesNotFoldCaseOnACaseSensitiveFilesystem(t *testing.T) {
	if filepath.Separator == '\\' {
		// The Windows filesystem folds case itself, so this assertion cannot distinguish folding
		// from the OS.
		t.Skip("the Windows filesystem is case-insensitive")
	}
	home := newHome(t)
	writeTree(t, home, "Skills/kibana/SKILL.md")

	result, err := Resolve([]string{"~/skills/*/SKILL.md"})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	if len(result.Files) != 0 {
		t.Errorf("a lowercase pattern matched %v, want nothing", result.Files)
	}
	if len(result.Findings) != 1 || result.Findings[0].Level != config.LevelWarn {
		t.Errorf("findings = %v, want one stale warning", result.Findings)
	}
}
