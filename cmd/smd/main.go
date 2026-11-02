// Command smd keeps skill frontmatter in sync with a declared list of patterns.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/yantonov/skill-model-deactivator/internal/config"
	"github.com/yantonov/skill-model-deactivator/internal/display"
	"github.com/yantonov/skill-model-deactivator/internal/platform"
)

const (
	appName = "smd"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if hasConfigFlag(args) {
		fmt.Fprintf(stderr, "%s: unknown flag %q: the config path is always %s\n",
			appName, "--config", configPath())
		return 1
	}

	if len(args) == 0 {
		// Help on stdout, and still a usage error: stderr stays for failure reports.
		runHelp(stdout, stderr)
		return 1
	}

	command, rest := args[0], args[1:]
	switch command {
	case "config":
		return runConfig(rest, stdout, stderr)
	case "serve":
		return runServe(rest, stdout, stderr)
	case "log":
		return runLog(rest, stdout, stderr)
	case "help":
		if !parseFlags("help", stderr, rest, func(*flag.FlagSet) {}) {
			return 1
		}
		return runHelp(stdout, stderr)
	case "version":
		if !parseFlags("version", stderr, rest, func(*flag.FlagSet) {}) {
			return 1
		}
		fmt.Fprintf(stdout, "%s %s %s %s/%s\n",
			appName, version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n", appName, command)
		return 1
	}
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "%s: missing subcommand for %q\n", appName, "config")
		return 1
	}

	subcommand, rest := args[0], args[1:]
	switch subcommand {
	case "show":
		jsonOutput := false
		parsed := parseFlags("config show", stderr, rest, func(flags *flag.FlagSet) {
			flags.BoolVar(&jsonOutput, "json", false, "emit machine-readable output")
		})
		if !parsed {
			return 1
		}
		return runConfigShow(stdout, stderr, jsonOutput)
	case "validate":
		jsonOutput, strict := false, false
		parsed := parseFlags("config validate", stderr, rest, func(flags *flag.FlagSet) {
			flags.BoolVar(&jsonOutput, "json", false, "emit machine-readable output")
			flags.BoolVar(&strict, "strict", false, "treat a warning as a failure")
		})
		if !parsed {
			return 1
		}
		return runConfigValidate(stdout, stderr, jsonOutput, strict)
	case "create":
		if !parseFlags("config create", stderr, rest, func(*flag.FlagSet) {}) {
			return 1
		}
		return runConfigCreate(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n", appName, "config "+subcommand)
		return 1
	}
}

// parseFlags parses args with a flag set for one command. The flag package writes its
// own one-line error to stderr; this function adds a line only when the package wrote
// none, which is the case for -h and --help.
func parseFlags(command string, stderr io.Writer, args []string, define func(*flag.FlagSet)) bool {
	flags := flag.NewFlagSet(appName+" "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {}
	define(flags)

	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(stderr, "%s: %q takes no help flag: run %q\n", appName, command, "smd help")
		return false
	}
	if err != nil {
		return false
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unknown argument %q\n", appName, flags.Arg(0))
		return false
	}
	return true
}

// runConfigShow reports what is there and judges nothing, so it exits 0 even when the
// config does not parse.
func runConfigShow(stdout, stderr io.Writer, jsonOutput bool) int {
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

	report := display.Report{Loaded: loaded, LogPath: logPath}
	if info, err := os.Stat(logPath); err == nil && !info.IsDir() {
		report.LogExists = true
		report.LogBytes = info.Size()
	}

	if jsonOutput {
		err = display.ShowJSON(stdout, report)
	} else {
		err = display.Show(stdout, report)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}
	return 0
}

// runConfigCreate refuses an existing file rather than overwriting a list the user wrote.
func runConfigCreate(stdout, stderr io.Writer) int {
	path, err := config.Create()
	if err != nil {
		if errors.Is(err, config.ErrExists) {
			fmt.Fprintf(stderr, "%s: %s: %s\n", appName, err, display.Tilde(path))
			fmt.Fprintf(stderr, "%s: edit the file instead, or move it out of the way to start over\n", appName)
			return 1
		}
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	fmt.Fprintf(stdout, "created %s\n\n%s", display.Tilde(path), config.DefaultConfig)
	return 0
}

type lineCount struct {
	value int
}

func (c *lineCount) String() string {
	return strconv.Itoa(c.value)
}

func (c *lineCount) Set(raw string) error {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return errors.New("must be a number greater than 0")
	}
	c.value = n
	return nil
}

// hasConfigFlag rejects --config before dispatch, wherever it appears: the path is fixed.
func hasConfigFlag(args []string) bool {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		if name == "config" || strings.HasPrefix(name, "config=") {
			return true
		}
	}
	return false
}

// runHelp loads the config only to report its status, so it works with no config file and
// with no state directory, and it writes nothing.
func runHelp(stdout, stderr io.Writer) int {
	loaded, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	logPathValue, err := platform.LogPath()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	exists, size := logStatusOf(logPathValue)
	if _, err := fmt.Fprintf(stdout, "%s - %s\n\n", appName, helpTitle); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}
	if err := display.HelpPaths(stdout, loaded, logPathValue, exists, size); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}

	if _, err := io.WriteString(stdout, helpText()); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 1
	}
	return 0
}

// configPath is the config path as a message shows it: abbreviated and slash-separated.
func configPath() string {
	path, err := config.Path()
	if err != nil {
		return config.FileName
	}
	return display.Tilde(path)
}

// logStatusOf reports the log path as missing rather than failing, so help never errors on it.
func logStatusOf(path string) (bool, int64) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false, 0
	}
	return true, info.Size()
}

const helpTitle = "keep skill frontmatter in sync with a declared list of patterns"

// helpText is the help body without the title and the two path lines, which runHelp prints.
func helpText() string {
	return `
COMMANDS
  config show       Show the effective config and both paths.
  config validate   Check the config and the matched files. No writes.
  config create     Write the default config. Refuses an existing file.
  serve             Start the daemon: reconcile now, then watch for changes.
  log               Print the last log lines. --lines N, --follow, --all.
  help              Show this text.
  version           Show the build version.

OPTIONS
  --dry-run   Report the planned changes and write nothing.
  --once      Run one reconcile and exit.
  --json      Emit machine-readable output (config show, config validate).
  --strict    Treat a warning as a failure (config validate).
  --lines N   Number of log lines to print (log). Default 10.
  --follow    Keep printing new log lines (log).
  --all       Print the whole log, including the rotated file (log).

The config path is always ~/.skill-model-deactivator.json. There is no --config flag.
`
}
