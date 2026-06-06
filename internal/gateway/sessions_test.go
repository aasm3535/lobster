package gateway

import (
	"strings"
	"testing"

	"github.com/aasm3535/lobster/internal/llm"
)

func TestNewSessionCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		c := newSessionCode()
		if len(c) != 8 {
			t.Fatalf("code length = %d, want 8 (%q)", len(c), c)
		}
		for _, r := range c {
			if !strings.ContainsRune(sessionAlphabet, r) {
				t.Fatalf("code has out-of-alphabet rune: %q", c)
			}
		}
		if seen[c] {
			t.Fatalf("duplicate code within 100 draws: %q", c)
		}
		seen[c] = true
	}
}

func TestFormatSessionLog(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "find the bug"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{Name: "shell", Arguments: `{"command":"go test ./..."}`}}},
		{Role: llm.RoleTool, Content: "FAIL: ./internal/x"},
		{Role: llm.RoleAssistant, Content: "Found it: a nil deref in x.go."},
	}
	out := formatSessionLog("abc123", msgs)
	for _, want := range []string{"abc123", "find the bug", "shell", "go test", "result:", "Found it"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q:\n%s", want, out)
		}
	}
}
