package gateway

import (
	"testing"

	"github.com/aasm3535/lobster/internal/event"
)

func TestAgentHubLifecycle(t *testing.T) {
	h := newAgentHub()
	if h.runningFor("c1") != 0 {
		t.Fatal("fresh hub should have no running agents")
	}

	r1 := h.start("c1", "auth-audit", "audit the auth module")
	r2 := h.start("c1", "perf-audit", "profile the hot path")
	h.start("other", "elsewhere", "do a thing") // a different chat — must not leak into c1

	if h.runningFor("c1") != 2 {
		t.Fatalf("runningFor(c1) = %d, want 2", h.runningFor("c1"))
	}
	if len(h.cardsFor("c1", 8)) != 2 { // both agents for this chat, not the other
		t.Fatalf("cardsFor(c1) = %d, want 2", len(h.cardsFor("c1", 8)))
	}

	r1.finish("done")
	if h.runningFor("c1") != 1 {
		t.Fatalf("after one finishes, runningFor = %d, want 1", h.runningFor("c1"))
	}

	r2.finish("failed")
	if h.runningFor("c1") != 0 {
		t.Fatal("all finished, expected 0 running")
	}
	// Finished runs still show in the detail view.
	if len(h.recentFor("c1", 8)) != 2 {
		t.Fatalf("recentFor(c1) = %d, want 2", len(h.recentFor("c1", 8)))
	}
}

func TestHubSinkRecords(t *testing.T) {
	h := newAgentHub()
	run := h.start("c1", "worker", "list the files")
	s := &hubSink{run: run}

	s.Emit(event.Event{Kind: event.KindToolCall, Tool: "shell", Args: `{"command":"ls"}`})
	s.Emit(event.Event{Kind: event.KindReply, Text: "all done, found 3 files"})

	reply, fail := s.result()
	if reply != "all done, found 3 files" || fail != "" {
		t.Fatalf("result = (%q, %q)", reply, fail)
	}
	c := run.card()
	if c.Reply != "all done, found 3 files" {
		t.Fatalf("reply not stored: %q", c.Reply)
	}
	if len(c.Lines) < 1 {
		t.Fatalf("timeline too short: %v", c.Lines)
	}
	if c.Task != "list the files" {
		t.Fatalf("task not stored: %q", c.Task)
	}
}
