// Package match expands patterns, resolves globs, and dedupes by file identity.
package match

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/yantonov/skill-model-deactivator/internal/config"
)

// wildcardChars make a pattern a glob.
const wildcardChars = "*?[{"

// staleText ends the message of a stale finding, so a caller can select it.
const staleText = "stale"

// Both are vars so a test can count calls.
var (
	statFile  = os.Stat
	globFiles = doublestar.FilepathGlob
)

// Pattern is one config pattern after ~ expansion.
type Pattern struct {
	Raw      string
	Slash    string
	Wildcard bool
	Root     string
}

type Result struct {
	Files    []string // absolute, OS-native separators, deduplicated
	Findings []config.Finding
}

// Parse does not touch the filesystem and does not report whether the pattern is valid. The
// only error is a home directory that cannot be determined.
func Parse(raw string) (Pattern, error) {
	expanded, err := expandTilde(raw)
	if err != nil {
		return Pattern{}, err
	}
	slash := filepath.ToSlash(expanded)
	wildcard := strings.ContainsAny(slash, wildcardChars)
	return Pattern{
		Raw:      expanded,
		Slash:    slash,
		Wildcard: wildcard,
		Root:     root(slash, wildcard),
	}, nil
}

// Resolve returns an error only when the home directory cannot be determined: a pattern that
// is malformed or stale is a Finding, because a stale pattern is a warning the user can live
// with.
func Resolve(patterns []string) (Result, error) {
	result := Result{Files: []string{}}
	// seen holds the identity of every accepted file, in the order accepted, so the
	// first spelling of a file is the one kept.
	seen := make([]os.FileInfo, 0, len(patterns))

	for _, raw := range patterns {
		pattern, err := Parse(raw)
		if err != nil {
			return Result{}, err
		}

		// doublestar reads a backslash as an escape, so the pattern would match nothing
		// and report no error.
		if strings.Contains(pattern.Slash, `\`) {
			result.Findings = append(result.Findings, backslashFinding(pattern))
		}

		matched := 0
		for _, candidate := range candidates(pattern) {
			info, err := statFile(candidate)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			matched++
			if sameFileSeen(seen, info) {
				continue
			}
			seen = append(seen, info)
			result.Files = append(result.Files, absolute(candidate))
		}

		if matched == 0 {
			result.Findings = append(result.Findings, staleFinding(pattern))
		}
	}

	return result, nil
}

// IsStale reports whether finding is the warning a pattern that matched no file produces.
func IsStale(finding config.Finding) bool {
	return finding.Level == config.LevelWarn && strings.HasSuffix(finding.Message, staleText)
}

// candidates returns the paths the pattern names, with OS separators.
func candidates(pattern Pattern) []string {
	if !pattern.Wildcard {
		return []string{filepath.FromSlash(pattern.Slash)}
	}
	// A malformed pattern gives doublestar.ErrBadPattern and no match.
	matches, _ := globFiles(pattern.Slash)
	return matches
}

// expandTilde handles a leading ~ followed by a separator or the end of the string. ~user is
// not supported and stays literal text.
func expandTilde(raw string) (string, error) {
	if !strings.HasPrefix(raw, "~") {
		return raw, nil
	}
	if len(raw) > 1 && raw[1] != '/' && raw[1] != '\\' {
		return raw, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot expand %q: %w", raw, err)
	}
	if home == "" {
		return "", fmt.Errorf("cannot expand %q: the home directory is empty", raw)
	}
	return home + raw[1:], nil
}

// root is the longest wildcard-free prefix of slash. A pattern without a wildcard is its own
// root, so resolving it needs no directory walk.
func root(slash string, wildcard bool) string {
	if !wildcard {
		return slash
	}
	base, _ := doublestar.SplitPattern(slash)
	return base
}

// absolute is the fallback for a path that cannot be made absolute.
func absolute(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// sameFileSeen compares file identity, so a hard link or a second spelling of one file is
// resolved once.
func sameFileSeen(seen []os.FileInfo, info os.FileInfo) bool {
	for _, accepted := range seen {
		if os.SameFile(accepted, info) {
			return true
		}
	}
	return false
}

// backslashFinding shows the pattern with forward slashes, which is the form the matcher
// reads as separators and not as escapes.
func backslashFinding(pattern Pattern) config.Finding {
	slash := strings.ReplaceAll(pattern.Slash, `\`, "/")
	return config.Finding{
		Level:   config.LevelFail,
		Subject: slash,
		Message: fmt.Sprintf("pattern %q contains a backslash: the matcher reads it as an escape, not as a separator", slash),
		Suggest: fmt.Sprintf("write it with forward slashes: %q", slash),
	}
}

// staleFinding is a warning, so a user can write the config before the skill exists.
func staleFinding(pattern Pattern) config.Finding {
	return config.Finding{
		Level:   config.LevelWarn,
		Subject: pattern.Slash,
		Message: fmt.Sprintf("pattern %q matches nothing: %s", pattern.Slash, staleText),
	}
}
