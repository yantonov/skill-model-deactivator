package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests start the real binary, because an in-process test cannot show that the operating
// system releases the lock after a crash, nor that a delivered signal reaches
// signal.NotifyContext. The in-process cases are in serve_lock_test.go.

var smdBinary struct {
	once sync.Once
	path string
	err  error
}

// buildSmd builds once per test binary run, from the test's own package, so no test depends on
// a stale bin/ from an earlier `make build`.
func buildSmd(t *testing.T) string {
	t.Helper()
	smdBinary.once.Do(func() {
		dir, err := os.MkdirTemp("", "smd-process-test")
		if err != nil {
			smdBinary.err = err
			return
		}
		path := filepath.Join(dir, "smd")
		if runtime.GOOS == "windows" {
			path += ".exe"
		}
		out, err := exec.Command(goTool(), "build", "-o", path, ".").CombinedOutput()
		if err != nil {
			smdBinary.err = fmt.Errorf("go build: %v: %s", err, out)
			return
		}
		smdBinary.path = path
	})
	if smdBinary.err != nil {
		t.Fatalf("build smd: %v", smdBinary.err)
	}
	return smdBinary.path
}

// goTool is the toolchain that built this test binary, so the build does not depend on PATH.
func goTool() string {
	if path, err := exec.LookPath("go"); err == nil {
		return path
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

type process struct {
	cmd *exec.Cmd
	out safeBuffer
	err safeBuffer
}

func (p *process) wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		t.Fatalf("smd did not exit within %s", timeout)
	}
	return p.cmd.ProcessState.ExitCode()
}

func (p *process) stdout() string { return p.out.String() }
func (p *process) stderr() string { return p.err.String() }

// safeBuffer is read by the test while the child writes into it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startServe waits until the daemon holds the lock, so a signal sent by the test reaches a
// daemon that is past its startup.
func startServe(t *testing.T, home string) *process {
	t.Helper()
	cmd := exec.Command(buildSmd(t), "serve")
	setFixtureEnv(cmd, home)
	// On Windows a console control event goes to a whole process group, so the child needs one of
	// its own and the test's Ctrl-Break must not reach the test process.
	cmd.SysProcAttr = newProcessGroup()
	p := &process{cmd: cmd}
	cmd.Stdout = &p.out
	cmd.Stderr = &p.err
	if err := cmd.Start(); err != nil {
		t.Fatalf("start smd: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pid := pidInLockFileOrZero(stateDirOf(t, home)); pid == cmd.Process.Pid {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("smd did not take the lock within 10 s: %s", p.stderr())
	return nil
}

// pidInLockFileOrZero never fails: a poll has to tolerate every intermediate state, and the
// state directory of a starting daemon is one of them.
func pidInLockFileOrZero(stateDir string) int {
	file, err := os.Open(filepath.Join(stateDir, "lock"))
	if err != nil {
		return 0
	}
	defer file.Close()
	data := make([]byte, 32)
	n, err := file.ReadAt(data, 1)
	if err != nil && n == 0 {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data[:n])))
	if err != nil {
		return 0
	}
	return pid
}

