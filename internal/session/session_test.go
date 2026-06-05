package session

import (
	"testing"
	"time"
)

func TestStore_ArchiveSearchAndSessions(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Deterministic, advancing clock so the two sessions get distinct ids.
	clock := time.Date(2026, 6, 5, 17, 0, 0, 0, time.UTC)
	s.now = func() time.Time { clock = clock.Add(time.Second); return clock }

	// Session 1.
	mustAppend(t, s, "c1", "user", "find papus on my pc")
	mustAppend(t, s, "c1", "assistant", "found it at C:\\Users\\gotli\\papus")

	// /reset → next message starts session 2.
	if err := s.Close("c1"); err != nil {
		t.Fatalf("close: %v", err)
	}
	mustAppend(t, s, "c1", "user", "what's the weather today")

	// Listing: two sessions, newest first, with auto titles from the first user msg.
	list := s.List("c1")
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	if list[0].Title != "what's the weather today" {
		t.Fatalf("newest title = %q", list[0].Title)
	}
	if list[1].Title != "find papus on my pc" || list[1].Messages != 2 {
		t.Fatalf("oldest session wrong: %+v", list[1])
	}

	// Search across sessions.
	hits := s.Search("c1", "papus", 10)
	if len(hits) == 0 {
		t.Fatal("expected a hit for 'papus'")
	}
	if !contains(hits[0].Snippet, "papus") {
		t.Fatalf("snippet missing match: %q", hits[0].Snippet)
	}

	if h := s.Search("c1", "weather", 10); len(h) != 1 || h[0].Role != "user" {
		t.Fatalf("weather search wrong: %+v", h)
	}
	if h := s.Search("c1", "nonexistent-xyz", 10); len(h) != 0 {
		t.Fatalf("expected no hits, got %d", len(h))
	}

	// A different chat sees nothing from c1 (isolation).
	if h := s.Search("c2", "papus", 10); len(h) != 0 {
		t.Fatalf("cross-chat leak: %+v", h)
	}
}

func mustAppend(t *testing.T, s *Store, chat, role, content string) {
	t.Helper()
	if err := s.Append(chat, role, content); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
