package gateway

import "testing"

func TestAuthGate_OpenFlagAdmitsEveryone(t *testing.T) {
	g := newAuthGate(true, "", nil)
	if !g.open() {
		t.Fatal("expected open mode when auth.open is true")
	}
	if ok, granted := g.admit("anyone", "hello"); !ok || granted {
		t.Fatalf("open gate should admit everyone without granting; got ok=%v granted=%v", ok, granted)
	}
}

func TestAuthGate_LockedByDefault(t *testing.T) {
	g := newAuthGate(false, "", nil)
	if g.open() {
		t.Fatal("expected locked mode when nothing is configured")
	}
	// Nobody gets in until they are whitelisted — not even via /start.
	if ok, granted := g.admit("u1", "/start"); ok || granted {
		t.Fatalf("a stranger must be denied until whitelisted; got ok=%v granted=%v", ok, granted)
	}
}

func TestAuthGate_PreAllowedSkipCode(t *testing.T) {
	g := newAuthGate(false, "", []string{"boss"})
	if g.open() {
		t.Fatal("an allowlist must still count as locked")
	}
	if ok, granted := g.admit("boss", "hi"); !ok || granted {
		t.Fatalf("pre-allowed chat should pass without granting; got ok=%v granted=%v", ok, granted)
	}
	if ok, _ := g.admit("intruder", "hi"); ok {
		t.Fatal("a non-whitelisted chat must be denied")
	}
}

func TestAuthGate_CodeUnlocksAndRemembers(t *testing.T) {
	g := newAuthGate(false, "hunter2", nil)

	// Wrong / missing code is denied and must not reach the agent.
	if ok, granted := g.admit("u1", "let me in"); ok || granted {
		t.Fatalf("stranger should be denied; got ok=%v granted=%v", ok, granted)
	}

	// Exact code unlocks, but the code message itself is not forwarded (justGranted).
	if ok, granted := g.admit("u1", "  hunter2 "); !ok || !granted {
		t.Fatalf("correct code should grant access; got ok=%v granted=%v", ok, granted)
	}

	// Subsequent messages from that chat flow through normally.
	if ok, granted := g.admit("u1", "now do something"); !ok || granted {
		t.Fatalf("authorized chat should pass through; got ok=%v granted=%v", ok, granted)
	}

	// A different chat is still locked out.
	if ok, _ := g.admit("u2", "now do something"); ok {
		t.Fatal("a different chat must not inherit authorization")
	}
}

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		"/start":     "start",
		"/start@bot": "start",
		"/id now":    "id",
		"/RESET":     "reset",
	}
	for in, want := range cases {
		if got, ok := commandName(in); !ok || got != want {
			t.Errorf("commandName(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	for _, s := range []string{"start", "hello /start", "do /reset please"} {
		if got, ok := commandName(s); ok {
			t.Errorf("commandName(%q) = (%q, true), want not-a-command", s, got)
		}
	}
}
