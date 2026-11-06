// Package reconcile runs the reconcile loop, its plans, dry-run, and summaries.
package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yantonov/skill-model-deactivator/internal/config"
	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/frontmatter"
	"github.com/yantonov/skill-model-deactivator/internal/match"
)

// tmpSuffix is the tail of the writer's temp file, so a stray temp file that a pattern
// happens to match is never treated as a skill.
const tmpSuffix = ".smd-tmp"

// A logger labels a line with one of these and never filters on it.
const (
	levelDebug = "debug"
	levelInfo  = "info"
	levelWarn  = "warn"
	levelError = "error"
)

// readFile is a var so a test can fail a read on every platform.
var readFile = os.ReadFile

// KV is one field of a log line.
type KV struct {
	Key   string
	Value string
}

// Logger receives one log line as data, with the fields in print order. The caller renders it.
type Logger interface {
	Line(level, msg string, kv ...KV)
}

// Options is the caller's request for one pass.
type Options struct {
	// DryRun never calls Write, so a file that needs a patch is logged as would-patch.
	DryRun bool

	// Logger receives every line. A nil Logger discards them.
	Logger Logger

	// Locks is the per-file lock. The daemon passes one so every file is read, planned, and
	// written under its own lock; a one-off run passes nil and takes no lock.
	Locks *FileLocks

	// Write applies one plan. Nil is report-only, so a caller that forgets the writer cannot
	// write by accident.
	Write func(path string, plan frontmatter.Plan) error
}

// Plan is the decision for one file, returned as data so a caller can assert without parsing
// a log line. A file the planner could only warn about, or could not read, has no plan.
type Plan struct {
	Path   string
	Action frontmatter.Action
	Reason string
}

// Summary is what one pass did. Watched counts the resolved files, so a file the pass skipped
// as a temp file is still watched.
type Summary struct {
	Watched, Changed, Wrote, Skipped, Failed int
	Duration                                 time.Duration
}

// Pass resolves the patterns, plans every resolved file, and applies the ones that need it.
// The error is non-nil only when the pass as a whole cannot run: a per-file problem is a log
// line and a counter, never a stop.
func Pass(cfg config.Config, opts Options) (Summary, []Plan, error) {
	start := time.Now()
	logger := opts.logger()
	plans := make([]Plan, 0)

	resolved, err := match.Resolve(cfg.Patterns)
	if err != nil {
		// No summary line: the counts would read as a pass that did nothing wrong.
		return Summary{}, plans, err
	}

	for _, finding := range resolved.Findings {
		logFinding(logger, finding)
	}

	summary := Summary{Watched: len(resolved.Files)}
	for _, path := range resolved.Files {
		if strings.HasSuffix(filepath.Base(path), tmpSuffix) {
			continue
		}
		unlock := lockPath(opts.Locks, path)
		func() {
			defer unlock()
			reconcileFile(path, opts, &summary, &plans)
		}()
	}

	summary.Duration = time.Since(start)
	// The duration is in the message and not a field, because the message prints last.
	logger.Line(levelInfo, fmt.Sprintf("reconcile finished duration=%s", summary.Duration),
		kv("watched", strconv.Itoa(summary.Watched)),
		kv("changed", strconv.Itoa(summary.Changed)),
		kv("wrote", strconv.Itoa(summary.Wrote)),
		kv("skipped", strconv.Itoa(summary.Skipped)),
		kv("failed", strconv.Itoa(summary.Failed)))
	return summary, plans, nil
}

func reconcileFile(path string, opts Options, summary *Summary, plans *[]Plan) {
	logger := opts.logger()

	data, err := readFile(path)
	if err != nil {
		summary.Failed++
		logger.Line(levelError, "",
			kv("path", displayPath(path)),
			kv("result", "failed"),
			kv("error", err.Error()))
		return
	}

	plan := frontmatter.PlanFor(data)
	if plan.Action != frontmatter.ActionWarn {
		*plans = append(*plans, Plan{Path: path, Action: plan.Action, Reason: plan.Reason})
	}
	switch plan.Action {
	case frontmatter.ActionNone:
		logger.Line(levelDebug, "",
			kv("path", displayPath(path)),
			kv("desired", "true"),
			kv("action", actionText(plan.Action)),
			kv("result", "noop"))
	case frontmatter.ActionWarn:
		// There is no edit to make, so there is no write attempt either.
		summary.Skipped++
		logger.Line(levelWarn, plan.Warning,
			kv("path", displayPath(path)),
			kv("result", "skipped"))
	case frontmatter.ActionInsert, frontmatter.ActionRewrite:
		summary.Changed++
		if opts.DryRun || opts.Write == nil {
			logger.Line(levelInfo, "",
				kv("path", displayPath(path)),
				kv("desired", "true"),
				kv("reason", plan.Reason),
				kv("action", actionText(plan.Action)),
				kv("result", "would-patch"))
		} else if err := opts.Write(path, plan); err != nil {
			summary.Failed++
			logger.Line(levelError, "",
				kv("path", displayPath(path)),
				kv("action", actionText(plan.Action)),
				kv("result", "failed"),
				kv("error", err.Error()))
		} else {
			summary.Wrote++
			logger.Line(levelInfo, "",
				kv("path", displayPath(path)),
				kv("desired", "true"),
				kv("reason", plan.Reason),
				kv("action", actionText(plan.Action)),
				kv("result", "ok"))
		}
	}
}

func lockPath(locks *FileLocks, path string) func() {
	if locks == nil {
		return func() {}
	}
	return locks.Lock(path)
}

func (o Options) logger() Logger {
	if o.Logger == nil {
		return discard{}
	}
	return o.Logger
}

type discard struct{}

func (discard) Line(string, string, ...KV) {}

func kv(key, value string) KV {
	return KV{Key: key, Value: value}
}

// displayPath is display-only: the pass reads and plans the real path.
func displayPath(path string) string {
	return display.Tilde(path)
}

func actionText(action frontmatter.Action) string {
	switch action {
	case frontmatter.ActionInsert:
		return "inserted"
	case frontmatter.ActionRewrite:
		return "rewritten"
	default:
		return "none"
	}
}

// logFinding logs a fail finding as an error: every failure is an error line.
func logFinding(logger Logger, finding config.Finding) {
	level := levelWarn
	if finding.Level == config.LevelFail {
		level = levelError
	}
	logger.Line(level, finding.Message, kv("pattern", finding.Subject))
}
