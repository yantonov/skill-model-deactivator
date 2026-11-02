package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FileName sits in the home directory, at the same path on all three operating systems.
const FileName = ".skill-model-deactivator.json"

// The three config keys.
const (
	FieldPatterns       = "patterns"
	FieldDebounce       = "debounce"
	FieldRescanInterval = "rescan_interval"
)

// The two values Loaded.Source returns.
const (
	SourceFile    = "file"
	SourceDefault = "default"
)

// Level is the severity of a Finding. The zero value is LevelOK.
type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelFail
)

func (l Level) String() string {
	switch l {
	case LevelWarn:
		return "warn"
	case LevelFail:
		return "fail"
	default:
		return "ok"
	}
}

// Config is the effective config: the file contents merged over the defaults.
type Config struct {
	Patterns       []string
	Debounce       time.Duration
	RescanInterval time.Duration
}

type Finding struct {
	Level   Level
	Subject string
	Message string
	Suggest string
}

type Loaded struct {
	Path     string
	Exists   bool
	Bytes    int64
	Config   Config
	Findings []Finding

	// fromFile holds the keys the file named, so Source can mark a default.
	fromFile map[string]bool
}

func (l Loaded) Source(field string) string {
	if l.fromFile[field] {
		return SourceFile
	}
	return SourceDefault
}

var (
	// ReadFile is a var so a test can count opens and assert that the config is read once.
	ReadFile = os.ReadFile

	// knownKeys is sorted, so a tie in the closest-key search breaks by sorted order.
	knownKeys = []string{FieldDebounce, FieldPatterns, FieldRescanInterval}
)

const (
	defaultDebounce       = 250 * time.Millisecond
	defaultRescanInterval = 30 * time.Minute
)

// defaults leaves Patterns empty: an installed pattern would act on files the user never
// named, so a run needs at least one pattern and refuses to start without one.
func defaults() Config {
	return Config{
		Debounce:       defaultDebounce,
		RescanInterval: defaultRescanInterval,
	}
}

// DefaultConfig is the one config text the tool emits: `smd config create` writes it and
// `smd config validate` prints it when the file is missing. It names two skills rather than a
// glob, so it never reads as "disable everything", and it holds no duration, so the defaults
// apply until the user writes a value.
const DefaultConfig = `{
  "patterns": [
    "~/.claude/skills/foo-bar/SKILL.md",
    "~/.agents/skills/foo-bar/SKILL.md"
  ]
}
`

// ErrExists is returned rather than overwriting a file the user may have written.
var ErrExists = errors.New("the config file already exists")

// Create writes DefaultConfig exclusively, so a file that appeared meanwhile fails here and is
// left as it is.
func Create() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return path, ErrExists
		}
		return path, fmt.Errorf("cannot create the config file %s: %w", path, err)
	}

	_, writeErr := io.WriteString(file, DefaultConfig)
	closeErr := file.Close()
	if writeErr != nil {
		// A partial file is worse than none: the next create must be able to succeed.
		_ = os.Remove(path)
		return path, fmt.Errorf("cannot write the config file %s: %w", path, writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return path, fmt.Errorf("cannot write the config file %s: %w", path, closeErr)
	}
	return path, nil
}

// Path is the home directory plus FileName.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the config path: %w", err)
	}
	if home == "" {
		return "", errors.New("cannot resolve the config path: the home directory is empty")
	}
	return filepath.Join(home, FileName), nil
}

// Load returns an error only when the path cannot be resolved or the file exists but cannot
// be read: a missing file yields the defaults plus one warn finding.
//
// Load is the only function in the tree that opens the config file. Call it once per process
// and pass the Config by value.
func Load() (Loaded, error) {
	path, err := Path()
	if err != nil {
		return Loaded{}, err
	}
	loaded := Loaded{
		Path:     path,
		Config:   defaults(),
		fromFile: make(map[string]bool, len(knownKeys)),
	}

	data, err := ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			loaded.Findings = []Finding{{
				Level:   LevelWarn,
				Subject: path,
				Message: "config file not found: patterns is empty, so no file can be matched",
				Suggest: "create " + path + " with the keys patterns, debounce, and rescan_interval",
			}}
			return loaded, nil
		}
		return Loaded{Path: path}, fmt.Errorf("cannot read the config file %s: %w", path, err)
	}

	loaded.Exists = true
	loaded.Bytes = int64(len(data))
	loaded.Findings = decode(data, path, &loaded)
	return loaded, nil
}

// Validate reads no file: the load findings come first, then the checks the caller computed.
func Validate(l Loaded, extra []Finding) []Finding {
	findings := make([]Finding, 0, len(l.Findings)+len(extra))
	findings = append(findings, l.Findings...)
	findings = append(findings, extra...)
	return findings
}

