package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestStateDirIsPerOperatingSystem: the log sits inside the state directory, and the directory
// keeps its fixed name.
func TestStateDirIsPerOperatingSystem(t *testing.T) {
	stateDir, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir() error = %v, want nil", err)
	}
	if filepath.Base(stateDir) != stateDirName {
		t.Errorf("StateDir() = %q, want the base name %q", stateDir, stateDirName)
	}
	if !filepath.IsAbs(stateDir) {
		t.Errorf("StateDir() = %q, want an absolute path", stateDir)
	}

	logPath, err := LogPath()
	if err != nil {
		t.Fatalf("LogPath() error = %v, want nil", err)
	}
	if want := filepath.Join(stateDir, LogFileName); logPath != want {
		t.Errorf("LogPath() = %q, want %q", logPath, want)
	}
}

// TestStateBaseMatchesTheSpec pins the base directory, so a change to the per-OS file cannot
// pass unnoticed.
func TestStateBaseMatchesTheSpec(t *testing.T) {
	dir, err := home()
	if err != nil {
		t.Fatalf("home() error = %v, want nil", err)
	}

	var want string
	switch runtime.GOOS {
	case "linux":
		want = filepath.Join(dir, ".local", "state")
	case "darwin":
		want = filepath.Join(dir, "Library", "Logs")
	case "windows":
		want = os.Getenv("LOCALAPPDATA")
		if want == "" {
			t.Skip("%LOCALAPPDATA% is not set")
		}
	default:
		t.Skipf("no expectation for %s", runtime.GOOS)
	}

	if got, err := stateBase(); err != nil || got != want {
		t.Errorf("stateBase() = %q, %v, want %q", got, err, want)
	}
}
