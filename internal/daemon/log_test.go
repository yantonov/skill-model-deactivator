package daemon

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/platform"
	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
)

// lineStart is the fixed start of every log line: an RFC 3339 UTC timestamp with milliseconds,
// then the level padded to five columns.
var lineStart = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z (DEBUG|INFO |WARN |ERROR) `)

// newSink returns a logger and the buffer that receives stdout.
func newSink(t *testing.T) (*Logger, string, *bytes.Buffer) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	var stdout bytes.Buffer
	logger, err := NewLogger(dir, &stdout)
	if err != nil {
		t.Fatalf("NewLogger() error = %v, want nil", err)
	}
	t.Cleanup(func() { logger.file.Close() })
	return logger, dir, &stdout
}

func logText(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(dir, name), err)
	}
	return string(data)
}

// TestLineFormat covers the three shapes: the timestamp first, the level padded to five
// columns, the fields in the caller's order, and the message last.
func TestLineFormat(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 12, 4, 317000000, time.UTC)

	tests := []struct {
		name  string
		level string
		msg   string
		kv    []reconcile.KV
		want  string
	}{
		{
			name:  "summary with the duration in the message",
			level: "info",
			msg:   "reconcile finished duration=41ms",
			kv: []reconcile.KV{
				{Key: "watched", Value: "78"}, {Key: "changed", Value: "1"},
				{Key: "wrote", Value: "1"}, {Key: "skipped", Value: "0"},
				{Key: "failed", Value: "0"},
			},
			want: "2026-11-02T10:12:04.317Z INFO  watched=78 changed=1 wrote=1 skipped=0 failed=0" +
				"  reconcile finished duration=41ms\n",
		},
		{
			name:  "write line without a message",
			level: "info",
			kv: []reconcile.KV{
				{Key: "path", Value: "~/.agents/skills/kibana/SKILL.md"}, {Key: "desired", Value: "true"},
				{Key: "reason", Value: "absent"}, {Key: "action", Value: "inserted"},
				{Key: "result", Value: "ok"},
			},
			want: "2026-11-02T10:12:04.317Z INFO  path=~/.agents/skills/kibana/SKILL.md desired=true" +
				" reason=absent action=inserted result=ok\n",
		},
		{
			name:  "error with a quoted value",
			level: "error",
			kv: []reconcile.KV{
				{Key: "path", Value: "~/.agents/skills/locked/SKILL.md"},
				{Key: "action", Value: "inserted"}, {Key: "result", Value: "failed"},
				{Key: "error", Value: "permission denied"},
			},
			want: "2026-11-02T10:12:04.317Z ERROR path=~/.agents/skills/locked/SKILL.md action=inserted" +
				" result=failed error=\"permission denied\"\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(formatLine(start, test.level, test.msg, test.kv)); got != test.want {
				t.Errorf("formatLine() =\n%q\nwant\n%q", got, test.want)
			}
		})
	}
}

// TestEveryLevelIsPaddedToFiveColumns: the columns line up whatever a caller passes.
func TestEveryLevelIsPaddedToFiveColumns(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 12, 4, 0, time.UTC)

	tests := []struct{ level, want string }{
		{"debug", "DEBUG"}, {"info", "INFO "}, {"warn", "WARN "}, {"error", "ERROR"},
	}
	for _, test := range tests {
		t.Run(test.level, func(t *testing.T) {
			got := string(formatLine(start, test.level, "", nil))
			if want := "2026-11-02T10:12:04.000Z " + test.want + "\n"; got != want {
				t.Errorf("formatLine() = %q, want %q", got, want)
			}
		})
	}
}

// TestFieldValueQuotesOnlyWhatNeedsIt: a path without a space stays bare, because a reader
// greps path=<file>.
func TestFieldValueQuotesOnlyWhatNeedsIt(t *testing.T) {
	tests := []struct{ name, value, want string }{
		{"a path with no space", "~/.agents/skills/stale-skill/SKILL.md", "~/.agents/skills/stale-skill/SKILL.md"},
		{"a value with a space", "permission denied", `"permission denied"`},
		{"a value with a quote", `say "no"`, `"say \"no\""`},
		{"the empty value", "", `""`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fieldValue(test.value); got != test.want {
				t.Errorf("fieldValue(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

// TestEveryLevelReachesBothDestinations: no level is filtered, so a debug line reaches the
// output with no configuration change and no flag.
func TestEveryLevelReachesBothDestinations(t *testing.T) {
	logger, dir, stdout := newSink(t)

	for _, level := range []string{"debug", "info", "warn", "error"} {
		logger.Line(level, "", reconcile.KV{Key: "level", Value: level})
	}

	printed := stdout.String()
	written := logText(t, dir, platform.LogFileName)
	for _, level := range []string{"debug", "info", "warn", "error"} {
		want := "level=" + level + "\n"
		if !strings.Contains(printed, want) {
			t.Errorf("stdout is missing the %s line:\n%s", level, printed)
		}
		if !strings.Contains(written, want) {
			t.Errorf("the log file is missing the %s line:\n%s", level, written)
		}
	}
}

// TestLogLineReachesStdoutAndTheFile: the tool treats a terminal, a pipe, and a file the same
// way, so it writes the line and the file without inspecting stdout at all.
func TestLogLineReachesStdoutAndTheFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	t.Cleanup(func() { read.Close() })

	collected := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		collected <- string(data)
	}()

	logger, err := NewLogger(dir, write)
	if err != nil {
		t.Fatalf("NewLogger() error = %v, want nil", err)
	}
	t.Cleanup(func() { logger.file.Close() })

	logger.Line("info", "", reconcile.KV{Key: "path", Value: "~/skills/a/SKILL.md"})
	write.Close()
	stdout := <-collected

	if !lineStart.MatchString(stdout) {
		t.Fatalf("stdout = %q, want one complete log line", stdout)
	}
	if written := logText(t, dir, platform.LogFileName); written != stdout {
		t.Errorf("the file holds %q, want the same line as stdout %q", written, stdout)
	}
}

// TestStateDirectoryIsCreatedOnFirstWrite: the state directory is created on first write,
// including its missing parents.
func TestStateDirectoryIsCreatedOnFirstWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing", "state")
	var stdout bytes.Buffer
	logger, err := NewLogger(dir, &stdout)
	if err != nil {
		t.Fatalf("NewLogger() error = %v, want nil", err)
	}
	t.Cleanup(func() { logger.file.Close() })

	logger.Line("info", "", reconcile.KV{Key: "path", Value: "~/skills/a/SKILL.md"})

	if !strings.Contains(logText(t, dir, platform.LogFileName), "path=~/skills/a/SKILL.md\n") {
		t.Errorf("the log file is missing the line:\n%s", logText(t, dir, platform.LogFileName))
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("os.Stat(%q) = %v, %v, want a directory", dir, info, err)
	}
}

// TestNewLoggerFailsOnAStateDirectoryItCannotCreate: a state directory that cannot be created
// is not a silent no-op.
func TestNewLoggerFailsOnAStateDirectoryItCannotCreate(t *testing.T) {
	// A regular file where the directory belongs makes MkdirAll fail on every platform.
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("create the blocking file: %v", err)
	}

	logger, err := NewLogger(dir, io.Discard)
	if err == nil {
		logger.file.Close()
		t.Fatalf("NewLogger() error = nil, want the directory failure")
	}
	if !strings.Contains(err.Error(), "state directory") {
		t.Errorf("error = %q, want it to name the state directory", err)
	}
}

// TestNilStdoutStillWritesTheFile: a nil writer is a discard and not a panic.
func TestNilStdoutStillWritesTheFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	logger, err := NewLogger(dir, nil)
	if err != nil {
		t.Fatalf("NewLogger() error = %v, want nil", err)
	}
	t.Cleanup(func() { logger.file.Close() })

	logger.Line("info", "", reconcile.KV{Key: "path", Value: "~/skills/a/SKILL.md"})

	if !strings.Contains(logText(t, dir, platform.LogFileName), "path=~/skills/a/SKILL.md\n") {
		t.Error("a nil stdout stopped the file from being written")
	}
}

// TestAppendKeepsTheExistingLog: a restart appends instead of truncating.
func TestAppendKeepsTheExistingLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the state directory: %v", err)
	}
	existing := "2026-11-02T10:00:00.000Z INFO  path=~/skills/old/SKILL.md\n"
	if err := os.WriteFile(filepath.Join(dir, platform.LogFileName), []byte(existing), 0o644); err != nil {
		t.Fatalf("write the existing log: %v", err)
	}

	var stdout bytes.Buffer
	logger, err := NewLogger(dir, &stdout)
	if err != nil {
		t.Fatalf("NewLogger() error = %v, want nil", err)
	}
	t.Cleanup(func() { logger.file.Close() })
	logger.Line("info", "", reconcile.KV{Key: "path", Value: "~/skills/new/SKILL.md"})

	written := logText(t, dir, platform.LogFileName)
	if !strings.HasPrefix(written, existing) {
		t.Errorf("the existing line is gone:\n%s", written)
	}
	if !strings.Contains(written, "path=~/skills/new/SKILL.md\n") {
		t.Errorf("the new line is missing:\n%s", written)
	}
}

// TestRotationKeepsOneOldFileAndNeverSplitsALine: the file rotates at 1 MiB, exactly one file
// named smd.log.1 is kept, the old file holds the older part, and the new file starts on a line
// boundary.
func TestRotationKeepsOneOldFileAndNeverSplitsALine(t *testing.T) {
	logger, dir, stdout := newSink(t)

	// Each line is about 4 KiB, so 300 of them pass 1 MiB exactly once.
	const lines = 300
	pad := strings.Repeat("x", 4000)
	for i := 0; i < lines; i++ {
		logger.Line("info", "", reconcile.KV{Key: "n", Value: strconv.Itoa(i)},
			reconcile.KV{Key: "pad", Value: pad})
	}

	if entries := namesIn(t, dir); !equalNames(entries, []string{platform.LogFileName, rotatedLogName()}) {
		t.Fatalf("the state directory holds %v, want exactly %v and %v",
			entries, platform.LogFileName, rotatedLogName())
	}

	old := logText(t, dir, rotatedLogName())
	current := logText(t, dir, platform.LogFileName)

	// Nothing was cut in the middle of a line.
	oldLines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	for i, line := range oldLines {
		if !lineStart.MatchString(line) {
			t.Fatalf("old file line %d is not a whole log line: %q", i, line)
		}
	}
	if !lineStart.MatchString(current) {
		t.Fatalf("the new file starts mid-line: %q", firstLineOf(current))
	}

	// The old file ends with the rotation record, which is written before the rename.
	if last := oldLines[len(oldLines)-1]; !strings.Contains(last, "log rotating") ||
		!strings.Contains(last, "size=") {
		t.Errorf("the old file ends with %q, want the rotation record", last)
	}
	if got := strings.Count(old+current, "log rotating"); got != 1 {
		t.Errorf("rotation records = %d, want exactly one rotation", got)
	}

	// The older part is in smd.log.1 and no line was written twice or lost.
	oldLast := counterOf(t, oldLines[len(oldLines)-2])
	currentFirst := counterOf(t, firstLineOf(current))
	if currentFirst != oldLast+1 {
		t.Errorf("the new file starts at n=%d, want %d after the old file's last line",
			currentFirst, oldLast+1)
	}

	// The old file stopped just under the 1 MiB limit, because the size is tested before a line
	// is written.
	oldSize := int64(len(old))
	if oldSize > maxLogBytes {
		t.Errorf("smd.log.1 is %d bytes, want it under the %d byte limit", oldSize, maxLogBytes)
	}
	if oldSize < maxLogBytes-int64(len(pad))-1024 {
		t.Errorf("smd.log.1 is %d bytes, want it to hold the part up to %d bytes", oldSize, maxLogBytes)
	}
	if size := int64(len(current)); size >= maxLogBytes {
		t.Errorf("smd.log is %d bytes, want the newer part under %d bytes", size, maxLogBytes)
	}

	// stdout received every line of both files, once each.
	if got, want := strings.Count(stdout.String(), "\n"), len(oldLines)+countLines(current); got != want {
		t.Errorf("stdout lines = %d, want %d, the two files' lines", got, want)
	}
}

// TestSecondRotationStillKeepsOneOldFile: the older of the two old parts is replaced and the
// directory still holds exactly one smd.log.1.
func TestSecondRotationStillKeepsOneOldFile(t *testing.T) {
	logger, dir, _ := newSink(t)

	pad := strings.Repeat("x", 4000)
	for i := 0; i < 600; i++ {
		logger.Line("info", "", reconcile.KV{Key: "n", Value: strconv.Itoa(i)},
			reconcile.KV{Key: "pad", Value: pad})
	}

	if entries := namesIn(t, dir); !equalNames(entries, []string{platform.LogFileName, rotatedLogName()}) {
		t.Fatalf("the state directory holds %v, want exactly %v and %v",
			entries, platform.LogFileName, rotatedLogName())
	}
	// The kept old file is the later part: the earliest lines are gone and smd.log.1 no longer
	// starts at the first line written.
	firstLine := firstLineOf(logText(t, dir, rotatedLogName()))
	if got := counterOf(t, firstLine); got == 0 {
		t.Errorf("smd.log.1 starts at n=%d, want the later part after the first rotation", got)
	}
}

func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func equalNames(got, want []string) bool {
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

func firstLineOf(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func countLines(text string) int {
	return strings.Count(text, "\n")
}

// counterOf proves that the two files hold one continuous sequence.
func counterOf(t *testing.T, line string) int {
	t.Helper()
	index := strings.Index(line, " n=")
	if index < 0 {
		t.Fatalf("line %q carries no n= field", line)
	}
	digits := line[index+3:]
	digits = digits[:strings.IndexAny(digits, " \n")]
	n, err := strconv.Atoi(digits)
	if err != nil {
		t.Fatalf("n= of %q is not a number: %v", line, err)
	}
	return n
}
