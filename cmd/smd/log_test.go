package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

const acceptLog = time.Second

func numberedLines(first, n int) []string {
	lines := make([]string, 0, n)
	for i := first; i < first+n; i++ {
		lines = append(lines, fmt.Sprintf("2026-11-02T10:12:04.%03dZ INFO  line=%d", i%1000, i))
	}
	return lines
}

// writeLogFile returns the lines it wrote.
func writeLogFile(t *testing.T, home, name string, lines []string) []string {
	t.Helper()
	dir := stateDirOf(t, home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return lines
}

func logFixture(t *testing.T, home string, n int) []string {
	t.Helper()
	return writeLogFile(t, home, platform.LogFileName, numberedLines(0, n))
}

func tildeLog(t *testing.T, home string) string {
	t.Helper()
	return display.Tilde(filepath.Join(stateDirOf(t, home), platform.LogFileName))
}

// logHeaderOf returns the header line and asserts the blank line after it.
func logHeaderOf(t *testing.T, stdout string) string {
	t.Helper()
	header, rest, found := strings.Cut(stdout, "\n")
	if !found || !strings.HasPrefix(rest, "\n") {
		t.Fatalf("stdout does not start with a header and a blank line:\n%s", stdout)
	}
	return header
}

func bodyOf(t *testing.T, stdout string) []string {
	t.Helper()
	_, rest, found := strings.Cut(stdout, "\n\n")
	if !found {
		t.Fatalf("stdout has no blank line after the header:\n%s", stdout)
	}
	if rest == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(rest, "\n"), "\n")
}

// TestLogPrintsTheLastTenLinesExact: the default prints exactly the last 10 lines, with the
// resolved path on the first line.
func TestLogPrintsTheLastTenLinesExact(t *testing.T) {
	home := newHome(t)
	logFixture(t, home, 25)

	stdout, stderr, code := runCLI(t, "log")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	want := tildeLog(t, home) + " (last 10 of 25 lines)"
	if got := logHeaderOf(t, stdout); got != want {
		t.Errorf("header = %q, want %q", got, want)
	}

	body := bodyOf(t, stdout)
	if len(body) != 10 {
		t.Fatalf("printed %d lines, want 10:\n%s", len(body), stdout)
	}
	lastTen := numberedLines(15, 10)
	for i := range lastTen {
		if body[i] != lastTen[i] {
			t.Errorf("line %d = %q, want %q", i, body[i], lastTen[i])
		}
	}
}

// TestLogLineCount covers --lines 1, --lines 3, and --lines 10000.
func TestLogLineCount(t *testing.T) {
	home := newHome(t)
	lines := logFixture(t, home, 12)

	tests := []struct {
		name string
		n    string
		want []string
	}{
		{name: "--lines 1", n: "1", want: lines[11:]},
		{name: "--lines 3", n: "3", want: lines[9:]},
		{name: "--lines 10000 prints the whole file", n: "10000", want: lines},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, "log", "--lines", test.n)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0: %s", code, stderr)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}

			want := fmt.Sprintf("%s (last %s of %d lines)", tildeLog(t, home), test.n, len(lines))
			if got := logHeaderOf(t, stdout); got != want {
				t.Errorf("header = %q, want %q", got, want)
			}

			body := bodyOf(t, stdout)
			if len(body) != len(test.want) {
				t.Fatalf("printed %d lines, want %d:\n%s", len(body), len(test.want), stdout)
			}
			for i := range test.want {
				if body[i] != test.want[i] {
					t.Errorf("line %d = %q, want %q", i, body[i], test.want[i])
				}
			}
		})
	}
}

// TestLogShortFilePrintsEveryLine: every line, no error.
func TestLogShortFilePrintsEveryLine(t *testing.T) {
	home := newHome(t)
	lines := logFixture(t, home, 3)

	stdout, stderr, code := runCLI(t, "log")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if got := logHeaderOf(t, stdout); got != tildeLog(t, home)+" (last 10 of 3 lines)" {
		t.Errorf("header = %q, want a count of 3 lines", got)
	}

	body := bodyOf(t, stdout)
	if len(body) != len(lines) {
		t.Fatalf("printed %d lines, want %d:\n%s", len(body), len(lines), stdout)
	}
	for i := range lines {
		if body[i] != lines[i] {
			t.Errorf("line %d = %q, want %q", i, body[i], lines[i])
		}
	}
}

