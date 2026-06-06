package gateway

import (
	"strings"
	"testing"
)

func TestResolvePastes(t *testing.T) {
	token := string(pasteOpen) + "pasted 3 lines #1" + string(pasteClose)
	pastes := map[string]string{token: "line1\nline2\nline3"}
	in := "look at this " + token + " ok?"

	echo := resolvePastes(in, pastes, true)
	if echo != "look at this [pasted 3 lines #1] ok?" {
		t.Fatalf("echo form wrong: %q", echo)
	}
	full := resolvePastes(in, pastes, false)
	if full != "look at this line1\nline2\nline3 ok?" {
		t.Fatalf("full form wrong: %q", full)
	}

	// No tokens → unchanged.
	if resolvePastes("plain text", nil, false) != "plain text" {
		t.Fatal("plain text should be unchanged")
	}
}

// The chip rendered by chipify must be the same visible width as the placeholder token, so
// the input caret stays aligned when a paste chip precedes the cursor.
func TestPasteChipWidthMatchesPlaceholder(t *testing.T) {
	label := "pasted 12 lines #2"
	token := []rune(string(pasteOpen) + label + string(pasteClose))
	rendered := stripANSI(chipify(string(token)))
	if len([]rune(rendered)) != len(token) {
		t.Fatalf("chip width %d != placeholder width %d (%q)", len([]rune(rendered)), len(token), rendered)
	}
	if rendered != "["+label+"]" {
		t.Fatalf("chip text = %q", rendered)
	}
}

func TestInsertPasteSmallVsChip(t *testing.T) {
	u := newTUI()
	u.insertPaste("just a short line")
	if string(u.input) != "just a short line" || len(u.pastes) != 0 {
		t.Fatalf("short paste should insert literally, got %q pastes=%d", string(u.input), len(u.pastes))
	}

	u = newTUI()
	u.insertPaste("a\nb\nc")
	if len(u.pastes) != 1 {
		t.Fatalf("multiline paste should become a chip, pastes=%d", len(u.pastes))
	}
	if !strings.ContainsRune(string(u.input), pasteOpen) {
		t.Fatal("input should hold a paste placeholder")
	}
}
