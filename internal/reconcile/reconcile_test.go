package reconcile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/config"
	"github.com/yantonov/skill-model-deactivator/internal/frontmatter"
)

// entry keeps the fields in print order, so a test can assert the order and not only the
// values.
type entry struct {
	level string
	msg   string
	kv    []KV
}

func (e entry) field(key string) (string, bool) {
	for _, field := range e.kv {
		if field.Key == key {
			return field.Value, true
		}
	}
	return "", false
}

func (e entry) keys() []string {
	names := make([]string, 0, len(e.kv))
	for _, field := range e.kv {
		names = append(names, field.Key)
	}
	return names
}

type recorder struct {
	lines []entry
}

func (r *recorder) Line(level, msg string, kv ...KV) {
	r.lines = append(r.lines, entry{level: level, msg: msg, kv: append([]KV(nil), kv...)})
}

func (r *recorder) atLevel(level string) []entry {
	var found []entry
	for _, line := range r.lines {
		if line.level == level {
			found = append(found, line)
		}
	}
	return found
}

func (r *recorder) summaryLine(t *testing.T) entry {
	t.Helper()
	if len(r.lines) == 0 {
		t.Fatal("the pass logged no line, want at least the summary line")
	}
	return r.lines[len(r.lines)-1]
}

// newHome points ~ at a temp directory, so a logged path is a ~ path on every machine.
func newHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// writeSkill returns the real path of the file it writes.
func writeSkill(t *testing.T, home, name, content string) string {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// skillsConfig reaches every fixture skill and nothing else.
func skillsConfig() config.Config {
	return config.Config{
		Patterns:       []string{"~/skills/*/SKILL.md"},
		Debounce:       250 * time.Millisecond,
		RescanInterval: 30 * time.Minute,
	}
}

func runPass(t *testing.T, cfg config.Config, opts Options) (Summary, []Plan, *recorder) {
	t.Helper()
	logger := &recorder{}
	if opts.Logger == nil {
		opts.Logger = logger
	}
	summary, plans, err := Pass(cfg, opts)
	if err != nil {
		t.Fatalf("Pass() error = %v, want nil", err)
	}
	return summary, plans, logger
}

func actionsOf(plans []Plan) []frontmatter.Action {
	actions := make([]frontmatter.Action, 0, len(plans))
	for _, plan := range plans {
		actions = append(actions, plan.Action)
	}
	return actions
}

func snapshot(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func tmpFiles(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(d.Name(), tmpSuffix) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return found
}

func TestPassPlansAnInsertAndWritesNothing(t *testing.T) {
	home := newHome(t)
	path := writeSkill(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\nbody\n")
	before := snapshot(t, path)

	summary, plans, logger := runPass(t, skillsConfig(), Options{DryRun: true})

	if len(plans) != 1 {
		t.Fatalf("plans = %+v, want exactly one", plans)
	}
	if plans[0].Path != path {
		t.Errorf("plan path = %q, want %q", plans[0].Path, path)
	}
	if plans[0].Action != frontmatter.ActionInsert {
		t.Errorf("plan action = %v, want %v", plans[0].Action, frontmatter.ActionInsert)
	}
	if plans[0].Reason != "absent" {
		t.Errorf("plan reason = %q, want %q", plans[0].Reason, "absent")
	}

	info := logger.atLevel(levelInfo)
	if len(info) != 2 {
		t.Fatalf("info lines = %d, want 2 (the patch and the summary): %+v", len(info), info)
	}
	patch := info[0]
	wantFields := []string{"path", "desired", "reason", "action", "result"}
	if got := patch.keys(); !equalStrings(got, wantFields) {
		t.Errorf("info field order = %v, want %v", got, wantFields)
	}
	for key, want := range map[string]string{
		"path":    "~/skills/kibana/SKILL.md",
		"desired": "true",
		"reason":  "absent",
		"action":  "inserted",
		"result":  "would-patch",
	} {
		if got, _ := patch.field(key); got != want {
			t.Errorf("info %s = %q, want %q", key, got, want)
		}
	}

	if after := snapshot(t, path); string(after) != string(before) {
		t.Errorf("the pass changed the file:\n before %q\n after  %q", before, after)
	}
	if summary.Changed != 1 || summary.Wrote != 0 || summary.Failed != 0 {
		t.Errorf("summary = %+v, want changed 1, wrote 0, failed 0", summary)
	}
}

func TestPassOnACanonicalTreeTouchesNothing(t *testing.T) {
	home := newHome(t)
	paths := []string{
		writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\ndisable-model-invocation: true\n---\n"),
		writeSkill(t, home, "skills/b/SKILL.md", "---\nname: b\ndisable-model-invocation: true\n---\n"),
	}

	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		before[path] = snapshot(t, path)
	}
	fileInfo := make(map[string]os.FileInfo, len(paths))
	dirInfo := make(map[string]os.FileInfo, len(paths))
	for _, path := range paths {
		fileInfo[path] = stat(t, path)
		dirInfo[path] = stat(t, filepath.Dir(path))
	}

	summary, plans, logger := runPass(t, skillsConfig(), Options{})

	if len(plans) != len(paths) {
		t.Fatalf("plans = %d, want %d", len(plans), len(paths))
	}
	for _, plan := range plans {
		if plan.Action != frontmatter.ActionNone {
			t.Errorf("plan for %s = %v, want %v", plan.Path, plan.Action, frontmatter.ActionNone)
		}
	}
	if info := logger.atLevel(levelInfo); len(info) != 1 {
		t.Errorf("info lines = %d, want only the summary: %+v", len(info), info)
	}
	if summary.Changed != 0 || summary.Wrote != 0 || summary.Skipped != 0 || summary.Failed != 0 {
		t.Errorf("summary = %+v, want every counter but watched at zero", summary)
	}

	for _, path := range paths {
		if after := snapshot(t, path); string(after) != string(before[path]) {
			t.Errorf("%s changed: %q, want %q", path, after, before[path])
		}
		if got := stat(t, path).ModTime(); !got.Equal(fileInfo[path].ModTime()) {
			t.Errorf("%s mtime = %v, want %v", path, got, fileInfo[path].ModTime())
		}
		dir := filepath.Dir(path)
		if got := stat(t, dir).ModTime(); !got.Equal(dirInfo[path].ModTime()) {
			t.Errorf("%s mtime = %v, want %v", dir, got, dirInfo[path].ModTime())
		}
	}
	if found := tmpFiles(t, home); len(found) != 0 {
		t.Errorf("the pass left temp files: %v", found)
	}
}

func stat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func TestPassReportsTheReason(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		action frontmatter.Action
		reason string
	}{
		{"absent", "", frontmatter.ActionInsert, "absent"},
		{"yes", "yes", frontmatter.ActionRewrite, "non-canonical"},
		{"quoted true", `"true"`, frontmatter.ActionRewrite, "non-canonical"},
		{"false", "false", frontmatter.ActionRewrite, "falsy"},
		{"off", "off", frontmatter.ActionRewrite, "falsy"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := newHome(t)
			content := "---\nname: a\n"
			if test.name != "absent" {
				content += "disable-model-invocation: " + test.value + "\n"
			}
			content += "---\n"
			writeSkill(t, home, "skills/a/SKILL.md", content)

			_, plans, _ := runPass(t, skillsConfig(), Options{})

			if len(plans) != 1 {
				t.Fatalf("plans = %+v, want exactly one", plans)
			}
			if plans[0].Action != test.action {
				t.Errorf("action = %v, want %v", plans[0].Action, test.action)
			}
			if plans[0].Reason != test.reason {
				t.Errorf("reason = %q, want %q", plans[0].Reason, test.reason)
			}
		})
	}
}

