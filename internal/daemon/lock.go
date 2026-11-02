package daemon

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gofrs/flock"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

// pidOffset skips byte 0, which the creation newline holds. The lock of gofrs/flock is a
// byte-range lock on the first byte, and on Windows a write that starts inside the locked
// range fails even for the holder, so a pid at byte 1 is the only one another process can read
// while the holder runs.
const pidOffset = 1

// Lock is an operating-system file lock at <stateDir>/lock, held for the life of the process.
// Not a bare pid file, because the operating system releases it on exit, including after a
// crash, whereas a pid file survives a crash and then lies.
type Lock struct {
	file *flock.Flock
}

// HeldError is the second instance's answer. PidKnown is false when the holder's pid could not
// be read, and the message then names no pid.
type HeldError struct {
	Pid      int
	PidKnown bool
}

func (e *HeldError) Error() string {
	if !e.PidKnown {
		return "already running"
	}
	return fmt.Sprintf("already running (pid %d)", e.Pid)
}

// AcquireLock returns a *HeldError when another process holds the lock, which is not a
// failure; any other error means the lock could not be taken at all.
//
// The pid is written only so a second process can name the holder. Nothing reads it to decide
// whether a lock is live: the lock itself says that.
func AcquireLock(stateDir string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create the state directory %s: %w", stateDir, err)
	}
	path := filepath.Join(stateDir, platform.LockFileName)
	if err := createLockFile(path); err != nil {
		return nil, fmt.Errorf("cannot create the lock file %s: %w", path, err)
	}

	// O_RDWR and not the package's read-only default, because the holder writes its pid here.
	file := flock.New(path, flock.SetFlag(os.O_CREATE|os.O_RDWR), flock.SetPermissions(0o644))
	locked, err := file.TryLock()
	if err != nil {
		return nil, fmt.Errorf("cannot lock %s: %w", path, err)
	}
	if !locked {
		pid, known := readPid(path)
		return nil, &HeldError{Pid: pid, PidKnown: known}
	}
	if err := writePid(path); err != nil {
		_ = file.Unlock()
		return nil, fmt.Errorf("cannot write the pid into the lock file %s: %w", path, err)
	}
	return &Lock{file: file}, nil
}

// Release leaves the lock file in place: deleting it would let a racing process lock a second
// inode of the same name and defeat the whole mechanism. Nothing can be done when the release
// fails, and the process exit releases an operating-system lock anyway.
func (l *Lock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Unlock()
	l.file = nil
}

// createLockFile writes the leading newline that keeps byte 0 out of the pid, and does nothing
// when the file is already there. It runs before the lock is taken, because byte 0 cannot be
// written once the lock covers it.
func createLockFile(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return err
	}
	defer file.Close()
	_, err = file.WriteString("\n")
	return err
}

// writePid runs while the caller holds the lock, so the file is this process's to write apart
// from byte 0.
func writePid(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	data := []byte(strconv.Itoa(os.Getpid()) + "\n")
	if _, err := file.WriteAt(data, pidOffset); err != nil {
		return err
	}
	// A shorter pid than the previous holder's must not leave its last digits behind.
	return file.Truncate(pidOffset + int64(len(data)))
}

// readPid reports no pid for an empty file, a file that holds no number, and a read the
// holder's lock blocks, and the second instance then says "already running" alone.
func readPid(path string) (int, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	if _, err := file.Seek(pidOffset, io.SeekStart); err != nil {
		return 0, false
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
