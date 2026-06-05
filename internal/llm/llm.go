// Package llm defines a provider-agnostic chat interface built around NATIVE tool
// calling. Each provider maps these types onto its own native function-calling API
// (OpenAI tool_calls, Anthropic tool_use) — we never parse tools out of free text.
package llm

import "context"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Image is an attached picture (e.g. a photo the user sent). Data is the raw bytes;
// providers base64-encode it. It's never persisted (json:"-") so transcripts stay small
// — images are live-context only.
type Image struct {
	MediaType string // e.g. "image/jpeg"
	Data      []byte
}

// Message is one entry in the conversation.
type Message struct {
	Role    Role
	Content string

	// Set on user messages that carry attached images (vision input).
	Images []Image `json:"-"`

	// Set on assistant messages that requested tools.
	ToolCalls []ToolCall

	// Set on tool-result messages.
	ToolCallID string
	Name       string
}

// ToolCall is a single native tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON object
}

// ToolDef describes a tool to the model.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// Response is the result of one model call.
type Response struct {
	Content   string
	ToolCalls []ToolCall
	Stop      string
}

// Provider is one LLM backend. The system prompt is passed separately so providers
// that treat it specially (Anthropic) can map it natively.
type Provider interface {
	Name() string
	Chat(ctx context.Context, system string, msgs []Message, tools []ToolDef) (*Response, error)
}

// StreamProvider is an optional capability: a provider that can stream emits the
// assistant's text via onText as it arrives, while still returning the fully assembled
// Response (text + tool calls). Callers detect support with a type assertion and fall
// back to Chat otherwise.
type StreamProvider interface {
	ChatStream(ctx context.Context, system string, msgs []Message, tools []ToolDef, onText func(string)) (*Response, error)
}