func TestPassWarnsOnFrontmatterThatDoesNotParse(t *testing.T) {
	home := newHome(t)
	writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\nname: b\n---\n")

	summary, plans, logger := runPass(t, skillsConfig(), Options{})

	if summary.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", summary.Skipped)
	}
	if summary.Changed != 0 || summary.Failed != 0 {
		t.Errorf("summary = %+v, want changed 0 and failed 0", summary)
	}
	warns := logger.atLevel(levelWarn)
	if len(warns) != 1 {
		t.Fatalf("warn lines = %d, want 1: %+v", len(warns), warns)
	}
	if !strings.Contains(warns[0].msg, "does not parse") {
		t.Errorf("warn message = %q, want it to name the parse failure", warns[0].msg)
	}
	for _, plan := range plans {
		if plan.Action == frontmatter.ActionWarn {
			t.Errorf("plan for %s = %v, want no plan for a skipped file", plan.Path, plan.Action)
		}
	}
	if info := logger.atLevel(levelInfo); len(info) != 1 {
		t.Errorf("info lines = %d, want only the summary: %+v", len(info), info)
	}
}

func TestPassWarnsOnAStalePatternAndFinishes(t *testing.T) {
	home := newHome(t)
	writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")

	cfg := skillsConfig()
	cfg.Patterns = append(cfg.Patterns, "~/skills/missing-*/SKILL.md")

	summary, plans, logger := runPass(t, cfg, Options{})

	if len(plans) != 1 {
		t.Fatalf("plans = %+v, want exactly the one resolved file", plans)
	}
	warns := logger.atLevel(levelWarn)
	if len(warns) != 1 {
		t.Fatalf("warn lines = %d, want 1 for the stale pattern: %+v", len(warns), warns)
	}
	if !strings.Contains(warns[0].msg, "stale") {
		t.Errorf("warn message = %q, want it to say stale", warns[0].msg)
	}
	if summary.Watched != 1 {
		t.Errorf("watched = %d, want 1", summary.Watched)
	}
}

