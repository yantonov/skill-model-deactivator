package daemon

import (
	"strconv"
	"strings"

	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
)

// Startup puts the roots in the message, because a line carries each key once and a config
// usually names several roots. The sink never touches a value, so nothing is abbreviated twice.
func Startup(logger reconcile.Logger, configPath, logPath string, roots []string) {
	message := ""
	if len(roots) > 0 {
		shown := make([]string, 0, len(roots))
		for _, root := range roots {
			shown = append(shown, display.Tilde(root))
		}
		message = "watched roots=" + strings.Join(shown, " ")
	}
	logger.Line("info", message,
		reconcile.KV{Key: "config", Value: display.Tilde(configPath)},
		reconcile.KV{Key: "log", Value: display.Tilde(logPath)})
}

// PassStart carries the pattern count, because the file count is not known until the pass
// resolves the patterns.
func PassStart(logger reconcile.Logger, patterns int) {
	logger.Line("info", "reconcile started",
		reconcile.KV{Key: "patterns", Value: strconv.Itoa(patterns)})
}

// PassError logs one line and the caller continues: the daemon keeps running, and the next
// start reconciles fully.
func PassError(logger reconcile.Logger, err error) {
	logger.Line("error", "reconcile failed",
		reconcile.KV{Key: "error", Value: err.Error()})
}

// Serving marks the moment the watcher is live, so a log that ends on this line is a daemon that
// is watching and has done no work since.
func Serving(logger reconcile.Logger) {
	logger.Line("info", "serving")
}

// Shutdown is logged after the loop has returned and the watcher is closed, so it follows the
// last write of the pass that was in flight and never interrupts one. Nothing interrupts it
// after that point: signal.NotifyContext's handler only cancels a context and calls no
// os.Exit.
func Shutdown(logger reconcile.Logger) {
	logger.Line("info", "shutdown")
}

// Refused records a run that never started: without the line the log ends at the last run of an
// older process, which reads as a tool that never started.
//
// The line goes to the log file and not to stdout, because the reason already went to stderr.
// The first refused run on a machine creates the state directory and the log, so a bootstrap
// that goes wrong leaves the reason in the one place `smd log` reads.
func Refused(stateDir, reason string) {
	logger, err := NewLogger(stateDir, nil)
	if err != nil {
		// The reason is already on stderr, so a failed log write is not a second report.
		return
	}
	defer logger.Close()
	logger.Line("error", "refused to start", reconcile.KV{Key: "reason", Value: reason})
}
