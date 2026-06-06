package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aasm3535/lobster/internal/event"
	"github.com/aasm3535/lobster/internal/llm"
	"github.com/aasm3535/lobster/internal/tools"
)

// fakeProvider scripts model responses and can simulate "thinking" latency so a
// test can inject a steering message mid-generation.
type fakeProvider struct {
	delay   time.Duration
	respond func(call int, msgs []llm.Message) *llm.Response

	mu    sync.Mutex
	calls int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Chat(ctx context.Context, _ string, msgs []llm.Message, _ []llm.ToolDef) (*llm.Response, error) {
	f.mu.Lock()
	call := f.calls
	f.calls++
	f.mu.Unlock()

	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	return f.respond(call, msgs), nil
}

type collector struct {
	mu     sync.Mutex
	events []event.Event
}

func (c *collector) Emit(ev event.Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *collector) waitReply(t *testing.T, timeout time.Duration) event.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, e := range c.events {
			if e.Kind == event.KindReply || e.Kind == event.KindError {
				c.mu.Unlock()
				return e
			}
		}
		c.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a reply/error event")
	return event.Event{}
}

func (c *collector) has(kind event.Kind) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// sysFn is a static system-prompt function for tests.
func sysFn() string { return "sys" }

func lastUser(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

// The model asks for a tool, we run it, then it replies.
func TestRunTurn_ToolThenReply(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.Tool{
		Name:   "echo",
		Schema: map[string]any{"type": "object"},
		Run:    func(context.Context, json.RawMessage) (string, error) { return "pong", nil },
	})

	fp := &fakeProvider{respond: func(call int, _ []llm.Message) *llm.Response {
		if call == 0 {
			return &llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Arguments: "{}"}}}
		}
		return &llm.Response{Content: "done"}
	}}

	col := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbound := make(chan Input, 4)
	go New(fp, reg, sysFn, 10).Run(ctx, &Session{ID: "t"}, inbound, col)

	inbound <- Input{Text: "hi"}
	reply := col.waitReply(t, 2*time.Second)

	if reply.Kind != event.KindReply || reply.Text != "done" {
		t.Fatalf("want reply 'done', got %+v", reply)
	}
	if !col.has(event.KindToolCall) || !col.has(event.KindToolResult) {
		t.Fatalf("expected tool_call and tool_result events; got %+v", col.events)
	}
}

// After running a tool the model returns an EMPTY answer; the loop should nudge once for a
// summary instead of finishing silently, and deliver the follow-up text.
func TestRunTurn_EmptyAnswerNudged(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.Tool{
		Name:   "edit_file",
		Schema: map[string]any{"type": "object"},
		Run:    func(context.Context, json.RawMessage) (string, error) { return "edited", nil },
	})

	var sawNudge bool
	fp := &fakeProvider{respond: func(call int, msgs []llm.Message) *llm.Response {
		switch call {
		case 0:
			return &llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "edit_file", Arguments: "{}"}}}
		case 1:
			return &llm.Response{Content: ""} // silent finish — should trigger the nudge
		default:
			if strings.Contains(lastUser(msgs), "короткий итог") {
				sawNudge = true
			}
			return &llm.Response{Content: "Готово: поправил файл."}
		}
	}}

	col := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbound := make(chan Input, 4)
	go New(fp, reg, sysFn, 10).Run(ctx, &Session{ID: "t"}, inbound, col)

	inbound <- Input{Text: "fix it"}
	reply := col.waitReply(t, 2*time.Second)

	if reply.Kind != event.KindReply || reply.Text != "Готово: поправил файл." {
		t.Fatalf("want the nudged summary, got %+v", reply)
	}
	if !sawNudge {
		t.Fatal("expected the empty-answer nudge to be injected")
	}
}

// The core differentiator: a message sent WHILE the model is generating cancels the
// in-flight call and is injected immediately, so the next response is built from it.
func TestInterruptDuringGeneration(t *testing.T) {
	reg := tools.NewRegistry()

	// Each response just echoes the latest user message, proving which input the
	// model "saw" when it produced the reply.
	fp := &fakeProvider{
		delay:   300 * time.Millisecond,
		respond: func(_ int, msgs []llm.Message) *llm.Response { return &llm.Response{Content: lastUser(msgs)} },
	}

	col := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbound := make(chan Input, 4)
	go New(fp, reg, sysFn, 10).Run(ctx, &Session{ID: "t"}, inbound, col)

	inbound <- Input{Text: "go do the wrong thing"}
	time.Sleep(100 * time.Millisecond) // let generation start
	inbound <- Input{Text: "STOP do it differently"}

	reply := col.waitReply(t, 3*time.Second)
	if reply.Text != "STOP do it differently" {
		t.Fatalf("steering failed: reply was built from %q, want the steering message", reply.Text)
	}
	if !col.has(event.KindInterrupt) {
		t.Fatalf("expected an interrupt event; got %+v", col.events)
	}
}

// The killer feature: a message arriving WHILE a tool runs forcefully stops it. The
// tool's (partial) output is kept and marked interrupted, the correction is folded in,
// and the next model call sees what the agent had done plus the new message — so it
// recovers with full context instead of barrelling on.
func TestSteering_MessageDuringToolExecution(t *testing.T) {
	started := make(chan struct{})
	reg := tools.NewRegistry()
	reg.Register(tools.Tool{
		Name:   "slow",
		Schema: map[string]any{"type": "object"},
		Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			close(started)                    // signal the tool is executing
			time.Sleep(50 * time.Millisecond) // window for the user to message mid-run
			return "tool result: did the slow thing", nil
		},
	})

	var sawToolResult, sawInterruptMark bool
	fp := &fakeProvider{respond: func(call int, msgs []llm.Message) *llm.Response {
		if call == 0 {
			return &llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "slow", Arguments: "{}"}}}
		}
		// Second call: confirm the (interrupted) tool result is still in context AND the
		// new user message landed. Echo the latest user message to prove what was seen.
		for _, m := range msgs {
			if m.Role == llm.RoleTool && strings.Contains(m.Content, "tool result: did the slow thing") {
				sawToolResult = true
			}
			if m.Role == llm.RoleTool && strings.Contains(m.Content, "interrupted by the user") {
				sawInterruptMark = true
			}
		}
		return &llm.Response{Content: lastUser(msgs)}
	}}

	col := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbound := make(chan Input, 4)
	go New(fp, reg, sysFn, 10).Run(ctx, &Session{ID: "t"}, inbound, col)

	inbound <- Input{Text: "do the slow thing"}
	<-started                                         // the tool is now executing
	inbound <- Input{Text: "actually, also handle Y"} // steer mid-tool

	reply := col.waitReply(t, 3*time.Second)
	if reply.Text != "actually, also handle Y" {
		t.Fatalf("mid-tool message was not folded in: reply built from %q", reply.Text)
	}
	if !sawToolResult {
		t.Fatal("context was lost: the model didn't see the tool result alongside the new message")
	}
	if !sawInterruptMark {
		t.Fatal("the tool result should be marked as interrupted")
	}
	if !col.has(event.KindInterrupt) {
		t.Fatal("expected an interrupt event for the mid-tool stop")
	}
}