func TestPassCountsWatchedAsTheResolvedFileCount(t *testing.T) {
	home := newHome(t)
	writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")
	writeSkill(t, home, "skills/b/SKILL.md", "---\nname: b\ndisable-model-invocation: true\n---\n")
	writeSkill(t, home, "skills/c/SKILL.md", "---\nname: c\nname: d\n---\n")

	summary, plans, _ := runPass(t, skillsConfig(), Options{})

	if summary.Watched != 3 {
		t.Errorf("watched = %d, want the 3 resolved files", summary.Watched)
	}
	if len(plans) != 2 {
		t.Errorf("plans = %d, want one per file that has a plan: the unparseable file has none", len(plans))
	}
}

func TestPassSkipsATempFileAndStillCountsItAsWatched(t *testing.T) {
	home := newHome(t)
	writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")
	// A pattern that ends in a bare * matches a stray temp file, so the guard has to hold even
	// though a real pattern never reaches one.
	tempPath := writeSkill(t, home, "skills/a/.SKILL.md.1-1.smd-tmp", "not a skill\n")

	cfg := config.Config{Patterns: []string{"~/skills/*/*"}}
	summary, plans, _ := runPass(t, cfg, Options{})

	if summary.Watched != 2 {
		t.Errorf("watched = %d, want 2 resolved files", summary.Watched)
	}
	for _, plan := range plans {
		if plan.Path == tempPath {
			t.Errorf("the pass planned the temp file %s", tempPath)
		}
	}
}

func TestPassReportsAReadFailureAndPlansTheRest(t *testing.T) {
	home := newHome(t)
	broken := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")
	writeSkill(t, home, "skills/b/SKILL.md", "---\nname: b\n---\n")

	// The pass reads through the package hook, so the fixture works on every platform.
	failRead(t, broken)

	summary, plans, logger := runPass(t, skillsConfig(), Options{})

	if summary.Failed != 1 {
		t.Errorf("failed = %d, want 1", summary.Failed)
	}
	if summary.Changed != 1 {
		t.Errorf("changed = %d, want 1: the other file is still planned", summary.Changed)
	}
	if len(plans) != 1 || plans[0].Path == broken {
		t.Errorf("plans = %+v, want the readable file only", plans)
	}
	errsAt := logger.atLevel(levelError)
	if len(errsAt) != 1 {
		t.Fatalf("error lines = %d, want 1: %+v", len(errsAt), errsAt)
	}
	if value, _ := errsAt[0].field("path"); value != "~/skills/a/SKILL.md" {
		t.Errorf("error path = %q, want %q", value, "~/skills/a/SKILL.md")
	}
	if _, ok := errsAt[0].field("error"); !ok {
		t.Errorf("error line has no error field: %+v", errsAt[0])
	}
}

// failRead exercises the failed-file branch on every platform: a permission-based fixture
// cannot be written on Windows with os.Chmod, and a test that runs as root ignores the mode on
// Unix.
func failRead(t *testing.T, path string) {
	t.Helper()
	previous := readFile
	readFile = func(name string) ([]byte, error) {
		if name == path {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
		return previous(name)
	}
	t.Cleanup(func() { readFile = previous })
}

func TestPassReportsAReadFailureOnAPermissionFixture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod on Windows only toggles the read-only bit, so a file stays readable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file, so the fixture cannot fail the read")
	}
	home := newHome(t)
	path := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	summary, _, logger := runPass(t, skillsConfig(), Options{})

	if summary.Failed != 1 {
		t.Errorf("failed = %d, want 1", summary.Failed)
	}
	if errsAt := logger.atLevel(levelError); len(errsAt) != 1 {
		t.Errorf("error lines = %d, want 1", len(errsAt))
	}
}

