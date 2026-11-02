package frontmatter

import (
	"strings"
	"testing"
)

func lineEnd(s string, start int) int {
	end := start
	for end < len(s) && s[end] != '\n' {
		end++
	}
	if end > start && s[end-1] == '\r' {
		end--
	}
	return end
}

// checkIdempotent: planning the plan's output gives no action at all, so a caller that applies
// the same plan twice changes nothing.
func checkIdempotent(t *testing.T, plan Plan) {
	t.Helper()
	if plan.Output == nil {
		return
	}
	second := PlanFor(plan.Output)
	if second.Action != ActionNone {
		t.Errorf("PlanFor(PlanFor(input).Output) = %v, want %v", second.Action, ActionNone)
	}
	if second.Output != nil {
		t.Errorf("PlanFor(PlanFor(input).Output).Output = %q, want nil", second.Output)
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Region
	}{
		{
			name:  "no frontmatter",
			input: "name: a\ndescription: a skill\n",
			want:  Region{},
		},
		{
			name:  "empty file",
			input: "",
			want:  Region{},
		},
		{
			name:  "three bytes with no newline",
			input: "---",
			want:  Region{},
		},
		{
			name:  "four dashes do not open frontmatter",
			input: "----\nname: a\n---\n",
			want:  Region{},
		},
		{
			name:  "frontmatter only",
			input: "---\n---\n",
			want:  Region{Found: true, OpenStart: 0, OpenEnd: 4, CloseStart: 4, CloseEnd: 8},
		},
		{
			name:  "one key and a closing delimiter at end of file",
			input: "---\nname: a\n---",
			want:  Region{Found: true, OpenStart: 0, OpenEnd: 4, CloseStart: 12, CloseEnd: 15},
		},
		{
			name:  "BOM before the opening delimiter",
			input: "\xef\xbb\xbf---\nname: a\n---\n",
			want:  Region{Found: true, OpenStart: 3, OpenEnd: 7, CloseStart: 15, CloseEnd: 19},
		},
		{
			name:  "CRLF throughout",
			input: "---\r\nname: a\r\n---\r\n",
			want:  Region{Found: true, OpenStart: 0, OpenEnd: 5, CloseStart: 14, CloseEnd: 19},
		},
		{
			name:  "a delimiter in the body is outside the region",
			input: "---\nname: a\n---\nbody\n---\n",
			want:  Region{Found: true, OpenStart: 0, OpenEnd: 4, CloseStart: 12, CloseEnd: 16},
		},
		{
			name:  "frontmatter that never closes",
			input: "---\nname: a\n",
			want:  Region{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Detect([]byte(test.input)); got != test.want {
				t.Errorf("Detect(%q) = %+v, want %+v", test.input, got, test.want)
			}
		})
	}
}