// setFixtureEnv points the child at the fixture home. Everything else is inherited, because the
// operating system needs its own variables and the home is the only thing the tool reads.
func setFixtureEnv(cmd *exec.Cmd, home string) {
	overrides := map[string]string{
		"HOME":         home,
		"USERPROFILE":  home,
		"LOCALAPPDATA": home,
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[name]; replaced {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	for name, value := range overrides {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
}

// TestProcessSIGINTStopsCleanly: the real signal stops a real daemon, which exits 0, logs
// exactly one shutdown line, and leaves the lock released.
func TestProcessSIGINTStopsCleanly(t *testing.T) {
	home := daemonHome(t)
	canonical := "---\nname: kibana\ndisable-model-invocation: true\n---\n"
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", canonical)

	p := startServe(t, home)
	waitForFile(t, path, []byte(canonical))

	sendInterrupt(t, p)

	if code := p.wait(t, 10*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0: %s", code, p.stderr())
	}
	if got := strings.Count(p.stdout(), "shutdown"); got != 1 {
		t.Errorf("shutdown lines = %d, want 1:\n%s", got, p.stdout())
	}
	if p.stderr() != "" {
		t.Errorf("stderr = %q, want empty", p.stderr())
	}

	// A later serve takes the lock.
	again := startServe(t, home)
	if again.cmd.Process.Pid != pidInLockFileOrZero(stateDirOf(t, home)) {
		t.Errorf("the restarted serve does not hold the lock")
	}
	sendInterrupt(t, again)
	if code := again.wait(t, 10*time.Second); code != 0 {
		t.Errorf("restarted serve exit code = %d, want 0", code)
	}
}

// TestProcessASecondInstanceIsRefused: the second exits 0, prints the message on stderr, and
// the first is still healthy afterwards.
func TestProcessASecondInstanceIsRefused(t *testing.T) {
	home := daemonHome(t)
	canonical := "---\nname: kibana\ndisable-model-invocation: true\n---\n"
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", canonical)

	first := startServe(t, home)
	waitForFile(t, path, []byte(canonical))

	second := exec.Command(buildSmd(t), "serve")
	setFixtureEnv(second, home)
	var out, errOut bytes.Buffer
	second.Stdout, second.Stderr = &out, &errOut
	// A refusal must not hang, so it needs its own deadline.
	if err := second.Start(); err != nil {
		t.Fatalf("start the second serve: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- second.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the second serve failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = second.Process.Kill()
		t.Fatal("the second serve did not exit")
	}

	if got := second.ProcessState.ExitCode(); got != 0 {
		t.Errorf("the second serve exit code = %d, want 0", got)
	}
	want := "already running (pid " + strconv.Itoa(first.cmd.Process.Pid) + ")"
	if !strings.Contains(errOut.String(), want) {
		t.Errorf("second stderr = %q, want it to contain %q", errOut.String(), want)
	}
	if out.String() != "" {
		t.Errorf("second stdout = %q, want nothing: it reconciles nothing", out.String())
	}

	// The first is still healthy.
	if err := os.WriteFile(path, []byte("---\nname: kibana\n---\n"), 0o644); err != nil {
		t.Fatalf("reset the skill file: %v", err)
	}
	waitForFile(t, path, []byte(canonical))

	sendInterrupt(t, first)
	if code := first.wait(t, 10*time.Second); code != 0 {
		t.Errorf("the first serve exit code = %d, want 0", code)
	}
}

// TestProcessCrashReleasesTheLock: the operating system releases the lock when the process dies
// without a shutdown, so the next instance takes it. That is the reason the lock is an
// operating-system file lock and not a bare pid file.
func TestProcessCrashReleasesTheLock(t *testing.T) {
	home := daemonHome(t)
	canonical := "---\nname: kibana\ndisable-model-invocation: true\n---\n"
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", canonical)

	doomed := startServe(t, home)
	waitForFile(t, path, []byte(canonical))
	if err := doomed.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the daemon: %v", err)
	}
	if err := doomed.cmd.Wait(); err == nil {
		t.Error("the killed daemon exited 0, want a crash")
	}

	// The lock file is still there with the dead pid, which is exactly what a pid file would lie
	// about. The lock itself is gone, so this instance takes it.
	next := startServe(t, home)
	if got := pidInLockFileOrZero(stateDirOf(t, home)); got != next.cmd.Process.Pid {
		t.Errorf("the lock file holds pid %d, want the new instance's %d", got, next.cmd.Process.Pid)
	}
	sendInterrupt(t, next)
	if code := next.wait(t, 10*time.Second); code != 0 {
		t.Errorf("the new instance exit code = %d, want 0", code)
	}
	if got := strings.Count(next.stdout(), "already running"); got != 0 {
		t.Errorf("the new instance saw a live lock %d times:\n%s", got, next.stdout())
	}
}
