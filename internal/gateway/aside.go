package gateway

import (
	"context"
	"strings"

	"github.com/aasm3535/lobster/internal/agent"
	"github.com/aasm3535/lobster/internal/event"
)

// "by the way" lets you ask a quick SIDE question while the agent is busy with the main task
// — it's answered separately (an ephemeral run with the conversation as context) and never
// steers or changes what the agent is already doing. Triggers: "by the way", "btw", "кстати".

var asidePrefixes = []string{"by the way", "btw", "кстати"}

// asideQuestion reports whether text is a side question and returns it without the prefix.
func asideQuestion(text string) (string, bool) {
	t := strings.TrimSpace(text)
	low := strings.ToLower(t)
	for _, p := range asidePrefixes {
		if low == p {
			return "", true // bare "btw" with no question
		}
		if strings.HasPrefix(low, p) {
			rest := strings.TrimSpace(t[len(p):])
			rest = strings.TrimLeft(rest, ",:—-. ") // drop the separator after the prefix
			if rest != "" {
				return rest, true
			}
		}
	}
	return "", false
}

const asideNote = "The user asked a quick SIDE question (\"by the way\"). Answer it directly and briefly using the " +
	"conversation context below. Do NOT start or change the main task you may be working on — this is just a question " +
	"on the side; don't run tools unless the question truly needs it."

// asideSink captures only the final reply/error of an aside run.
type asideSink struct {
	reply string
	fail  string
}

func (s *asideSink) Emit(ev event.Event) {
	switch ev.Kind {
	case event.KindReply:
		s.reply = ev.Text
	case event.KindError:
		s.fail = ev.Text
	}
}

// answerAside runs a side question to completion against a read-only snapshot of the chat's
// recent transcript (loaded from disk, so it never races the live session) and returns the
// answer text. It does not touch the running turn.
func (g *Gateway) answerAside(ctx context.Context, chatID, histID, question string) string {
	if strings.TrimSpace(question) == "" {
		question = "(no question — just acknowledge briefly)"
	}
	msgs, _ := g.hist.Load(histID)
	if len(msgs) > 40 {
		msgs = msgs[len(msgs)-40:]
	}
	contextText := ""
	if len(msgs) > 0 {
		contextText = "\n\nRecent conversation so far:\n" + formatSessionLog("current", msgs)
	}

	self := g.selfInfo()
	systemFn := func() string {
		return composeSystem(g.cfg.System, self, g.prefsLine(chatID), g.promptSections(), g.mem.Notes(chatID)) +
			"\n\n" + asideNote + contextText
	}
	// Ephemeral session: only the side question, so there's no role-pairing risk and the
	// main session is untouched.
	sess := agent.NewSession("aside", nil, 0)
	ag := agent.New(g.activeProvider(chatID), g.chatTools(chatID), systemFn, g.cfg.MaxSteps)
	sink := &asideSink{}
	ag.Once(ctx, sess, agent.Input{Text: question}, sink)

	switch {
	case strings.TrimSpace(sink.reply) != "":
		return sink.reply
	case sink.fail != "":
		return "(side question failed: " + sink.fail + ")"
	default:
		return "(no answer)"
	}
}

// runAsideTelegram answers a side question and sends it as a separate Telegram message,
// prefixed so it reads as an aside, without interrupting the main turn.
func (g *Gateway) runAsideTelegram(chatID, question string) {
	reply := g.answerAside(g.appCtx, chatID, chatID, question)
	msg := "💬 by the way —\n\n" + reply
	if _, err := g.ch.SendHTML(g.appCtx, chatID, mdToHTML(msg)); err != nil {
		_, _ = g.ch.SendText(g.appCtx, chatID, msg)
	}
}
