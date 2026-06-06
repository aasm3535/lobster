package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aasm3535/lobster/internal/agent"
	"github.com/aasm3535/lobster/internal/tools"
	"github.com/aasm3535/lobster/internal/workflows"
)

// Workflows are saved multi-step playbooks (markdown files in cfg.WorkflowsDir). The user
// replays one with /workflow <name> (Telegram and terminal alike); the agent can author,
// inspect and update them with the tools below — so "запомни этот процесс" becomes a
// one-command procedure forever after.

// workflowKickoff frames a triggered workflow as the agent's next input: execute it now,
// autonomously, and report — the steps come from the saved file, not from the user.
func workflowKickoff(name, body, extra string) string {
	s := "(▶️ The user triggered the saved workflow «" + name + "». Execute it now, step by step, autonomously — " +
		"don't ask for confirmation, carry it through and report the result.)\n\n--- WORKFLOW ---\n" + body
	if strings.TrimSpace(extra) != "" {
		s += "\n\n--- EXTRA INPUT FROM THE USER ---\n" + extra
	}
	return s
}

// workflowsSection lists saved workflows for the system prompt (appended after skills).
func (g *Gateway) workflowsSection() string {
	list := g.wf.List()
	var b strings.Builder
	b.WriteString("# Workflows (saved multi-step playbooks in " + g.wf.Dir() + ")\n")
	if len(list) == 0 {
		b.WriteString("None saved yet. When the user has a repeatable multi-step procedure (deploy, weekly report, " +
			"project bootstrap), offer to save it with save_workflow — afterwards /workflow <name> replays it.")
		return b.String()
	}
	for _, m := range list {
		fmt.Fprintf(&b, "- %s: %s\n", m.Name, m.Description)
	}
	b.WriteString("The user runs one with /workflow <name>. You can load the steps with get_workflow (e.g. when they " +
		"ask to run or tweak one), and create/update them with save_workflow.")
	return b.String()
}

// registerWorkflowTools lets the agent author and read workflows itself.
func (g *Gateway) registerWorkflowTools(reg *tools.Registry) {
	reg.Register(tools.Tool{
		Name: "save_workflow",
		Description: "Save (create or overwrite) a WORKFLOW: a reusable multi-step playbook the user can replay any time " +
			"with /workflow <name>. Write the content as clear markdown steps, self-contained enough to execute later " +
			"without this conversation (include paths, commands, what success looks like). Use this when the user " +
			"describes a repeatable procedure or asks you to remember a process.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "Short name (becomes <name>.md; lowercase, dashes)."},
				"content": map[string]any{"type": "string", "description": "The full markdown playbook: a title line, then the steps."},
			},
			"required": []string{"name", "content"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ Name, Content string }
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if err := g.wf.Save(a.Name, a.Content); err != nil {
				return "", err
			}
			return "saved workflow «" + a.Name + "» — the user can run it with /workflow " + a.Name, nil
		},
	})

	reg.Register(tools.Tool{
		Name:        "get_workflow",
		Description: "Load a saved workflow's full steps by name (the available ones are listed in your system prompt).",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string", "description": "The workflow's name."}},
			"required":   []string{"name"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ Name string }
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			body, ok := g.wf.Get(a.Name)
			if !ok {
				return "", fmt.Errorf("no workflow named %q", a.Name)
			}
			return body, nil
		},
	})

	reg.Register(tools.Tool{
		Name:        "delete_workflow",
		Description: "Delete a saved workflow by name (only when the user asks).",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string", "description": "The workflow's name."}},
			"required":   []string{"name"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ Name string }
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if g.wf.Delete(a.Name) {
				return "deleted workflow «" + a.Name + "»", nil
			}
			return "no workflow named «" + a.Name + "»", nil
		},
	})
}

// runWorkflowCommand handles /workflow for the Telegram side: no arg lists, an arg runs.
func (g *Gateway) runWorkflowCommand(ctx context.Context, chatID, arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		g.replyMarkdown(ctx, chatID, workflowsMessage(g.wf.List()))
		return
	}
	name, extra, _ := strings.Cut(arg, " ")
	body, ok := g.wf.Get(name)
	if !ok {
		g.replyMarkdown(ctx, chatID, "⚠️ "+mdV2("Unknown workflow: "+name)+"\n\n"+workflowsMessage(g.wf.List()))
		return
	}
	g.reply(ctx, chatID, "▶️ Running workflow «"+name+"»…")
	g.enqueue(ctx, chatID, agent.Input{Text: workflowKickoff(name, body, extra)})
}

// workflowsMessage (MarkdownV2) lists saved workflows for /workflows.
func workflowsMessage(list []workflows.Meta) string {
	if len(list) == 0 {
		return "▶️ *" + mdV2("Workflows") + "*\n\n" +
			mdV2("None saved yet. Describe a repeatable procedure to me (\"сохрани как workflow: …\") and I'll save it — then /workflow <name> replays it any time.")
	}
	var b strings.Builder
	b.WriteString("▶️ *" + mdV2("Workflows") + "*\n\n")
	for _, m := range list {
		b.WriteString("• *" + mdV2(m.Name) + "* — " + mdV2(m.Description) + "\n")
	}
	b.WriteString("\n" + mdV2("Run one with /workflow <name> (extra words after the name are passed in as input)."))
	return b.String()
}
