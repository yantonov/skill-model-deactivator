package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

func lockPathIn(dir string) string {
	return filepath.Join(dir, platform.LockFileName)
}

// TestAcquireLockNamesTheHolder: the pid in the lock file is the holder's.
func TestAcquireLockNamesTheHolder(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() = %v, want the lock", err)
	}
	t.Cleanup(first.Release)

	_, err = AcquireLock(dir)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("second AcquireLock() = %v, want *HeldError", err)
	}
	if !held.PidKnown {
		t.Error("PidKnown = false, want the pid the holder wrote")
	}
	if held.Pid != os.Getpid() {
		t.Errorf("Pid = %d, want %d", held.Pid, os.Getpid())
	}
	if want := "already running (pid " + strconv.Itoa(os.Getpid()) + ")"; held.Error() != want {
		t.Errorf("message = %q, want %q", held.Error(), want)
	}
}

// TestReleaseLetsTheNextInstanceIn: the lock goes, the file stays, and the next acquire
// succeeds without deleting anything.
func TestReleaseLetsTheNextInstanceIn(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() = %v, want the lock", err)
	}
	first.Release()
	// A second Release is a no-op and not a surprise.
	first.Release()

	if _, err := os.Stat(lockPathIn(dir)); err != nil {
		t.Fatalf("the lock file is gone after the release: %v", err)
	}

	second, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() after the release = %v, want the lock", err)
	}
	second.Release()
	if _, err := os.Stat(lockPathIn(dir)); err != nil {
		t.Errorf("the lock file is gone after the second release: %v", err)
	}
}

// TestAStaleLockFileIsTakenOver is the reason the lock is not a bare pid file: a file left by a
// process that died holds no operating-system lock, so the next instance takes it and
// overwrites the pid that survived the crash.
func TestAStaleLockFileIsTakenOver(t *testing.T) {
	dir := t.TempDir()
	path := lockPathIn(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("\n999999\n"), 0o644); err != nil {
		t.Fatalf("write the stale lock file: %v", err)
	}

	lock, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() = %v, want the stale file to be taken over", err)
	}
	t.Cleanup(lock.Release)

	if pid, known := readPid(path); !known || pid != os.Getpid() {
		t.Errorf("the lock file holds pid %d (known %v), want %d", pid, known, os.Getpid())
	}
}

// TestAnUnreadablePidSaysOnlyAlreadyRunning: when the holder wrote no pid, the second instance
// prints "already running" with no number.
func TestAnUnreadablePidSaysOnlyAlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	path := lockPathIn(dir)
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatalf("write the lock file: %v", err)
	}
	// A holder that took the lock but wrote no pid, which a crash can leave.
	holder := flock.New(path, flock.SetFlag(os.O_CREATE|os.O_RDWR))
	locked, err := holder.TryLock()
	if err != nil || !locked {
		t.Fatalf("holding the lock: locked = %v, error = %v", locked, err)
	}
	t.Cleanup(func() { holder.Unlock() })

	_, err = AcquireLock(dir)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("AcquireLock() = %v, want *HeldError", err)
	}
	if held.PidKnown {
		t.Errorf("PidKnown = true with pid %d, want no pid", held.Pid)
	}
	if got, want := held.Error(), "already running"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// TestABrokenStateDirectoryIsNotASecondInstance: the error is not a *HeldError and it names the
// directory.
func TestABrokenStateDirectoryIsNotASecondInstance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("create the blocking file: %v", err)
	}

	_, err := AcquireLock(dir)
	if err == nil {
		t.Fatal("AcquireLock() = nil, want the broken directory reported")
	}
	var held *HeldError
	if errors.As(err, &held) {
		t.Errorf("AcquireLock() = %v, want a directory failure and not a second instance", err)
	}
	if !strings.Contains(err.Error(), "state directory") {
		t.Errorf("error = %q, want it to name the state directory", err)
	}
}

// TestTheLockFileCarriesThePidAwayFromTheLockedByte pins the layout the message depends on:
// byte 0 is the byte-range lock of gofrs/flock, and the pid starts at byte 1. Windows refuses a
// read that starts inside a locked range, so a pid at byte 0 would be unreadable by the second
// instance.
func TestTheLockFileCarriesThePidAwayFromTheLockedByte(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "lock")
	if err := createLockFile(fresh); err != nil {
		t.Fatalf("createLockFile() = %v", err)
	}
	data, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatalf("read the new lock file: %v", err)
	}
	if string(data) != "\n" {
		t.Fatalf("new lock file = %q, want a single newline", data)
	}

	dir := t.TempDir()
	lock, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock() = %v, want the lock", err)
	}
	t.Cleanup(lock.Release)

	path := lockPathIn(dir)
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open the lock file: %v", err)
	}
	defer file.Close()
	pidText := make([]byte, 32)
	n, err := file.ReadAt(pidText, pidOffset)
	if err != nil && n == 0 {
		t.Fatalf("read the pid at %d: %v", pidOffset, err)
	}
	if got, want := strings.TrimSpace(string(pidText[:n])), strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("pid at byte %d = %q, want %q", pidOffset, got, want)
	}
}
