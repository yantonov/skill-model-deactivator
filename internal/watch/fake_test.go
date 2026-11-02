package watch

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/yantonov/skill-model-deactivator/internal/config"
)

// fakeWatcher lets a test drive the debounce without depending on how many events the
// filesystem delivers for one save.
type fakeWatcher struct {
	mu     sync.Mutex
	counts map[string]int // adds per directory
	order  []string
	events chan fsnotify.Event
	errs   chan error
}

func (f *fakeWatcher) Add(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts[dir] == 0 {
		f.order = append(f.order, dir)
	}
	f.counts[dir]++
	return nil
}

// Close does nothing: the run goroutine stops on its own done channel.
func (f *fakeWatcher) Close() error { return nil }

func (f *fakeWatcher) Events() <-chan fsnotify.Event { return f.events }

func (f *fakeWatcher) Errors() <-chan error { return f.errs }

func (f *fakeWatcher) added(dir string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[dir]
}

// watched is in the order the directories were first added.
func (f *fakeWatcher) watched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

// startFake uses the real walk: the roots and the names come from the patterns.
func startFake(t *testing.T, cfg config.Config) (*Watcher, *fakeWatcher) {
	t.Helper()
	fake := &fakeWatcher{
		counts: make(map[string]int),
		events: make(chan fsnotify.Event),
		errs:   make(chan error),
	}
	roots, names, err := targets(cfg.Patterns)
	if err != nil {
		t.Fatalf("targets() error = %v", err)
	}
	w := newWatcher(cfg, &recorder{}, fake, roots, names)
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return w, fake
}

