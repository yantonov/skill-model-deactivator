package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/config"
	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
)

const acceptLimit = time.Second

// disableLine is the canonical line, so the readiness probe can take it out of a fixture.
const disableLine = "disable-model-invocation: true\n"

// serveHome names one pattern, so the pass reaches the fixture skills and nothing else.
func serveHome(t *testing.T) string {
	t.Helper()
	home := newHome(t)
	writeConfig(t, home, `{"patterns": ["~/skills/*/SKILL.md"]}`)
	return home
}

// daemonHome adds a short debounce, so an event reaches a pass well inside acceptLimit even on
// a loaded machine.
func daemonHome(t *testing.T) string {
	t.Helper()
	home := newHome(t)
	writeConfig(t, home, `{"patterns": ["~/skills/*/SKILL.md"], "debounce": "20ms"}`)
	return home
}

// writeSkillFile returns the path it wrote.
func writeSkillFile(t *testing.T, home, name, content string) string {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// linesOf drops the empty lines and the timestamp, so a test that asserts the rest of a line
// needs no regexp.
func linesOf(rendered string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(rendered, "\n"), "\n") {
		if line == "" {
			continue
		}
		if _, rest, found := strings.Cut(line, " "); found {
			line = rest
		}
		lines = append(lines, line)
	}
	return lines
}

// summaryOf is the last stdout line of a pass.
func summaryOf(t *testing.T, stdout string) string {
	t.Helper()
	lines := linesOf(stdout)
	if len(lines) == 0 {
		t.Fatal("the pass printed nothing, want at least the summary line")
	}
	return lines[len(lines)-1]
}

// serveDaemon ends the daemon the way a signal ends the real one: the context is cancelled and
// the same code path returns. The buffers are written from the daemon goroutine, so the caller
// reads them only after stop, and stop is idempotent because a test that fails early still
// needs the goroutine gone.
func serveDaemon(t *testing.T, home string) (stop func() (stdout, stderr string, code int)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	previous := daemonSignals
	daemonSignals = func() (context.Context, context.CancelFunc) {
		return ctx, func() {}
	}

	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"serve"}, &out, &errOut)
	}()

	var once sync.Once
	code := 0
	stop = func() (string, string, int) {
		once.Do(func() {
			cancel()
			select {
			case code = <-done:
			case <-time.After(5 * time.Second):
				t.Error("serve did not stop after its context was done")
			}
			daemonSignals = previous
		})
		return out.String(), errOut.String(), code
	}
	t.Cleanup(func() { stop() })
	return stop
}

