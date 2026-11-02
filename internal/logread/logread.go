// Package logread never writes, truncates, or rotates anything. A trailing partial line is
// dropped, which is what makes a concurrent read safe.
package logread

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

// Lines is the number of lines `smd log` prints by default.
const Lines = 10

const rotatedSuffix = ".1"

// pollInterval is the fallback of --follow: the directory watch is the fast path, and the poll
// keeps the stream alive when a notification is missed.
const pollInterval = 200 * time.Millisecond

var ErrNoFile = errors.New("log file not found")

// NoFileError names the path, which is what the one stderr line carries.
type NoFileError struct {
	Path string
}

func (e *NoFileError) Error() string {
	return ErrNoFile.Error() + ": " + e.Path
}

func (e *NoFileError) Unwrap() error {
	return ErrNoFile
}

// Tail returns the last n complete lines, the total the file holds, and the byte offset just
// past the last complete line. n must be positive, which the flag parse guarantees.
//
// The offset belongs to the same read as the lines, so a caller that prints them and then
// follows from that offset cannot print a line twice and cannot miss one written after the
// read.
func Tail(stateDir string, n int) ([]string, int, int64, error) {
	path := currentPath(stateDir)
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, openError(path, err)
	}
	defer file.Close()

	lines, total, offset, err := scan(file, n)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("cannot read the log file %s: %w", path, err)
	}
	return lines, total, offset, nil
}

// All returns the rotated file first, then the current file, oldest first, plus whether the
// rotated file was there and the offset where Follow starts. A missing current file fails the
// way the default does.
func All(stateDir string) (lines []string, rotated bool, offset int64, err error) {
	older, found, _, err := readAll(rotatedPath(stateDir))
	if err != nil {
		return nil, false, 0, err
	}

	current, exists, offset, err := readAll(currentPath(stateDir))
	if err != nil {
		return nil, false, 0, err
	}
	if !exists {
		return nil, false, 0, &NoFileError{Path: currentPath(stateDir)}
	}
	return append(older, current...), found, offset, nil
}

// Follow starts at offset, the one Tail returned, and reopens the file after a rotation, so a
// rotation does not end the stream. It returns nil when done is closed, which is how Ctrl-C
// exits 0.
func Follow(stateDir string, offset int64, out io.Writer, done <-chan struct{}) error {
	path := currentPath(stateDir)
	if _, err := os.Stat(path); err != nil {
		return openError(path, err)
	}

	// The watch is a hint: the poll below is the fallback, so a watcher that cannot be built
	// costs speed and not correctness.
	watcher, err := fsnotify.NewWatcher()
	if err == nil && watcher.Add(stateDir) != nil {
		watcher.Close()
		watcher = nil
	}
	if watcher != nil {
		defer watcher.Close()
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return nil
		case <-ticker.C:
		case <-eventsOf(watcher):
		}

		next, err := readFrom(path, &offset)
		if err != nil {
			return err
		}
		if err := writeLines(out, next); err != nil {
			return err
		}
	}
}

// readFrom advances offset past the lines it returns. A file shorter than offset was rotated
// away or truncated, so the read restarts at the beginning.
func readFrom(path string, offset *int64) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A rotation briefly leaves no smd.log. The next event or poll picks the new
			// file up, so this is not an error.
			*offset = 0
			return nil, nil
		}
		return nil, openError(path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot read the log file %s: %w", path, err)
	}
	if info.Size() < *offset {
		*offset = 0
	}
	if _, err := file.Seek(*offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("cannot read the log file %s: %w", path, err)
	}

	lines, _, consumed, err := scan(file, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot read the log file %s: %w", path, err)
	}
	*offset += consumed
	return lines, nil
}

// readAll treats a missing file as not an error: --all omits the rotated file when it is
// absent.
func readAll(path string) ([]string, bool, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, 0, nil
		}
		return nil, false, 0, openError(path, err)
	}
	defer file.Close()

	lines, _, consumed, err := scan(file, 0)
	if err != nil {
		return nil, false, 0, fmt.Errorf("cannot read the log file %s: %w", path, err)
	}
	return lines, true, consumed, nil
}

// openError names the path for a missing file, and keeps the operating-system detail
// otherwise.
func openError(path string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return &NoFileError{Path: path}
	}
	return fmt.Errorf("cannot read the log file %s: %w", path, err)
}

// eventsOf returns nil for no watcher. A nil channel blocks for ever in a select, so the poll
// alone keeps the loop alive.
func eventsOf(watcher *fsnotify.Watcher) <-chan fsnotify.Event {
	if watcher == nil {
		return nil
	}
	return watcher.Events
}

// writeLines stops at the first error, so a closed pipe ends the stream instead of being
// ignored.
func writeLines(out io.Writer, lines []string) error {
	for _, line := range lines {
		if _, err := io.WriteString(out, line+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func currentPath(stateDir string) string {
	return filepath.Join(stateDir, platform.LogFileName)
}

func rotatedPath(stateDir string) string {
	return filepath.Join(stateDir, platform.LogFileName+rotatedSuffix)
}
