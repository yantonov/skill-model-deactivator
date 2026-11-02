package display

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yantonov/skill-model-deactivator/internal/config"
)

// ValidateReport is everything `smd config validate` prints.
type ValidateReport struct {
	Loaded   config.Loaded
	LogPath  string
	Patterns int
	Files    int
	Findings []config.Finding
	Strict   bool
	NoConfig bool
}

func (r ValidateReport) Counts() (ok, warn, fail int) {
	for _, finding := range r.Findings {
		switch finding.Level {
		case config.LevelFail:
			fail++
		case config.LevelWarn:
			warn++
		default:
			ok++
		}
	}
	return ok, warn, fail
}

// ExitCode is 0 when every finding is ok or warn, and 1 when any finding is fail, when
// --strict meets a warn, or when there is no config file to validate.
func (r ValidateReport) ExitCode() int {
	_, warn, fail := r.Counts()
	if fail > 0 || r.NoConfig {
		return 1
	}
	if r.Strict && warn > 0 {
		return 1
	}
	return 0
}

// Validate writes the header block, one line per finding, then the summary. A finding line
// starts with its level, so a reader can select on it.
func Validate(w io.Writer, r ValidateReport) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%-13s%s %s\n", "config", Tilde(r.Loaded.Path), ConfigStatus(r.Loaded))
	fmt.Fprintf(&b, "%-13s%s, %s\n", "patterns", count(r.Patterns, "pattern"), count(r.Files, "file"))
	exists, size := logStatusOf(r.LogPath)
	fmt.Fprintf(&b, "%-13s%s %s\n", "log", Tilde(r.LogPath), LogStatus(exists, size))
	b.WriteString("\n")

	for _, finding := range r.Findings {
		fmt.Fprintf(&b, "%-13s%s\n", finding.Level.String(), findingLine(finding))
	}

	_, warn, fail := r.Counts()
	fmt.Fprintf(&b, "\n%s, %s\n", count(warn, "warning"), count(fail, "failure"))

	_, err := io.WriteString(w, b.String())
	return err
}

// jsonValidateReport holds the fields in the order they appear.
type jsonValidateReport struct {
	Findings []jsonFinding `json:"findings"`
	Summary  jsonSummary   `json:"summary"`
	ExitCode int           `json:"exit_code"`
}

// jsonFinding always carries every field, so a reader needs no presence test.
type jsonFinding struct {
	Level   string `json:"level"`
	Subject string `json:"subject"`
	Message string `json:"message"`
	Suggest string `json:"suggest"`
}

type jsonSummary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

func ValidateJSON(w io.Writer, r ValidateReport) error {
	ok, warn, fail := r.Counts()
	report := jsonValidateReport{
		Findings: make([]jsonFinding, 0, len(r.Findings)),
		Summary:  jsonSummary{OK: ok, Warn: warn, Fail: fail},
		ExitCode: r.ExitCode(),
	}
	for _, finding := range r.Findings {
		report.Findings = append(report.Findings, jsonFinding{
			Level:   finding.Level.String(),
			Subject: abbreviateHome(finding.Subject),
			Message: abbreviateHome(finding.Message),
			Suggest: abbreviateHome(finding.Suggest),
		})
	}
	return json.NewEncoder(w).Encode(report)
}

// findingLine renders `subject: message`, and a message that already names its subject on its
// own, which is how a pattern finding prints. The home directory is written as ~, so the line
// reads like the config the user wrote.
func findingLine(finding config.Finding) string {
	subject, message := abbreviateHome(finding.Subject), abbreviateHome(finding.Message)
	switch {
	case subject == "":
		return message
	case message == "":
		return subject
	case strings.Contains(message, subject):
		return message
	}
	return subject + ": " + message
}

func count(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// logStatusOf reports an unreadable path as missing: validate never fails on the log path.
func logStatusOf(path string) (bool, int64) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false, 0
	}
	return true, info.Size()
}
