package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lobster/internal/channel"
	"lobster/internal/event"
)

// streamEditEvery throttles live message edits. Telegram rate-limits editMessageText, so
// ~2-3 edits/sec per message is the safe ceiling; we edit at most ~1.4/sec.
const streamEditEvery = 700 * time.Millisecond

// telegramSink renders the agent's event stream to Telegram. The reply types out live
// (streamed, throttled edits), and a "typing…" indicator is kept alive the whole time so
// it never looks stalled. Verbosity only adds the tool timeline:
//   - quiet/normal: typing + the streamed answer
//   - verbose:      + a live tool timeline kept after the answer
type telegramSink struct {
	ch        channel.Channel
	chatID    string
	sessCtx   context.Context // cancelled on /reset or shutdown — stops the typing loop
	verbosity func() string
	streaming func() bool // when false, the reply is sent once at the end, not typed out live
	archive   func(role, text string)

	// live tool timeline (verbose only)
	statusID string
	lines    []string

	// live streamed reply
	streamID  string
	streamBuf strings.Builder
	lastEdit  time.Time

	typingStop chan struct{}
}

func newTelegramSink(ch channel.Channel, chatID string, sessCtx context.Context, verbosity func() string, streaming func() bool, archive func(role, text string)) *telegramSink {
	return &telegramSink{ch: ch, chatID: chatID, sessCtx: sessCtx, verbosity: verbosity, streaming: streaming, archive: archive}
}

// streamEnabled reports whether the reply should type out live (default true).
func (s *telegramSink) streamEnabled() bool {
	return s.streaming == nil || s.streaming()
}

func (s *telegramSink) level() string {
	if s.verbosity == nil {
		return verbosityNormal
	}
	if v := s.verbosity(); v != "" {
		return v
	}
	return verbosityNormal
}

// Emit is called from the agent goroutine (most events) and the provider goroutine
// (KindDelta, during a streaming call) — never concurrently, since the agent goroutine
// blocks on the call while deltas stream. The typing loop is a separate goroutine that
// only touches the channel, so sink state needs no locking.
func (s *telegramSink) Emit(ev event.Event) {
	ctx := context.Background()
	v := s.level()
	switch ev.Kind {
	case event.KindThinking:
		s.startTyping()
	case event.KindDelta:
		if s.streamEnabled() {
			s.streamDelta(ctx, ev.Text)
		}
	case event.KindToolCall:
		s.discardStream(ctx) // any text that streamed before the tools was a preamble — drop it
		if v == verbosityVerbose {
			s.appendLine(ctx, fmt.Sprintf("🛠 %s  %s", ev.Tool, oneLine(ev.Args, 72)))
		}
	case event.KindToolResult:
		if v == verbosityVerbose {
			s.appendLine(ctx, fmt.Sprintf("   ↳ %s · %s", fmtDur(ev.Elapsed), oneLine(ev.Text, 72)))
		}
	case event.KindInterrupt:
		s.discardStream(ctx)
		if v == verbosityVerbose {
			s.appendLine(ctx, ev.Text)
		}
	case event.KindReply:
		s.stopTyping()
		if v != verbosityVerbose {
			s.clearStatus(ctx)
		}
		if s.archive != nil {
			s.archive("assistant", ev.Text)
		}
		s.finalizeStream(ctx, ev.Text)
		s.reset()
	case event.KindError:
		s.stopTyping()
		s.discardStream(ctx)
		s.clearStatus(ctx)
		s.sendChunks(ctx, "⚠️ "+ev.Text)
		s.reset()
	}
}

// --- streamed reply ---

func (s *telegramSink) streamDelta(ctx context.Context, delta string) {
	s.streamBuf.WriteString(delta)
	body := s.streamBody()
	if body == "" {
		return
	}
	if s.streamID == "" {
		if id, err := s.ch.SendText(ctx, s.chatID, body); err == nil {
			s.streamID = id
			s.lastEdit = time.Now()
		}
		return
	}
	if time.Since(s.lastEdit) >= streamEditEvery {
		_ = s.ch.EditText(ctx, s.chatID, s.streamID, body)
		s.lastEdit = time.Now()
	}
}

