// Package display renders the config paths and values for a human and for --json. Rendered
// paths abbreviate the home directory and use forward slashes; every other package uses the
// real path.
package display

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/config"
)

// homeDir is the only test hook here, so the abbreviation can be asserted against a fake home.
var homeDir = os.UserHomeDir

// Report is everything `smd config show` prints. The caller collects the log size, because
// this package reads no file.
type Report struct {
	Loaded    config.Loaded
	LogPath   string
	LogExists bool
	LogBytes  int64
}

// Tilde renders a path outside the home directory in its full form.
func Tilde(path string) string {
	home, err := homeDir()
	if err != nil || home == "" {
		return filepath.ToSlash(path)
	}
	slashPath, slashHome := filepath.ToSlash(path), filepath.ToSlash(home)
	if slashPath == slashHome {
		return "~"
	}
	if strings.HasPrefix(slashPath, strings.TrimSuffix(slashHome, "/")+"/") {
		return "~" + strings.TrimPrefix(slashPath, strings.TrimSuffix(slashHome, "/"))
	}
	return slashPath
}

// abbreviateHome works inside text, so a finding that quotes a pattern or a path reads the way
// the user wrote it.
func abbreviateHome(text string) string {
	home, err := homeDir()
	if err != nil || home == "" {
		return filepath.ToSlash(text)
	}
	slashText, slashHome := filepath.ToSlash(text), strings.TrimSuffix(filepath.ToSlash(home), "/")
	return strings.ReplaceAll(slashText, slashHome, "~")
}

// Size rounds up, so a non-empty file never prints as 0 KiB.
func Size(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	return fmt.Sprintf("%d KiB", (bytes+1023)/1024)
}

// Duration renders a duration the way the config writes it: the trailing empty units are
// dropped, so 30 minutes prints as 30m and not as Go's 30m0s.
func Duration(d time.Duration) string {
	if d == 0 || d < time.Minute || d%time.Minute != 0 {
		return d.String()
	}
	hours, minutes := d/time.Hour, (d%time.Hour)/time.Minute
	switch {
	case hours == 0:
		return fmt.Sprintf("%dm", minutes)
	case minutes == 0:
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, minutes)
}

func ConfigStatus(l config.Loaded) string {
	if !l.Exists {
		return "(missing)"
	}
	if reason := invalidReason(l.Findings); reason != "" {
		return fmt.Sprintf("(exists, %d bytes, invalid: %s)", l.Bytes, reason)
	}
	return fmt.Sprintf("(exists, %d bytes, valid JSON)", l.Bytes)
}

// HelpConfigStatus ends in `valid` rather than `valid JSON`: the help header is shorter than
// the config show header.
func HelpConfigStatus(l config.Loaded) string {
	return strings.Replace(ConfigStatus(l), ", valid JSON)", ", valid)", 1)
}

func LogStatus(exists bool, bytes int64) string {
	if !exists {
		return "(missing)"
	}
	return "(" + Size(bytes) + ")"
}

// HelpPaths reports what is there and writes nothing, even when neither the config file nor
// the state directory exists.
func HelpPaths(w io.Writer, l config.Loaded, logPath string, logExists bool, logBytes int64) error {
	var b strings.Builder
	pathField(&b, "CONFIG", Tilde(l.Path), HelpConfigStatus(l))
	pathField(&b, "LOG", Tilde(logPath), LogStatus(logExists, logBytes))
	_, err := io.WriteString(w, b.String())
	return err
}

// Show puts the (default) marker on the label line of a value that came from a default.
func Show(w io.Writer, r Report) error {
	var b strings.Builder
	pathField(&b, "CONFIG", Tilde(r.Loaded.Path), ConfigStatus(r.Loaded))
	pathField(&b, "LOG", Tilde(r.LogPath), LogStatus(r.LogExists, r.LogBytes))
	b.WriteString("\n")

	listField(&b, config.FieldPatterns, r.Loaded.Source(config.FieldPatterns), r.Loaded.Config.Patterns)
	scalarField(&b, config.FieldDebounce, r.Loaded.Source(config.FieldDebounce), Duration(r.Loaded.Config.Debounce))
	scalarField(&b, config.FieldRescanInterval, r.Loaded.Source(config.FieldRescanInterval), Duration(r.Loaded.Config.RescanInterval))

	_, err := io.WriteString(w, b.String())
	return err
}

// jsonReport holds the fields in the order they appear.
type jsonReport struct {
	ConfigPath     string    `json:"config_path"`
	ConfigExists   bool      `json:"config_exists"`
	ConfigValid    bool      `json:"config_valid"`
	LogPath        string    `json:"log_path"`
	Patterns       jsonValue `json:"patterns"`
	Debounce       jsonValue `json:"debounce"`
	RescanInterval jsonValue `json:"rescan_interval"`
}

type jsonValue struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// ShowJSON writes one object on one line, never pretty-printed, so a script can read it with
// one pass.
func ShowJSON(w io.Writer, r Report) error {
	report := jsonReport{
		ConfigPath:   Tilde(r.Loaded.Path),
		ConfigExists: r.Loaded.Exists,
		ConfigValid:  r.Loaded.Exists && invalidReason(r.Loaded.Findings) == "",
		LogPath:      Tilde(r.LogPath),
		Patterns: jsonValue{
			Value:  r.Loaded.Config.Patterns,
			Source: r.Loaded.Source(config.FieldPatterns),
		},
		Debounce: jsonValue{
			Value:  Duration(r.Loaded.Config.Debounce),
			Source: r.Loaded.Source(config.FieldDebounce),
		},
		RescanInterval: jsonValue{
			Value:  Duration(r.Loaded.Config.RescanInterval),
			Source: r.Loaded.Source(config.FieldRescanInterval),
		},
	}
	encoder := json.NewEncoder(w)
	return encoder.Encode(report)
}

func pathField(b *strings.Builder, label, path, status string) {
	fmt.Fprintf(b, "%-9s%s   %s\n", label, path, status)
}

// listField prints (empty) rather than a bare label, which would read as a missing line. A
// default marker goes on the label line and every element under the value column, so the
// marker belongs to the field and not to an element.
func listField(b *strings.Builder, label, source string, patterns []string) {
	if len(patterns) == 0 {
		fmt.Fprintf(b, "%-18s(empty)\n", label)
		return
	}
	if marker(source) != "" {
		fmt.Fprintf(b, "%-18s(default)\n", label)
	} else if len(patterns) > 0 {
		fmt.Fprintf(b, "%-18s%s\n", label, patterns[0])
		patterns = patterns[1:]
	}
	for _, pattern := range patterns {
		fmt.Fprintf(b, "%18s%s\n", "", pattern)
	}
}

func scalarField(b *strings.Builder, label, source, value string) {
	fmt.Fprintf(b, "%-18s%s%s\n", label, value, marker(source))
}

func marker(source string) string {
	if source == config.SourceDefault {
		return " (default)"
	}
	return ""
}

// invalidReason reports the first failure, so a JSON parse error and an unknown key both make
// the file invalid.
func invalidReason(findings []config.Finding) string {
	for _, finding := range findings {
		if finding.Level != config.LevelFail {
			continue
		}
		if line, _, _ := strings.Cut(finding.Message, "\n"); line != "" {
			return line
		}
		return "see smd config validate"
	}
	return ""
}