// decode unmarshals into raw messages first, so an unknown key is reported before any known
// key is decoded.
func decode(data []byte, path string, loaded *Loaded) []Finding {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return []Finding{{
			Level:   LevelFail,
			Subject: path,
			Message: fmt.Sprintf("config is not valid JSON: %v", err),
			Suggest: "write the config as a JSON object with the keys patterns, debounce, and rescan_interval",
		}}
	}

	var findings []Finding

	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if isKnownKey(key) {
			continue
		}
		closest := closestKey(key)
		findings = append(findings, Finding{
			Level:   LevelFail,
			Subject: key,
			Message: fmt.Sprintf("unknown key %q: the closest known key is %q", key, closest),
			Suggest: "remove it, or use one of the known keys: " + strings.Join(knownKeys, ", "),
		})
	}

	if rawPatterns, ok := raw[FieldPatterns]; !ok {
		findings = append(findings, Finding{
			Level:   LevelFail,
			Subject: FieldPatterns,
			Message: FieldPatterns + " is missing: list the files to keep in sync",
			Suggest: fmt.Sprintf(`add %q with at least one path or glob`, FieldPatterns),
		})
	} else {
		var patterns []string
		if err := json.Unmarshal(rawPatterns, &patterns); err != nil {
			findings = append(findings, Finding{
				Level:   LevelFail,
				Subject: FieldPatterns,
				Message: fmt.Sprintf("%s must be an array of strings: %v", FieldPatterns, err),
				Suggest: `write the patterns as a JSON array of strings, for example ["~/.agents/skills/foo-bar/SKILL.md"]`,
			})
		} else if len(patterns) == 0 {
			findings = append(findings, Finding{
				Level:   LevelFail,
				Subject: FieldPatterns,
				Message: FieldPatterns + " is empty: list the files to keep in sync",
				Suggest: fmt.Sprintf(`add at least one path or glob to %q`, FieldPatterns),
			})
		} else {
			loaded.Config.Patterns = patterns
			loaded.fromFile[FieldPatterns] = true
		}
	}

	if rawDebounce, ok := raw[FieldDebounce]; ok {
		if debounce, finding := decodeDuration(FieldDebounce, rawDebounce); finding != nil {
			findings = append(findings, *finding)
		} else {
			loaded.Config.Debounce = debounce
			loaded.fromFile[FieldDebounce] = true
			if debounce <= 0 {
				findings = append(findings, Finding{
					Level:   LevelFail,
					Subject: FieldDebounce,
					Message: fmt.Sprintf("%s must be greater than zero: got %s", FieldDebounce, debounce),
					Suggest: fmt.Sprintf(`set %q to a positive duration, for example "250ms"`, FieldDebounce),
				})
			}
		}
	}

	if rawInterval, ok := raw[FieldRescanInterval]; ok {
		if interval, finding := decodeDuration(FieldRescanInterval, rawInterval); finding != nil {
			findings = append(findings, *finding)
		} else {
			loaded.Config.RescanInterval = interval
			loaded.fromFile[FieldRescanInterval] = true
		}
	}

	return findings
}

// decodeDuration reports a value that is not a JSON string, so a bare number is a finding and
// not a panic.
func decodeDuration(field string, raw json.RawMessage) (time.Duration, *Finding) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, &Finding{
			Level:   LevelFail,
			Subject: field,
			Message: fmt.Sprintf("%s must be a string holding a Go duration: %v", field, err),
			Suggest: fmt.Sprintf(`write %q as a string, for example "250ms"`, field),
		}
	}

	duration, err := time.ParseDuration(text)
	if err != nil {
		finding := &Finding{
			Level:   LevelFail,
			Subject: field,
			Message: fmt.Sprintf("%s: %v", field, err),
			Suggest: fmt.Sprintf("write %q with a unit, for example \"250ms\"", field),
		}
		if unit, ok := unitForm(text); ok {
			finding.Message = fmt.Sprintf("%s: %q is not a duration: did you mean %q?", field, text, unit)
			finding.Suggest = fmt.Sprintf("did you mean %q?", unit)
		}
		return 0, finding
	}
	return duration, nil
}

// unitForm adds the ms unit to a bare number: a duration always writes its unit, so "300" is a
// value the user meant as "300ms".
func unitForm(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	if _, err := strconv.ParseFloat(text, 64); err != nil {
		return "", false
	}
	return text + "ms", true
}

func isKnownKey(key string) bool {
	for _, known := range knownKeys {
		if known == key {
			return true
		}
	}
	return false
}

func closestKey(key string) string {
	closest := knownKeys[0]
	best := levenshtein(key, closest)
	for _, known := range knownKeys[1:] {
		if distance := levenshtein(key, known); distance < best {
			closest, best = known, distance
		}
	}
	return closest
}

func levenshtein(a, b string) int {
	left, right := []rune(a), []rune(b)
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			current[j] = previous[j] + 1
			if candidate := current[j-1] + 1; candidate < current[j] {
				current[j] = candidate
			}
			if candidate := previous[j-1] + cost; candidate < current[j] {
				current[j] = candidate
			}
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}