func waitForFile(t *testing.T, path string, want []byte) {
	t.Helper()
	deadline := time.Now().Add(acceptLimit)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && bytes.Equal(data, want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	t.Fatalf("%s = %q after %s, want %q", path, data, acceptLimit, want)
}

// waitUntilWatching makes the watcher's liveness visible from outside, so a test measures the
// watcher and not the startup pass. It rewrites the file without the canonical key and polls for
// the patch. One patch can still be the startup pass, which may run after the rewrite, so it
// needs two consecutive patches: the startup pass runs once and cannot explain the second.
func waitUntilWatching(t *testing.T, path string, canonical []byte) {
	t.Helper()
	unpatched := bytes.ReplaceAll(canonical, []byte(disableLine), nil)
	confirmed := 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && confirmed < 2 {
		if err := os.WriteFile(path, unpatched, 0o644); err != nil {
			t.Fatalf("write the readiness probe: %v", err)
		}
		probeBy := time.Now().Add(acceptLimit)
		patched := false
		for time.Now().Before(probeBy) {
			data, err := os.ReadFile(path)
			if err == nil && bytes.Equal(data, canonical) {
				patched = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if patched {
			confirmed++
		}
	}
	if confirmed < 2 {
		t.Fatalf("the daemon never became ready to watch %s", path)
	}
}

// TestServeRunsTheStartupPassBeforeTheWatcher: a file that needs a patch is patched by the pass
// that runs before the watcher exists, so no watcher event is needed.
func TestServeRunsTheStartupPassBeforeTheWatcher(t *testing.T) {
	home := serveHome(t)
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")

	stop := serveDaemon(t, home)
	// A patch on its own is not proof of the order: the proof is that the startup line, the pass
	// start, and the pass finish all come before the first watcher event.
	waitForFile(t, path, []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n"))

	stdout, stderr, code := stop()
	if code != 0 {
		t.Errorf("exit code = %d, want 0: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	// The startup line and the pass lines come from the pass at startup, not from an event.
	for _, want := range []string{"INFO  config=~/.skill-model-deactivator.json",
		"reconcile started", "reconcile finished"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
	// The startup pass finished before the watcher existed, so no watcher event can come before
	// its summary line.
	finished := strings.Index(stdout, "reconcile finished")
	if event := strings.Index(stdout, "watcher event"); event >= 0 && event < finished {
		t.Errorf("a watcher event was logged before the startup pass finished:\n%s", stdout)
	}
}

// TestServePicksUpAManualEdit: an edit of a matched SKILL.md while the daemon runs is
// reconciled within 1 s and the file ends in the desired state.
func TestServePicksUpAManualEdit(t *testing.T) {
	home := daemonHome(t)
	canonical := []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n")
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")

	stop := serveDaemon(t, home)
	waitForFile(t, path, canonical)
	// The startup pass already patched the file, so that is not yet evidence that the watcher is
	// live. The probe is.
	waitUntilWatching(t, path, canonical)

	// The edit a user makes by hand: the key goes back to a non-canonical value.
	if err := os.WriteFile(path, []byte("---\nname: kibana\ndisable-model-invocation: yes\n---\n"), 0o644); err != nil {
		t.Fatalf("write the manual edit: %v", err)
	}
	waitForFile(t, path, canonical)
	stop()
}

// TestServePicksUpASkillDirectoryCreatedWhileItRuns: a skill directory created after start is
// picked up within 1 s.
func TestServePicksUpASkillDirectoryCreatedWhileItRuns(t *testing.T) {
	home := daemonHome(t)
	existing := writeSkillFile(t, home, "skills/kibana/SKILL.md",
		"---\nname: kibana\ndisable-model-invocation: true\n---\n")

	stop := serveDaemon(t, home)
	// The daemon must be watching before the directory appears, and only a change that lands
	// shows that it is.
	waitUntilWatching(t, existing, []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n"))

	path := writeSkillFile(t, home, "skills/created/SKILL.md", "---\nname: created\n---\n")
	waitForFile(t, path, []byte("---\nname: created\ndisable-model-invocation: true\n---\n"))
	stop()
}

// TestServeKeepsTheStartupPatterns: delete a path from `patterns` while the daemon runs, and
// that skill stays disabled, because the daemon keeps the values it started with.
func TestServeKeepsTheStartupPatterns(t *testing.T) {
	home := daemonHome(t)
	kept := writeSkillFile(t, home, "skills/kept/SKILL.md", "---\nname: kept\n---\n")
	dropped := writeSkillFile(t, home, "skills/dropped/SKILL.md", "---\nname: dropped\n---\n")

	stop := serveDaemon(t, home)
	canonical := func(name string) []byte {
		return []byte("---\nname: " + name + "\ndisable-model-invocation: true\n---\n")
	}
	waitForFile(t, kept, canonical("kept"))
	waitForFile(t, dropped, canonical("dropped"))
	waitUntilWatching(t, kept, canonical("kept"))

	// The edited config reaches only the startup pattern of the kept skill.
	writeConfig(t, home, `{"patterns": ["~/skills/kept/SKILL.md"]}`)
	// A daemon that re-read the config would skip the dropped file; one that keeps the startup
	// patterns patches it.
	if err := os.WriteFile(dropped, []byte("---\nname: dropped\n---\n"), 0o644); err != nil {
		t.Fatalf("reset the dropped file: %v", err)
	}
	waitForFile(t, dropped, canonical("dropped"))

	stdout, _, code := stop()
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "path=~/skills/dropped/SKILL.md") {
		t.Errorf("the daemon did not keep the startup pattern for the dropped path:\n%s", stdout)
	}

	// A restart reads the edited config, so the dropped path is no longer touched.
	restart := serveDaemon(t, home)
	waitUntilWatching(t, kept, canonical("kept"))
	restartOut, _, _ := restart()
	if strings.Contains(restartOut, "path=~/skills/dropped/SKILL.md") {
		t.Errorf("the restarted daemon touched the dropped path:\n%s", restartOut)
	}
}

// TestServeNeverReadsTheConfigAgain: the config is read once, at startup. The file is
// overwritten with one that matches nothing, a pass is forced, and the pass still uses the
// startup values. The open count proves the file was not opened again. Then the file is
// replaced with unparseable bytes, and the daemon keeps running with no parse error.
func TestServeNeverReadsTheConfigAgain(t *testing.T) {
	home := daemonHome(t)
	canonical := []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n")
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md",
		"---\nname: kibana\ndisable-model-invocation: true\n---\n")

	opens := 0
	previousReadFile := config.ReadFile
	config.ReadFile = func(name string) ([]byte, error) {
		opens++
		return previousReadFile(name)
	}
	t.Cleanup(func() { config.ReadFile = previousReadFile })

	stop := serveDaemon(t, home)
	waitUntilWatching(t, path, canonical)

	// A config that would match nothing, if it were read again.
	writeConfig(t, home, `{"patterns": ["~/nothing/*/SKILL.md"]}`)
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\n"), 0o644); err != nil {
		t.Fatalf("reset the skill file: %v", err)
	}
	// A write to the matched file is a forced pass.
	waitForFile(t, path, canonical)

	// A daemon that re-read the config would log a parse error and might stop; this one never
	// opens it.
	writeConfig(t, home, "not json at all")
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\n"), 0o644); err != nil {
		t.Fatalf("reset the skill file again: %v", err)
	}
	waitForFile(t, path, canonical)

	stdout, stderr, code := stop()
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if opens != 1 {
		t.Errorf("the config file was opened %d times, want 1", opens)
	}
	if strings.Contains(stdout, "not valid JSON") || strings.Contains(stdout, "config") &&
		strings.Contains(stdout, "ERROR") {
		t.Errorf("the daemon reported a config parse error:\n%s", stdout)
	}
}

// TestServeAPassErrorDoesNotStopTheLoop: one error line for a pass that failed as a whole, and
// a later trigger still produces a pass.
func TestServeAPassErrorDoesNotStopTheLoop(t *testing.T) {
	home := daemonHome(t)
	canonical := []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n")
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md",
		"---\nname: kibana\ndisable-model-invocation: true\n---\n")

	// A home directory that cannot be expanded makes every pass fail as a whole.
	previousHome := os.Getenv("HOME")
	previousProfile := os.Getenv("USERPROFILE")

	stop := serveDaemon(t, home)
	waitUntilWatching(t, path, canonical)

	if err := os.Setenv("HOME", ""); err != nil {
		t.Fatalf("unset HOME: %v", err)
	}
	if err := os.Setenv("USERPROFILE", ""); err != nil {
		t.Fatalf("unset USERPROFILE: %v", err)
	}
	// A write to the matched file is a forced pass, which now fails as a whole.
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\n"), 0o644); err != nil {
		t.Fatalf("write the skill file: %v", err)
	}
	waitForLog(t, home, "reconcile failed")

	os.Setenv("HOME", previousHome)
	os.Setenv("USERPROFILE", previousProfile)

	// The loop is still alive, so the next trigger patches the file again.
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\n"), 0o644); err != nil {
		t.Fatalf("write the skill file again: %v", err)
	}
	waitForFile(t, path, canonical)

	stdout, _, code := stop()
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got := strings.Count(stdout, "reconcile failed"); got != 1 {
		t.Errorf("error lines = %d, want 1:\n%s", got, stdout)
	}
}

// waitForLog treats a missing file as a poll: the sink creates it only when it opens.
func waitForLog(t *testing.T, home, want string) {
	t.Helper()
	path := filepath.Join(stateDirOf(t, home), "smd.log")
	deadline := time.Now().Add(acceptLimit)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the log did not appear: %v", err)
	}
	t.Fatalf("the log did not contain %q within %s:\n%s", want, acceptLimit, data)
}

