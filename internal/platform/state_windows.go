//go:build windows

package platform

import (
	"errors"
	"os"
)

// stateBase is %LOCALAPPDATA%, which is fixed and not configurable.
func stateBase() (string, error) {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return "", errors.New(`cannot resolve the state directory: %LOCALAPPDATA% is not set`)
	}
	return dir, nil
}