func TestPlanValueMatrix(t *testing.T) {
	const head = "---\nname: kibana\ndescription: \"Search Kibana logs.\"\n"
	const tail = "---\n"
	const canonicalLine = "disable-model-invocation: true\n"

	tests := []struct {
		name        string
		line        string // the key line, empty for a file without the key
		wantAction  Action
		wantReason  string
		wantOutput  string
		wantWarning string
	}{
		{name: "absent", wantAction: ActionInsert, wantReason: "absent", wantOutput: head + canonicalLine + tail},
		{name: "bare true", line: "disable-model-invocation: true", wantAction: ActionNone},
		{name: "True", line: "disable-model-invocation: True", wantAction: ActionRewrite, wantReason: "non-canonical", wantOutput: head + canonicalLine + tail},
		{name: "TRUE", line: "disable-model-invocation: TRUE", wantAction: ActionRewrite, wantReason: "non-canonical", wantOutput: head + canonicalLine + tail},
		{name: "double quoted true", line: `disable-model-invocation: "true"`, wantAction: ActionRewrite, wantReason: "non-canonical", wantOutput: head + canonicalLine + tail},
		{name: "yes", line: "disable-model-invocation: yes", wantAction: ActionRewrite, wantReason: "non-canonical", wantOutput: head + canonicalLine + tail},
		{name: "on", line: "disable-model-invocation: on", wantAction: ActionRewrite, wantReason: "non-canonical", wantOutput: head + canonicalLine + tail},
		{name: "one", line: "disable-model-invocation: 1", wantAction: ActionRewrite, wantReason: "non-canonical", wantOutput: head + canonicalLine + tail},
		{name: "quoted yes", line: `disable-model-invocation: "yes"`, wantAction: ActionRewrite, wantReason: "non-canonical", wantOutput: head + canonicalLine + tail},
		{name: "single quoted on", line: "disable-model-invocation: 'on'", wantAction: ActionRewrite, wantReason: "non-canonical", wantOutput: head + canonicalLine + tail},
		{name: "false", line: "disable-model-invocation: false", wantAction: ActionRewrite, wantReason: "falsy", wantOutput: head + canonicalLine + tail},
		{name: "False", line: "disable-model-invocation: False", wantAction: ActionRewrite, wantReason: "falsy", wantOutput: head + canonicalLine + tail},
		{name: "FALSE", line: "disable-model-invocation: FALSE", wantAction: ActionRewrite, wantReason: "falsy", wantOutput: head + canonicalLine + tail},
		{name: "no", line: "disable-model-invocation: no", wantAction: ActionRewrite, wantReason: "falsy", wantOutput: head + canonicalLine + tail},
		{name: "off", line: "disable-model-invocation: off", wantAction: ActionRewrite, wantReason: "falsy", wantOutput: head + canonicalLine + tail},
		{name: "zero", line: "disable-model-invocation: 0", wantAction: ActionRewrite, wantReason: "falsy", wantOutput: head + canonicalLine + tail},
		{name: "quoted false", line: `disable-model-invocation: "false"`, wantAction: ActionRewrite, wantReason: "falsy", wantOutput: head + canonicalLine + tail},
		{name: "quoted no", line: `disable-model-invocation: 'no'`, wantAction: ActionRewrite, wantReason: "falsy", wantOutput: head + canonicalLine + tail},
		{name: "empty value", line: "disable-model-invocation:", wantAction: ActionWarn, wantWarning: "has value"},
		{name: "null", line: "disable-model-invocation: null", wantAction: ActionWarn, wantWarning: "has value"},
		{name: "tagged null", line: "disable-model-invocation: !!null", wantAction: ActionWarn, wantWarning: "has value"},
		{name: "tilde", line: "disable-model-invocation: ~", wantAction: ActionWarn, wantWarning: "has value"},
		{name: "arbitrary text", line: "disable-model-invocation: whatever", wantAction: ActionWarn, wantWarning: "has value"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := head
			if test.line != "" {
				input += test.line + "\n"
			}
			input += tail
			original := []byte(input)

			plan := PlanFor([]byte(input))

			if plan.Action != test.wantAction {
				t.Fatalf("Action = %v, want %v (warning %q)", plan.Action, test.wantAction, plan.Warning)
			}
			if plan.Reason != test.wantReason {
				t.Errorf("Reason = %q, want %q", plan.Reason, test.wantReason)
			}
			if test.wantOutput == "" {
				if plan.Output != nil {
					t.Errorf("Output = %q, want nil", plan.Output)
				}
			} else if got := string(plan.Output); got != test.wantOutput {
				t.Errorf("Output = %q, want %q", got, test.wantOutput)
			}
			if test.wantWarning == "" {
				if plan.Warning != "" {
					t.Errorf("Warning = %q, want none", plan.Warning)
				}
			} else if !strings.Contains(plan.Warning, test.wantWarning) {
				t.Errorf("Warning = %q, want it to contain %q", plan.Warning, test.wantWarning)
			}
			if string(original) != input {
				t.Errorf("PlanFor mutated its input: got %q, want %q", original, input)
			}
			checkIdempotent(t, plan)
		})
	}
}

// A line that is not at column 0 is not the key, so the planner falls back to the absent case:
// it inserts the top-level key and leaves the indented line byte-identical.
func TestPlanIgnoresAKeyThatIsNotTopLevel(t *testing.T) {
	const canonicalLine = "disable-model-invocation: true\n"
	tests := []struct {
		name     string
		head     string
		indented string
	}{
		{
			name:     "nested under another key",
			head:     "---\nname: a\nmeta:\n",
			indented: "  disable-model-invocation: true\n",
		},
		{
			name:     "inside a literal block scalar",
			head:     "---\nname: a\ndescription: |\n",
			indented: "  disable-model-invocation: true\n",
		},
		{
			name:     "inside a folded block scalar",
			head:     "---\nname: a\ndescription: >\n",
			indented: "  disable-model-invocation: yes\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const tail = "---\n"
			input := []byte(test.head + test.indented + tail)

			plan := PlanFor(input)

			if plan.Action != ActionInsert {
				t.Fatalf("Action = %v, want %v (warning %q)", plan.Action, ActionInsert, plan.Warning)
			}
			if plan.Warning != "" {
				t.Errorf("Warning = %q, want none: the indented line is not a decision", plan.Warning)
			}
			want := test.head + test.indented + canonicalLine + tail
			if got := string(plan.Output); got != want {
				t.Errorf("Output = %q, want %q", got, want)
			}
			checkIdempotent(t, plan)
		})
	}
}