func TestServeOnceDryRunPlansAnInsertAndWritesNothing(t *testing.T) {
	for _, args := range [][]string{
		{"serve", "--once", "--dry-run"},
		{"serve", "--dry-run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := serveHome(t)
			path := writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\nbody\n")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read the fixture: %v", err)
			}

			stdout, stderr, code := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0: %s", code, stderr)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}

			want := "INFO  path=~/skills/kibana/SKILL.md desired=true reason=absent action=inserted result=would-patch\n"
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout does not contain %q:\n%s", want, stdout)
			}
			if got := summaryOf(t, stdout); !strings.HasPrefix(got,
				"INFO  watched=1 changed=1 wrote=0 skipped=0 failed=0  reconcile finished duration=") {
				t.Errorf("summary line = %q, want watched 1, changed 1, wrote 0", got)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read the fixture after the pass: %v", err)
			}
			if string(after) != string(before) {
				t.Errorf("the file changed:\n before %q\n after  %q", before, after)
			}
		})
	}
}

// TestServeOnceAddsExactlyOneLine asserts a byte-level diff: the one added line is the last
// line of the region and every other byte is identical.
func TestServeOnceAddsExactlyOneLine(t *testing.T) {
	home := serveHome(t)
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\nbody\n")
	before := readBytes(t, path)

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	want := "INFO  path=~/skills/kibana/SKILL.md desired=true reason=absent action=inserted result=ok\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout does not contain %q:\n%s", want, stdout)
	}
	if got := summaryOf(t, stdout); !strings.HasPrefix(got,
		"INFO  watched=1 changed=1 wrote=1 skipped=0 failed=0  reconcile finished duration=") {
		t.Errorf("summary line = %q, want wrote 1", got)
	}

	wantBytes := []byte("---\nname: kibana\ndisable-model-invocation: true\n---\nbody\n")
	if got := readBytes(t, path); !bytes.Equal(got, wantBytes) {
		t.Errorf("the file is\n%q\nwant\n%q", got, wantBytes)
	}
	if got := addedBytes(before, readBytes(t, path)); string(got) != "disable-model-invocation: true\n" {
		t.Errorf("the diff added %q, want exactly the canonical line", got)
	}
}

// TestServeOnceRewritesYesInOneLine: one line changed to the canonical spelling.
func TestServeOnceRewritesYesInOneLine(t *testing.T) {
	home := serveHome(t)
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md",
		"---\nname: kibana\ndisable-model-invocation: yes\n---\n")

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	want := "INFO  path=~/skills/kibana/SKILL.md desired=true reason=non-canonical action=rewritten result=ok\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout does not contain %q:\n%s", want, stdout)
	}
	wantBytes := []byte("---\nname: kibana\ndisable-model-invocation: true\n---\n")
	if got := readBytes(t, path); !bytes.Equal(got, wantBytes) {
		t.Errorf("the file is %q, want %q", got, wantBytes)
	}
}

