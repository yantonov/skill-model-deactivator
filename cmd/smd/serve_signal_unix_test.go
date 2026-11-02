//go:build !windows

package main

import (
	"strings"
	"syscall"
	"testing"
	"time"
)

// newProcessGroup returns no special attributes: on Unix a signal is sent to one process.
func newProcessGroup() *syscall.SysProcAttr {
	return nil
}

// sendInterrupt delivers SIGINT to the process.
func sendInterrupt(t *testing.T, p *process) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}
}

// TestProcessSIGTERMStopsTheSameWay: both signals go to the same context of
// signal.NotifyContext, so the same loop exit and the same single shutdown line are the whole
// expectation. The test lives here because Windows has no SIGTERM to send: it maps the console
// close, logoff, and shutdown events onto it, which a test cannot synthesize.
func TestProcessSIGTERMStopsTheSameWay(t *testing.T) {
	home := daemonHome(t)
	canonical := "---\nname: kibana\ndisable-model-invocation: true\n---\n"
	path := writeSkillFile(t, home, "skills/kibana/SKILL.md", canonical)

	p := startServe(t, home)
	waitForFile(t, path, []byte(canonical))

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	if code := p.wait(t, 10*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0: %s", code, p.stderr())
	}
	if got := strings.Count(p.stdout(), "shutdown"); got != 1 {
		t.Errorf("shutdown lines = %d, want 1:\n%s", got, p.stdout())
	}
	if p.stderr() != "" {
		t.Errorf("stderr = %q, want empty", p.stderr())
	}
	// The lock is released, so the next serve starts instead of reporting a running daemon.
	next := startServe(t, home)
	sendInterrupt(t, next)
	if code := next.wait(t, 10*time.Second); code != 0 {
		t.Errorf("the next serve exit code = %d, want 0", code)
	}
	if got := strings.Count(next.stderr(), "already running"); got != 0 {
		t.Errorf("the next serve saw a live lock: %s", next.stderr())
	}
}
