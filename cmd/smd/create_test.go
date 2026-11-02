package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yantonov/skill-model-deactivator/internal/config"
)

// TestConfigCreate: the command writes the default file, prints its path, and leaves the rest
// of the home alone.
func TestConfigCreate(t *testing.T) {
	home := newHome(t)

	stdout, stderr, code := runCLI(t, "config", "create")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if want := "created ~/.skill-model-deactivator.json\n"; !strings.HasPrefix(stdout, want) {
		t.Errorf("stdout = %q, want it to start with %q", stdout, want)
	}

	data, err := os.ReadFile(filepath.Join(home, ".skill-model-deactivator.json"))
	if err != nil {
		t.Fatalf("read the created file: %v", err)
	}
	if string(data) != config.DefaultConfig {
		t.Errorf("file = %q, want %q", data, config.DefaultConfig)
	}

	// No state directory and no log.
	if _, err := os.Stat(stateDirOf(t, home)); !os.IsNotExist(err) {
		t.Error("config create made the state directory, want none")
	}
}

// TestConfigCreateRefusesAnExistingFile: the file is unchanged and the error names it.
func TestConfigCreateRefusesAnExistingFile(t *testing.T) {
	home := newHome(t)
	content := `{"patterns": ["~/skills/*/SKILL.md"]}`
	writeConfig(t, home, content)

	stdout, stderr, code := runCLI(t, "config", "create")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "already exists") ||
		!strings.Contains(stderr, "~/.skill-model-deactivator.json") {
		t.Errorf("stderr does not report the existing config:\n%s", stderr)
	}

	data, err := os.ReadFile(filepath.Join(home, ".skill-model-deactivator.json"))
	if err != nil {
		t.Fatalf("read the config file: %v", err)
	}
	if string(data) != content {
		t.Errorf("file = %q, want the original %q", data, content)
	}
}

// TestConfigCreateThenShowAndValidate: the created file is a runnable config, so validate is
// green and both durations come from the defaults.
func TestConfigCreateThenShowAndValidate(t *testing.T) {
	newHome(t)

	if _, stderr, code := runCLI(t, "config", "create"); code != 0 {
		t.Fatalf("config create exit code = %d, want 0: %s", code, stderr)
	}

	stdout, stderr, code := runCLI(t, "config", "show", "--json")
	if code != 0 {
		t.Fatalf("config show exit code = %d, want 0: %s", code, stderr)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("config show --json output does not unmarshal: %v", err)
	}
	if exists, ok := decoded["config_exists"].(bool); !ok || !exists {
		t.Errorf("config_exists = %v, want true", decoded["config_exists"])
	}
	for _, field := range []string{"debounce", "rescan_interval"} {
		value, ok := decoded[field].(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object: %v", field, decoded[field])
		}
		if source, _ := value["source"].(string); source != config.SourceDefault {
			t.Errorf("%s source = %v, want %q: create writes no duration", field, value["source"], config.SourceDefault)
		}
	}

	_, stderr, code = runCLI(t, "config", "validate")
	if code != 0 {
		t.Fatalf("config validate exit code = %d, want 0: %s", code, stderr)
	}
	if !strings.Contains(stderr, "2 patterns, 0 files") {
		t.Errorf("validate does not count the two example paths:\n%s", stderr)
	}
}