// TestServeOnceIsIdempotent: the first pass writes, the second plans ActionNone for every file
// and writes nothing.
func TestServeOnceIsIdempotent(t *testing.T) {
	home := serveHome(t)
	paths := []string{
		writeSkillFile(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n"),
		writeSkillFile(t, home, "skills/b/SKILL.md", "---\nname: b\ndisable-model-invocation: on\n---\n"),
	}

	if _, stderr, code := runCLI(t, "serve", "--once"); code != 0 {
		t.Fatalf("first pass exit code = %d, want 0: %s", code, stderr)
	}
	first := make(map[string][]byte, len(paths))
	for _, path := range paths {
		first[path] = readBytes(t, path)
	}

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("second pass exit code = %d, want 0: %s", code, stderr)
	}
	if strings.Contains(stdout, "result=ok") || strings.Contains(stdout, "result=would-patch") {
		t.Errorf("the second pass patched a file:\n%s", stdout)
	}
	if got := summaryOf(t, stdout); !strings.Contains(got, "changed=0 wrote=0") {
		t.Errorf("summary line = %q, want changed=0 wrote=0", got)
	}
	for _, path := range paths {
		if got := readBytes(t, path); !bytes.Equal(got, first[path]) {
			t.Errorf("the second pass changed %s:\n before %q\n after  %q", path, first[path], got)
		}
	}
}

// TestServeOnceWritesTwoFilesWithEqualContent: dedupe compares file identity and not content,
// so two separate files with the same bytes are two targets.
func TestServeOnceWritesTwoFilesWithEqualContent(t *testing.T) {
	content := "---\nname: same\n---\nbody\n"
	home := serveHome(t)
	paths := []string{
		writeSkillFile(t, home, "skills/a/SKILL.md", content),
		writeSkillFile(t, home, "skills/b/SKILL.md", content),
	}

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if got := summaryOf(t, stdout); !strings.Contains(got, "wrote=2") {
		t.Errorf("summary line = %q, want wrote=2", got)
	}
	for _, path := range paths {
		want := "---\nname: same\ndisable-model-invocation: true\n---\nbody\n"
		if got := readBytes(t, path); string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

// readBytes lets a test compare whole files and not lines.
func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// addedBytes fails when the two are not a single insertion, which is what a one-line add must
// be.
func addedBytes(before, after []byte) []byte {
	start := 0
	for start < len(before) && start < len(after) && before[start] == after[start] {
		start++
	}
	suffix := 0
	for suffix < len(before)-start && suffix < len(after)-start &&
		before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	return after[start : len(after)-suffix]
}

func TestServeOnceOnACanonicalTreePrintsNoPatch(t *testing.T) {
	home := serveHome(t)
	canonical := "---\nname: kibana\ndisable-model-invocation: true\n---\n"
	paths := []string{
		writeSkillFile(t, home, "skills/a/SKILL.md", canonical),
		writeSkillFile(t, home, "skills/b/SKILL.md", canonical),
	}
	// A read-only file that already holds the desired state is never tested for writability.
	readOnly := writeSkillFile(t, home, "skills/c/SKILL.md", canonical)
	readOnlyMode(t, readOnly)
	paths = append(paths, readOnly)

	before := make(map[string]os.FileInfo, len(paths))
	content := make(map[string][]byte, len(paths))
	for _, path := range paths {
		before[path] = statFile(t, path)
		content[path] = readBytes(t, path)
	}
	dir := filepath.Dir(paths[0])
	dirBefore := statFile(t, dir)
	dirListing := listing(t, dir)

	// Watch for the whole run, so a temp file that appears and vanishes inside one write is
	// still caught.
	stop := watchForTempFiles(home)
	stdout, stderr, code := runCLI(t, "serve", "--once")
	seen := stop()

	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}

	lines := linesOf(stdout)
	// The pass contributes one debug line per file and the summary.
	if len(lines) != 6 {
		t.Fatalf("stdout lines = %d, want the two lifecycle lines, one debug line per file, and the summary:\n%s",
			len(lines), stdout)
	}
	if strings.Contains(stdout, "INFO  path=") || strings.Contains(stdout, "ERROR") ||
		strings.Contains(stdout, "WARN") {
		t.Errorf("the pass reported a patch, an error, or a warning:\n%s", stdout)
	}
	if want := "INFO  watched=3 changed=0 wrote=0 skipped=0 failed=0  reconcile finished duration="; !strings.HasPrefix(lines[len(lines)-1], want) {
		t.Errorf("summary line = %q, want %q", lines[len(lines)-1], want)
	}

	for _, path := range paths {
		if got := readBytes(t, path); !bytes.Equal(got, content[path]) {
			t.Errorf("%s changed: %q, want %q", path, got, content[path])
		}
		after := statFile(t, path)
		if !after.ModTime().Equal(before[path].ModTime()) {
			t.Errorf("%s mtime = %v, want %v", path, after.ModTime(), before[path].ModTime())
		}
		if after.Mode() != before[path].Mode() {
			t.Errorf("%s mode = %v, want %v", path, after.Mode(), before[path].Mode())
		}
	}
	if after := statFile(t, dir); !after.ModTime().Equal(dirBefore.ModTime()) {
		t.Errorf("%s mtime = %v, want %v", dir, after.ModTime(), dirBefore.ModTime())
	}
	if entries := listing(t, dir); entries != dirListing {
		t.Errorf("directory listing = %q, want %q", entries, dirListing)
	}
	if len(seen) != 0 {
		t.Errorf("a temp file appeared during the pass: %v", seen)
	}
	if found := tempFiles(t, home); len(found) != 0 {
		t.Errorf("the pass left temp files: %v", found)
	}
}

// readOnlyMode makes a pass that needs to patch path fail at the write.
func readOnlyMode(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })
}

func statFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func listing(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return strings.Join(names, ",")
}

func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(d.Name(), ".smd-tmp") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return found
}

// watchForTempFiles polls home every millisecond, so a temp file that appears and vanishes
// inside one write is still caught. It never calls t from the goroutine, so the polling cannot
// fail a test after it ended.
func watchForTempFiles(home string) func() []string {
	var mu sync.Mutex
	seen := make(map[string]bool)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if strings.HasSuffix(d.Name(), ".smd-tmp") {
					mu.Lock()
					seen[path] = true
					mu.Unlock()
				}
				return nil
			})
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return func() []string {
		close(done)
		<-stopped
		mu.Lock()
		defer mu.Unlock()
		paths := make([]string, 0, len(seen))
		for path := range seen {
			paths = append(paths, path)
		}
		return paths
	}
}

func TestServeOnceWarnsOnFrontmatterThatDoesNotParse(t *testing.T) {
	home := serveHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: a\nname: b\n---\n")

	stdout, stderr, code := runCLI(t, "serve", "--once", "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if want := "WARN  path=~/skills/kibana/SKILL.md result=skipped  "; !strings.Contains(stdout, want) {
		t.Errorf("stdout does not contain %q:\n%s", want, stdout)
	}
	if got := summaryOf(t, stdout); !strings.Contains(got, "watched=1 changed=0 wrote=0 skipped=1 failed=0") {
		t.Errorf("summary line = %q, want skipped=1", got)
	}
}

func TestServeOnceWarnsOnAStalePattern(t *testing.T) {
	home := serveHome(t)
	writeConfig(t, home, `{"patterns": ["~/skills/*/SKILL.md", "~/skills/gone-*/SKILL.md"]}`)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")

	stdout, stderr, code := runCLI(t, "serve", "--once", "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if !strings.Contains(stdout, "WARN  pattern=") || !strings.Contains(stdout, "stale") {
		t.Errorf("stdout does not warn about the stale pattern:\n%s", stdout)
	}
}

func TestServeOnceReportsAFileThatCannotBeRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod on Windows only toggles the read-only bit, so a file stays readable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file, so the fixture cannot fail the read")
	}
	home := serveHome(t)
	broken := writeSkillFile(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")
	writeSkillFile(t, home, "skills/b/SKILL.md", "---\nname: b\n---\n")
	if err := os.Chmod(broken, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", broken, err)
	}
	t.Cleanup(func() { os.Chmod(broken, 0o644) })

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 even with a failed file: %s", code, stderr)
	}
	// The two lifecycle lines come first, then the error, the patch, and the summary.
	lines := linesOf(stdout)
	if len(lines) != 5 {
		t.Fatalf("stdout lines = %d, want the two lifecycle lines, two file lines, and the summary:\n%s",
			len(lines), stdout)
	}
	if !strings.HasPrefix(lines[2], "ERROR path=~/skills/a/SKILL.md ") {
		t.Errorf("error line = %q, want it for the unreadable file", lines[2])
	}
	if !strings.Contains(lines[3], "path=~/skills/b/SKILL.md") {
		t.Errorf("patch line = %q, want it for the other file", lines[3])
	}
	if got := summaryOf(t, stdout); !strings.Contains(got, "changed=1 wrote=1 skipped=0 failed=1") {
		t.Errorf("summary line = %q, want the other file written and failed=1", got)
	}
}

// TestServeOnceReportsAReadOnlyFileThatNeedsAPatch covers one error line naming the path, the
// action, and the OS error; no warn for the same failure; failed=1 and not wrote; the rest of
// the batch still written; the file byte-identical; and a second pass with the same single
// error line.
//
// The fixture blocks what the platform actually needs. On Windows a read-only file blocks the
// rename, which is the only test the writer makes there. On Unix the writer tests the parent
// directory, because the rename needs the directory and not the file.
func TestServeOnceReportsAReadOnlyFileThatNeedsAPatch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits of the fixture, so the write cannot fail")
	}

	home := serveHome(t)
	blocked := writeSkillFile(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")
	blockWrite(t, blocked)
	writable := writeSkillFile(t, home, "skills/b/SKILL.md", "---\nname: b\n---\n")
	before := readBytes(t, blocked)

	for pass := 1; pass <= 2; pass++ {
		stdout, stderr, code := runCLI(t, "serve", "--once")
		if code != 0 {
			t.Fatalf("pass %d exit code = %d, want 0: %s", pass, code, stderr)
		}

		errs := linesWith(stdout, "ERROR path=~/skills/a/SKILL.md ")
		if len(errs) != 1 {
			t.Fatalf("pass %d error lines for the blocked file = %d, want 1:\n%s", pass, len(errs), stdout)
		}
		for _, key := range []string{"action=inserted", "error="} {
			if !strings.Contains(errs[0], key) {
				t.Errorf("pass %d error line lacks %q: %q", pass, key, errs[0])
			}
		}
		if warns := linesWith(stdout, "WARN"); len(warns) != 0 {
			t.Errorf("pass %d warned as well as failed: %v", pass, warns)
		}
		if got := summaryOf(t, stdout); !strings.Contains(got, "changed=2 wrote=1 skipped=0 failed=1") {
			t.Errorf("pass %d summary line = %q, want wrote=1 and failed=1", pass, got)
		}

		if got := readBytes(t, blocked); !bytes.Equal(got, before) {
			t.Errorf("pass %d changed the blocked file: %q, want %q", pass, got, before)
		}
		want := "---\nname: b\ndisable-model-invocation: true\n---\n"
		if got := readBytes(t, writable); string(got) != want {
			t.Errorf("pass %d did not write the other file: %q, want %q", pass, got, want)
		}
		if found := tempFiles(t, home); len(found) != 0 {
			t.Errorf("pass %d left temp files: %v", pass, found)
		}
		// Put the second file back, so the next pass plans a patch for it again.
		writeSkillFile(t, home, "skills/b/SKILL.md", "---\nname: b\n---\n")
	}
}

