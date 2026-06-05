package agent

import (
	"context"
	"encoding/json"
	"time"

	"lobster/internal/event"
	"lobster/internal/llm"
)

// act executes every tool the model requested, in order, recording each result back
// into the session and emitting timeline events (with timings) as it goes.
//
// A tool error is fed back to the model as that tool's result rather than aborting the
// turn — the model can then read the error and recover on the next step.
func (a *Agent) act(ctx context.Context, sess *Session, calls []llm.ToolCall, sink event.Sink) {
	for _, tc := range calls {
		sink.Emit(event.Event{Kind: event.KindToolCall, Tool: tc.Name, Args: tc.Arguments})

		start := time.Now()
		result, err := a.tools.Run(ctx, tc.Name, json.RawMessage(tc.Arguments))
		if err != nil {
			result = "error: " + err.Error()
		}

		sink.Emit(event.Event{Kind: event.KindToolResult, Tool: tc.Name, Text: result, Elapsed: time.Since(start)})
		sess.addToolResult(tc, result)
	}
}
