package reconcile

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestFileLocksSerialiseTwoGoroutinesOnOnePath: two goroutines asking for one path serialise,
// and the second sees the result of the first.
func TestFileLocksSerialiseTwoGoroutinesOnOnePath(t *testing.T) {
	var locks FileLocks
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// held is closed once the first goroutine holds the lock, so the second is started only
	// while the lock is taken.
	held := make(chan struct{})
	release := make(chan struct{})
	read := make(chan string, 1)

	var first sync.WaitGroup
	first.Add(1)
	go func() {
		defer first.Done()
		unlock := locks.Lock(path)
		defer unlock()
		if err := os.WriteFile(path, []byte("after\n"), 0o644); err != nil {
			t.Errorf("WriteFile() error = %v", err)
		}
		close(held)
		<-release
	}()
	<-held

	second := make(chan struct{})
	go func() {
		defer close(second)
		unlock := locks.Lock(path)
		defer unlock()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("ReadFile() error = %v", err)
			return
		}
		read <- string(data)
	}()

	select {
	case data := <-read:
		t.Fatalf("the second goroutine read %q while the first held the lock", data)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	first.Wait()
	<-second

	select {
	case data := <-read:
		if data != "after\n" {
			t.Errorf("the second goroutine read %q, want %q", data, "after\n")
		}
	default:
		t.Fatal("the second goroutine reported no read")
	}
}

// TestFileLocksArePerPath: one path does not block another.
func TestFileLocksArePerPath(t *testing.T) {
	var locks FileLocks

	unlockFirst := locks.Lock("/skills/a/SKILL.md")
	defer unlockFirst()

	done := make(chan struct{})
	go func() {
		unlock := locks.Lock("/skills/b/SKILL.md")
		defer unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a second path is blocked by the first, so the lock is not per path")
	}
}

// TestFileLocksReleaseTheEntry: the map holds one entry per path that is held and none for a
// path that nobody holds.
func TestFileLocksReleaseTheEntry(t *testing.T) {
	var locks FileLocks
	path := "/skills/kibana/SKILL.md"

	unlock := locks.Lock(path)
	if len(locks.locks) != 1 {
		t.Fatalf("entries while held = %d, want 1", len(locks.locks))
	}
	unlock()
	if len(locks.locks) != 0 {
		t.Fatalf("entries after release = %d, want 0", len(locks.locks))
	}
}
