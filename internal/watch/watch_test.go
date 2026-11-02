package watch

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/config"
	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
)

// debounce is short enough to keep a test fast and long enough that three writes in a row land
// inside one window on a loaded machine.
const debounce = 200 * time.Millisecond

const acceptLimit = time.Second

type entry struct {
	level string
	msg   string
}

type recorder struct {
	mu      sync.Mutex
	entries []entry
}

func (r *recorder) Line(level, msg string, _ ...reconcile.KV) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry{level: level, msg: msg})
}

func (r *recorder) count(level, msg string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, e := range r.entries {
		if e.level == level && e.msg == msg {
			total++
		}
	}
	return total
}

// newHome resolves symlinks, because a temporary directory on macOS lives under /var, which is
// a link to /private/var.
func newHome(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	return dir
}

// skillsConfig is one glob that reaches one skill level under skills.
func skillsConfig(home string) config.Config {
	return config.Config{
		Patterns: []string{filepath.Join(home, "skills", "*", "SKILL.md")},
		Debounce: debounce,
	}
}

func writeSkill(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func startWatcher(t *testing.T, cfg config.Config) (*Watcher, *recorder) {
	t.Helper()
	log := &recorder{}
	w, err := New(cfg, log)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return w, log
}

// waitTrigger turns a missing event into a failure instead of a hang.
func waitTrigger(t *testing.T, w *Watcher, timeout time.Duration) Trigger {
	t.Helper()
	select {
	case trigger, ok := <-w.Triggers():
		if !ok {
			t.Fatalf("the trigger channel is closed, want a trigger")
		}
		return trigger
	case <-time.After(timeout):
		t.Fatalf("no trigger within %s", timeout)
		return TriggerChanged
	}
}

func expectNoTrigger(t *testing.T, w *Watcher, window time.Duration) {
	t.Helper()
	select {
	case trigger, ok := <-w.Triggers():
		if ok {
			t.Fatalf("trigger = %v, want none within %s", trigger, window)
		}
	case <-time.After(window):
	}
}

// TestWriteToAMatchedFileGivesATrigger: a write to a matched SKILL.md gives a trigger within
// 1 s.
func TestWriteToAMatchedFileGivesATrigger(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	path := filepath.Join(home, "skills", "kibana", "SKILL.md")
	writeSkill(t, path, "---\nname: kibana\n---\n")

	w, _ := startWatcher(t, cfg)

	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\nx\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerChanged {
		t.Errorf("trigger = %v, want %v", trigger, TriggerChanged)
	}
}

// TestDirectoryCreatedAfterStartIsPickedUpThroughRefresh: a skill directory created after
// start gives a trigger within 1 s through Refresh.
func TestDirectoryCreatedAfterStartIsPickedUpThroughRefresh(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)

	w, _ := startWatcher(t, cfg)

	path := filepath.Join(home, "skills", "kibana", "SKILL.md")
	writeSkill(t, path, "---\nname: kibana\n---\n")
	if err := w.Refresh(); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerRescan {
		t.Errorf("trigger = %v, want %v", trigger, TriggerRescan)
	}

	// The refresh must leave a working watcher, so the next write is seen too.
	drain(t, w, 2*debounce)
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\ny\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerChanged {
		t.Errorf("trigger after Refresh = %v, want %v", trigger, TriggerChanged)
	}
}

// drain empties the channel, so a test can assert on a quiet channel afterwards.
func drain(t *testing.T, w *Watcher, window time.Duration) {
	t.Helper()
	deadline := time.After(window)
	for {
		select {
		case <-w.Triggers():
		case <-deadline:
			return
		}
	}
}

