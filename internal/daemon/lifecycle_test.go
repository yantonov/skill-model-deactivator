package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
)

// capture asserts the level and the field order without rendering a line.
type capture struct {
	lines []struct {
		level, msg string
		kv         []reconcile.KV
	}
}

func (c *capture) Line(level, msg string, kv ...reconcile.KV) {
	c.lines = append(c.lines, struct {
		level, msg string
		kv         []reconcile.KV
	}{level: level, msg: msg, kv: append([]reconcile.KV(nil), kv...)})
}

// TestStartupNamesTheConfigTheLogAndTheRoots: the roots sit in the message, because a line
// carries each key once, and the paths are abbreviated with ~.
func TestStartupNamesTheConfigTheLogAndTheRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	logger := &capture{}
	Startup(logger,
		filepath.Join(home, ".skill-model-deactivator.json"),
		filepath.Join(home, "state", "smd.log"),
		[]string{filepath.Join(home, ".agents", "skills"), filepath.Join(home, ".claude", "skills")})

	if len(logger.lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(logger.lines))
	}
	line := logger.lines[0]
	if line.level != "info" {
		t.Errorf("level = %q, want %q", line.level, "info")
	}
	wantKeys := []string{"config", "log"}
	if len(line.kv) != len(wantKeys) {
		t.Fatalf("fields = %d, want %d", len(line.kv), len(wantKeys))
	}
	for i, key := range wantKeys {
		if line.kv[i].Key != key {
			t.Errorf("field %d = %q, want %q", i, line.kv[i].Key, key)
		}
	}
	if got, want := line.kv[0].Value, "~/.skill-model-deactivator.json"; got != want {
		t.Errorf("config = %q, want %q", got, want)
	}
	if got, want := line.kv[1].Value, "~/state/smd.log"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
	if got, want := line.msg, "watched roots=~/.agents/skills ~/.claude/skills"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// TestStartupWithoutRootsHasNoMessage: nothing prints a trailing fragment.
func TestStartupWithoutRootsHasNoMessage(t *testing.T) {
	logger := &capture{}
	Startup(logger, "/a/config.json", "/a/state/smd.log", nil)

	if len(logger.lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(logger.lines))
	}
	if got := logger.lines[0].msg; got != "" {
		t.Errorf("message = %q, want no message", got)
	}
}

// TestPassStartCountsThePatterns: the line carries the pass's input, because the file count is
// not known before the pass resolves.
func TestPassStartCountsThePatterns(t *testing.T) {
	logger := &capture{}
	PassStart(logger, 3)

	if len(logger.lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(logger.lines))
	}
	line := logger.lines[0]
	if line.level != "info" {
		t.Errorf("level = %q, want %q", line.level, "info")
	}
	if len(line.kv) != 1 || line.kv[0].Key != "patterns" {
		t.Fatalf("fields = %v, want the one patterns field", line.kv)
	}
	if got, want := line.kv[0].Value, strconv.Itoa(3); got != want {
		t.Errorf("patterns = %q, want %q", got, want)
	}
}

// TestLifecycleLinesGoToTheSinkAndTheFile proves the wiring and not only the field order.
func TestLifecycleLinesGoToTheSinkAndTheFile(t *testing.T) {
	logger, dir, stdout := newSink(t)

	Startup(logger, "/a/config.json", "/a/state/smd.log", []string{"/a/skills"})
	PassStart(logger, 1)

	for _, want := range []string{"config=/a/config.json", "log=/a/state/smd.log",
		"patterns=1  reconcile started"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout.String())
		}
		if written := logText(t, dir, "smd.log"); !strings.Contains(written, want) {
			t.Errorf("the log file is missing %q:\n%s", want, written)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		if !lineStart.MatchString(line) {
			t.Errorf("line = %q, want the line shape", line)
		}
	}
}

// TestShutdownIsOneInfoLine: one line at level info, so a count of the line is a count of the
// stops.
func TestShutdownIsOneInfoLine(t *testing.T) {
	logger, dir, stdout := newSink(t)

	Shutdown(logger)

	if got := strings.Count(stdout.String(), "shutdown"); got != 1 {
		t.Errorf("shutdown mentions = %d, want 1:\n%s", got, stdout.String())
	}
	line := strings.SplitN(strings.TrimSuffix(stdout.String(), "\n"), "\n", 2)[0]
	if !lineStart.MatchString(line) {
		t.Errorf("line = %q, want the line shape", line)
	}
	if !strings.Contains(line, " INFO  ") {
		t.Errorf("line = %q, want level info", line)
	}
	if written := logText(t, dir, "smd.log"); written != stdout.String() {
		t.Errorf("the file holds %q, want the one stdout line %q", written, stdout.String())
	}
}

// TestNewLoggerOpensTheLogFile: the file exists for `smd log` before the first line is
// written.
func TestNewLoggerOpensTheLogFile(t *testing.T) {
	logger, dir, _ := newSink(t)

	if _, err := os.Stat(filepath.Join(dir, "smd.log")); err != nil {
		t.Fatalf("os.Stat() = %v, want the opened log file", err)
	}
	logger.Line("info", "", reconcile.KV{Key: "path", Value: "~/a/SKILL.md"})
	if !strings.Contains(logText(t, dir, "smd.log"), "path=~/a/SKILL.md\n") {
		t.Error("the file did not receive the line")
	}
}
