package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/daemon"
	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
)

// The in-process lock and shutdown cases. The real-signal process tests are in
// serve_process_test.go.

func lockFileOf(t *testing.T, home string) string {
	t.Helper()
	return filepath.Join(stateDirOf(t, home), "lock")
}

func logFileText(t *testing.T, home string) string {
	t.Helper()
	return string(readBytes(t, filepath.Join(stateDirOf(t, home), "smd.log")))
}

// pidInLockFile starts at byte 1, because byte 0 is the byte-range lock itself: on Windows a
// read that starts inside the locked range fails, so this is also the read a second instance
// makes.
func pidInLockFile(t *testing.T, home string) int {
	t.Helper()
	path := lockFileOf(t, home)
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open the lock file: %v", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.NewSectionReader(file, 1, 32))
	if err != nil {
		t.Fatalf("read the pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse the pid %q: %v", strings.TrimSpace(string(data)), err)
	}
	return pid
}

// TestASecondServeExitsZeroAndNamesTheFirst: a second `smd serve` exits 0, prints "already
// running (pid N)" on stderr, and reconciles nothing. The change the first instance applies
// afterwards proves it is still healthy.
func TestASecondServeExitsZeroAndNamesTheFirst(t *testing.T) {
	home := daemonHome(t)
	canonical := []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n")
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md",
		"---\nname: kibana\ndisable-model-invocation: true\n---\n")

	stop := serveDaemon(t, home)
	waitUntilWatching(t, path, canonical)
	firstPid := pidInLockFile(t, home)

	// A file the second instance would patch if it reconciled at all.
	pending := writeSkillFile(t, home, "skills/second/SKILL.md", "---\nname: second\n---\n")
	before := readBytes(t, pending)

	stdout, stderr, code := runCLI(t, "serve")
	if code != 0 {
		t.Errorf("second serve exit code = %d, want 0", code)
	}
	if stdout != "" {
		t.Errorf("second serve stdout = %q, want nothing: it logs no line", stdout)
	}
	if !strings.HasSuffix(stderr, "\n") {
		t.Errorf("second serve stderr = %q, want it to end with a newline", stderr)
	}
	if lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n"); len(lines) != 1 {
		t.Fatalf("second serve stderr lines = %d, want 1: %q", len(lines), stderr)
	}
	// The pid in the message is the one the first instance wrote.
	want := "smd: already running (pid " + strconv.Itoa(firstPid) + ")\n"
	if stderr != want {
		t.Errorf("second serve stderr = %q, want %q", stderr, want)
	}
	if got := readBytes(t, pending); !bytes.Equal(got, before) {
		t.Errorf("the second instance wrote %s: %q, want %q", pending, got, before)
	}
	// The second instance opened no log either: the startup line in the file is the first
	// instance's.
	startups := linesWith(logFileText(t, home), "config=~/.skill-model-deactivator.json")
	if len(startups) != 1 {
		t.Errorf("startup lines = %d, want 1: the second instance wrote to the log", len(startups))
	}

	// The first instance is still healthy: a change it sees lands.
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\n"), 0o644); err != nil {
		t.Fatalf("reset the skill file: %v", err)
	}
	waitForFile(t, path, canonical)

	firstOut, _, code := stop()
	if code != 0 {
		t.Errorf("first serve exit code = %d, want 0", code)
	}
	if !strings.Contains(firstOut, "path=~/skills/kibana/SKILL.md") {
		t.Errorf("the first instance did not reconcile:\n%s", firstOut)
	}
}

