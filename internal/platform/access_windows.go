//go:build windows

package platform

// WritableDir always succeeds: file attributes do not predict whether a rename succeeds on
// Windows, so the writer relies on the rename itself. The signature matches the Unix form.
func WritableDir(string) error {
	return nil
}
