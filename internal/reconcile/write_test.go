package reconcile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yantonov/skill-model-deactivator/internal/frontmatter"
)

// planFor keeps a writer test from restating the planner's decisions.
func planFor(t *testing.T, content string) frontmatter.Plan {
	t.Helper()
	plan := frontmatter.PlanFor([]byte(content))
	if plan.Output == nil {
		t.Fatalf("the fixture %q has no output, so there is nothing to write", content)
	}
	return plan
}

// replaceRename counts its calls in the returned pointer.
func replaceRename(t *testing.T, fn func(call int, oldpath, newpath string) error) *int {
	t.Helper()
	previous := Rename
	calls := 0
	Rename = func(oldpath, newpath string) error {
		calls++
		return fn(calls, oldpath, newpath)
	}
	t.Cleanup(func() { Rename = previous })
	return &calls
}

// TestWriteSucceedsOnTheThirdRenameAttempt states the attempt count in its name, so the
// constant and the test cannot drift apart.
func TestWriteSucceedsOnTheThirdRenameAttempt(t *testing.T) {
	home := newHome(t)
	content := "---\nname: a\n---\n"
	path := writeSkill(t, home, "skills/a/SKILL.md", content)

	calls := replaceRename(t, func(call int, oldpath, newpath string) error {
		if call < renameAttempts {
			return errors.New("the file is locked")
		}
		return os.Rename(oldpath, newpath)
	})

	if err := Write(path, planFor(t, content)); err != nil {
		t.Fatalf("Write() error = %v, want nil on attempt %d", err, renameAttempts)
	}
	if *calls != renameAttempts {
		t.Errorf("rename calls = %d, want %d", *calls, renameAttempts)
	}
	want := "---\nname: a\ndisable-model-invocation: true\n---\n"
	if got := string(snapshot(t, path)); got != want {
		t.Errorf("the file is %q, want %q", got, want)
	}
	if found := tmpFiles(t, home); len(found) != 0 {
		t.Errorf("the write left temp files: %v", found)
	}
}

// TestWriteFailsWhenAllRenameAttemptsFail: one error, an untouched original, and no temp
// file.
func TestWriteFailsWhenAllRenameAttemptsFail(t *testing.T) {
	home := newHome(t)
	content := "---\nname: a\n---\n"
	path := writeSkill(t, home, "skills/a/SKILL.md", content)

	calls := replaceRename(t, func(int, string, string) error {
		return errors.New("the file is locked")
	})

	err := Write(path, planFor(t, content))
	if err == nil {
		t.Fatalf("Write() error = nil, want the rename failure")
	}
	if !strings.Contains(err.Error(), "the file is locked") {
		t.Errorf("Write() error = %q, want it to carry the OS error text", err)
	}
	if *calls != renameAttempts {
		t.Errorf("rename calls = %d, want %d", *calls, renameAttempts)
	}
	if got := string(snapshot(t, path)); got != content {
		t.Errorf("the original changed: %q, want %q", got, content)
	}
	if found := tmpFiles(t, home); len(found) != 0 {
		t.Errorf("the write left temp files: %v", found)
	}
}

// TestWritePreservesTheFileMode: the temp file takes the target's mode, so the exec bit and
// the read-only bit survive the rename. Windows is skipped because os.Chmod there only toggles
// the read-only bit and cannot express 0644.
func TestWritePreservesTheFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod on Windows only toggles the read-only bit, so 0644 cannot be set and asserted")
	}
	home := newHome(t)
	content := "---\nname: a\n---\n"
	path := writeSkill(t, home, "skills/a/SKILL.md", content)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}

	if err := Write(path, planFor(t, content)); err != nil {
		t.Fatalf("Write() error = %v, want nil", err)
	}

	if got := stat(t, path).Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %o, want 644 after the rename", got)
	}
}

// TestWriteUsesAUniqueTempNameInTheTargetDirectory: two writes must not share a name, or two
// processes could truncate each other's half-written file.
func TestWriteUsesAUniqueTempNameInTheTargetDirectory(t *testing.T) {
	home := newHome(t)
	content := "---\nname: a\n---\n"
	path := writeSkill(t, home, "skills/a/SKILL.md", content)

	var names []string
	replaceRename(t, func(_ int, oldpath, newpath string) error {
		names = append(names, oldpath)
		return os.Rename(oldpath, newpath)
	})

	for round := 0; round < 2; round++ {
		if err := Write(path, planFor(t, content)); err != nil {
			t.Fatalf("Write() round %d error = %v, want nil", round, err)
		}
		// The first write applied the plan, so put the original back.
		writeSkill(t, home, "skills/a/SKILL.md", content)
	}

	if len(names) != 2 {
		t.Fatalf("temp names = %v, want two", names)
	}
	if names[0] == names[1] {
		t.Errorf("temp name %q repeated, so two writes could truncate each other", names[0])
	}
	for _, name := range names {
		if filepath.Dir(name) != filepath.Dir(path) {
			t.Errorf("temp file %q is not in the target directory %q", name, filepath.Dir(path))
		}
		if want := "." + filepath.Base(path) + "."; !strings.HasPrefix(filepath.Base(name), want) {
			t.Errorf("temp name = %q, want it to start with %q", name, want)
		}
		if !strings.HasSuffix(name, tmpSuffix) {
			t.Errorf("temp name = %q, want the %q suffix", name, tmpSuffix)
		}
		if want := "." + strconv.Itoa(os.Getpid()) + "-"; !strings.Contains(name, want) {
			t.Errorf("temp name = %q, want it to carry %q", name, want)
		}
	}
}