func TestPassCallsWriteAndCountsTheWrite(t *testing.T) {
	home := newHome(t)
	path := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\ndisable-model-invocation: yes\n---\n")

	var wrote []string
	var planned []frontmatter.Plan
	write := func(gotPath string, plan frontmatter.Plan) error {
		wrote = append(wrote, gotPath)
		planned = append(planned, plan)
		return nil
	}

	summary, _, logger := runPass(t, skillsConfig(), Options{Write: write})

	if len(wrote) != 1 || wrote[0] != path {
		t.Fatalf("wrote = %v, want %q", wrote, path)
	}
	if planned[0].Action != frontmatter.ActionRewrite {
		t.Errorf("plan action = %v, want %v", planned[0].Action, frontmatter.ActionRewrite)
	}
	if summary.Wrote != 1 || summary.Changed != 1 || summary.Failed != 0 {
		t.Errorf("summary = %+v, want changed 1, wrote 1, failed 0", summary)
	}
	info := logger.atLevel(levelInfo)
	if len(info) != 2 {
		t.Fatalf("info lines = %d, want the write and the summary: %+v", len(info), info)
	}
	if got, _ := info[0].field("result"); got != "ok" {
		t.Errorf("result = %q, want %q", got, "ok")
	}
}

func TestPassReportsAFailedWrite(t *testing.T) {
	home := newHome(t)
	path := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")

	write := func(string, frontmatter.Plan) error { return errors.New("permission denied") }

	summary, _, logger := runPass(t, skillsConfig(), Options{Write: write})

	if summary.Failed != 1 || summary.Wrote != 0 {
		t.Errorf("summary = %+v, want failed 1 and wrote 0", summary)
	}
	errsAt := logger.atLevel(levelError)
	if len(errsAt) != 1 {
		t.Fatalf("error lines = %d, want 1: %+v", len(errsAt), errsAt)
	}
	if got, _ := errsAt[0].field("path"); got != "~/skills/a/SKILL.md" {
		t.Errorf("error path = %q, want %q", got, "~/skills/a/SKILL.md")
	}
	if got, _ := errsAt[0].field("error"); got != "permission denied" {
		t.Errorf("error value = %q, want %q", got, "permission denied")
	}
	if string(snapshot(t, path)) != "---\nname: a\n---\n" {
		t.Errorf("%s changed, want it untouched", path)
	}
}

// TestPassDryRunNeverCallsWrite: a dry run with a writer passed in still writes nothing, so the
// safety does not depend on the caller.
func TestPassDryRunNeverCallsWrite(t *testing.T) {
	home := newHome(t)
	path := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")
	before := snapshot(t, path)

	called := false
	write := func(string, frontmatter.Plan) error {
		called = true
		return nil
	}
	summary, _, logger := runPass(t, skillsConfig(), Options{DryRun: true, Write: write})

	if called {
		t.Errorf("the pass called Write on a dry run")
	}
	if summary.Wrote != 0 || summary.Changed != 1 {
		t.Errorf("summary = %+v, want changed 1 and wrote 0", summary)
	}
	info := logger.atLevel(levelInfo)
	if len(info) != 2 {
		t.Fatalf("info lines = %d, want the plan and the summary: %+v", len(info), info)
	}
	if got, _ := info[0].field("result"); got != "would-patch" {
		t.Errorf("result = %q, want %q", got, "would-patch")
	}
	if after := snapshot(t, path); string(after) != string(before) {
		t.Errorf("the dry run changed the file: %q, want %q", after, before)
	}
}

// TestPassAppliesThroughTheRealWriter: a pass with Write set to this package's Write applies the
// plan and counts the file as wrote.
func TestPassAppliesThroughTheRealWriter(t *testing.T) {
	home := newHome(t)
	path := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")

	summary, _, logger := runPass(t, skillsConfig(), Options{Write: Write})

	if summary.Wrote != 1 || summary.Failed != 0 {
		t.Errorf("summary = %+v, want wrote 1 and failed 0", summary)
	}
	want := "---\nname: a\ndisable-model-invocation: true\n---\n"
	if got := string(snapshot(t, path)); got != want {
		t.Errorf("the file is %q, want %q", got, want)
	}
	for _, line := range logger.atLevel(levelError) {
		t.Errorf("error line = %+v, want none", line)
	}
	if found := tmpFiles(t, home); len(found) != 0 {
		t.Errorf("the pass left temp files: %v", found)
	}
}

