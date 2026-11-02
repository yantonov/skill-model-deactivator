package frontmatter

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	key = "disable-model-invocation"

	// nameKey is read by config validate only. The planner never reads it.
	nameKey = "name"

	// canonical is the one spelling both harnesses read: unquoted, exactly one space after
	// the colon, and none before it.
	canonical = key + ": true"

	delimiter = "---"

	// bom is accepted before the opening delimiter.
	bom = "\xef\xbb\xbf"
)

// Region holds the byte offsets of the frontmatter and not a copy of the text. The bytes
// before OpenStart are the BOM, which the caller copies unchanged.
type Region struct {
	Found      bool
	OpenStart  int
	OpenEnd    int
	CloseStart int
	CloseEnd   int
}

// Detect returns Region{Found: false} for a file without frontmatter or with frontmatter that
// never closes. That is not an error.
func Detect(b []byte) Region {
	openStart := 0
	if bytes.HasPrefix(b, []byte(bom)) {
		openStart = len(bom)
	}
	openEnd, ok := fenceEnd(b, openStart)
	if !ok {
		return Region{}
	}
	for i := openEnd; i < len(b); {
		if closeEnd, ok := fenceEnd(b, i); ok {
			return Region{Found: true, OpenStart: openStart, OpenEnd: openEnd, CloseStart: i, CloseEnd: closeEnd}
		}
		_, next := line(b, i, len(b))
		i = next
	}
	return Region{}
}

func HasFrontmatter(b []byte) bool {
	return Detect(b).Found
}

// Name returns the top-level name scalar. A name that parses as a non-string scalar is
// returned as its text. A missing name is never a plan, so this is a read and nothing else.
func Name(b []byte) (string, bool) {
	region := Detect(b)
	if !region.Found {
		return "", false
	}
	doc, err := parse(b, region)
	if err != nil {
		return "", false
	}
	value, ok := doc[nameKey]
	if !ok || value == nil {
		return "", false
	}
	if text, ok := value.(string); ok {
		return text, true
	}
	return fmt.Sprint(value), true
}

// newline is the terminator of the opening delimiter line. An inserted line takes it, so a
// CRLF file stays CRLF even when its closing delimiter ends with a bare LF.
func (r Region) newline(b []byte) string {
	return string(b[r.OpenStart+len(delimiter) : r.OpenEnd])
}

// fenceEnd returns the offset just past the line that starts at i when that line is exactly
// the delimiter, for LF, CRLF, or end of file.
func fenceEnd(b []byte, i int) (int, bool) {
	if !bytes.HasPrefix(b[i:], []byte(delimiter)) {
		return 0, false
	}
	end := i + len(delimiter)
	switch {
	case end == len(b):
		return end, true
	case b[end] == '\n':
		return end + 1, true
	case b[end] == '\r' && end+1 < len(b) && b[end+1] == '\n':
		return end + 2, true
	}
	return 0, false
}

// line returns the end of the content with one trailing CR removed, and the start of the next
// line.
func line(b []byte, i, end int) (contentEnd, next int) {
	j := i
	for j < end && b[j] != '\n' {
		j++
	}
	next = j
	if j < end {
		next = j + 1
	}
	contentEnd = j
	if contentEnd > i && b[contentEnd-1] == '\r' {
		contentEnd--
	}
	return contentEnd, next
}

// keyLine matches one line at a time. The column-0 anchor excludes a key nested under another
// key and the same text inside a block scalar, so no (?m) flag is needed and the byte offsets
// stay usable for the rewrite.
var keyLine = regexp.MustCompile("^" + key + `[ \t]*:[ \t]*(.*)$`)

var (
	// Both sets are matched against a value already trimmed, unquoted, and lowercased.
	truthy = map[string]bool{"true": true, "yes": true, "on": true, "1": true}
	falsy  = map[string]bool{"false": true, "no": true, "off": true, "0": true}
)

// Action is the one thing a Plan does to a file. The zero value is ActionNone.
type Action int