// blockWrite blocks what this platform makes the write depend on: the file on Windows, the
// parent directory elsewhere.
func blockWrite(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if err := os.Chmod(path, 0o444); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
		t.Cleanup(func() { os.Chmod(path, 0o644) })
		return
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
}

func linesWith(stdout, want string) []string {
	var found []string
	for _, line := range linesOf(stdout) {
		if strings.Contains(line, want) {
			found = append(found, line)
		}
	}
	return found
}

// TestServeOnceWritesOnTheThirdRenameAttempt fails the first two renames and lets the third
// succeed, through the command line, so the planner and the writer are proved to be wired
// together.
func TestServeOnceWritesOnTheThirdRenameAttempt(t *testing.T) {
	home := serveHome(t)
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")
	calls := failRenames(t, 3)

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if *calls != 3 {
		t.Errorf("rename calls = %d, want 3", *calls)
	}
	info := linesWith(stdout, "INFO  path=")
	if len(info) != 1 {
		t.Fatalf("info lines = %d, want the one write: %v", len(info), info)
	}
	if !strings.Contains(info[0], "result=ok") {
		t.Errorf("info line = %q, want result=ok", info[0])
	}
	if errs := linesWith(stdout, "ERROR"); len(errs) != 0 {
		t.Errorf("error lines = %v, want none", errs)
	}
	want := "---\nname: kibana\ndisable-model-invocation: true\n---\n"
	if got := readBytes(t, path); string(got) != want {
		t.Errorf("the file is %q, want %q", got, want)
	}
	if found := tempFiles(t, home); len(found) != 0 {
		t.Errorf("the pass left temp files: %v", found)
	}
}

// TestServeOnceReportsAFileWhenEveryRenameFails: one error line, the original byte-identical,
// no temp file, and the file counted in failed=.
func TestServeOnceReportsAFileWhenEveryRenameFails(t *testing.T) {
	home := serveHome(t)
	content := "---\nname: kibana\n---\n"
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", content)
	calls := failRenames(t, -1)

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 even with a failed file: %s", code, stderr)
	}
	if *calls != 3 {
		t.Errorf("rename calls = %d, want 3", *calls)
	}
	errs := linesWith(stdout, "ERROR path=~/skills/kibana/SKILL.md ")
	if len(errs) != 1 {
		t.Fatalf("error lines = %d, want 1:\n%s", len(errs), stdout)
	}
	if !strings.Contains(errs[0], "error=") {
		t.Errorf("error line = %q, want it to carry the OS error text", errs[0])
	}
	if got := summaryOf(t, stdout); !strings.Contains(got, "changed=1 wrote=0 skipped=0 failed=1") {
		t.Errorf("summary line = %q, want wrote=0 and failed=1", got)
	}
	if got := readBytes(t, path); string(got) != content {
		t.Errorf("the original changed: %q, want %q", got, content)
	}
	if found := tempFiles(t, home); len(found) != 0 {
		t.Errorf("the pass left temp files: %v", found)
	}
}

// failRenames fails the renames before successOn and then calls the real rename. A successOn of
// 0 or less fails every attempt. It returns the call counter.
func failRenames(t *testing.T, successOn int) *int {
	t.Helper()
	previous := reconcile.Rename
	calls := 0
	reconcile.Rename = func(oldpath, newpath string) error {
		calls++
		if successOn <= 0 || calls < successOn {
			return os.ErrPermission
		}
		return previous(oldpath, newpath)
	}
	t.Cleanup(func() { reconcile.Rename = previous })
	return &calls
}

// TestServeOncePreservesTheFileMode sets 0644 on a fixture and asserts it after the write.
// Windows is skipped because os.Chmod there only toggles the read-only bit.
func TestServeOncePreservesTheFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod on Windows only toggles the read-only bit, so 0644 cannot be set and asserted")
	}
	home := serveHome(t)
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}

	if _, stderr, code := runCLI(t, "serve", "--once"); code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}

	if got := statFile(t, path).Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %o, want 644 after the rename", got)
	}
}

