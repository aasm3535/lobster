package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aasm3535/lobster/internal/agent"
	"github.com/aasm3535/lobster/internal/tools"
)

// This file gives the agent real multi-agent orchestration: spawn_agents fans a job out
// to N parallel subagents, each with its own fresh context and the SAME full toolset
// (shell, files, MCP, skills) — so the main agent can decompose big work (audit these 5
// modules, research these 3 approaches, fix these files in parallel) instead of grinding
// through it serially in one context. Depth is capped at one level: subagents cannot
// spawn their own subagents, which keeps a runaway fan-out impossible.

// maxSpawnDepth is how deep agent nesting may go (0 = the user-facing agent).
const maxSpawnDepth = 1

// maxParallelAgents caps one spawn_agents call.
const maxParallelAgents = 8

// subagentSystem frames a delegated task: do it autonomously, report back completely.
const subagentSystem = "You are a Lobster subagent: one focused worker spawned by the main agent to do ONE " +
	"delegated task. You have the same real tools (shell, files, MCP) on the same machine. Work autonomously — " +
	"never ask questions, make sensible decisions yourself, carry the task through to the end. Your final reply " +
	"is your REPORT back to the main agent (a human never sees it directly): make it complete and self-contained — " +
	"concrete findings, paths, what you changed, what failed — so the main agent can act on it without re-doing " +
	"your work."

// registerSpawn adds the spawn_agents tool to a registry. depth is the registry owner's
// nesting level; the tool is only offered below maxSpawnDepth.
func (g *Gateway) registerSpawn(reg *tools.Registry, chatID string, depth int) {
	if depth >= maxSpawnDepth {
		return
	}
	reg.Register(tools.Tool{
		Name: "spawn_agents",
		Description: "Orchestrate PARALLEL subagents: fan one big job out into up to " + fmt.Sprint(maxParallelAgents) + " independent tasks, " +
			"each run by a full agent with its own fresh context and all your tools (shell, files, MCP). Use this for work " +
			"that decomposes — audit several modules at once, research multiple approaches, apply fixes across independent " +
			"files, build several components of a project. Each 'task' must be SELF-CONTAINED (include paths, context, and " +
			"exactly what to report back) because the subagent sees nothing of this conversation. Blocks until all finish " +
			"and returns every report. Don't use it for one small step — just do that yourself.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "The independent tasks to run in parallel (1–" + fmt.Sprint(maxParallelAgents) + ").",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"task":  map[string]any{"type": "string", "description": "Self-contained instruction for this subagent."},
							"label": map[string]any{"type": "string", "description": "Short label for the report (e.g. \"auth-module\")."},
						},
						"required": []string{"task"},
					},
				},
				"timeout_sec": map[string]any{"type": "integer", "description": "Overall timeout in seconds (default 900)."},
			},
			"required": []string{"tasks"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Tasks []struct {
					Task  string `json:"task"`
					Label string `json:"label"`
				} `json:"tasks"`
				TimeoutSec int `json:"timeout_sec"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if len(a.Tasks) == 0 {
				return "", fmt.Errorf("tasks is empty")
			}
			if len(a.Tasks) > maxParallelAgents {
				return "", fmt.Errorf("too many tasks (%d) — max %d per call", len(a.Tasks), maxParallelAgents)
			}
			if a.TimeoutSec <= 0 {
				a.TimeoutSec = 900
			}
			cctx, cancel := context.WithTimeout(ctx, time.Duration(a.TimeoutSec)*time.Second)
			defer cancel()

			reports := make([]string, len(a.Tasks))
			var wg sync.WaitGroup
			for i, t := range a.Tasks {
				if strings.TrimSpace(t.Task) == "" {
					reports[i] = "(empty task — skipped)"
					continue
				}
				wg.Add(1)
				go func(i int, task, label string) {
					defer wg.Done()
					reports[i] = g.runSubagent(cctx, chatID, depth+1, task, label)
				}(i, t.Task, t.Label)
			}
			wg.Wait()

			var b strings.Builder
			for i, rep := range reports {
				label := strings.TrimSpace(a.Tasks[i].Label)
				if label == "" {
					label = fmt.Sprintf("task %d", i+1)
				}
				fmt.Fprintf(&b, "=== agent %d/%d: %s ===\n%s\n\n", i+1, len(a.Tasks), label, strings.TrimSpace(rep))
			}
			if cctx.Err() == context.DeadlineExceeded {
				b.WriteString("[note: the overall timeout was hit — some reports above may be incomplete]\n")
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})
}

// runSubagent executes one delegated task to completion and returns its report. The
// subagent gets the chat's full toolset at depth+1 (so it cannot spawn further agents),
// the chat's active model, and an ephemeral, unpersisted session.
func (g *Gateway) runSubagent(ctx context.Context, chatID string, depth int, task, label string) string {
	system := subagentSystem
	if sk := g.promptSections(); sk != "" {
		system += "\n\n" + sk
	}
	if strings.TrimSpace(label) == "" {
		label = oneLine(task, 32)
	}
	run := g.hub.start(chatID, label, task)
	defer func() {
		if run.statusIs("running") {
			run.finish("done")
		}
	}()

	sess := agent.NewSession("sub:"+chatID+":"+label, nil, 0) // ephemeral
	sink := &hubSink{run: run}
	ag := agent.New(g.activeProvider(chatID), g.chatToolsAt(chatID, depth), func() string { return system }, g.cfg.MaxSteps)
	ag.Once(ctx, sess, agent.Input{Text: task}, sink)

	reply, fail := sink.result()
	if fail != "" {
		run.finish("failed")
	} else {
		run.finish("done")
	}
	switch {
	case reply != "":
		return reply
	case fail != "":
		return "[subagent error] " + fail
	case ctx.Err() != nil:
		return "[subagent stopped: " + ctx.Err().Error() + "]"
	default:
		return "[subagent produced no report]"
	}
}
