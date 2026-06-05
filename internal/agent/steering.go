package agent

import (
	"context"
	"errors"
	"strings"

	"lobster/internal/llm"
)

// errInterrupted signals that a new user message arrived mid-generation and was folded
// into the session, so the current model call was abandoned and the turn should re-ask.
var errInterrupted = errors.New("interrupted by new user message")

// callWithInterrupt runs one model call but aborts it the moment a new user message
// arrives, re-injecting that message so the model sees it on the next call. This is
// Lobster's headline trick: a "no, do it differently" lands BEFORE the agent commits to
// the wrong path, instead of after it has already run the tools.
//
// onText receives the assistant's text as it streams (when the provider supports it), so
// the reply can be shown typing out live. It fires only from the in-flight goroutine,
// which has fully returned by the time this function returns, so callers never see
// concurrent onText and post-call events.
func (a *Agent) callWithInterrupt(ctx context.Context, sess *Session, inbound <-chan Input, onText func(string)) (*llm.Response, error) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		resp *llm.Response
		err  error
	}

	// Accumulate the streamed text so that, if the user interrupts mid-generation, we can
	// keep what the model was in the middle of writing (not just throw it away).
	var partial strings.Builder
	stream := func(s string) {
		partial.WriteString(s)
		if onText != nil {
			onText(s)
		}
	}

	resCh := make(chan result, 1)
	go func() {
		var r *llm.Response
		var e error
		if sp, ok := a.provider.(llm.StreamProvider); ok {
			r, e = sp.ChatStream(cctx, a.system(), sess.Messages, a.tools.Defs(), stream)
		} else {
			r, e = a.provider.Chat(cctx, a.system(), sess.Messages, a.tools.Defs())
		}
		resCh <- result{r, e}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case in := <-inbound:
		cancel()
		<-resCh // let the in-flight request unwind before we touch the session
		// Record the half-written reply (if any) so the model sees what it was doing,
		// then the correction — letting it recover with full context instead of blind.
		if p := strings.TrimSpace(partial.String()); p != "" {
			sess.addAssistant(&llm.Response{Content: p})
		}
		sess.addUser(in)
		return nil, errInterrupted
	case r := <-resCh:
		return r.resp, r.err
	}
}

// drain non-blockingly folds any pending inbound messages into the session. It's called
// at the top of each step — a safe point where no tool-call/result pair is open.
func (a *Agent) drain(sess *Session, inbound <-chan Input) {
	for {
		select {
		case in := <-inbound:
			sess.addUser(in)
		default:
			return
		}
	}
}
