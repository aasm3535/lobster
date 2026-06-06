package agent

import (
	"github.com/aasm3535/lobster/internal/channel"
	"github.com/aasm3535/lobster/internal/llm"
)

// Input is one inbound user message: its text plus any attached media. Files are
// non-image attachments (voice, audio, video_note, documents, stickers) already
// downloaded to disk by the channel.
type Input struct {
	Text   string
	Images []llm.Image
	Files  []channel.InboundFile
}

// SessionStore persists a conversation so it survives a restart. It's an interface so
// the agent package stays decoupled from where transcripts are stored.
type SessionStore interface {
	Load(id string) ([]llm.Message, error)
	Save(id string, msgs []llm.Message) error
}

// Session is one conversation's running state: its id and the full message history that
// gets handed to the model on every call. If a store is attached, every change is
// persisted, and the history is trimmed to maxChars so it can't grow without bound.
type Session struct {
	ID       string
	Messages []llm.Message

	store    SessionStore
	maxChars int
}

// NewSession builds a session, loading any previously persisted transcript so the
// conversation continues where it left off. store/maxChars may be zero-valued for an
// ephemeral, unbounded session (used in tests).
func NewSession(id string, store SessionStore, maxChars int) *Session {
	s := &Session{ID: id, store: store, maxChars: maxChars}
	if store != nil {
		if msgs, err := store.Load(id); err == nil {
			s.Messages = msgs
		}
	}
	return s
}

// addUser appends a user message (text, images, and/or files) to the history.
func (s *Session) addUser(in Input) {
	msg := llm.Message{Role: llm.RoleUser, Content: in.Text, Images: in.Images}
	for _, f := range in.Files {
		msg.Files = append(msg.Files, llm.File{
			Path: f.Path, Filename: f.Filename, MIME: f.MIME,
			Kind: f.Kind, SizeBytes: f.SizeBytes, DurationSec: f.DurationSec,
		})
	}
	s.Messages = append(s.Messages, msg)
	s.persist()
}

// addAssistant records an assistant turn, including any tool calls it requested. Used
// both for a final answer (no tool calls) and for a tool-requesting turn.
func (s *Session) addAssistant(resp *llm.Response) {
	s.Messages = append(s.Messages, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	})
	s.persist()
}

// addToolResult records the result of one tool call. A tool result must directly follow
// its assistant tool-call turn with nothing injected in between, or providers reject the
// tool-call/result pairing — which is why user messages are only ever folded in at the
// safe points in the loop (drain) and never mid tool-run, and why trim() only ever cuts
// at user-message boundaries.
func (s *Session) addToolResult(tc llm.ToolCall, result string) {
	s.Messages = append(s.Messages, llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: tc.ID,
		Name:       tc.Name,
		Content:    result,
	})
	s.persist()
}

// persist trims the history to budget and writes it out (no-op without a store).
func (s *Session) persist() {
	s.trim()
	if s.store != nil {
		_ = s.store.Save(s.ID, s.Messages)
	}
}

// trim drops the oldest whole turns once the transcript exceeds maxChars. It only cuts
// at the start of a user message, so a turn (user → assistant → tool results) is never
// left dangling and tool-call/result pairs stay intact. The most recent turn is always
// kept. Durable facts that scroll out of this window live on via the remember tool.
func (s *Session) trim() {
	if s.maxChars <= 0 {
		return
	}
	total := 0
	for _, m := range s.Messages {
		total += len(m.Content)
	}
	if total <= s.maxChars {
		return
	}

	cut := 0
	for cut < len(s.Messages) && total > s.maxChars {
		total -= len(s.Messages[cut].Content)
		cut++
	}
	// Snap forward to the next user message so we don't start mid-turn.
	for cut < len(s.Messages) && s.Messages[cut].Role != llm.RoleUser {
		cut++
	}
	if cut > 0 && cut < len(s.Messages) {
		s.Messages = append(s.Messages[:0], s.Messages[cut:]...)
	}
}