func TestPlanTreatsTheSameLineAtColumnZeroAsARealKey(t *testing.T) {
	input := []byte("---\nname: a\ndisable-model-invocation: yes\n---\n")

	plan := PlanFor(input)

	if plan.Action != ActionRewrite {
		t.Fatalf("Action = %v, want %v", plan.Action, ActionRewrite)
	}
	if got, want := string(plan.Output), "---\nname: a\ndisable-model-invocation: true\n---\n"; got != want {
		t.Errorf("Output = %q, want %q", got, want)
	}
	checkIdempotent(t, plan)
}

// Each case names the bytes before the edited line, the line itself, and the bytes after it.
// The expected output is written as one splice at those offsets, so the test pins both the
// offsets and the replacement: any byte outside the intended line must be identical.
func TestPlanPreservesEveryOtherByte(t *testing.T) {
	const added = "disable-model-invocation: true"
	tests := []struct {
		name    string
		head    string // bytes before the edited line
		line    string // the line that is replaced, empty when the key is inserted
		tail    string // bytes after the edited line, including its terminator
		wantNew string // the bytes that replace the line
	}{
		{
			name:    "absent key in an LF file",
			head:    "---\nname: a\n",
			tail:    "---\n",
			wantNew: added + "\n",
		},
		{
			name:    "absent key in a CRLF file",
			head:    "---\r\nname: a\r\n",
			tail:    "---\r\n",
			wantNew: added + "\r\n",
		},
		{
			name:    "absent key after a BOM",
			head:    "\xef\xbb\xbf---\nname: a\n",
			tail:    "---\n",
			wantNew: added + "\n",
		},
		{
			name:    "absent key in an empty region",
			head:    "---\n",
			tail:    "---\n",
			wantNew: added + "\n",
		},
		{
			name:    "opening CRLF and closing LF take the opening line ending",
			head:    "---\r\nname: a\n",
			tail:    "---\n",
			wantNew: added + "\r\n",
		},
		{
			name:    "absent key before a closing delimiter at end of file",
			head:    "---\nname: a\n",
			tail:    "---",
			wantNew: added + "\n",
		},
		{
			name:    "rewritten line keeps its own CRLF",
			head:    "---\nname: a\n",
			line:    "disable-model-invocation: yes",
			tail:    "\r\n---\n",
			wantNew: added,
		},
		{
			name:    "rewritten line drops extra spacing",
			head:    "---\nname: a\n",
			line:    "disable-model-invocation  :   'on'  ",
			tail:    "\n---\n",
			wantNew: added,
		},
		{
			name:    "a delimiter in the body stays untouched",
			head:    "---\nname: a\n",
			line:    "disable-model-invocation: no",
			tail:    "\n---\nbody\n---\n",
			wantNew: added,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := test.head + test.line + test.tail
			start := len(test.head)
			end := start + len(test.line)
			want := input[:start] + test.wantNew + input[end:]

			plan := PlanFor([]byte(input))

			if plan.Output == nil {
				t.Fatalf("Output = nil, want a plan that changes the file (warning %q)", plan.Warning)
			}
			if got := string(plan.Output); got != want {
				t.Errorf("Output = %q, want %q", got, want)
			}
			if plan.Reason == "" {
				t.Errorf("Reason = the empty string, want absent, non-canonical, or falsy")
			}
			checkIdempotent(t, plan)
		})
	}
}

