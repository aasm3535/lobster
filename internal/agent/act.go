package agent

import (
	"context"
	"encoding/json"
	"time"

	"lobster/internal/event"
	"lobster/internal/llm"
)

type toolOutcome struct {
	result string
	err    error
}

// act executes the model's tool calls in order — and stays interruptible while it does.
//
// This is the heart of "steer it mid-task": if a new user message arrives WHILE a tool
// is running, the running tool is cancelled immediately, whatever it produced so far is
// kept, the remaining calls in the batch are recorded as skipped (so the tool-call /
// tool-result pairing the providers require stays intact), and the user's message is
// folded in. The next model call then sees exactly what the agent had done — including
// the half-finished work — plus the correction, so it recovers instead of barrelling on.
func (a *Agent) act(ctx context.Context, sess *Session, inbound <-chan Input, calls []llm.ToolCall, sink event.Sink) {
	interrupted := false
	for _, tc := range calls {
		if interrupted {
			// Keep the pairing valid: every requested call needs a result.
			sess.addToolResult(tc, "[skipped — interrupted by the user]")
			continue
		}

		sink.Emit(event.Event{Kind: event.KindToolCall, Tool: tc.Name, Args: tc.Arguments})
		start := time.Now()

		tctx, cancel := context.WithCancel(ctx)
		done := make(chan toolOutcome, 1)
		go func(name, args string) {
			out, err := a.tools.Run(tctx, name, json.RawMessage(args))
			done <- toolOutcome{out, err}
		}(tc.Name, tc.Arguments)

		select {
		case <-ctx.Done():
			cancel()
			return
		case in := <-inbound:
			// Forceful stop: kill the running tool, but keep whatever it produced.
			cancel()
			oc := <-done // let it unwind so we capture partial output
			result := oc.result
			if oc.err != nil && result == "" {
				result = "error: " + oc.err.Error()
			}
			if result == "" {
				result = "[interrupted by the user before it produced output]"
			} else {
				result += "\n[interrupted by the user]"
			}
			sink.Emit(event.Event{Kind: event.KindToolResult, Tool: tc.Name, Text: result, Elapsed: time.Since(start)})
			sess.addToolResult(tc, result)
			// Result recorded (pairing intact) — now it's safe to fold in the correction.
			sess.addUser(in)
			sink.Emit(event.Event{Kind: event.KindInterrupt, Text: "↩️ остановил, принял правку"})
			interrupted = true
		case oc := <-done:
			cancel()
			result := oc.result
			if oc.err != nil {
				result = "error: " + oc.err.Error()
			}
			sink.Emit(event.Event{Kind: event.KindToolResult, Tool: tc.Name, Text: result, Elapsed: time.Since(start)})
			sess.addToolResult(tc, result)
		}
	}
}
