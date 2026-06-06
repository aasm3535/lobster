// Package event defines the agent's timeline events and the sink that renders them.
// Keeping this tiny and dependency-free lets channels (Telegram, future web) each
// render the same event stream however they like.
package event

import "time"

type Kind string

const (
	KindThinking   Kind = "thinking"
	KindDelta      Kind = "delta" // a streamed chunk of the assistant's text
	KindToolCall   Kind = "tool_call"
	KindToolResult Kind = "tool_result"
	KindSay        Kind = "say" // assistant text that accompanies tool calls (not the final reply)
	KindReply      Kind = "reply"
	KindError      Kind = "error"
	KindInterrupt  Kind = "interrupt"
)

// Event is one step in the agent's timeline.
type Event struct {
	Kind Kind
	Text string
	Tool string
	Args string

	// Elapsed is how long a tool ran (set on KindToolResult).
	Elapsed time.Duration
}

// Sink receives events as the agent works. Implementations decide how to show them.
type Sink interface {
	Emit(Event)
}