func TestServeOnceExitsOneWhenTheConfigCannotBeRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// A directory at the config path makes the read fail with something other than "does not
	// exist", which is the one load error.
	if err := os.Mkdir(filepath.Join(home, ".skill-model-deactivator.json"), 0o755); err != nil {
		t.Fatalf("create the config directory: %v", err)
	}

	stdout, stderr, code := runCLI(t, "serve", "--once", "--dry-run")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.HasPrefix(stderr, "smd: cannot read the config file") {
		t.Errorf("stderr = %q, want it to name the unreadable config", stderr)
	}
}

func TestServeHelpDropsTheTemporaryState(t *testing.T) {
	stdout, stderr, code := runCLI(t, "serve", "-h")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{
		"smd serve - reconcile the matched skills",
		"smd serve [--once] [--dry-run]",
		"--once      Run one reconcile and exit.",
		"--dry-run   Run one dry pass, report the planned changes, write nothing, and exit.",
		"Without a flag, serve reconciles once at start and then keeps the matched files in sync.",
		"--dry-run without --once runs one dry pass and exits: it does not start the watch loop.",
		"~/.skill-model-deactivator.json",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("serve help does not contain %q:\n%s", want, stdout)
		}
	}
	for _, stale := range []string{"no writer", "dry run", "TEMPORARY"} {
		if strings.Contains(stdout, stale) {
			t.Errorf("serve help still says %q:\n%s", stale, stdout)
		}
	}
}

func TestServeOncePrintsTildePaths(t *testing.T) {
	home := serveHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")

	stdout, _, code := runCLI(t, "serve", "--once", "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.Contains(stdout, filepath.ToSlash(home)) {
		t.Errorf("stdout prints the home directory instead of ~:\n%s", stdout)
	}
	if !strings.Contains(stdout, "path=~/skills/kibana/SKILL.md") {
		t.Errorf("stdout does not carry the ~ path:\n%s", stdout)
	}
}

func stateDirOf(t *testing.T, home string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "skill-model-deactivator")
	}
	return filepath.Join(home, ".local", "state", "skill-model-deactivator")
}

func logLinesOf(t *testing.T, home string) []string {
	t.Helper()
	path := filepath.Join(stateDirOf(t, home), "smd.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return linesOf(string(data))
}

// TestServeWritesEveryLineToStdoutAndTheLogFile: the line reaches stdout and the same line
// reaches <state dir>/smd.log. The pipe stands for the terminal case as well, because the tool
// writes the file whatever stdout points at.
func TestServeWritesEveryLineToStdoutAndTheLogFile(t *testing.T) {
	home := serveHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}

	printed := linesOf(stdout)
	written := logLinesOf(t, home)
	if len(printed) == 0 {
		t.Fatal("stdout is empty, want the pass lines")
	}
	if len(written) != len(printed) {
		t.Fatalf("the file holds %d lines, want the %d stdout lines:\n%s",
			len(written), len(printed), strings.Join(written, "\n"))
	}
	for i := range printed {
		if printed[i] != written[i] {
			t.Errorf("line %d differs:\n stdout %q\n file   %q", i, printed[i], written[i])
		}
	}
}

