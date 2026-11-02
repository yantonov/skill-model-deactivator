//go:build linux

package platform

import "path/filepath"

// stateBase is fixed and not configurable.
func stateBase() (string, error) {
	dir, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".local", "state"), nil
}
