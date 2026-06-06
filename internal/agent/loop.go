package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aasm3535/lobster/internal/event"
)

// maxCallRetries is how many times a failed model call is retried (with backoff)
// before the turn gives up. Transient provider hiccups — rate limits, 5xx, dropped
// connections — shouldn't kill a long-running job.
const maxCallRetries = 3

// retryBackoff returns the wait before retry n (1-based): 2s, 5s, 10s.
func retryBackoff(n int) time.Duration {
	switch n {
	case 1:
		return 2 * time.Second
	case 2:
		return 5 * time.Second
	default:
		return 10 * time.Second
	}
}

// transientErr reports whether a model-call error is worth retrying. Permanent
// client-side errors (bad auth, malformed request) are not — but unknown/network
// errors default to retryable, which is the safer bet for an autonomous agent.
func transientErr(err error) bool {
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "http 4") {
		// 408 (timeout) and 429 (rate limit) are transient; other 4xx are our fault.
		return strings.Contains(s, "http 408") || strings.Contains(s, "http 429")
	}
	return true
}

// Once runs a single turn for one input and returns — used for scheduled / proactive
// runs that aren't part of the live chat (no steering, the inbound channel is empty).
func (a *Agent) Once(ctx context.Context, sess *Session, in Input, sink event.Sink) {
	sess.addUser(in)
	a.runTurn(ctx, sess, make(chan Input), sink)
}

// Run drives a session until ctx is cancelled, turning each inbound user message into a
// full reason-act turn. One Run goroutine owns one Session.
func (a *Agent) Run(ctx context.Context, sess *Session, inbound <-chan Input, sink event.Sink) {
	for {
		select {
		case <-ctx.Done():
			return
		case in := <-inbound:
			sess.addUser(in)
			a.runTurn(ctx, sess, inbound, sink)
		}
	}
}

// runTurn runs the reason-act loop for the current turn: ask the model, run any tools it
// requests, and repeat — until the model gives a tool-free answer (or, if a positive
// maxSteps is configured, until that ceiling). With maxSteps <= 0 it runs unbounded, so
// the agent can finish big multi-step jobs on its own. New user messages can land between
// steps (drain) or mid-call (steering).
func (a *Agent) runTurn(ctx context.Context, sess *Session, inbound <-chan Input, sink event.Sink) {
	// Forward streamed text to the channel so the reply types out live. Any text that
	// turns out to precede tool calls is discarded by the sink when the tools start.
	onText := func(delta string) { sink.Emit(event.Event{Kind: event.KindDelta, Text: delta}) }

	retries := 0
	for step := 0; a.maxSteps <= 0 || step < a.maxSteps; step++ {
		// Safe injection point: fold in anything that arrived during tool execution
		// before we ask the model for its next move.
		a.drain(sess, inbound)

		// Signal that the model is working so the channel can show a "typing" hint.
		sink.Emit(event.Event{Kind: event.KindThinking})

		resp, err := a.callWithInterrupt(ctx, sess, inbound, onText)
		if errors.Is(err, errInterrupted) {
			sink.Emit(event.Event{Kind: event.KindInterrupt, Text: "↩️ принял правку на лету"})
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Transient failure: back off and retry the SAME step instead of dying.
			// A user message during the wait cuts it short and is folded in.
			if retries < maxCallRetries && transientErr(err) {
				retries++
				sink.Emit(event.Event{Kind: event.KindInterrupt,
					Text: fmt.Sprintf("⚠️ сбой провайдера, пробую ещё раз (%d/%d)…", retries, maxCallRetries)})
				select {
				case <-ctx.Done():
					return
				case in := <-inbound:
					sess.addUser(in)
				case <-time.After(retryBackoff(retries)):
				}
				step--
				continue
			}
			sink.Emit(event.Event{Kind: event.KindError, Text: err.Error()})
			return
		}
		retries = 0

		// No tools requested → this is the final answer; record it and we're done.
		if len(resp.ToolCalls) == 0 {
			sess.addAssistant(resp)
			sink.Emit(event.Event{Kind: event.KindReply, Text: resp.Content})
			return
		}

		sess.addAssistant(resp)
		a.act(ctx, sess, inbound, resp.ToolCalls, sink)
	}

	// Only reachable when a positive step ceiling is set and exhausted.
	sink.Emit(event.Event{Kind: event.KindError, Text: fmt.Sprintf("остановился: достигнут предел шагов (%d)", a.maxSteps)})
}
