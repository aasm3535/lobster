package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, r *Registry, name, args string) (string, error) {
	t.Helper()
	return r.Run(context.Background(), name, json.RawMessage(args))
}

func newReg() *Registry {
	r := NewRegistry()
	RegisterBuiltins(r)
	return r
}

func TestEditFile(t *testing.T) {
	r := newReg()
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\ngamma\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	esc, _ := json.Marshal(p)

	// Ambiguous match must be rejected without replace_all.
	if _, err := run(t, r, "edit_file", `{"path":`+string(esc)+`,"find":"beta","replace":"BETA"}`); err == nil {
		t.Fatal("ambiguous edit should fail")
	}

	// Unique match edits in place.
	if _, err := run(t, r, "edit_file", `{"path":`+string(esc)+`,"find":"gamma","replace":"GAMMA"}`); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "GAMMA") {
		t.Fatalf("edit not applied: %q", data)
	}

	// replace_all replaces every occurrence.
	out, err := run(t, r, "edit_file", `{"path":`+string(esc)+`,"find":"beta","replace":"B","replace_all":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2") {
		t.Fatalf("expected 2 replacements, got %q", out)
	}

	// Missing text errors.
	if _, err := run(t, r, "edit_file", `{"path":`+string(esc)+`,"find":"nope","replace":"x"}`); err == nil {
		t.Fatal("missing text should fail")
	}
}

func TestReadFileWindow(t *testing.T) {
	r := newReg()
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("l1\nl2\nl3\nl4\nl5"), 0o644); err != nil {
		t.Fatal(err)
	}
	esc, _ := json.Marshal(p)

	out, err := run(t, r, "read_file", `{"path":`+string(esc)+`,"offset":2,"limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "l2\nl3") || strings.Contains(out, "l4") {
		t.Fatalf("window wrong: %q", out)
	}
	if !strings.Contains(out, "[lines 2-3 of 5]") {
		t.Fatalf("missing header: %q", out)
	}

	// Offset beyond EOF reports the file size instead of erroring.
	out, err = run(t, r, "read_file", `{"path":`+string(esc)+`,"offset":99}`)
	if err != nil || !strings.Contains(out, "only 5 lines") {
		t.Fatalf("eof handling wrong: %q %v", out, err)
	}
}
