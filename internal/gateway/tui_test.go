package gateway

import (
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// wrapLine should break on visible width and never count ANSI escape bytes toward it, so a
// coloured line wraps at the same place its plain text would.
func TestWrapLine(t *testing.T) {
	if got := wrapLine("abcdefg", 3); len(got) != 3 || got[0] != "abc" || got[2] != "g" {
		t.Fatalf("plain wrap = %#v", got)
	}

	colored := "\x1b[31mabcdef\x1b[39m" // 6 visible cols, wrapped at 3 → 2 rows
	rows := wrapLine(colored, 3)
	if len(rows) != 2 {
		t.Fatalf("coloured wrap rows = %d, want 2 (%#v)", len(rows), rows)
	}
	if joined := stripANSI(strings.Join(rows, "")); joined != "abcdef" {
		t.Fatalf("coloured wrap lost text: %q", joined)
	}
	for _, r := range rows {
		if w := len([]rune(stripANSI(r))); w > 3 {
			t.Fatalf("row exceeds width: %q (%d)", stripANSI(r), w)
		}
	}
}

// wrapLine should break on word boundaries, never mid-word, and re-apply the leading
// indent to continuation rows so a paragraph stays aligned.
func TestWrapLineWordWrap(t *testing.T) {
	rows := wrapLine("hello world foo", 8) // "hello " (6) fits; "world" would overflow
	if len(rows) < 2 {
		t.Fatalf("expected wrap, got %#v", rows)
	}
	for _, r := range rows {
		// No row should split a word: every row's text is whole words.
		if w := len([]rune(stripANSI(r))); w > 8 {
			t.Fatalf("row too wide: %q (%d)", r, w)
		}
	}
	joined := stripANSI(rows[0])
	if joined != "hello" && joined != "hello " {
		t.Fatalf("first row broke mid-word: %q", joined)
	}

	// Indented line: continuation rows keep the indent.
	ind := wrapLine("     a bb ccc dddd eeee", 12)
	if len(ind) < 2 {
		t.Fatalf("expected the indented line to wrap: %#v", ind)
	}
	for i := 1; i < len(ind); i++ {
		if !strings.HasPrefix(stripANSI(ind[i]), "     ") {
			t.Fatalf("continuation row %d lost its indent: %q", i, stripANSI(ind[i]))
		}
	}
}

// layoutInput places the caret correctly: on the first row for short input, and on a wrapped
// row once the text passes the box width.
func TestLayoutInputCaret(t *testing.T) {
	// cols 24 → textW = 24 - 2 (margin) - 3 ("#  ") = 19.
	in := []rune("hello")
	rows, line, col := layoutInput(in, len(in), 24)
	if len(rows) != 1 || line != 0 || col != 10 { // 2 margin + 3 prompt + 5
		t.Fatalf("short input: rows=%d line=%d col=%d", len(rows), line, col)
	}

	long := []rune(strings.Repeat("x", 25)) // 25 > 19 → wraps to a second row
	rows, line, col = layoutInput(long, len(long), 24)
	if len(rows) < 2 || line != 1 || col != 5+6 { // second row, 6 chars in (25-19)
		t.Fatalf("wrapped input: rows=%d line=%d col=%d", len(rows), line, col)
	}
}