// TestABurstOfWritesGivesOneTrigger: three writes in one window give one trigger, and a
// write after the window gives a second.
func TestABurstOfWritesGivesOneTrigger(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	path := filepath.Join(home, "skills", "kibana", "SKILL.md")
	writeSkill(t, path, "---\nname: kibana\n---\n")

	w, fake := startFake(t, cfg)

	for i := 0; i < 3; i++ {
		fake.events <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerChanged {
		t.Errorf("trigger = %v, want %v", trigger, TriggerChanged)
	}
	expectNoTrigger(t, w, 3*debounce)

	fake.events <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerChanged {
		t.Errorf("trigger after the window = %v, want %v", trigger, TriggerChanged)
	}
}

// TestCreateWriteRemoveAndRenameAllQualify: all four kinds are treated the same.
func TestCreateWriteRemoveAndRenameAllQualify(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	path := filepath.Join(home, "skills", "kibana", "SKILL.md")
	writeSkill(t, path, "---\nname: kibana\n---\n")

	for _, op := range []fsnotify.Op{fsnotify.Create, fsnotify.Write, fsnotify.Remove, fsnotify.Rename} {
		w, fake := startFake(t, cfg)
		fake.events <- fsnotify.Event{Name: path, Op: op}
		if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerChanged {
			t.Errorf("op %v gave trigger = %v, want %v", op, trigger, TriggerChanged)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}
}

// TestWatcherErrorGivesOneErrorTrigger: an error is a full reconcile, delivered as a trigger
// and never as an error return.
func TestWatcherErrorGivesOneErrorTrigger(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	writeSkill(t, filepath.Join(home, "skills", "kibana", "SKILL.md"), "---\nname: kibana\n---\n")

	w, fake := startFake(t, cfg)

	fake.errs <- errors.New("the queue overflowed")
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerError {
		t.Errorf("trigger = %v, want %v", trigger, TriggerError)
	}
	expectNoTrigger(t, w, 3*debounce)
}

// TestABurstOfErrorsGivesOneTrigger: three errors in one window are one follow-up pass.
func TestABurstOfErrorsGivesOneTrigger(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	writeSkill(t, filepath.Join(home, "skills", "kibana", "SKILL.md"), "---\nname: kibana\n---\n")

	w, fake := startFake(t, cfg)

	for i := 0; i < 3; i++ {
		fake.errs <- errors.New("the queue overflowed")
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerError {
		t.Errorf("trigger = %v, want %v", trigger, TriggerError)
	}
	expectNoTrigger(t, w, 3*debounce)
}

// TestCreatedDirectoryIsWalkedImmediately: a directory created under a root gets a watcher in
// the same window, so a file written into it at once is seen without waiting for a rescan.
func TestCreatedDirectoryIsWalkedImmediately(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	root := filepath.Join(home, "skills")
	writeSkill(t, filepath.Join(root, "kibana", "SKILL.md"), "---\nname: kibana\n---\n")

	w, fake := startFake(t, cfg)

	// The directory exists before its event arrives, which is what the create event means.
	newDir := filepath.Join(root, "created")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", newDir, err)
	}
	fake.events <- fsnotify.Event{Name: newDir, Op: fsnotify.Create}
	waitTrigger(t, w, acceptLimit)

	if fake.added(newDir) != 1 {
		t.Errorf("watched(%q) = %d, want 1; watched = %v", newDir, fake.added(newDir), fake.watched())
	}
}

// TestRemovedDirectoryIsForgotten: a directory re-created at the same path is walked again.
func TestRemovedDirectoryIsForgotten(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	root := filepath.Join(home, "skills")
	gone := filepath.Join(root, "kibana")
	writeSkill(t, filepath.Join(gone, "SKILL.md"), "---\nname: kibana\n---\n")

	w, fake := startFake(t, cfg)

	fake.events <- fsnotify.Event{Name: gone, Op: fsnotify.Remove}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerRescan {
		t.Errorf("trigger = %v, want %v for a removed directory", trigger, TriggerRescan)
	}

	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", gone, err)
	}
	fake.events <- fsnotify.Event{Name: gone, Op: fsnotify.Create}
	waitTrigger(t, w, acceptLimit)
	if fake.added(gone) != 2 {
		t.Errorf("watched(%q) = %d, want 2: the record must be dropped when the directory leaves", gone, fake.added(gone))
	}
}

// TestRemovedUnrelatedFileGivesNoTrigger: a file no pattern accepts is not a change of the
// file set.
func TestRemovedUnrelatedFileGivesNoTrigger(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	root := filepath.Join(home, "skills")
	writeSkill(t, filepath.Join(root, "kibana", "SKILL.md"), "---\nname: kibana\n---\n")

	w, fake := startFake(t, cfg)

	fake.events <- fsnotify.Event{Name: filepath.Join(root, "gone.txt"), Op: fsnotify.Remove}
	expectNoTrigger(t, w, 3*debounce)
}

// TestRefreshAddsOnlyMissingDirectories: Refresh adds a watcher for a directory that appeared
// after the walk and leaves the ones it already watches alone.
func TestRefreshAddsOnlyMissingDirectories(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	root := filepath.Join(home, "skills")
	writeSkill(t, filepath.Join(root, "kibana", "SKILL.md"), "---\nname: kibana\n---\n")

	w, fake := startFake(t, cfg)

	added := filepath.Join(root, "added")
	writeSkill(t, filepath.Join(added, "SKILL.md"), "---\nname: added\n---\n")
	if err := w.Refresh(); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerRescan {
		t.Errorf("trigger = %v, want %v", trigger, TriggerRescan)
	}

	if fake.added(added) != 1 {
		t.Errorf("watched(%q) = %d, want 1", added, fake.added(added))
	}
	if fake.added(root) != 1 {
		t.Errorf("watched(%q) = %d, want 1: a directory that is already watched must not be added twice", root, fake.added(root))
	}

	// A refresh that finds nothing new asks for no trigger.
	expectNoTrigger(t, w, 3*debounce)
	if err := w.Refresh(); err != nil {
		t.Fatalf("second Refresh() error = %v", err)
	}
	expectNoTrigger(t, w, 3*debounce)
}

// TestCreatedAncestorOfAMissingRootReconcilesImmediately: the create that brings the root back
// is seen, so the pass runs without waiting for the rescan.
func TestCreatedAncestorOfAMissingRootReconcilesImmediately(t *testing.T) {
	home := newHome(t)
	cfg := skillsConfig(home)
	root := filepath.Join(home, "skills")

	w, fake := startFake(t, cfg)

	if fake.added(home) != 1 {
		t.Fatalf("watched(%q) = %d, want 1: the nearest existing ancestor of a missing root must be watched", home, fake.added(home))
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", root, err)
	}
	fake.events <- fsnotify.Event{Name: root, Op: fsnotify.Create}

	if trigger := waitTrigger(t, w, acceptLimit); trigger != TriggerRescan {
		t.Errorf("trigger = %v, want %v for the root that appeared", trigger, TriggerRescan)
	}
	if fake.added(root) != 1 {
		t.Errorf("watched(%q) = %d, want 1 after the root appeared", root, fake.added(root))
	}
}