// TestLogDropsATrailingPartialLine: the daemon may be mid-write, so a line with no closing
// newline is not printed.
func TestLogDropsATrailingPartialLine(t *testing.T) {
	home := newHome(t)
	lines := numberedLines(0, 3)
	dir := stateDirOf(t, home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	content := strings.Join(lines, "\n") + "\nthe daemon is still writ"
	if err := os.WriteFile(filepath.Join(dir, platform.LogFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write the log fixture: %v", err)
	}

	stdout, stderr, code := runCLI(t, "log")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}

	body := bodyOf(t, stdout)
	if len(body) != len(lines) {
		t.Fatalf("printed %d lines, want %d:\n%s", len(body), len(lines), stdout)
	}
	if got := body[len(body)-1]; got != lines[len(lines)-1] {
		t.Errorf("last line = %q, want %q", got, lines[len(lines)-1])
	}
	if strings.Contains(stdout, "still writ") {
		t.Errorf("stdout carries the partial line:\n%s", stdout)
	}
	if got := logHeaderOf(t, stdout); got != tildeLog(t, home)+" (last 10 of 3 lines)" {
		t.Errorf("header = %q, want a count of 3 complete lines", got)
	}
}

// TestLogWithoutAFile: one stderr line naming the path, nothing on stdout, exit 1.
func TestLogWithoutAFile(t *testing.T) {
	home := newHome(t)

	stdout, stderr, code := runCLI(t, "log")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if lines := linesOf(stderr); len(lines) != 1 {
		t.Fatalf("stderr lines = %d, want 1: %q", len(lines), stderr)
	}
	if !strings.Contains(stderr, filepath.Join(stateDirOf(t, home), platform.LogFileName)) {
		t.Errorf("stderr = %q, want it to name the log path", stderr)
	}
}

// TestLogAllConcatenatesTheRotatedFile: smd.log.1 first, then smd.log.
func TestLogAllConcatenatesTheRotatedFile(t *testing.T) {
	home := newHome(t)
	older := writeLogFile(t, home, platform.LogFileName+".1", numberedLines(0, 4))
	current := writeLogFile(t, home, platform.LogFileName, numberedLines(4, 3))

	stdout, stderr, code := runCLI(t, "log", "--all")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if got := logHeaderOf(t, stdout); got != tildeLog(t, home)+" (7 lines)" {
		t.Errorf("header = %q, want the two-file count", got)
	}

	body := bodyOf(t, stdout)
	want := append(append([]string{}, older...), current...)
	if len(body) != len(want) {
		t.Fatalf("printed %d lines, want %d:\n%s", len(body), len(want), stdout)
	}
	for i := range want {
		if body[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, body[i], want[i])
		}
	}
}

// TestLogAllWithoutARotatedFile: --all omits the missing rotated file, and the header then
// names only smd.log and drops the count.
func TestLogAllWithoutARotatedFile(t *testing.T) {
	home := newHome(t)
	lines := logFixture(t, home, 5)

	stdout, stderr, code := runCLI(t, "log", "--all")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if got := logHeaderOf(t, stdout); got != tildeLog(t, home) {
		t.Errorf("header = %q, want the bare path", got)
	}
	if body := bodyOf(t, stdout); len(body) != len(lines) {
		t.Errorf("printed %d lines, want %d:\n%s", len(body), len(lines), stdout)
	}
}

// TestLogAllWithoutAFile: one stderr line and exit 1, because there is nothing to print.
func TestLogAllWithoutAFile(t *testing.T) {
	home := newHome(t)

	stdout, stderr, code := runCLI(t, "log", "--all")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, filepath.Join(stateDirOf(t, home), platform.LogFileName)) {
		t.Errorf("stderr = %q, want it to name the log file", stderr)
	}
}

// followProcess returns the buffers and the function that ends the stream the way Ctrl-C does.
// The buffers are written from the goroutine, so a caller reads them through safeBuffer, which
// is locked for that purpose.
func followProcess(t *testing.T, args ...string) (out, errOut *safeBuffer, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	previous := logSignals
	logSignals = func() (context.Context, context.CancelFunc) { return ctx, func() {} }

	out, errOut = &safeBuffer{}, &safeBuffer{}
	done := make(chan int, 1)
	go func() { done <- run(append([]string{"log"}, args...), out, errOut) }()

	stopped := false
	stop = func() int {
		if stopped {
			return 0
		}
		stopped = true
		cancel()
		select {
		case code := <-done:
			logSignals = previous
			return code
		case <-time.After(5 * time.Second):
			t.Error("smd log --follow did not stop after its context was done")
			logSignals = previous
			return -1
		}
	}
	t.Cleanup(func() { stop() })
	return out, errOut, stop
}

func waitForOutput(t *testing.T, out *safeBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(acceptLog)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("output does not hold %q within %s:\n%s", want, acceptLog, out.String())
}

