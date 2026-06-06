package gateway

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aasm3535/lobster/internal/llm"
	"github.com/aasm3535/lobster/internal/tools"
)

// Terminal sessions are coded conversations: each one is a JSON file (its full transcript +
// tool timeline) under the history dir, keyed by a short generated code. On exit you get the
// code; `lobster -r <code>` resumes it. The agent can list and read OTHER sessions to pull
// information from work done elsewhere.

// sessionAlphabet excludes ambiguous characters (0/o, 1/l/i) so codes are easy to retype.
const sessionAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// newSessionCode returns a short random session identifier (e.g. "k7m2xp9q").
func newSessionCode() string {
	const n = 8
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		// Extremely unlikely; fall back to a fixed-but-unique-ish code.
		return "sess" + fmt.Sprint(len(sessionAlphabet))
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = sessionAlphabet[int(v)%len(sessionAlphabet)]
	}
	return string(out)
}

// registerSessionTools lets the agent browse and read other sessions' logs. current is the
// session being run now, so the listing can mark it.
func (g *Gateway) registerSessionTools(reg *tools.Registry, current string) {
	reg.Register(tools.Tool{
		Name: "session_list",
		Description: "List saved sessions (each is a separate coded conversation with its full tool timeline, stored " +
			"as a JSON file). Use this to discover other sessions you can read with session_log — e.g. to reuse work " +
			"done in a different session. Returns each session's code, when it was last active, message count and a title.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			list := g.hist.List()
			if len(list) == 0 {
				return "no saved sessions", nil
			}
			var b strings.Builder
			for _, m := range list {
				mark := "  "
				if m.ID == current {
					mark = "* " // the current session
				}
				title := m.Title
				if title == "" {
					title = "(no title yet)"
				}
				fmt.Fprintf(&b, "%s%s | %s | %d msgs | %s\n",
					mark, m.ID, m.Modified.Format("2006-01-02 15:04"), m.Messages, title)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})

	reg.Register(tools.Tool{
		Name: "session_log",
		Description: "Read another session's log by its code (from session_list): a compact transcript of what that " +
			"session did — the user's asks, your tool calls and their results, and your answers. Use it to pull in " +
			"information or continue work from a different session.",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"code": map[string]any{"type": "string", "description": "The session code."}},
			"required":   []string{"code"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			a.Code = strings.TrimSpace(a.Code)
			if a.Code == "" {
				return "", fmt.Errorf("code is required")
			}
			msgs, err := g.hist.Load(a.Code)
			if err != nil {
				return "", err
			}
			if len(msgs) == 0 {
				return "session " + a.Code + " is empty or doesn't exist", nil
			}
			return formatSessionLog(a.Code, msgs), nil
		},
	})
}

// formatSessionLog renders a session's messages compactly: user asks, tool calls + results,
// and assistant answers — clipped so a long session doesn't blow the context budget.
func formatSessionLog(code string, msgs []llm.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s (%d messages):\n", code, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			t := strings.TrimSpace(m.Content)
			if strings.HasPrefix(t, "(") { // synthetic kickoff
				continue
			}
			fmt.Fprintf(&b, "\nuser: %s\n", oneLine(t, 200))
		case llm.RoleAssistant:
			if c := strings.TrimSpace(m.Content); c != "" {
				fmt.Fprintf(&b, "assistant: %s\n", oneLine(c, 300))
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "  → %s %s\n", tc.Name, argPreview(tc.Arguments))
			}
		case llm.RoleTool:
			fmt.Fprintf(&b, "    result: %s\n", oneLine(m.Content, 160))
		}
	}
	return clipText(b.String(), 12000)
}

// clipText trims a string to at most n runes, marking the cut.
func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n…[truncated]"
}
