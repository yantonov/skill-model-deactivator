package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yantonov/skill-model-deactivator/internal/config"
	"github.com/yantonov/skill-model-deactivator/internal/daemon"
	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/match"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
	"github.com/yantonov/skill-model-deactivator/internal/reconcile"
	"github.com/yantonov/skill-model-deactivator/internal/watch"
)

// runServe holds the parsed config for the life of the process: there is no watcher on the
// file and no re-read between passes, so restarting the daemon is how an edit takes effect.
// A pass exits 0 even when a file failed, because a per-file problem never stops a pass.
func runServe(rest []string, stdout, stderr io.Writer) int {
	once, dryRun, help := false, false, false
	parsed := parseFlags("serve", stderr, rest, func(flags *flag.FlagSet) {
		flags.BoolVar(&once, "once", false, "run one reconcile and exit")
		flags.BoolVar(&dryRun, "dry-run", false, "report the planned changes and write nothing")
		// serve answers a help flag with the command help, not the shared one-line error.
		flags.BoolVar(&help, "h", false, "show this help")
		flags.BoolVar(&help, "help", false, "show this help")
	})
	if !parsed {
		return 1
	}
	if help {
		fmt.Fprint(stdout, serveHelpText())
		return 0
	}

	loaded, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	// The check sits before the lock, so a refused run is never mistaken for a live daemon. It
	// does write the reason to the log, because a failed bootstrap is where the reason matters
	// most: reportRefusal creates the state directory and the log on a machine that has neither.
	if reason := unusableConfig(loaded); reason != "" {
		fmt.Fprintf(stderr, "%s: %s\n", appName, reason)
		reportRefusal(reason)
		return 1
	}

	stateDir, err := platform.StateDir()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	cfg := loaded.Config

	if once || dryRun {
		logPath := logPathOf(stateDir)
		// --once takes no lock: it may run beside the daemon, and the per-process temp name
		// already makes two concurrent writers safe.
		logger, err := daemon.NewLogger(stateDir, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", appName, err)
			return 1
		}
		defer logger.Close()

		daemon.Startup(logger, loaded.Path, logPath, watchRoots(cfg.Patterns))
		opts := reconcile.Options{DryRun: dryRun, Logger: logger}
		// A dry run gets no writer at all, not merely an unused one.
		if !dryRun {
			opts.Write = reconcile.Write
		}
		daemon.PassStart(logger, len(cfg.Patterns))
		pass := func() error {
			_, _, err := reconcile.Pass(cfg, opts)
			return err
		}
		if err := pass(); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", appName, err)
			return 1
		}
		return 0
	}

	return runDaemon(loaded, stateDir, stdout, stderr)
}

// unusableConfig prefers a load failure, which names the broken key, over an empty pattern
// list: a pass with no pattern would report a clean result it did not earn.
func unusableConfig(loaded config.Loaded) string {
	for _, finding := range loaded.Findings {
		if finding.Level != config.LevelFail {
			continue
		}
		if line, _, _ := strings.Cut(finding.Message, "\n"); line != "" {
			return line
		}
		return "the config is not usable"
	}
	if len(loaded.Config.Patterns) == 0 {
		return fmt.Sprintf("no %s in %s: list at least one file or glob to keep in sync",
			config.FieldPatterns, display.Tilde(loaded.Path))
	}
	return ""
}

// reportRefusal is best effort by design: the reason is already on stderr, and the log line only
// makes it visible to `smd log`, which is where a user looks when a daemon looks dead.
func reportRefusal(reason string) {
	stateDir, err := platform.StateDir()
	if err != nil {
		return
	}
	daemon.Refused(stateDir, reason)
}

// runDaemon takes the lock, builds the logger, logs the startup line, runs one pass, and only
// then starts the watcher. The startup pass therefore happens before the watcher exists and
// cannot be overtaken by an event it would duplicate.
func runDaemon(loaded config.Loaded, stateDir string, stdout, stderr io.Writer) int {
	lock, err := daemon.AcquireLock(stateDir)
	if err != nil {
		// A second instance is not a failure: the running daemon will do the work.
		var held *daemon.HeldError
		if errors.As(err, &held) {
			fmt.Fprintf(stderr, "%s: %v\n", appName, held)
			return 0
		}
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}
	defer lock.Release()

	logger, err := daemon.NewLogger(stateDir, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}
	defer logger.Close()

	// The context spans the pass and the loop, so a signal during the startup pass is not
	// lost. It is built after the lock, so the second instance installs no handler.
	ctx, stopSignals := daemonSignals()
	defer stopSignals()

	cfg := loaded.Config
	locks := &reconcile.FileLocks{}
	opts := reconcile.Options{Logger: logger, Write: reconcile.Write, Locks: locks}
	roots := watchRoots(cfg.Patterns)

	pass := func() error {
		daemon.PassStart(logger, len(cfg.Patterns))
		_, _, err := reconcile.Pass(cfg, opts)
		return err
	}

	daemon.Startup(logger, loaded.Path, logPathOf(stateDir), roots)
	if err := pass(); err != nil {
		daemon.PassError(logger, err)
	}

	watcher, err := watch.New(cfg, logger)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	loop := daemon.Loop{
		Ctx:       ctx,
		Trigger:   watcher.Triggers(),
		Rescan:    daemon.NewRescan(cfg.RescanInterval),
		Refresher: watcher,
		Logger:    logger,
		Pass:      pass,
	}
	daemon.Serving(logger)
	loop.Run()

	// The shutdown line comes after the watcher is closed and the loop has returned, so it
	// follows the last write of the pass that was in flight and never interrupts one.
	watcher.Close()
	daemon.Shutdown(logger)
	// The deferred calls run here: signal handling, log file, then the lock, so the lock
	// outlives everything that could still write a line.
	return 0
}

// daemonSignals is a var so a test can drive the loop it started.
var daemonSignals = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func logPathOf(stateDir string) string {
	return filepath.Join(stateDir, platform.LogFileName)
}

// watchRoots leaves out a pattern that cannot be parsed: the pass reports it, and the startup
// line is not the place for a second report.
func watchRoots(patterns []string) []string {
	roots := make([]string, 0, len(patterns))
	seen := make(map[string]bool, len(patterns))
	for _, raw := range patterns {
		pattern, err := match.Parse(raw)
		if err != nil || seen[pattern.Root] {
			continue
		}
		seen[pattern.Root] = true
		roots = append(roots, pattern.Root)
	}
	return roots
}

func serveHelpText() string {
	return `smd serve - reconcile the matched skills

USAGE
  smd serve [--once] [--dry-run]

OPTIONS
  --once      Run one reconcile and exit.
  --dry-run   Run one dry pass, report the planned changes, write nothing, and exit.

Without a flag, serve reconciles once at start and then keeps the matched files in sync.
--dry-run without --once runs one dry pass and exits: it does not start the watch loop.

The config is read once, at startup. The config path is always ~/.skill-model-deactivator.json.
`
}
