//go:build windows

package reconcile

import (
	"syscall"
	"testing"
)

// TestPassReportsAReadFailureOnAWindowsLock holds one file open with no sharing mode, so
// the pass cannot open it for reading. That is the Windows form of the fixture the ticket
// names: os.Chmod only toggles the read-only bit there, and a read-only file is still
// readable, so a permission fixture cannot fail a read on this platform.
func TestPassReportsAReadFailureOnAWindowsLock(t *testing.T) {
	home := newHome(t)
	locked := writeSkill(t, home, "skills/a/SKILL.md", "---\nname: a\n---\n")
	writeSkill(t, home, "skills/b/SKILL.md", "---\nname: b\n---\n")

	pathPtr, err := syscall.UTF16PtrFromString(locked)
	if err != nil {
		t.Fatalf("convert %s: %v", locked, err)
	}
	handle, err := syscall.CreateFile(pathPtr, syscall.GENERIC_READ, 0, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("open %s with no sharing: %v", locked, err)
	}
	t.Cleanup(func() { syscall.CloseHandle(handle) })

	summary, plans, logger := runPass(t, skillsConfig(), Options{})

	if summary.Failed != 1 {
		t.Errorf("failed = %d, want 1", summary.Failed)
	}
	if summary.Changed != 1 {
		t.Errorf("changed = %d, want 1: the other file is still planned", summary.Changed)
	}
	if len(plans) != 1 || plans[0].Path == locked {
		t.Errorf("plans = %+v, want the readable file only", plans)
	}
	errsAt := logger.atLevel(levelError)
	if len(errsAt) != 1 {
		t.Fatalf("error lines = %d, want 1: %+v", len(errsAt), errsAt)
	}
	if value, _ := errsAt[0].field("path"); value != "~/skills/a/SKILL.md" {
		t.Errorf("error path = %q, want %q", value, "~/skills/a/SKILL.md")
	}
	if _, ok := errsAt[0].field("error"); !ok {
		t.Errorf("error line has no error field: %+v", errsAt[0])
	}
}
