package agent

import (
	"testing"

	"github.com/aasm3535/lobster/internal/llm"
)

// fakeStore is an in-memory SessionStore for testing persistence.
type fakeStore struct {
	saved map[string][]llm.Message
}

func newFakeStore() *fakeStore { return &fakeStore{saved: map[string][]llm.Message{}} }

func (f *fakeStore) Load(id string) ([]llm.Message, error) { return f.saved[id], nil }
func (f *fakeStore) Save(id string, msgs []llm.Message) error {
	cp := make([]llm.Message, len(msgs))
	copy(cp, msgs)
	f.saved[id] = cp
	return nil
}

func TestSession_LoadsAndPersists(t *testing.T) {
	store := newFakeStore()
	store.saved["c1"] = []llm.Message{{Role: llm.RoleUser, Content: "earlier message"}}

	// A new session for c1 should resume the saved transcript.
	s := NewSession("c1", store, 0)
	if len(s.Messages) != 1 || s.Messages[0].Content != "earlier message" {
		t.Fatalf("expected resumed history, got %+v", s.Messages)
	}

	// Each mutation is persisted immediately.
	s.addUser(Input{Text: "new message"})
	if got := store.saved["c1"]; len(got) != 2 || got[1].Content != "new message" {
		t.Fatalf("addUser not persisted: %+v", got)
	}
}

func TestSession_TrimKeepsRecentTurnsAtUserBoundary(t *testing.T) {
	// Tiny budget forces trimming after a couple of turns.
	s := NewSession("c1", nil, 20)

	s.addUser(Input{Text: "aaaaa"})                                       // turn 1
	s.addAssistant(&llm.Response{Content: "bbbbb"})                       //
	s.addUser(Input{Text: "ccccc"})                                       // turn 2
	s.addAssistant(&llm.Response{ToolCalls: []llm.ToolCall{{ID: "1"}}})   //
	s.addToolResult(llm.ToolCall{ID: "1", Name: "x"}, "dddddddddddddddd") // big result
	s.addUser(Input{Text: "eeeee"})                                       // turn 3 (most recent)

	// The oldest turn(s) must be dropped, and whatever remains must start at a user
	// message so no tool result is left without its tool call.
	if len(s.Messages) == 0 || s.Messages[0].Role != llm.RoleUser {
		t.Fatalf("trim must leave history starting at a user message, got %+v", s.Messages)
	}
	// The most recent turn must always survive.
	last := s.Messages[len(s.Messages)-1]
	if last.Role != llm.RoleUser || last.Content != "eeeee" {
		t.Fatalf("most recent turn was trimmed away: %+v", s.Messages)
	}
	// Sanity: total content is within (a small multiple of) budget.
	total := 0
	for _, m := range s.Messages {
		total += len(m.Content)
	}
	if total > 100 {
		t.Fatalf("history not trimmed enough: %d chars in %d msgs", total, len(s.Messages))
	}
}
