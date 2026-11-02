package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/logread"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

// runLog needs no state, so it works whether or not the daemon runs, and it never writes,
// truncates, or rotates the file.
func runLog(rest []string, stdout, stderr io.Writer) int {
	lines := &lineCount{value: logread.Lines}
	follow, all := false, false
	parsed := parseFlags("log", stderr, rest, func(flags *flag.FlagSet) {
		flags.Var(lines, "lines", "number of log lines to print")
		flags.BoolVar(&follow, "follow", false, "keep printing new log lines")
		flags.BoolVar(&all, "all", false, "print the whole log, including the rotated file")
	})
	if !parsed {
		return 1
	}

	stateDir, err := platform.StateDir()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}
	logPath, err := platform.LogPath()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	initial, offset, err := readLog(stateDir, lines.value, all)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	header := logHeader(display.Tilde(logPath), lines.value, all, initial)
	if _, err := fmt.Fprintf(stdout, "%s\n\n", header); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}
	if err := writeLogLines(stdout, stderr, initial.lines); err != nil {
		return 1
	}
	if !follow {
		return 0
	}
	return followLog(stateDir, offset, stdout, stderr)
}

type logRead struct {
	lines   []string
	total   int
	rotated bool
}

func readLog(stateDir string, n int, all bool) (logRead, int64, error) {
	if !all {
		lines, total, offset, err := logread.Tail(stateDir, n)
		return logRead{lines: lines, total: total}, offset, err
	}
	lines, rotated, offset, err := logread.All(stateDir)
	return logRead{lines: lines, total: len(lines), rotated: rotated}, offset, err
}

// logHeader carries no thousands separator, because a script greps this line. `--all` covers
// the whole file, so it prints no "last N", and drops the total when there is no rotated file
// to explain it.
func logHeader(path string, n int, all bool, read logRead) string {
	switch {
	case all && read.rotated:
		return fmt.Sprintf("%s (%d lines)", path, read.total)
	case all:
		return path
	default:
		return fmt.Sprintf("%s (last %d of %d lines)", path, n, read.total)
	}
}

// followLog starts at the offset the first read ended at, so no line is printed twice. The
// stream ends on the signal context, so Ctrl-C exits 0.
func followLog(stateDir string, offset int64, stdout, stderr io.Writer) int {
	ctx, stop := logSignals()
	defer stop()

	if err := logread.Follow(stateDir, offset, stdout, ctx.Done()); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}
	return 0
}

// logSignals is a var so a test can drive a stream it started.
var logSignals = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func writeLogLines(stdout, stderr io.Writer, lines []string) error {
	for _, line := range lines {
		if _, err := io.WriteString(stdout, line+"\n"); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", appName, err)
			return err
		}
	}
	return nil
}
