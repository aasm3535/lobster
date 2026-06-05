package memory

import (
	"path/filepath"
	"testing"
)

func TestStore_RememberPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if notes := s.Notes("chat1"); notes != nil {
		t.Fatalf("expected no notes for a fresh chat, got %v", notes)
	}
	if err := s.Remember("chat1", "name is Sam"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if err := s.Remember("chat1", "likes a casual vibe"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if err := s.Remember("chat2", "different user"); err != nil {
		t.Fatalf("remember: %v", err)
	}

	// Reopen from disk: the notes must survive a restart, kept per chat and in order.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := s2.Notes("chat1")
	want := []string{"name is Sam", "likes a casual vibe"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("note %d: got %q, want %q", i, got[i], want[i])
		}
	}
	if n := s2.Notes("chat2"); len(n) != 1 || n[0] != "different user" {
		t.Fatalf("chat2 notes wrong: %v", n)
	}

	// Forget wipes one chat without touching the other.
	if err := s2.Forget("chat1"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if n := s2.Notes("chat1"); n != nil {
		t.Fatalf("expected chat1 forgotten, got %v", n)
	}
	if n := s2.Notes("chat2"); len(n) != 1 {
		t.Fatalf("chat2 should be untouched, got %v", n)
	}
}
