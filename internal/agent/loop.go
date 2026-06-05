package agent

import (
	"context"
	"errors"
	"fmt"

	"lobster/internal/event"
)

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
			sink.Emit(event.Event{Kind: event.KindError, Text: err.Error()})
			return
		}

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
