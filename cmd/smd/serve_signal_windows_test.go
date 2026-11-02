//go:build windows

package main

import (
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// newProcessGroup puts the child in its own console process group, so a control event the test
// sends to that group reaches the child and not the test process. Windows delivers a console
// control event to a process group, which is why the group is needed: Go maps Ctrl-C and
// Ctrl-Break onto os.Interrupt, and the console close, logoff, and shutdown events onto
// SIGTERM.
func newProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// sendInterrupt delivers a Ctrl-Break console control event, the Windows equivalent of SIGINT.
// GenerateConsoleCtrlEvent cannot send a Ctrl-C to another process group, so Ctrl-Break is the
// event a test can send; Go maps both onto os.Interrupt, which is what signal.NotifyContext
// listens for. This code lives here rather than in internal/platform because production needs
// no Windows branch.
func sendInterrupt(t *testing.T, p *process) {
	t.Helper()
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.cmd.Process.Pid)); err != nil {
		t.Fatalf("send Ctrl-Break: %v", err)
	}
}
