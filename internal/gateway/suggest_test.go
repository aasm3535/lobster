package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuggestForCommands(t *testing.T) {
	g := &Gateway{}
	sugs := g.suggestFor("/mo")
	if len(sugs) == 0 || sugs[0].label != "/model" {
		t.Fatalf("expected /model suggestion, got %+v", sugs)
	}
	if sugs[0].submit { // /model takes an arg → completes, not runs
		t.Fatal("/model should not submit on accept")
	}
	if sugs[0].insert != "/model " {
		t.Fatalf("insert = %q", sugs[0].insert)
	}

	// Arg-less command runs on accept.
	for _, s := range g.suggestFor("/age") {
		if s.label == "/agents" && !s.submit {
			t.Fatal("/agents should submit on accept")
		}
	}
}

func TestSuggestForFileMention(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	os.Chdir(dir)
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package x"), 0o644)

	g := &Gateway{}
	// "@hel" anywhere in the text → file picker, preserving the prefix.
	sugs := g.suggestFor("look at @hel")
	if len(sugs) == 0 {
		t.Fatal("expected a file suggestion")
	}
	if !strings.HasPrefix(sugs[0].insert, "look at @") || !strings.Contains(sugs[0].insert, "hello.go") {
		t.Fatalf("insert kept prefix wrong: %q", sugs[0].insert)
	}
}

func TestExpandMentions(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	os.Chdir(dir)
	os.WriteFile(filepath.Join(dir, "note.txt"), []byte("SECRET-CONTENT"), 0o644)

	g := &Gateway{}
	out := g.expandMentions("please read @note.txt and summarize")
	if !strings.Contains(out, "SECRET-CONTENT") {
		t.Fatalf("file contents not attached: %q", out)
	}
	// A non-existent mention is left as-is, no attachment.
	if g.expandMentions("@nope.txt hi") != "@nope.txt hi" {
		t.Fatal("missing file should not be expanded")
	}
}
