//go:build unix

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWritableDirAcceptsAWritableDirectory: the pass case, so the failing case below cannot
// pass for the wrong reason.
func TestWritableDirAcceptsAWritableDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := WritableDir(dir); err != nil {
		t.Errorf("WritableDir(%q) error = %v, want nil", dir, err)
	}
}

// TestWritableDirNamesTheDirectory: the error text names the directory, which is the thing the
// rename needs.
func TestWritableDirNamesTheDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits, so the fixture would not fail")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "locked")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	err := WritableDir(dir)
	if err == nil {
		t.Fatalf("WritableDir(%q) error = nil, want the permission failure", dir)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error = %q, want it to name the directory %q", err, dir)
	}
}