const (
	ActionNone Action = iota
	ActionInsert
	ActionRewrite
	ActionWarn
)

// Plan is the decision for one file. Output is valid only for ActionInsert and ActionRewrite
// and is nil otherwise, so a caller cannot write by accident. A plan never deletes the key.
type Plan struct {
	Action  Action
	Reason  string // "absent", "non-canonical", "falsy"; empty for none and warn
	Output  []byte
	Warning string // non-empty when Action is ActionWarn
}

// PlanFor classifies the frontmatter of b and returns the one-line edit that disables model
// invocation. It copies b and splices the edited line, so every other byte stays the same, and
// it never writes through b.
func PlanFor(b []byte) Plan {
	region := Detect(b)
	if !region.Found {
		return Plan{Action: ActionWarn, Warning: "no frontmatter: the file needs an opening --- line and a closing --- line"}
	}
	if _, err := parse(b, region); err != nil {
		return Plan{Action: ActionWarn, Warning: oneLine(err)}
	}

	for i := region.OpenEnd; i < region.CloseStart; {
		contentEnd, next := line(b, i, region.CloseStart)
		if match := keyLine.FindSubmatch(b[i:contentEnd]); match != nil {
			raw := string(match[1])
			action, reason := classify(raw)
			switch action {
			case ActionNone:
				return Plan{Action: ActionNone}
			case ActionRewrite:
				return Plan{Action: ActionRewrite, Reason: reason, Output: rewrite(b, i, contentEnd)}
			}
			return Plan{Action: ActionWarn, Warning: fmt.Sprintf(
				"%s has value %q: expected true, yes, on, 1, false, no, off, or 0", key, raw)}
		}
		i = next
	}
	return Plan{Action: ActionInsert, Reason: "absent", Output: insert(b, region)}
}

// classify decides from the raw captured text, not from the decoded YAML, because yaml.v3
// decodes True and true to the same bool. The canonical test is a byte comparison on the
// untrimmed text and runs first: otherwise True would be trimmed and lowered to true, and that
// is a spelling Pi ignores.
func classify(raw string) (Action, string) {
	if raw == "true" {
		return ActionNone, ""
	}
	value := strings.ToLower(unquote(strings.Trim(raw, " \t")))
	switch {
	case truthy[value]:
		return ActionRewrite, "non-canonical"
	case falsy[value]:
		return ActionRewrite, "falsy"
	}
	return ActionWarn, ""
}

func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	quote := s[0]
	if (quote == '"' || quote == '\'') && s[len(s)-1] == quote {
		return s[1 : len(s)-1]
	}
	return s
}

// insert puts the canonical line last in the region, just before the closing delimiter, so it
// never lands between existing keys.
func insert(b []byte, region Region) []byte {
	added := canonical + region.newline(b)
	out := make([]byte, 0, len(b)+len(added))
	out = append(out, b[:region.CloseStart]...)
	out = append(out, added...)
	out = append(out, b[region.CloseStart:]...)
	return out
}

// rewrite replaces the whole key line with the canonical line, which keeps that line's own
// terminator.
func rewrite(b []byte, start, contentEnd int) []byte {
	out := make([]byte, 0, len(b)+len(canonical)-(contentEnd-start))
	out = append(out, b[:start]...)
	out = append(out, canonical...)
	out = append(out, b[contentEnd:]...)
	return out
}

type ParseError struct {
	Err error
}

func (e *ParseError) Error() string {
	return "frontmatter does not parse: " + oneLine(e.Err)
}

func (e *ParseError) Unwrap() error {
	return e.Err
}

// parse decodes the region into a map for reading only. The BOM sits outside the region, so
// it never reaches the parser.
func parse(b []byte, region Region) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(b[region.OpenEnd:region.CloseStart], &doc); err != nil {
		return nil, &ParseError{Err: err}
	}
	return doc, nil
}

// oneLine flattens a multi-line parser message into one line for a log entry.
func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