// The plan is built by copying the input and splicing, so it must never write through the
// slice it was given.
func TestPlanDoesNotMutateItsInput(t *testing.T) {
	inputs := []string{
		"---\nname: a\n---\n",
		"---\nname: a\ndisable-model-invocation: yes\n---\n",
		"\xef\xbb\xbf---\r\nname: a\r\ndisable-model-invocation: no\r\n---\r\n",
		"name: a\n",
	}

	for _, original := range inputs {
		input := []byte(original)

		plan := PlanFor(input)
		_ = plan

		if string(input) != original {
			t.Errorf("PlanFor mutated its input: got %q, want %q", input, original)
		}
	}
}

func TestPlanWarnsWithoutChangingAnything(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantWarning string
	}{
		{name: "no frontmatter", input: "name: a\n", wantWarning: "no frontmatter"},
		{name: "empty file", input: "", wantWarning: "no frontmatter"},
		{name: "three bytes with no newline", input: "---", wantWarning: "no frontmatter"},
		{name: "frontmatter that never closes", input: "---\nname: a\n", wantWarning: "no frontmatter"},
		{
			name:        "frontmatter that does not parse",
			input:       "---\nname: a\nname: b\n---\n",
			wantWarning: "does not parse",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := []byte(test.input)

			plan := PlanFor(input)

			if plan.Action != ActionWarn {
				t.Fatalf("Action = %v, want %v", plan.Action, ActionWarn)
			}
			if plan.Output != nil {
				t.Errorf("Output = %q, want nil so a caller cannot write", plan.Output)
			}
			if plan.Reason != "" {
				t.Errorf("Reason = %q, want the empty string", plan.Reason)
			}
			if !strings.Contains(plan.Warning, test.wantWarning) {
				t.Errorf("Warning = %q, want it to contain %q", plan.Warning, test.wantWarning)
			}
			if strings.Contains(plan.Warning, "\n") {
				t.Errorf("Warning = %q, want one line", plan.Warning)
			}
			if string(input) != test.input {
				t.Errorf("PlanFor mutated its input: got %q, want %q", input, test.input)
			}
		})
	}
}

func TestPlanForACanonicalFileHasNoOutput(t *testing.T) {
	input := []byte("---\nname: a\ndisable-model-invocation: true\n---\n")

	plan := PlanFor(input)

	if plan.Action != ActionNone {
		t.Fatalf("Action = %v, want %v", plan.Action, ActionNone)
	}
	if plan.Output != nil {
		t.Errorf("Output = %q, want nil", plan.Output)
	}
	if plan.Warning != "" {
		t.Errorf("Warning = %q, want none", plan.Warning)
	}
	if plan.Reason != "" {
		t.Errorf("Reason = %q, want the empty string", plan.Reason)
	}
}

func TestHasFrontmatter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"fenced", "---\nname: a\n---\n", true},
		{"fenced with a BOM", "\xef\xbb\xbf---\nname: a\n---\n", true},
		{"no frontmatter", "name: a\n", false},
		{"never closes", "---\nname: a\n", false},
		{"empty file", "", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HasFrontmatter([]byte(test.input)); got != test.want {
				t.Errorf("HasFrontmatter(%q) = %v, want %v", test.input, got, test.want)
			}
		})
	}
}

func TestName(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{"plain scalar", "---\nname: kibana\n---\n", "kibana", true},
		{"quoted scalar", "---\nname: \"kibana logs\"\n---\n", "kibana logs", true},
		{"other keys around it", "---\ndescription: x\nname: bl\n---\n", "bl", true},
		{"no name key", "---\ndescription: x\n---\n", "", false},
		{"no frontmatter", "name: kibana\n", "", false},
		{"frontmatter does not parse", "---\nname: a\nname: b\n---\n", "", false},
		{"nested name is not top level", "---\nmeta:\n  name: nested\n---\n", "", false},
		{"a null name is not a useful name", "---\nname: null\n---\n", "", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := Name([]byte(test.input))
			if ok != test.wantOK {
				t.Fatalf("Name(%q) ok = %v, want %v", test.input, ok, test.wantOK)
			}
			if got != test.want {
				t.Errorf("Name(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestNameDoesNotChangeThePlan(t *testing.T) {
	// The planner does not read name, so a file with frontmatter and no name still plans the
	// edit and never warns.
	input := []byte("---\ndescription: x\n---\n")

	plan := PlanFor(input)

	if plan.Action != ActionInsert {
		t.Errorf("Action = %v, want %v: a missing name is not a plan", plan.Action, ActionInsert)
	}
}
