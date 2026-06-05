// Package agent runs the interruptible reason-act loop: it calls the model with native
// tools, executes the tools, and — crucially — lets the user steer mid-run. A message
// that arrives while the model is "thinking" cancels the in-flight call and is injected
// immediately, so the agent sees "no, do it differently" BEFORE it goes down the wrong
// path. That is the thing that's painful in OpenClaw.
//
// The package is split by concern:
//   - agent.go    — the Agent type and its construction
//   - session.go  — Session: the conversation history and how messages are recorded
//   - loop.go     — Run + runTurn: the reason-act driver
//   - act.go      — tool execution
//   - steering.go — the mid-run interrupt / steering mechanism
package agent

import (
	"lobster/internal/llm"
	"lobster/internal/tools"
)

// Agent turns user messages into model calls and tool executions. It holds no
// per-conversation state — that all lives in the Session handed to Run — so a single
// Agent can drive many independent conversations.
type Agent struct {
	provider llm.Provider
	tools    *tools.Registry
	system   func() string
	maxSteps int // <= 0 means no limit: the agent runs until it answers (full autonomy)
}

// New builds an agent. system is evaluated before every model call, so the prompt can
// fold in things learned mid-conversation (e.g. freshly remembered facts). A maxSteps
// of 0 or less removes the step ceiling entirely.
func New(provider llm.Provider, reg *tools.Registry, system func() string, maxSteps int) *Agent {
	if system == nil {
		system = func() string { return "" }
	}
	return &Agent{provider: provider, tools: reg, system: system, maxSteps: maxSteps}
}