// TestPassIsIdempotentWithTheRealWriter: the first pass writes, and the second plans ActionNone
// for every file and writes nothing.
func TestPassIsIdempotentWithTheRealWriter(t *testing.T) {
	home := newHome(t)
	paths := []string{
		writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n"),
		writeSkill(t, home, "skills/b/SKILL.md", "---\nname: b\ndisable-model-invocation: on\n---\n"),
	}

	first, _, _ := runPass(t, skillsConfig(), Options{Write: Write})
	if first.Wrote != 2 {
		t.Fatalf("first pass wrote = %d, want 2", first.Wrote)
	}
	applied := make(map[string][]byte, len(paths))
	for _, path := range paths {
		applied[path] = snapshot(t, path)
	}

	second, plans, logger := runPass(t, skillsConfig(), Options{Write: Write})

	if second.Wrote != 0 || second.Changed != 0 || second.Failed != 0 {
		t.Errorf("second pass summary = %+v, want changed 0, wrote 0, failed 0", second)
	}
	if len(plans) != len(paths) {
		t.Fatalf("second pass plans = %+v, want one per file", plans)
	}
	for _, plan := range plans {
		if plan.Action != frontmatter.ActionNone {
			t.Errorf("plan for %s = %v, want %v", plan.Path, plan.Action, frontmatter.ActionNone)
		}
	}
	for _, line := range logger.atLevel(levelError) {
		t.Errorf("error line = %+v, want none", line)
	}
	for _, path := range paths {
		if got := snapshot(t, path); string(got) != string(applied[path]) {
			t.Errorf("the second pass changed %s: %q, want %q", path, got, applied[path])
		}
	}
}

// TestPassOnACanonicalReadOnlyFileIsQuiet: a read-only file that already holds the desired
// state is never tested for writability, so it produces no error and no warn.
func TestPassOnACanonicalReadOnlyFileIsQuiet(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits, so the fixture would not prove anything")
	}
	home := newHome(t)
	path := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\ndisable-model-invocation: true\n---\n")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })
	before := stat(t, path)

	summary, plans, logger := runPass(t, skillsConfig(), Options{Write: Write})

	if summary.Failed != 0 || summary.Wrote != 0 {
		t.Errorf("summary = %+v, want failed 0 and wrote 0", summary)
	}
	if len(plans) != 1 || plans[0].Action != frontmatter.ActionNone {
		t.Errorf("plans = %+v, want the one ActionNone plan", plans)
	}
	if errs := logger.atLevel(levelError); len(errs) != 0 {
		t.Errorf("error lines = %+v, want none", errs)
	}
	if warns := logger.atLevel(levelWarn); len(warns) != 0 {
		t.Errorf("warn lines = %+v, want none", warns)
	}
	if after := stat(t, path); !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("mtime = %v, want %v", after.ModTime(), before.ModTime())
	}
}

func TestPassReturnsAResolverErrorAsThePassError(t *testing.T) {
	// A home directory that cannot be resolved makes ~ expansion fail, which stops the pass
	// before it reads anything.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	logger := &recorder{}
	summary, plans, err := Pass(skillsConfig(), Options{Logger: logger})
	if err == nil {
		t.Fatalf("Pass() error = nil, want the resolver error")
	}
	if len(plans) != 0 {
		t.Errorf("plans = %+v, want none", plans)
	}
	if summary != (Summary{}) {
		t.Errorf("summary = %+v, want the zero value: the pass earned no counts", summary)
	}
	if len(logger.lines) != 0 {
		t.Errorf("lines = %+v, want none: the caller prints the error instead", logger.lines)
	}
}

