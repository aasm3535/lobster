package gateway

import (
	"strings"
	"testing"
)

// With colour off (piped / NO_COLOR), mdToANSI must degrade to clean plain text: markup
// characters removed, structure preserved. This pins that so a piped `lobster tui` or a
// log never shows raw ** or ` noise.
func TestMdToANSIPlain(t *testing.T) {
	old := termColor
	termColor = false
	defer func() { termColor = old }()

	in := "# Title\n\nUse **bold** and `code` here.\n\n- one\n- two\n\n```\nx := 1\n```\n"
	got := mdToANSI(in)

	for _, bad := range []string{"**", "`", "# Title"} {
		if strings.Contains(got, bad) {
			t.Errorf("plain render still contains %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{"Title", "bold", "code", "• one", "• two", "x := 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("plain render missing %q:\n%s", want, got)
		}
	}
	// No runs of blank lines.
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("collapseBlanks left a triple newline:\n%q", got)
	}
}

// Inline parsing shouldn't choke on unterminated markers — they pass through literally.
func TestRenderInlineUnterminated(t *testing.T) {
	old := termColor
	termColor = false
	defer func() { termColor = old }()

	if got := renderInline("a *b and `c"); got != "a *b and `c" {
		t.Errorf("unterminated markers mangled: %q", got)
	}
}
