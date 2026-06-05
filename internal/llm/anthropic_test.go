package llm

// Tool-call/result pairing is the one thing Anthropic-compatible providers reject the
// whole request over (error 2013). These tests pin the two ways history can go bad and
// the encoder's self-healing of each, so a regression can't silently 400 the live bot.

import "testing"

// blockTypes lists every block type in a user turn, in order — what the provider sees.
func userBlockTypes(am []anMessage) [][]string {
	var out [][]string
	for _, m := range am {
		if m.Role != "user" {
			continue
		}
		var ts []string
		for _, b := range m.Content {
			ts = append(ts, b.Type)
		}
		out = append(out, ts)
	}
	return out
}

// A tool_use with no following tool_result (left by a mid-tool shutdown / respawn) is
// the idx-125 bug. It must self-heal into a stub result so the request stays valid.
func TestEncodeRepairsDanglingToolUse(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "do it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "tool_1", Name: "shell", Arguments: "{}"}}},
		{Role: RoleUser, Content: "actually stop"}, // arrived before the result was recorded
	}
	am := encodeMessages(msgs)

	// Find the assistant turn; the very next message must answer tool_1.
	var assistantIdx = -1
	for i, m := range am {
		for _, b := range m.Content {
			if b.Type == "tool_use" {
				assistantIdx = i
			}
		}
	}
	if assistantIdx == -1 || assistantIdx+1 >= len(am) {
		t.Fatalf("no assistant tool_use turn followed by anything: %+v", am)
	}
	next := am[assistantIdx+1]
	if next.Role != "user" || len(next.Content) == 0 || next.Content[0].Type != "tool_result" {
		t.Fatalf("dangling tool_use not repaired; next turn = %+v", next)
	}
	if next.Content[0].ToolUseID != "tool_1" {
		t.Fatalf("stub result has wrong tool_use_id: %q", next.Content[0].ToolUseID)
	}
}

// Within a user turn, tool_results must precede any text/image. A correction folded in
// next to a result can scramble that; the encoder reorders it back.
func TestEncodeToolResultsLeadUserTurn(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "a", Name: "x", Arguments: "{}"},
			{ID: "b", Name: "y", Arguments: "{}"},
		}},
		{Role: RoleTool, ToolCallID: "a", Content: "ra"},
		{Role: RoleUser, Content: "mid-batch note"}, // text wedged between results
		{Role: RoleTool, ToolCallID: "b", Content: "rb"},
	}
	am := encodeMessages(msgs)
	for _, turn := range userBlockTypes(am) {
		seenText := false
		for _, typ := range turn {
			if typ == "text" || typ == "image" {
				seenText = true
			}
			if typ == "tool_result" && seenText {
				t.Fatalf("tool_result after text within a user turn: %v", turn)
			}
		}
	}
}
