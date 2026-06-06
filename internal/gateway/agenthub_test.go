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

	r1 := h.start("c1", "auth-audit")
	r2 := h.start("c1", "perf-audit")
	h.start("other", "elsewhere") // a different chat — must not leak into c1

	if h.runningFor("c1") != 2 {
		t.Fatalf("runningFor(c1) = %d, want 2", h.runningFor("c1"))
	}
	if len(h.panelLines("c1")) != 3 { // header + 2 agents
		t.Fatalf("panel lines = %d, want 3", len(h.panelLines("c1")))
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
	run := h.start("c1", "worker")
	s := &hubSink{run: run}

	s.Emit(event.Event{Kind: event.KindToolCall, Tool: "shell", Args: `{"command":"ls"}`})
	s.Emit(event.Event{Kind: event.KindReply, Text: "all done, found 3 files"})

	reply, fail := s.result()
	if reply != "all done, found 3 files" || fail != "" {
		t.Fatalf("result = (%q, %q)", reply, fail)
	}
	_, _, _, last, _, lines := run.view()
	if last != "✓ done" {
		t.Fatalf("last activity = %q", last)
	}
	if len(lines) < 2 {
		t.Fatalf("timeline too short: %v", lines)
	}
}
