package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/frontmatter"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

// renameAttempts is the first attempt plus two retries.
const renameAttempts = 3

// renameDelays holds the pause before each retry. On Windows Go opens files without
// FILE_SHARE_DELETE, so a virus scanner, an editor, or a sync client can hold the target
// open for a few milliseconds, and a short pause lets it go.
var renameDelays = [...]time.Duration{20 * time.Millisecond, 80 * time.Millisecond}

// Rename is rename(2) on Unix and MoveFileExW with MOVEFILE_REPLACE_EXISTING on Windows, so
// the writer needs no platform branch. It is a var so a test can fail a rename.
var Rename = os.Rename

// tempCounter keeps a temp name unique inside one process. The process id keeps it unique
// across processes, so a running serve and an operator running serve --once cannot
// truncate each other's half-written file.
var tempCounter atomic.Uint64

// Write streams the planned bytes into a temp file next to the target and renames it over
// the target. It never opens the target for writing and never writes in place, so a crash can
// leave a stray temp file but never a truncated skill.
func Write(path string, plan frontmatter.Plan) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot stat the target: %w", err)
	}
	dir := filepath.Dir(path)
	// The rename needs the directory, not the file.
	if err := platform.WritableDir(dir); err != nil {
		return err
	}

	tmp := tempName(path)
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		// The file was not created, so there is nothing of ours to remove.
		return fmt.Errorf("cannot create the temp file: %w", err)
	}
	if err := writeTemp(file, plan.Output); err != nil {
		removeTemp(tmp)
		return err
	}
	if err := renameWithRetry(tmp, path); err != nil {
		removeTemp(tmp)
		return err
	}
	return nil
}

// tempName stays inside the target's own directory, so the rename never crosses one, and ends
// in tmpSuffix, so a stray temp file is never read as a skill.
func tempName(path string) string {
	return filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d-%d%s",
		filepath.Base(path), os.Getpid(), tempCounter.Add(1), tmpSuffix))
}

func writeTemp(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("cannot write the temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("cannot sync the temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot close the temp file: %w", err)
	}
	return nil
}

// removeTemp ignores its own failure: the write error is the one the caller reports.
func removeTemp(tmp string) {
	_ = os.Remove(tmp)
}

// renameWithRetry reports a failure only when every attempt failed.
func renameWithRetry(tmp, path string) error {
	var err error
	for attempt := 1; attempt <= renameAttempts; attempt++ {
		if err = Rename(tmp, path); err == nil {
			return nil
		}
		if attempt < renameAttempts {
			time.Sleep(renameDelays[attempt-1])
		}
	}
	return fmt.Errorf("cannot replace the target after %d attempts: %w", renameAttempts, err)
}
