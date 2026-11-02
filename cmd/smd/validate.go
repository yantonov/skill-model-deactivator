package main

import (
	"fmt"
	"io"
	"os"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/yantonov/skill-model-deactivator/internal/config"
	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/frontmatter"
	"github.com/yantonov/skill-model-deactivator/internal/match"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

// Each ok finding prints once for the whole run and not once per file, so a tree of many
// clean files still prints three lines.
const (
	okConfigParses    = "config parses"
	okPatternsYield   = "patterns yield files"
	okFrontmatterRead = "frontmatter parses in all matched files"
)

// The frontmatter warnings end with these, so a reader can select them.
const (
	noNameText     = "no name key in frontmatter"
	needsPatchText = "needs disable-model-invocation: true"
)

// runConfigValidate reads and never writes, so it is safe in a pre-commit hook. The report
// goes to stderr; only --json and the example config for a missing file reach stdout.
func runConfigValidate(stdout, stderr io.Writer, jsonOutput, strict bool) int {
	loaded, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	logPath, err := platform.LogPath()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	resolved, err := match.Resolve(loaded.Config.Patterns)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	report := display.ValidateReport{
		Loaded:   loaded,
		LogPath:  logPath,
		Patterns: len(loaded.Config.Patterns),
		Files:    len(resolved.Files),
		Findings: config.Validate(loaded, validateFindings(loaded, resolved)),
		Strict:   strict,
		NoConfig: !loaded.Exists,
	}

	if jsonOutput {
		if err := display.ValidateJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", appName, err)
			return 1
		}
		return report.ExitCode()
	}

	if err := display.Validate(stderr, report); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}
	if !loaded.Exists {
		fmt.Fprintf(stdout, "No config file at %s. Write the default one with:\n\n  smd config create\n\nOr paste this example:\n\n%s",
			display.Tilde(loaded.Path), config.DefaultConfig)
	}
	return report.ExitCode()
}

func validateFindings(loaded config.Loaded, resolved match.Result) []config.Finding {
	var findings []config.Finding

	if loaded.Exists && !hasLevel(loaded.Findings, config.LevelFail) {
		findings = append(findings, config.Finding{Level: config.LevelOK, Message: okConfigParses})
	}
	if len(loaded.Config.Patterns) > 0 && !hasStale(resolved.Findings) {
		findings = append(findings, config.Finding{Level: config.LevelOK, Message: okPatternsYield})
	}

	parsed := make([]match.Pattern, 0, len(loaded.Config.Patterns))
	for _, raw := range loaded.Config.Patterns {
		pattern, err := match.Parse(raw)
		if err != nil {
			// Resolve already returned this error.
			continue
		}
		parsed = append(parsed, pattern)
		if !doublestar.ValidatePattern(pattern.Slash) {
			findings = append(findings, config.Finding{
				Level:   config.LevelFail,
				Subject: pattern.Slash,
				Message: fmt.Sprintf("pattern %q has an unbalanced bracket or brace", pattern.Slash),
				Suggest: "close the bracket or brace, or remove it from the pattern",
			})
		}
	}

	findings = append(findings, resolved.Findings...)

	// The third ok line prints with the other two and before the per-file warnings.
	fileFindings, frontmatterOK := frontmatterFindings(resolved.Files)
	if frontmatterOK {
		findings = append(findings, config.Finding{Level: config.LevelOK, Message: okFrontmatterRead})
	}

	// Two patterns usually share one root, so a missing root is reported once.
	seenRoots := make(map[string]bool, len(parsed))
	for _, pattern := range parsed {
		if seenRoots[pattern.Root] {
			continue
		}
		seenRoots[pattern.Root] = true
		if _, err := os.Stat(pattern.Root); err != nil {
			findings = append(findings, config.Finding{
				Level:   config.LevelWarn,
				Subject: pattern.Root,
				Message: fmt.Sprintf("watch root %q does not exist", pattern.Root),
				Suggest: "create the directory, or fix the pattern",
			})
		}
	}

	return append(findings, fileFindings...)
}

// frontmatterFindings reads each file and writes none: config validate never writes. It also
// reports whether the tree is clean. A suggested change names the action the planner would
// take, insert or rewrite.
func frontmatterFindings(files []string) ([]config.Finding, bool) {
	var findings []config.Finding
	clean := len(files) > 0

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			// A file that cannot be read leaves the aggregate ok line out: a tree that could
			// not be read must not report that every frontmatter parses.
			clean = false
			continue
		}

		if !frontmatter.HasFrontmatter(data) {
			clean = false
			findings = append(findings, config.Finding{
				Level:   config.LevelWarn,
				Subject: path,
				Message: "no frontmatter: the file needs an opening --- line and a closing --- line",
				Suggest: "add the frontmatter by hand: this tool never creates it",
			})
			continue
		}

		plan := frontmatter.PlanFor(data)
		if plan.Action == frontmatter.ActionWarn {
			clean = false
			findings = append(findings, config.Finding{
				Level:   config.LevelWarn,
				Subject: path,
				Message: plan.Warning,
				Suggest: "fix the frontmatter by hand: this tool never repairs it",
			})
			continue
		}

		if _, ok := frontmatter.Name(data); !ok {
			findings = append(findings, config.Finding{
				Level:   config.LevelWarn,
				Subject: path,
				Message: noNameText,
				Suggest: "add a top-level name key",
			})
		}

		// A needed patch is a warn and not an ok, because validate never writes.
		switch plan.Action {
		case frontmatter.ActionInsert:
			findings = append(findings, config.Finding{
				Level:   config.LevelWarn,
				Subject: path,
				Message: needsPatchText + " (insert)",
				Suggest: "run smd serve --once to apply it",
			})
		case frontmatter.ActionRewrite:
			findings = append(findings, config.Finding{
				Level:   config.LevelWarn,
				Subject: path,
				Message: needsPatchText + " (rewrite)",
				Suggest: "run smd serve --once to apply it",
			})
		}
	}

	return findings, clean
}

func hasLevel(findings []config.Finding, level config.Level) bool {
	for _, finding := range findings {
		if finding.Level == level {
			return true
		}
	}
	return false
}

func hasStale(findings []config.Finding) bool {
	for _, finding := range findings {
		if match.IsStale(finding) {
			return true
		}
	}
	return false
}
