// Package llm defines a provider-agnostic chat interface built around NATIVE tool
// calling. Each provider maps these types onto its own native function-calling API
// (OpenAI tool_calls, Anthropic tool_use) — we never parse tools out of free text.
package llm

import (
	"context"
	"encoding/json"
	"strings"
)

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

	// Set on user messages that carry non-image files (voice, audio, docs, stickers).
	// Paths are local; the agent reads them with file/shell tools. Never persisted
	// (json:"-") so transcripts stay small — files are live-context only.
	Files []File `json:"-"`

	// Set on assistant messages that requested tools.
	ToolCalls []ToolCall

	// Set on tool-result messages.
	ToolCallID string
	Name       string
}

// File is an attached non-image file carried in a user message. It mirrors channel.InboundFile
// (the package split keeps the llm package free of channel-import cycles).
type File struct {
	Path        string `json:"path"`
	Filename    string `json:"filename,omitempty"`
	MIME        string `json:"mime,omitempty"`
	Kind        string `json:"kind,omitempty"` // voice | audio | video_note | document | sticker | other
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	DurationSec int    `json:"duration_sec,omitempty"`
}

// ToolCall is a single native tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON object
}

// SanitizeArgs guarantees tool-call arguments are valid JSON. A stream cut off
// mid-call (max_tokens, network drop) leaves truncated JSON; if that ever lands in the
// transcript, every later request fails to marshal and the conversation is poisoned
// until /reset. Both providers run all arguments — incoming and outgoing — through this.
func SanitizeArgs(s string) string {
	t := strings.TrimSpace(s)
	if t == "" || !json.Valid([]byte(t)) {
		return "{}"
	}
	return t
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