func TestLogFindingUsesTheLevelOfTheFinding(t *testing.T) {
	tests := []struct {
		name  string
		level config.Level
		want  string
	}{
		{"stale pattern is a warning", config.LevelWarn, levelWarn},
		{"fail finding is an error", config.LevelFail, levelError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := &recorder{}
			logFinding(logger, config.Finding{
				Level:   test.level,
				Subject: "~/skills/x-*/SKILL.md",
				Message: "pattern matches nothing: stale",
			})

			if len(logger.lines) != 1 {
				t.Fatalf("lines = %+v, want exactly one", logger.lines)
			}
			if got := logger.lines[0].level; got != test.want {
				t.Errorf("level = %q, want %q", got, test.want)
			}
			if got, _ := logger.lines[0].field("pattern"); got != "~/skills/x-*/SKILL.md" {
				t.Errorf("pattern = %q, want the finding subject", got)
			}
		})
	}
}

func TestSummaryLineShape(t *testing.T) {
	home := newHome(t)
	writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")
	writeSkill(t, home, "skills/b/SKILL.md", "---\nname: b\ndisable-model-invocation: true\n---\n")
	writeSkill(t, home, "skills/c/SKILL.md", "---\nname: c\nname: d\n---\n")

	summary, _, logger := runPass(t, skillsConfig(), Options{})

	line := logger.summaryLine(t)
	if line.level != levelInfo {
		t.Errorf("summary level = %q, want %q", line.level, levelInfo)
	}
	wantFields := []string{"watched", "changed", "wrote", "skipped", "failed"}
	if got := line.keys(); !equalStrings(got, wantFields) {
		t.Errorf("summary field order = %v, want %v", got, wantFields)
	}
	if !strings.HasPrefix(line.msg, "reconcile finished duration=") {
		t.Errorf("summary message = %q, want it to start with the fixed text and the duration", line.msg)
	}
	for key, want := range map[string]string{
		"watched": "3",
		"changed": "1",
		"wrote":   "0",
		"skipped": "1",
		"failed":  "0",
	} {
		if got, _ := line.field(key); got != want {
			t.Errorf("summary %s = %q, want %q", key, got, want)
		}
	}
	if summary.Duration < 0 {
		t.Errorf("duration = %v, want a value that is not negative", summary.Duration)
	}
}

func TestDisplayPathAbbreviatesTheHomeDirectory(t *testing.T) {
	home := newHome(t)

	tests := []struct {
		name string
		path string
		want string
	}{
		{"under home", filepath.Join(home, "skills", "a", "SKILL.md"), "~/skills/a/SKILL.md"},
		{"home itself", home, "~"},
		{"outside home", filepath.Join(filepath.Dir(home), "elsewhere", "SKILL.md"),
			filepath.ToSlash(filepath.Join(filepath.Dir(home), "elsewhere", "SKILL.md"))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := displayPath(test.path); got != test.want {
				t.Errorf("displayPath(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestDisplayPathNeverReachesThePlanner(t *testing.T) {
	home := newHome(t)
	path := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")

	var got string
	write := func(gotPath string, _ frontmatter.Plan) error {
		got = gotPath
		return nil
	}
	if _, _, err := Pass(skillsConfig(), Options{Write: write}); err != nil {
		t.Fatalf("Pass() error = %v, want nil", err)
	}
	if got != path {
		t.Errorf("the writer saw %q, want the real path %q", got, path)
	}
}

func equalStrings(got, want []string) bool {
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

// TestPassTakesThePerFileLockForEveryFile: a second goroutine that holds the lock of one path
// makes the pass wait for that path and not for the others.
func TestPassTakesThePerFileLockForEveryFile(t *testing.T) {
	home := newHome(t)
	locked := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\ndisable-model-invocation: true\n---\n")
	writeSkill(t, home, "skills/b/SKILL.md", "---\nname: b\ndisable-model-invocation: true\n---\n")

	locks := &FileLocks{}
	// The wait is what proves the pass took the lock: an unlocked pass would finish at once.
	unlock := locks.Lock(locked)
	done := make(chan Summary, 1)
	go func() {
		summary, _, err := Pass(skillsConfig(), Options{Locks: locks, Write: Write})
		if err != nil {
			t.Errorf("Pass() error = %v, want nil", err)
		}
		done <- summary
	}()

	select {
	case summary := <-done:
		unlock()
		t.Fatalf("the pass finished with %+v while a path was locked", summary)
	case <-time.After(100 * time.Millisecond):
	}

	unlock()
	select {
	case summary := <-done:
		if summary.Watched != 2 {
			t.Errorf("summary = %+v, want watched 2", summary)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pass did not finish after the per-file lock was released")
	}
}