// streamBody is the in-progress text shown while streaming, capped to fit a message.
func (s *telegramSink) streamBody() string {
	t := strings.TrimRight(s.streamBuf.String(), " \n")
	if t == "" {
		return ""
	}
	return truncateTail(t, 3950)
}

// finalizeStream turns the live streamed message into the final, Markdown-rendered reply.
func (s *telegramSink) finalizeStream(ctx context.Context, text string) {
	defer func() {
		s.streamID = ""
		s.streamBuf.Reset()
		s.lastEdit = time.Time{}
	}()

	if strings.TrimSpace(text) == "" {
		s.discardStreamMsg(ctx)
		return
	}
	// If the whole reply fits one message, edit the streamed one in place — no flicker.
	if s.streamID != "" && len([]rune(text)) <= 4000 {
		if s.ch.EditHTML(ctx, s.chatID, s.streamID, mdToHTML(text)) == nil {
			return
		}
		if s.ch.EditText(ctx, s.chatID, s.streamID, text) == nil {
			return
		}
	}
	// Otherwise (no live msg, or too long for one edit): drop the partial, send fresh.
	s.discardStreamMsg(ctx)
	s.sendReply(ctx, text)
}

func (s *telegramSink) discardStream(ctx context.Context) {
	s.discardStreamMsg(ctx)
	s.streamBuf.Reset()
	s.lastEdit = time.Time{}
}

func (s *telegramSink) discardStreamMsg(ctx context.Context) {
	if s.streamID != "" {
		_ = s.ch.DeleteText(ctx, s.chatID, s.streamID)
		s.streamID = ""
	}
}

// sendReply delivers text as rich Telegram HTML, falling back to plain per chunk if
// Telegram rejects the markup — used when there's no live message to finalize.
func (s *telegramSink) sendReply(ctx context.Context, text string) {
	for _, c := range chunk(text, 4000) {
		if _, err := s.ch.SendHTML(ctx, s.chatID, mdToHTML(c)); err != nil {
			_, _ = s.ch.SendText(ctx, s.chatID, c)
		}
	}
}

// --- typing keep-alive ---

func (s *telegramSink) startTyping() {
	if s.typingStop != nil {
		go func() { _ = s.ch.SendChatAction(context.Background(), s.chatID, "typing") }()
		return
	}
	stop := make(chan struct{})
	s.typingStop = stop
	go func() {
		ctx := context.Background()
		_ = s.ch.SendChatAction(ctx, s.chatID, "typing")
		t := time.NewTicker(4 * time.Second) // Telegram's "typing" lasts ~5s; refresh before it lapses
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-s.sessCtx.Done():
				return
			case <-t.C:
				_ = s.ch.SendChatAction(ctx, s.chatID, "typing")
			}
		}
	}()
}

func (s *telegramSink) stopTyping() {
	if s.typingStop != nil {
		close(s.typingStop)
		s.typingStop = nil
	}
}

// --- tool timeline (verbose) ---

// clearStatus removes the live tool timeline once the turn is done.
func (s *telegramSink) clearStatus(ctx context.Context) {
	if s.statusID != "" {
		_ = s.ch.DeleteText(ctx, s.chatID, s.statusID)
	}
}

func (s *telegramSink) appendLine(ctx context.Context, line string) {
	s.lines = append(s.lines, line)
	body := truncateTail("🦞 работаю…\n"+strings.Join(s.lines, "\n"), 3500)
	if s.statusID == "" {
		if id, err := s.ch.SendText(ctx, s.chatID, body); err == nil {
			s.statusID = id
		}
		return
	}
	_ = s.ch.EditText(ctx, s.chatID, s.statusID, body)
}

func (s *telegramSink) reset() {
	s.statusID = ""
	s.lines = nil
	s.streamID = ""
	s.streamBuf.Reset()
	s.lastEdit = time.Time{}
}

func (s *telegramSink) sendChunks(ctx context.Context, text string) {
	for _, c := range chunk(text, 4000) {
		_, _ = s.ch.SendText(ctx, s.chatID, c)
	}
}

// fmtDur renders a tool/job duration compactly: 340ms, 1.2s, 2m05s.
func fmtDur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

func oneLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func truncateTail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func chunk(s string, n int) []string {
	if strings.TrimSpace(s) == "" {
		return []string{"(пустой ответ)"}
	}
	var out []string
	r := []rune(s)
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return append(out, string(r))
}
