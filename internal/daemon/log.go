package daemon

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/platform"
	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
)

// The file rotates at 1 MiB and keeps one old file, named smd.log.1.
const (
	maxLogBytes   = 1 << 20
	rotatedSuffix = ".1"
)

// levelInfo labels the one line the sink writes itself, the rotation record.
const levelInfo = "info"

const timeFormat = "2006-01-02T15:04:05.000Z"

// Logger sends every line to stdout and to <stateDir>/smd.log, with no level filter: the
// level is a label for the reader and never a test, so nothing can silence a line.
//
// A Logger is safe for concurrent use, because the daemon loop and the watcher both log.
type Logger struct {
	mu       sync.Mutex
	stateDir string
	stdout   io.Writer
	file     *os.File
	size     int64 // bytes written to file, so a rotation check needs no stat
}

var _ reconcile.Logger = (*Logger)(nil)

// NewLogger keeps the log file open for the life of the process. The error means the durable
// record is unavailable, so the caller reports it and exits 1.
func NewLogger(stateDir string, stdout io.Writer) (*Logger, error) {
	file, size, err := openLog(stateDir)
	if err != nil {
		return nil, err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	return &Logger{stateDir: stateDir, stdout: stdout, file: file, size: size}, nil
}

// Line writes every level: there is no test on it.
func (l *Logger) Line(level, msg string, kv ...reconcile.KV) {
	line := formatLine(time.Now(), level, msg, kv)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > 0 && l.size+int64(len(line)) > maxLogBytes {
		// The size is tested before the line is written, so a line is never split across the
		// two files.
		l.rotate()
	}
	l.emit(line)
}

// formatLine pads the level to five columns and puts the message last, two spaces after the
// fields.
func formatLine(now time.Time, level, msg string, kv []reconcile.KV) []byte {
	var b strings.Builder
	b.WriteString(now.UTC().Format(timeFormat))
	b.WriteByte(' ')
	fmt.Fprintf(&b, "%-5s", strings.ToUpper(level))
	for _, field := range kv {
		b.WriteString(" " + field.Key + "=" + fieldValue(field.Value))
	}
	if msg != "" {
		b.WriteString("  " + msg)
	}
	b.WriteByte('\n')
	return []byte(b.String())
}

// fieldValue quotes only what a reader could not split on spaces, so a path without a space
// stays bare for a grep of path=<file>.
func fieldValue(value string) string {
	if value == "" || strings.Contains(value, " ") || strings.Contains(value, "\"") {
		return strconv.Quote(value)
	}
	return value
}

// emit writes one line in one call and buffers nothing across lines, so a pipe sees a line as
// it happens. The caller holds the lock.
func (l *Logger) emit(line []byte) {
	// Neither error can be reported: stderr carries command failures only.
	_, _ = l.stdout.Write(line)
	if l.file == nil {
		return
	}
	if _, err := l.file.Write(line); err != nil {
		return
	}
	l.size += int64(len(line))
}

// rotate records the rotation in the old file before renaming it to smd.log.1, so the old
// file never ends mid-event without a word about it. The caller holds the lock.
func (l *Logger) rotate() {
	record := formatLine(time.Now(), levelInfo, "log rotating to "+rotatedLogName(),
		[]reconcile.KV{
			{Key: "size", Value: strconv.FormatInt(l.size, 10)},
			{Key: "limit", Value: strconv.Itoa(maxLogBytes)},
		})
	l.emit(record)

	path := filepath.Join(l.stateDir, platform.LogFileName)
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	// A failed rename leaves the file where it is, so reopening in append mode loses
	// nothing and the next line tries the rotation again.
	_ = os.Rename(path, filepath.Join(l.stateDir, rotatedLogName()))

	file, size, err := openLog(l.stateDir)
	if err != nil {
		// A log file that cannot be reopened must not stop the daemon, and the failure has no
		// stream of its own.
		l.size = 0
		return
	}
	l.file, l.size = file, size
}

// Close releases the log file, so a one-pass command leaves nothing open behind it.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func rotatedLogName() string {
	return platform.LogFileName + rotatedSuffix
}

// openLog returns the size the file already holds, so the first rotation check starts from the
// real file.
func openLog(stateDir string) (*os.File, int64, error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, 0, fmt.Errorf("cannot create the state directory %s: %w", stateDir, err)
	}
	path := filepath.Join(stateDir, platform.LogFileName)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot open the log file %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, fmt.Errorf("cannot read the log file size %s: %w", path, err)
	}
	return file, info.Size(), nil
}
