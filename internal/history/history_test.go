package history

import (
	"testing"

	"github.com/aasm3535/lobster/internal/llm"
)

func TestStore_RoundTripAndClear(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if msgs, err := s.Load("123"); err != nil || msgs != nil {
		t.Fatalf("fresh chat should be empty; got %v, %v", msgs, err)
	}

	want := []llm.Message{
		{Role: llm.RoleUser, Content: "find papus"},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{ID: "1", Name: "shell", Arguments: "{}"}}},
		{Role: llm.RoleTool, ToolCallID: "1", Name: "shell", Content: "C:\\Users\\gotli\\papus"},
		{Role: llm.RoleAssistant, Content: "it's at C:\\Users\\gotli\\papus"},
	}
	if err := s.Save("123", want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.Load("123")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d", len(got), len(want))
	}
	if got[0].Content != "find papus" || got[2].ToolCallID != "1" || got[3].Content != "it's at C:\\Users\\gotli\\papus" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if err := s.Clear("123"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if msgs, _ := s.Load("123"); msgs != nil {
		t.Fatalf("expected cleared, got %v", msgs)
	}
}
