package scheduler

import (
	"path/filepath"
	"testing"
	"time"
)

func TestScheduler_FireRecurringAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.json")
	var fired []string
	clock := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)

	s, err := Open(path, func(chatID, prompt string) { fired = append(fired, chatID+":"+prompt) })
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.now = func() time.Time { return clock }

	one := s.Add("c1", "do once", "", time.Minute, 0)             // one-shot in 1m
	rec := s.Add("c1", "check papus", "papus", 0, 30*time.Minute) // recurring every 30m

	s.fireDue()
	if len(fired) != 0 {
		t.Fatalf("nothing should fire yet: %v", fired)
	}

	// Jump past both first runs: both fire once.
	clock = clock.Add(31 * time.Minute)
	s.fireDue()
	if len(fired) != 2 {
		t.Fatalf("expected 2 fires, got %v", fired)
	}
	if _, ok := s.jobs[one.ID]; ok {
		t.Fatal("one-shot should be gone after firing")
	}
	if r := s.jobs[rec.ID]; r == nil || !r.NextAt.Equal(clock.Add(30*time.Minute)) {
		t.Fatalf("recurring not re-armed to +30m: %+v", r)
	}

	// It must not re-fire until another interval elapses.
	fired = nil
	s.fireDue()
	if len(fired) != 0 {
		t.Fatalf("recurring fired early: %v", fired)
	}
	clock = clock.Add(30 * time.Minute)
	s.fireDue()
	if len(fired) != 1 || fired[0] != "c1:check papus" {
		t.Fatalf("recurring re-fire wrong: %v", fired)
	}

	// Persistence: reopen and confirm the recurring job survived.
	s2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s2.List("c1"); len(got) != 1 || got[0].Prompt != "check papus" {
		t.Fatalf("expected the recurring job to persist, got %+v", got)
	}
	// A different chat can't remove someone else's job.
	if s2.Remove("intruder", rec.ID) {
		t.Fatal("removed a foreign chat's job")
	}
	if !s2.Remove("c1", rec.ID) || len(s2.List("c1")) != 0 {
		t.Fatal("owner failed to remove its job")
	}
}
