package gateway

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/aasm3535/lobster/internal/memory"
)

func goalTestGateway(t *testing.T) *Gateway {
	t.Helper()
	mem, err := memory.Open(filepath.Join(t.TempDir(), "mem.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Gateway{mem: mem, goalRuns: map[string]int{}}
}

func TestGoalContinuation(t *testing.T) {
	g := goalTestGateway(t)
	const chat = "c1"

	// No goal — no continuation.
	if _, ok := g.goalContinuation(chat, "done something"); ok {
		t.Fatal("continuation without a goal")
	}

	g.setGoal(chat, "ship the feature")
	prompt, ok := g.goalContinuation(chat, "step 1 done")
	if !ok || !strings.Contains(prompt, "ship the feature") {
		t.Fatalf("expected continuation with the goal text, got ok=%v %q", ok, prompt)
	}

	// A ❓ reply pauses the loop (agent is asking the user).
	if _, ok := g.goalContinuation(chat, "❓ какой пароль от сервера?"); ok {
		t.Fatal("❓ reply must pause auto-continue")
	}

	// goal_done path: clearing stops it.
	g.clearGoal(chat)
	if _, ok := g.goalContinuation(chat, "anything"); ok {
		t.Fatal("continuation after clear")
	}
}

func TestGoalAutoRunCap(t *testing.T) {
	g := goalTestGateway(t)
	const chat = "c2"
	g.setGoal(chat, "big goal")

	for i := 0; i < goalMaxAutoRuns; i++ {
		if _, ok := g.goalContinuation(chat, "progress"); !ok {
			t.Fatalf("continuation %d unexpectedly stopped", i+1)
		}
	}
	if _, ok := g.goalContinuation(chat, "progress"); ok {
		t.Fatal("cap not enforced")
	}

	// A real user message re-arms the loop.
	g.resetGoalRuns(chat)
	if _, ok := g.goalContinuation(chat, "progress"); !ok {
		t.Fatal("reset did not re-arm auto-continue")
	}
}

func TestGoalPromptBlock(t *testing.T) {
	g := goalTestGateway(t)
	const chat = "c3"
	if g.goalPromptBlock(chat) != "" {
		t.Fatal("prompt block without a goal")
	}
	g.setGoal(chat, "fix all the tests")
	if b := g.goalPromptBlock(chat); !strings.Contains(b, "fix all the tests") {
		t.Fatalf("prompt block missing goal: %q", b)
	}
}
