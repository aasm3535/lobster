package gateway

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aasm3535/lobster/internal/event"
)

// A tool call should render as a dot + tool name + a readable command preview (not raw
// JSON), in normal verbosity. Colour is forced off so we assert on plain text.
func TestTerminalSinkToolLine(t *testing.T) {
	old := termColor
	termColor = false
	defer func() { termColor = old }()

	var buf bytes.Buffer
	s := newTerminalSink(&buf, func() string { return verbosityNormal }, func() bool { return false }, nil)
	s.begin()
	s.Emit(event.Event{Kind: event.KindToolCall, Tool: "shell", Args: `{"command":"echo hi","shell":"bash"}`})

	out := buf.String()
	if !strings.Contains(out, "● shell") {
		t.Errorf("missing dot + tool name:\n%q", out)
	}
	if !strings.Contains(out, "echo hi") {
		t.Errorf("missing command preview:\n%q", out)
	}
	if strings.Contains(out, "{") {
		t.Errorf("raw JSON leaked into the tool line:\n%q", out)
	}
}

// Quiet verbosity hides the tool timeline entirely.
func TestTerminalSinkQuietHidesTools(t *testing.T) {
	old := termColor
	termColor = false
	defer func() { termColor = old }()

	var buf bytes.Buffer
	s := newTerminalSink(&buf, func() string { return verbosityQuiet }, func() bool { return false }, nil)
	s.begin()
	s.Emit(event.Event{Kind: event.KindToolCall, Tool: "shell", Args: `{"command":"echo hi"}`})
	if buf.Len() != 0 {
		t.Errorf("quiet mode printed a tool line: %q", buf.String())
	}
}

func TestArgPreview(t *testing.T) {
	cases := map[string]string{
		`{"command":"ls -la"}`:        "ls -la",
		`{"path":"/etc/hosts"}`:       "/etc/hosts",
		`{"query":"golang channels"}`: "golang channels",
		`{}`:                          "",
		``:                            "",
	}
	for in, want := range cases {
		if got := argPreview(in); got != want {
			t.Errorf("argPreview(%q) = %q, want %q", in, got, want)
		}
	}
}
