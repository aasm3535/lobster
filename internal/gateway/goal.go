package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aasm3535/lobster/internal/agent"
	"github.com/aasm3535/lobster/internal/event"
	"github.com/aasm3535/lobster/internal/tools"
)

// Goal mode: /goal <что сделать> pins a persistent objective and the agent keeps working
// on it across turns — every time it finishes a reply without marking the goal done, a
// continuation is fed back in automatically (capped, so it can't spin forever). The agent
// ends the loop itself with goal_done, or pauses it by genuinely asking the user (a reply
// starting with ❓). The goal lives in the per-chat memory prefs, so it survives restarts.

// goalMaxAutoRuns caps consecutive auto-continues; a real user message resets the count.
const goalMaxAutoRuns = 20

// activeGoal returns the chat's pinned goal text ("" when none).
func (g *Gateway) activeGoal(chatID string) string {
	return strings.TrimSpace(g.mem.Pref(chatID, "goal"))
}

func (g *Gateway) setGoal(chatID, text string) {
	_ = g.mem.SetPref(chatID, "goal", strings.TrimSpace(text))
	g.resetGoalRuns(chatID)
}

func (g *Gateway) clearGoal(chatID string) {
	_ = g.mem.SetPref(chatID, "goal", "")
	g.resetGoalRuns(chatID)
}

func (g *Gateway) resetGoalRuns(chatID string) {
	g.goalMu.Lock()
	g.goalRuns[chatID] = 0
	g.goalMu.Unlock()
}

// goalContinuation decides whether a finished reply should auto-continue the goal, and
// returns the next input if so. It pauses when the agent is asking the user something
// (reply starts with ❓) and stops at the auto-run cap until the user speaks again.
func (g *Gateway) goalContinuation(chatID, reply string) (string, bool) {
	goal := g.activeGoal(chatID)
	if goal == "" {
		return "", false
	}
	if strings.HasPrefix(strings.TrimSpace(reply), "❓") {
		return "", false // blocked on the user — wait for their answer
	}
	g.goalMu.Lock()
	g.goalRuns[chatID]++
	n := g.goalRuns[chatID]
	g.goalMu.Unlock()
	if n > goalMaxAutoRuns {
		return "", false
	}
	return fmt.Sprintf("(⛳ Goal mode auto-continue %d/%d — no human sent this. The active goal is NOT marked done yet:\n«%s»\n"+
		"Continue working toward it RIGHT NOW: pick the next concrete step(s) and do them. When the goal is fully achieved "+
		"and verified, call goal_done with a short summary. If you truly cannot proceed without the user, reply with a "+
		"message starting with ❓ — that pauses the auto-continue until they answer.)", n, goalMaxAutoRuns, goal), true
}

// goalKickoff frames a freshly set goal as the agent's next input.
func goalKickoff(goal string) string {
	return "(⛳ The user pinned a GOAL — work toward it persistently until it is truly achieved:\n«" + goal + "»\n" +
		"Plan briefly, then execute step by step with your tools. After each of your replies you will be auto-prompted to " +
		"continue until you call goal_done. Decompose big work — spawn_agents can parallelize independent parts. " +
		"If you're blocked on the user, start your reply with ❓.)"
}

// goalPromptBlock is appended to the system prompt while a goal is active.
func (g *Gateway) goalPromptBlock(chatID string) string {
	goal := g.activeGoal(chatID)
	if goal == "" {
		return ""
	}
	return "\n\n# ⛳ ACTIVE GOAL (goal mode)\n«" + goal + "»\n" +
		"Keep working toward this until it's truly done, then call goal_done. Don't stop halfway; don't re-ask things you " +
		"can decide. If genuinely blocked on the user, start your reply with ❓."
}

// registerGoalTools adds goal_done / goal_set so the agent can end (or start) the loop.
func (g *Gateway) registerGoalTools(reg *tools.Registry, chatID string) {
	reg.Register(tools.Tool{
		Name: "goal_done",
		Description: "Mark the ACTIVE GOAL as fully achieved and stop the goal-mode auto-continue loop. Call this ONLY " +
			"when the goal is really done and verified — include a one-line summary of the outcome.",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"summary": map[string]any{"type": "string", "description": "One line: what was achieved."}},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ Summary string }
			_ = json.Unmarshal(args, &a)
			if g.activeGoal(chatID) == "" {
				return "no goal was active", nil
			}
			g.clearGoal(chatID)
			out := "✅ goal marked done and cleared"
			if strings.TrimSpace(a.Summary) != "" {
				out += ": " + a.Summary
			}
			return out, nil
		},
	})

	reg.Register(tools.Tool{
		Name: "goal_set",
		Description: "Pin a persistent GOAL for yourself in this chat (goal mode): after every reply you'll be auto-" +
			"prompted to keep working until you call goal_done. Use it when the user gives you a big objective to carry " +
			"through (\"сделай X до конца\"), or to replace the current goal.",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"goal": map[string]any{"type": "string", "description": "The objective, self-contained."}},
			"required":   []string{"goal"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ Goal string }
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Goal) == "" {
				return "", fmt.Errorf("goal is empty")
			}
			g.setGoal(chatID, a.Goal)
			return "⛳ goal pinned: " + strings.TrimSpace(a.Goal) + " (auto-continue is on; finish with goal_done)", nil
		},
	})
}

// goalSink wraps a chat's sink: when a turn ends with an active, unfinished goal, it
// feeds the continuation back in (off the agent goroutine, slightly delayed so the
// turn fully unwinds first).
type goalSink struct {
	event.Sink
	g        *Gateway
	chatID   string
	resubmit func(text string)
}

func (s *goalSink) Emit(ev event.Event) {
	s.Sink.Emit(ev)
	if ev.Kind != event.KindReply {
		return
	}
	if text, ok := s.g.goalContinuation(s.chatID, ev.Text); ok {
		go func() {
			time.Sleep(150 * time.Millisecond)
			s.resubmit(text)
		}()
	}
}

// goalCommand handles /goal for the Telegram side: show, clear, or set + kick off.
func (g *Gateway) goalCommand(ctx context.Context, chatID, arg string) {
	arg = strings.TrimSpace(arg)
	low := strings.ToLower(arg)
	switch {
	case arg == "":
		if goal := g.activeGoal(chatID); goal != "" {
			g.replyMarkdown(ctx, chatID, "⛳ *"+mdV2("Active goal")+"*\n\n"+mdV2(goal)+"\n\n"+mdV2("Clear it with /goal clear"))
		} else {
			g.replyMarkdown(ctx, chatID, mdV2("No active goal. Set one with /goal <what to achieve> — I'll keep working on it until it's done."))
		}
	case low == "clear" || low == "done" || low == "stop" || low == "стоп" || low == "отмена":
		g.clearGoal(chatID)
		g.reply(ctx, chatID, "⛳ Goal cleared.")
	default:
		g.setGoal(chatID, arg)
		g.enqueue(ctx, chatID, agent.Input{Text: goalKickoff(arg)})
	}
}