// TestRenameIntoAndOutOfPlaceGivesATrigger: both a create and a rename qualify.
func TestRenameIntoAndOutOfPlaceGivesATrigger(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	outside := filepath.Join(home, "incoming")
	path := filepath.Join(home, "skills", "kibana", "SKILL.md")
	writeSkill(t, filepath.Join(outside, "SKILL.md"), "---\nname: kibana\n---\n")
	writeSkill(t, filepath.Join(home, "skills", "other", "SKILL.md"), "---\nname: other\n---\n")
	// The target directory exists without the file, so the first rename creates it.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}

	w, _ := startWatcher(t, cfg)

	if err := os.Rename(filepath.Join(outside, "SKILL.md"), path); err != nil {
		t.Fatalf("Rename() into place error = %v", err)
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerChanged {
		t.Errorf("trigger = %v, want %v for a rename into place", trigger, TriggerChanged)
	}

	drain(t, w, 2*debounce)
	if err := os.Rename(path, filepath.Join(outside, "SKILL.md")); err != nil {
		t.Fatalf("Rename() out of place error = %v", err)
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerChanged {
		t.Errorf("trigger = %v, want %v for a rename out of place", trigger, TriggerChanged)
	}
}

// TestUnrelatedFileGivesNoTrigger: a file next to a matched one that no pattern accepts is
// ignored.
func TestUnrelatedFileGivesNoTrigger(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	writeSkill(t, filepath.Join(home, "skills", "kibana", "SKILL.md"), "---\nname: kibana\n---\n")

	w, _ := startWatcher(t, cfg)

	notes := filepath.Join(home, "skills", "kibana", "notes.txt")
	if err := os.WriteFile(notes, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(notes, []byte("hello again\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	expectNoTrigger(t, w, 3*debounce)
}

// TestThreeRapidWritesGiveOneTrigger: fsnotify sends a burst for one save, and the window
// turns it into one pass. The count is asserted with a timeout, so a leaked trigger is a
// failure and not a hang.
func TestThreeRapidWritesGiveOneTrigger(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	path := filepath.Join(home, "skills", "kibana", "SKILL.md")
	writeSkill(t, path, "---\nname: kibana\n---\n")

	w, _ := startWatcher(t, cfg)

	for i := 0; i < 3; i++ {
		if err := os.WriteFile(path, []byte("---\nname: kibana\n---\n"+string(rune('a'+i))), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitTrigger(t, w, acceptLimit)
	expectNoTrigger(t, w, 3*debounce)
}

// TestDoubleStarPatternWatchesANestedDirectory: `**` reaches a directory three levels under
// the root.
func TestDoubleStarPatternWatchesANestedDirectory(t *testing.T) {
	home := newHome(t)
	cfg := config.Config{
		Patterns: []string{filepath.Join(home, "skills", "**", "SKILL.md")},
		Debounce: debounce,
	}

	// Build the tree before the watcher starts, so the walk is what covers the depth.
	nested := filepath.Join(home, "skills", "synced", "8f14e45f", "kibana")
	path := filepath.Join(nested, "SKILL.md")
	writeSkill(t, path, "---\nname: kibana\n---\n")

	w, _ := startWatcher(t, cfg)

	// A write reaches the watcher only if the walk put one on the nested directory, so the
	// trigger is the assertion on the depth.
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\nz\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerChanged {
		t.Errorf("trigger = %v, want %v", trigger, TriggerChanged)
	}
}

// TestMissingRootWarnsOnceAndRefreshRecovers: one warn, no panic, and a retry at each rescan
// that adds the watcher once the directory exists.
func TestMissingRootWarnsOnceAndRefreshRecovers(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	root := filepath.Join(home, "skills")

	w, log := startWatcher(t, cfg)

	if warns := log.count(levelWarn, "watch root does not exist"); warns != 1 {
		t.Fatalf("warn lines = %d, want 1 for a missing root", warns)
	}
	// A failing retry logs no second warn.
	if err := w.Refresh(); err != nil {
		t.Fatalf("Refresh() on a missing root error = %v", err)
	}
	if warns := log.count(levelWarn, "watch root does not exist"); warns != 1 {
		t.Errorf("warn lines after a retry = %d, want 1", warns)
	}

	path := filepath.Join(root, "kibana", "SKILL.md")
	writeSkill(t, path, "---\nname: kibana\n---\n")
	if err := w.Refresh(); err != nil {
		t.Fatalf("Refresh() after the root appeared error = %v", err)
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerRescan {
		t.Errorf("trigger = %v, want %v for the refresh", trigger, TriggerRescan)
	}

	// The retry added a watcher on the root, so a write through it reaches the test.
	drain(t, w, 2*debounce)
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\nw\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerChanged {
		t.Errorf("trigger after the root appeared = %v, want %v", trigger, TriggerChanged)
	}
}

// TestCloseClosesTheTriggerChannel also covers the idempotence of Close.
func TestCloseClosesTheTriggerChannel(t *testing.T) {
	home := newHome(t)
	writeSkill(t, filepath.Join(home, "skills", "kibana", "SKILL.md"), "---\nname: kibana\n---\n")

	w, _ := startWatcher(t, skillsConfig(home))
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	select {
	case _, ok := <-w.Triggers():
		if ok {
			t.Error("a trigger arrived after Close, want the closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("the trigger channel is still open after Close")
	}
	// Refresh after Close must not hang or panic.
	if err := w.Refresh(); err != nil {
		t.Errorf("Refresh() after Close error = %v", err)
	}
}

// TestTargetsDedupesRootsAndDerivesNames: one watcher per directory, and the accepted names
// come from the patterns.
func TestTargetsDedupesRootsAndDerivesNames(t *testing.T) {
	home := newHome(t)
	patterns := []string{
		filepath.Join(home, "skills", "*", "SKILL.md"),
		filepath.Join(home, "skills", "**", "SKILL.md"),
		filepath.Join(home, "skills", "kibana", "SKILL.md"),
		filepath.Join(home, "other", "prefix-*.md"),
	}

	roots, names, err := targets(patterns)
	if err != nil {
		t.Fatalf("targets() error = %v", err)
	}

	wantRoots := []string{filepath.Join(home, "skills"), filepath.Join(home, "other")}
	if len(roots) != len(wantRoots) {
		t.Fatalf("roots = %v, want %d entries, want %v", roots, len(wantRoots), wantRoots)
	}
	for i, want := range wantRoots {
		if roots[i] != want {
			t.Errorf("roots[%d] = %q, want %q", i, roots[i], want)
		}
	}

	wantNames := []string{"SKILL.md", "prefix-*.md"}
	if len(names) != len(wantNames) {
		t.Fatalf("names = %v, want %v", names, wantNames)
	}
	for i, want := range wantNames {
		if names[i] != want {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want)
		}
	}
}

// TestAcceptUsesThePatternFileName: the filter is the pattern's last segment, literal or glob,
// so the name SKILL.md is never hardcoded.
func TestAcceptUsesThePatternFileName(t *testing.T) {
	w := &Watcher{names: []string{"SKILL.md", "prefix-*.md"}}
	cases := []struct {
		base string
		want bool
	}{
		{"SKILL.md", true},
		{"SKILL.md.bak.2026-10-26_17-17-25", false},
		{"prefix-kibana.md", true},
		{"kibana.md", false},
		{"notes.txt", false},
	}
	for _, c := range cases {
		if got := w.accept(c.base); got != c.want {
			t.Errorf("accept(%q) = %v, want %v", c.base, got, c.want)
		}
	}
}