// TestServeOnceRunsBesideTheDaemon: `serve --once` takes no lock, so both succeed and the
// daemon is not disturbed.
func TestServeOnceRunsBesideTheDaemon(t *testing.T) {
	home := daemonHome(t)
	canonical := []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n")
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md",
		"---\nname: kibana\ndisable-model-invocation: true\n---\n")

	stop := serveDaemon(t, home)
	waitUntilWatching(t, path, canonical)
	daemonPid := pidInLockFile(t, home)

	// The one-off sees a patch the daemon has not reached yet, and applies it.
	pending := writeSkillFile(t, home, "skills/once/SKILL.md", "---\nname: once\n---\n")
	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("serve --once exit code = %d, want 0: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("serve --once stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "path=~/skills/once/SKILL.md") {
		t.Errorf("serve --once did not reconcile:\n%s", stdout)
	}
	want := "---\nname: once\ndisable-model-invocation: true\n---\n"
	if got := readBytes(t, pending); string(got) != want {
		t.Errorf("%s = %q, want %q", pending, got, want)
	}

	// The lock still holds the daemon's pid, so the one-off did not take or overwrite it.
	if got := pidInLockFile(t, home); got != daemonPid {
		t.Errorf("the lock file holds pid %d, want the daemon's %d", got, daemonPid)
	}
	// The daemon still reconciles, so the one-off did not disturb it.
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\n"), 0o644); err != nil {
		t.Fatalf("reset the skill file: %v", err)
	}
	waitForFile(t, path, canonical)

	if _, _, code := stop(); code != 0 {
		t.Errorf("the daemon exit code = %d, want 0", code)
	}
}

// TestServeOnASignalExitsZeroLogsOneShutdownAndReleasesTheLock: the loop ends, exactly one
// shutdown line is logged, and a later serve takes the lock. The signal reaches the daemon as a
// done context, which is the one path signal.NotifyContext sends both signals down.
func TestServeOnASignalExitsZeroLogsOneShutdownAndReleasesTheLock(t *testing.T) {
	home := daemonHome(t)
	canonical := []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n")
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md",
		"---\nname: kibana\ndisable-model-invocation: true\n---\n")

	stop := serveDaemon(t, home)
	waitUntilWatching(t, path, canonical)

	stdout, stderr, code := stop()
	if code != 0 {
		t.Errorf("exit code = %d, want 0: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	shutdowns := linesWith(stdout, "shutdown")
	if len(shutdowns) != 1 {
		t.Fatalf("shutdown lines = %d, want 1:\n%s", len(shutdowns), stdout)
	}
	if !strings.HasPrefix(shutdowns[0], "INFO  ") {
		t.Errorf("shutdown line = %q, want level info", shutdowns[0])
	}

	// The lock is gone with the process, so the next serve takes it.
	relock, err := daemon.AcquireLock(stateDirOf(t, home))
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	relock.Release()
}

// TestTwoSignalsStillGiveOneShutdownLine: the second cancel is a no-op on the same context, so
// the shutdown cannot be interrupted into an inconsistent state and one line records it.
func TestTwoSignalsStillGiveOneShutdownLine(t *testing.T) {
	home := daemonHome(t)
	canonical := []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n")
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md",
		"---\nname: kibana\ndisable-model-invocation: true\n---\n")

	ctx, cancel := context.WithCancel(context.Background())
	previous := daemonSignals
	daemonSignals = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	t.Cleanup(func() { daemonSignals = previous })

	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"serve"}, &out, &errOut) }()
	waitUntilWatching(t, path, canonical)

	// Two signals in quick succession.
	cancel()
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0: %s", code, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after two signals")
	}
	if got := strings.Count(out.String(), "shutdown"); got != 1 {
		t.Errorf("shutdown lines = %d, want 1:\n%s", got, out.String())
	}
}

