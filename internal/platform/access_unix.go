//go:build unix

package platform

import (
	"fmt"
	"syscall"
)

// The access(2) bits W_OK and X_OK, which the syscall package does not export by name. They
// hold the same value on every Unix.
const (
	accessExecute = 0x1
	accessWrite   = 0x2
)

// WritableDir tests the directory and not the file, because the writer replaces the file with
// a rename: a read-only file in a writable directory can still be replaced. The error text
// names the directory, which is the thing that failed.
func WritableDir(dir string) error {
	if err := syscall.Access(dir, accessWrite|accessExecute); err != nil {
		return fmt.Errorf("cannot replace a file in %s: %w", dir, err)
	}
	return nil
}