// TestServeLogLinesMatchTheLineFormat: every line starts with the timestamp, then a padded
// level, and a write line carries path desired reason action result.
func TestServeLogLinesMatchTheLineFormat(t *testing.T) {
	home := serveHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}

	stamp := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z (DEBUG|INFO |WARN |ERROR) `)
	for i, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		if !stamp.MatchString(line) {
			t.Errorf("line %d does not match the line shape: %q", i, line)
		}
	}

	writes := linesWith(stdout, "INFO  path=~/skills/kibana/SKILL.md")
	if len(writes) != 1 {
		t.Fatalf("write lines = %d, want 1:\n%s", len(writes), stdout)
	}
	// This test only asserts that the field order survived the sink.
	wantFields := []string{"path=~/skills/kibana/SKILL.md", "desired=true", "reason=absent",
		"action=inserted", "result=ok"}
	last := -1
	for _, field := range wantFields {
		index := strings.Index(writes[0], field)
		if index < 0 {
			t.Fatalf("write line lacks %q: %q", field, writes[0])
		}
		if index < last {
			t.Errorf("write line puts %q out of order: %q", field, writes[0])
		}
		last = index
	}
}

// TestServeNeverLogsToStderr uses its own buffers, so a refactor cannot quietly merge the two
// streams.
func TestServeNeverLogsToStderr(t *testing.T) {
	home := serveHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")

	stdout, stderr, _ := runCLI(t, "serve", "--once")
	if stderr != "" {
		t.Errorf("stderr = %q, want empty: a log line never goes there", stderr)
	}
	if stdout == "" {
		t.Error("stdout is empty, want the log lines")
	}
	for _, level := range []string{"DEBUG ", "INFO  ", "WARN  ", "ERROR "} {
		if strings.Contains(stderr, level) {
			t.Errorf("stderr carries a %s level line: %q", level, stderr)
		}
	}
}

// TestValidateNeverPrintsAFindingOnStdout: a finding goes to stderr and stdout stays empty.
func TestValidateNeverPrintsAFindingOnStdout(t *testing.T) {
	home := newHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")
	writeConfig(t, home, `{"patterns": ["~/skills/*/SKILL.md", "~/skills/gone-*/SKILL.md"]}`)

	stdout, stderr, code := runCLI(t, "config", "validate")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for warnings only: %s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty: a finding never goes there", stdout)
	}
	if !strings.Contains(stderr, "stale") {
		t.Errorf("stderr = %q, want the stale finding", stderr)
	}
	for _, level := range []string{"DEBUG ", "INFO  "} {
		if strings.Contains(stdout, level) {
			t.Errorf("stdout carries a log level %q", level)
		}
	}
}

// TestServeExitsOneWhenTheStateDirectoryCannotBeCreated: one error on stderr and a non-zero
// exit, not a silent no-op.
func TestServeExitsOneWhenTheStateDirectoryCannotBeCreated(t *testing.T) {
	// A regular file where the state directory belongs makes the creation fail on every
	// platform.
	home := serveHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")
	dir := stateDirOf(t, home)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatalf("create the parent directory: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("create the blocking file: %v", err)
	}

	stdout, stderr, code := runCLI(t, "serve", "--once")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty: the sink opened no destination", stdout)
	}
	if lines := linesOf(stderr); len(lines) != 1 {
		t.Errorf("stderr lines = %d, want 1: %q", len(lines), stderr)
	}
	if !strings.Contains(stderr, "state directory") {
		t.Errorf("stderr = %q, want it to name the state directory", stderr)
	}
}

// TestServeLogsTheLifecycleLines: the shutdown line is the daemon's, and it is covered where a
// daemon runs.
func TestServeLogsTheLifecycleLines(t *testing.T) {
	home := serveHome(t)
	writeSkillFile(t, home, "skills/kibana/SKILL.md", "---\nname: kibana\n---\n")

	stdout, _, code := runCLI(t, "serve", "--once", "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	for _, want := range []string{
		"INFO  config=~/.skill-model-deactivator.json log=" + display.Tilde(
			filepath.Join(stateDirOf(t, home), "smd.log")),
		"reconcile started",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

// TestWatchRootsAreDeduplicated: two patterns that share a root give one entry.
func TestWatchRootsAreDeduplicated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	roots := watchRoots([]string{"~/skills/*/SKILL.md", "~/skills/**/SKILL.md", "~/other/*/SKILL.md"})
	want := []string{filepath.ToSlash(filepath.Join(home, "skills")),
		filepath.ToSlash(filepath.Join(home, "other"))}
	if len(roots) != len(want) {
		t.Fatalf("watchRoots() = %v, want %v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Errorf("watchRoots()[%d] = %q, want %q", i, roots[i], want[i])
		}
	}
}

// TestServeRefusesAnUnusableConfig: every serve form exits 1 with one stderr line, and a
// refused run creates no state directory, so it cannot leave a log or a lock behind either.
func TestServeRefusesAnUnusableConfig(t *testing.T) {
	tests := []struct {
		name   string
		config string // "" writes no config file
		want   string
	}{
		{"no config file", "", "patterns"},
		{"empty patterns", `{"patterns": []}`, "patterns is empty"},
		{"unknown key", `{"paterns": ["~/skills/*/SKILL.md"]}`, "unknown key"},
		{"not JSON", `not json`, "not valid JSON"},
	}

	for _, test := range tests {
		for _, args := range [][]string{
			{"serve"},
			{"serve", "--once"},
			{"serve", "--once", "--dry-run"},
		} {
			t.Run(test.name+"/"+strings.Join(args, " "), func(t *testing.T) {
				home := newHome(t)
				if test.config != "" {
					writeConfig(t, home, test.config)
				}

				stdout, stderr, code := runCLI(t, args...)
				if code != 1 {
					t.Errorf("exit code = %d, want 1", code)
				}
				if stdout != "" {
					t.Errorf("stdout = %q, want empty", stdout)
				}
				if !strings.Contains(stderr, test.want) {
					t.Errorf("stderr = %q, want it to mention %q", stderr, test.want)
				}
				if strings.Count(stderr, "\n") != 1 {
					t.Errorf("stderr = %q, want exactly one line", stderr)
				}
				if _, err := os.Stat(stateDirOf(t, home)); !os.IsNotExist(err) {
					t.Errorf("a refused run created the state directory: %v", err)
				}
			})
		}
	}
}

// TestServeOnceTouchesNoSkillWithoutAPattern is the regression for the defect where an
// unconfigured run matched the example patterns and wrote into a real skills tree.
func TestServeOnceTouchesNoSkillWithoutAPattern(t *testing.T) {
	home := newHome(t)
	path := writeSkillFile(t, home, ".claude/skills/kibana/SKILL.md", "---\nname: kibana\n---\nbody\n")
	before := readBytes(t, path)

	if _, _, code := runCLI(t, "serve", "--once"); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	if after := readBytes(t, path); string(after) != string(before) {
		t.Errorf("the skill file changed:\n before %q\n after  %q", before, after)
	}
}