// TestServeExitsOneWhenTheLockCannotBeTaken: a lock that cannot be acquired for a reason other
// than "held" is one stderr line and exit 1.
func TestServeExitsOneWhenTheLockCannotBeTaken(t *testing.T) {
	home := daemonHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")
	// A directory where the lock file belongs: a directory is the one shape of "not a lock" that
	// behaves the same on every platform, and it is not the second-instance answer.
	dir := stateDirOf(t, home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "lock"), 0o755); err != nil {
		t.Fatalf("create the blocking directory: %v", err)
	}

	stdout, stderr, code := runCLI(t, "serve")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty: no pass ran", stdout)
	}
	if lines := linesOf(stderr); len(lines) != 1 {
		t.Errorf("stderr lines = %d, want 1: %q", len(lines), stderr)
	}
	// It is not the second-instance message, because this is not a second instance.
	if strings.Contains(stderr, "already running") {
		t.Errorf("stderr = %q, want the directory failure and not \"already running\"", stderr)
	}
	if !strings.Contains(stderr, "lock file") {
		t.Errorf("stderr = %q, want it to name the lock file", stderr)
	}
}

// TestAPassInFlightFinishesBeforeTheShutdownLine: a signal that arrives with a write in flight
// does not cancel the write, so the file is complete and the shutdown line comes after the
// write's own line. The write is held at the rename, which turns the complete temp file into
// the target.
func TestAPassInFlightFinishesBeforeTheShutdownLine(t *testing.T) {
	home := daemonHome(t)
	canonicalText := "---\nname: kibana\ndisable-model-invocation: true\n---\n"
	canonical := []byte(canonicalText)
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", canonicalText)

	// The rename seam is installed before the daemon starts, so no pass can slip past it. It
	// holds the first rename after the test opens the gate, which is the write the signal is
	// meant to interrupt.
	inRename := make(chan struct{}, 1)
	release := make(chan struct{})
	previousRename := reconcile.Rename
	var gate, held atomic.Bool
	reconcile.Rename = func(oldpath, newpath string) error {
		if gate.Load() && held.CompareAndSwap(false, true) {
			inRename <- struct{}{}
			<-release
		}
		return previousRename(oldpath, newpath)
	}
	t.Cleanup(func() { reconcile.Rename = previousRename })

	stop := serveDaemon(t, home)
	waitUntilWatching(t, path, canonical)

	// From here on, the first rename is the one the signal interrupts.
	gate.Store(true)
	// Make the file need a patch again, which sends the daemon into the held write.
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\n"), 0o644); err != nil {
		t.Fatalf("reset the skill file: %v", err)
	}
	select {
	case <-inRename:
	case <-time.After(10 * time.Second):
		t.Fatal("the pass never reached the write")
	}

	// stop blocks until the loop returns, so it runs in its own goroutine while the test releases
	// the rename.
	type result struct {
		stdout, stderr string
		code           int
	}
	stopped := make(chan result, 1)
	go func() {
		stdout, stderr, code := stop()
		stopped <- result{stdout, stderr, code}
	}()
	// Give the signal time to reach the loop, so the case is exercised and not raced past.
	time.Sleep(100 * time.Millisecond)
	close(release)

	var got result
	select {
	case got = <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop after the write finished")
	}

	if got.code != 0 {
		t.Errorf("exit code = %d, want 0: %s", got.code, got.stderr)
	}
	// The file is the complete patched file and not the truncated or old one.
	if after := readBytes(t, path); !bytes.Equal(after, canonical) {
		t.Errorf("the file is %q, want the complete %q", after, canonical)
	}
	if found := tempFiles(t, home); len(found) != 0 {
		t.Errorf("the shutdown left temp files: %v", found)
	}

	lines := linesOf(got.stdout)
	write, shutdown := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "path=~/skills/kibana/SKILL.md") && strings.Contains(line, "result=ok") {
			write = i
		}
		if strings.Contains(line, "shutdown") {
			shutdown = i
		}
	}
	if write < 0 {
		t.Fatalf("no write line in the output:\n%s", got.stdout)
	}
	if shutdown != len(lines)-1 {
		t.Errorf("the shutdown line is at %d of %d, want last:\n%s", shutdown, len(lines), got.stdout)
	}
	if n := strings.Count(got.stdout, "shutdown"); n != 1 {
		t.Errorf("shutdown lines = %d, want 1:\n%s", n, got.stdout)
	}
}
