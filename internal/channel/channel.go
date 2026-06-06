// Package channel defines the surface an assistant talks on (Telegram today, more later).
package channel

import (
	"context"

	"github.com/aasm3535/lobster/internal/llm"
)

// InboundFile is a file that came in with a message (voice, audio, document, sticker,
// etc.). Channels download the bytes and expose the on-disk path; the agent reads them
// with its file/shell tools. MIME and SizeBytes are advisory.
type InboundFile struct {
	Path        string // absolute path on the local filesystem
	Filename    string // original filename when known
	MIME        string // e.g. "audio/ogg", "image/webp", "application/pdf"
	Kind        string // "voice" | "audio" | "video_note" | "document" | "sticker" | "photo" | "other"
	SizeBytes   int64
	DurationSec int // 0 when unknown (e.g. documents)
}

// Inbound is a message arriving from a channel.
type Inbound struct {
	ChatID string
	Text   string
	From   string
	Images []llm.Image   // attached photos, already downloaded (vision input)
	Files  []InboundFile // any other attached media, downloaded to disk
}

// Command is a bot command advertised in the channel's UI (e.g. Telegram's "/" menu).
type Command struct {
	Name        string // without the leading slash, e.g. "start"
	Description string
}

// Channel is a bidirectional messaging surface.
type Channel interface {
	Name() string
	// Start blocks, delivering inbound messages to onMessage until ctx is cancelled.
	Start(ctx context.Context, onMessage func(Inbound)) error
	// SendText posts a new plain-text message and returns its id (for later edits).
	SendText(ctx context.Context, chatID, text string) (msgID string, err error)
	// SendMarkdown posts a message rendered as Markdown (MarkdownV2 on Telegram).
	SendMarkdown(ctx context.Context, chatID, text string) (msgID string, err error)
	// SendHTML posts a message rendered as HTML (Telegram's most forgiving rich format).
	SendHTML(ctx context.Context, chatID, text string) (msgID string, err error)
	// SendPhoto sends an image (a local file path or an http(s) URL) with an optional caption.
	SendPhoto(ctx context.Context, chatID, photo, caption string) (msgID string, err error)
	// EditText edits a previously sent message in place (plain text).
	EditText(ctx context.Context, chatID, msgID, text string) error
	// EditHTML edits a previously sent message in place, rendered as HTML.
	EditHTML(ctx context.Context, chatID, msgID, text string) error
	// DeleteText removes a previously sent message (best-effort).
	DeleteText(ctx context.Context, chatID, msgID string) error
	// SendChatAction shows a transient status like "typing" in the chat.
	SendChatAction(ctx context.Context, chatID, action string) error
	// SetCommands advertises the bot's command list in the channel UI.
	SetCommands(ctx context.Context, cmds []Command) error
	// SetMessageReaction attaches an emoji reaction to a message. An empty emoji
	// removes the bot's reaction. Channels that don't support reactions may no-op.
	SetMessageReaction(ctx context.Context, chatID, msgID, emoji string) error
}
