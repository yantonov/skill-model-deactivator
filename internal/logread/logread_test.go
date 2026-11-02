package logread

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

// writeLog returns the state directory it created.
func writeLog(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return dir
}

func numbered(n int) []string {
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	return lines
}

// TestTail covers the last n complete lines and the total.
func TestTail(t *testing.T) {
	lines := numbered(25)
	dir := writeLog(t, platform.LogFileName, strings.Join(lines, "\n")+"\n")

	got, total, _, err := Tail(dir, 10)
	if err != nil {
		t.Fatalf("Tail() error = %v, want nil", err)
	}
	if total != 25 {
		t.Errorf("total = %d, want 25", total)
	}
	want := lines[15:]
	if len(got) != len(want) {
		t.Fatalf("Tail() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTailMoreLinesThanTheFile: N larger than the file gives the whole file and no error.
func TestTailMoreLinesThanTheFile(t *testing.T) {
	lines := numbered(3)
	dir := writeLog(t, platform.LogFileName, strings.Join(lines, "\n")+"\n")

	got, total, _, err := Tail(dir, 10000)
	if err != nil {
		t.Fatalf("Tail() error = %v, want nil", err)
	}
	if total != len(lines) || len(got) != len(lines) {
		t.Fatalf("Tail() = %v, total %d, want %d lines", got, total, len(lines))
	}
}

// TestTailDropsATrailingPartialLine: a line with no closing newline is not printed and does not
// count.
func TestTailDropsATrailingPartialLine(t *testing.T) {
	dir := writeLog(t, platform.LogFileName, "one\ntwo\nthree")

	got, total, _, err := Tail(dir, 10)
	if err != nil {
		t.Fatalf("Tail() error = %v, want nil", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2: the partial line does not count", total)
	}
	if len(got) != 2 || got[1] != "two" {
		t.Errorf("Tail() = %v, want the two complete lines", got)
	}
}

// TestTailWithoutAFile: an error that names the path.
func TestTailWithoutAFile(t *testing.T) {
	dir := t.TempDir()

	_, _, _, err := Tail(dir, 10)
	if !errors.Is(err, ErrNoFile) {
		t.Fatalf("Tail() error = %v, want ErrNoFile", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, platform.LogFileName)) {
		t.Errorf("error = %q, want it to name the log path", err)
	}
}

// TestTailWithAnEmptyFile: the file exists and holds nothing.
func TestTailWithAnEmptyFile(t *testing.T) {
	dir := writeLog(t, platform.LogFileName, "")

	got, total, _, err := Tail(dir, 10)
	if err != nil {
		t.Fatalf("Tail() error = %v, want nil", err)
	}
	if total != 0 || len(got) != 0 {
		t.Errorf("Tail() = %v, total %d, want no lines", got, total)
	}
}

// TestAllConcatenatesOldestFirst: smd.log.1 then smd.log.
func TestAllConcatenatesOldestFirst(t *testing.T) {
	dir := writeLog(t, platform.LogFileName, "new-1\nnew-2\n")
	if err := os.WriteFile(filepath.Join(dir, platform.LogFileName+".1"), []byte("old-1\nold-2\n"), 0o644); err != nil {
		t.Fatalf("write the rotated file: %v", err)
	}

	lines, rotated, _, err := All(dir)
	if err != nil {
		t.Fatalf("All() error = %v, want nil", err)
	}
	if !rotated {
		t.Error("All() reported no rotated file, want one")
	}
	want := []string{"old-1", "old-2", "new-1", "new-2"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Errorf("All() = %v, want %v", lines, want)
	}
}

// TestAllWithoutTheRotatedFile omits it and says so, which is what the header needs.
func TestAllWithoutTheRotatedFile(t *testing.T) {
	dir := writeLog(t, platform.LogFileName, "only\n")

	lines, rotated, _, err := All(dir)
	if err != nil {
		t.Fatalf("All() error = %v, want nil", err)
	}
	if rotated {
		t.Error("All() reported a rotated file, want none")
	}
	if len(lines) != 1 || lines[0] != "only" {
		t.Errorf("All() = %v, want [only]", lines)
	}
}

// TestAllWithoutTheCurrentFile: there is nothing to print.
func TestAllWithoutTheCurrentFile(t *testing.T) {
	dir := writeLog(t, platform.LogFileName+".1", "old\n")

	_, _, _, err := All(dir)
	if !errors.Is(err, ErrNoFile) {
		t.Fatalf("All() error = %v, want ErrNoFile", err)
	}
}

// TestAllDropsATrailingPartialLine holds the same rule as Tail, for both files.
func TestAllDropsATrailingPartialLine(t *testing.T) {
	dir := writeLog(t, platform.LogFileName, "complete\npartial")
	if err := os.WriteFile(filepath.Join(dir, platform.LogFileName+".1"), []byte("old\nhalf"), 0o644); err != nil {
		t.Fatalf("write the rotated file: %v", err)
	}

	lines, _, _, err := All(dir)
	if err != nil {
		t.Fatalf("All() error = %v, want nil", err)
	}
	if want := "old,complete"; strings.Join(lines, ",") != want {
		t.Errorf("All() = %v, want %q", lines, want)
	}
}

// TestFollowPrintsNewLines: the offset is the one the caller already printed, so a line is
// never printed twice.
func TestFollowPrintsNewLines(t *testing.T) {
	lines := numbered(3)
	dir := writeLog(t, platform.LogFileName, strings.Join(lines, "\n")+"\n")
	offset := tailEnd(t, dir, len(lines))

	done := make(chan struct{})
	var out bytes.Buffer
	errs := make(chan error, 1)
	go func() { errs <- Follow(dir, offset, &out, done) }()

	appendTo(t, filepath.Join(dir, platform.LogFileName), "four\n")
	waitFor(t, &out, "four")

	close(done)
	if err := <-errs; err != nil {
		t.Fatalf("Follow() error = %v, want nil", err)
	}
	if strings.Contains(out.String(), "line 0") {
		t.Errorf("Follow() printed a line the caller already printed:\n%s", out.String())
	}
}

// TestFollowStartsAfterTheLinesTheCallerPrinted: the stream starts after the last n lines and
// not at the start of the file.
func TestFollowStartsAfterTheLinesTheCallerPrinted(t *testing.T) {
	dir := writeLog(t, platform.LogFileName, "one\ntwo\n")
	offset := tailEnd(t, dir, 2)

	done := make(chan struct{})
	out := &safeWriter{}
	errs := make(chan error, 1)
	go func() { errs <- Follow(dir, offset, out, done) }()

	appendTo(t, filepath.Join(dir, platform.LogFileName), "three\n")
	waitForWriter(t, out, "three")

	close(done)
	if err := <-errs; err != nil {
		t.Fatalf("Follow() error = %v, want nil", err)
	}
	if got := out.String(); strings.Contains(got, "one") || strings.Contains(got, "two") {
		t.Errorf("Follow() reprinted a line from before the offset:\n%s", got)
	}
}

// TestFollowWithoutAFile is the absent-file case for the stream.
func TestFollowWithoutAFile(t *testing.T) {
	dir := t.TempDir()

	err := Follow(dir, 0, io.Discard, make(chan struct{}))
	if !errors.Is(err, ErrNoFile) {
		t.Fatalf("Follow() error = %v, want ErrNoFile", err)
	}
}

// appendTo appends one line to path, the way the daemon does.
func appendTo(t *testing.T, path, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s for appending: %v", path, err)
	}
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		t.Fatalf("append to %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// waitFor polls until the buffer holds want or the poll fallback has run twice.
func waitFor(t *testing.T, out *bytes.Buffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("output does not hold %q:\n%s", want, out.String())
}

// safeWriter is read by the test while the follower writes.
type safeWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *safeWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(data)
}

func (w *safeWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitForWriter polls until the writer holds want or two poll windows have closed.
func waitForWriter(t *testing.T, out *safeWriter, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("output does not hold %q:\n%s", want, out.String())
}

// tailEnd is where a follower starts.
func tailEnd(t *testing.T, dir string, n int) int64 {
	t.Helper()
	_, _, offset, err := Tail(dir, n)
	if err != nil {
		t.Fatalf("Tail() error = %v, want nil", err)
	}
	return offset
}