// appendLog appends one line to smd.log, the way the daemon does.
func appendLog(t *testing.T, home, line string) {
	t.Helper()
	path := filepath.Join(stateDirOf(t, home), platform.LogFileName)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open the log for appending: %v", err)
	}
	if _, err := file.WriteString(line + "\n"); err != nil {
		file.Close()
		t.Fatalf("append a line: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close the log: %v", err)
	}
}

// TestLogFollowShowsANewLine: --follow shows a write within 1 s, and Ctrl-C then exits 0.
func TestLogFollowShowsANewLine(t *testing.T) {
	home := newHome(t)
	lines := logFixture(t, home, 2)

	out, errOut, stop := followProcess(t, "--follow")
	waitForOutput(t, out, lines[1])

	appendLog(t, home, "2026-11-02T10:12:09.000Z INFO  line=new")
	waitForOutput(t, out, "line=new")

	if code := stop(); code != 0 {
		t.Errorf("exit code = %d, want 0 on Ctrl-C", code)
	}
	if stderr := errOut.String(); stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if got := logHeaderOf(t, out.String()); got != tildeLog(t, home)+" (last 10 of 2 lines)" {
		t.Errorf("header = %q, want the follow header", got)
	}
}

// TestLogFollowSurvivesARotation: after smd.log becomes smd.log.1, the stream continues on the
// new file.
func TestLogFollowSurvivesARotation(t *testing.T) {
	home := newHome(t)
	lines := logFixture(t, home, 1)

	out, errOut, stop := followProcess(t, "--follow")
	waitForOutput(t, out, lines[0])

	dir := stateDirOf(t, home)
	current := filepath.Join(dir, platform.LogFileName)
	if err := os.Rename(current, current+".1"); err != nil {
		t.Fatalf("rotate the log: %v", err)
	}
	if err := os.WriteFile(current, []byte("2026-11-02T10:12:10.000Z INFO  line=after-rotation\n"), 0o644); err != nil {
		t.Fatalf("write the new log: %v", err)
	}

	waitForOutput(t, out, "line=after-rotation")
	if code := stop(); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stderr := errOut.String(); stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

// TestLogFollowWithoutAFile: one stderr line and exit 1, because there is nothing to follow.
func TestLogFollowWithoutAFile(t *testing.T) {
	home := newHome(t)

	stdout, stderr, code := runCLI(t, "log", "--follow")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, filepath.Join(stateDirOf(t, home), platform.LogFileName)) {
		t.Errorf("stderr = %q, want it to name the log file", stderr)
	}
}

// TestLogNeverWrites: the state directory and the config file are byte-identical after a read,
// and no temp file appears.
func TestLogNeverWrites(t *testing.T) {
	home := newHome(t)
	writeConfig(t, home, `{"patterns": ["~/skills/*/SKILL.md"]}`)
	logFixture(t, home, 20)
	writeLogFile(t, home, platform.LogFileName+".1", numberedLines(0, 4))

	before := snapshot(t, home)
	if _, _, code := runCLI(t, "log", "--all"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	after := snapshot(t, home)

	if len(before) != len(after) {
		t.Fatalf("the file count changed from %d to %d", len(before), len(after))
	}
	for name, hash := range before {
		if after[name] != hash {
			t.Errorf("%s changed: %s became %q", name, hash, after[name])
		}
	}
}

// TestLogReadsWhileTheDaemonRuns: the command reads the file the daemon holds open, so the log
// lines of a running daemon are what it prints.
func TestLogReadsWhileTheDaemonRuns(t *testing.T) {
	home := serveHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")

	stop := serveDaemon(t, home)
	waitForLog(t, home, "reconcile finished")

	stdout, stderr, code := runCLI(t, "log")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 while the daemon runs: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "reconcile finished") {
		t.Errorf("stdout does not carry the daemon's lines:\n%s", stdout)
	}
	if got := logHeaderOf(t, stdout); !strings.HasPrefix(got, tildeLog(t, home)+" (last ") {
		t.Errorf("header = %q, want the resolved log path first", got)
	}

	if _, _, code := stop(); code != 0 {
		t.Errorf("daemon exit code = %d, want 0", code)
	}

	// And the same read with the file gone: one stderr line and exit 1.
	if err := os.Remove(filepath.Join(stateDirOf(t, home), platform.LogFileName)); err != nil {
		t.Fatalf("delete the log file: %v", err)
	}
	stdout, stderr, code = runCLI(t, "log")
	if code != 1 {
		t.Errorf("exit code = %d, want 1 after the file is deleted", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, platform.LogFileName) {
		t.Errorf("stderr = %q, want it to name the log file", stderr)
	}
}
