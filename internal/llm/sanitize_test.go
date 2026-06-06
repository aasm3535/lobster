package llm

import (
	"encoding/json"
	"testing"
)

func TestSanitizeArgs(t *testing.T) {
	cases := map[string]string{
		"":                     "{}",
		"   ":                  "{}",
		`{"cmd":"dir`:          "{}", // truncated mid-stream (max_tokens / dropped connection)
		`{"cmd": "dir"}`:       `{"cmd": "dir"}`,
		`{"a":1,"b":[1,2,3]}`:  `{"a":1,"b":[1,2,3]}`,
		"\n\t{\"x\":\"y\"}\n ": `{"x":"y"}`,
	}
	for in, want := range cases {
		if got := SanitizeArgs(in); got != want {
			t.Errorf("SanitizeArgs(%q) = %q, want %q", in, got, want)
		}
	}
}

// A transcript poisoned with truncated tool-call JSON must still marshal into a valid
// request — this was the "unexpected end of JSON input" that bricked conversations.
func TestEncodeMessagesHealsTruncatedArgs(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "shell", Arguments: `{"command":"di`}}},
		{Role: RoleTool, ToolCallID: "t1", Content: "result"},
	}
	body := anRequest{Model: "m", Messages: encodeMessages(msgs), MaxTokens: 10}
	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("request with truncated args failed to marshal: %v", err)
	}
}
