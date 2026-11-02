package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// stateDirName sits under the per-OS base directory. The tool creates nothing here until it
// writes.
const stateDirName = "skill-model-deactivator"

// LogFileName is the log file name inside the state directory.
const LogFileName = "smd.log"

// LockFileName is the same on every platform, and it is never deleted.
const LockFileName = "lock"

// StateDir holds the lock file and the log, and nothing else. It is not created.
func StateDir() (string, error) {
	base, err := stateBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, stateDirName), nil
}

func LogPath() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, LogFileName), nil
}

// home is the parent of both the config path and the state directory.
func home() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the home directory: %w", err)
	}
	if dir == "" {
		return "", fmt.Errorf("cannot resolve the home directory: it is empty")
	}
	return dir, nil
}
