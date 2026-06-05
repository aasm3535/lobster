package gateway

import (
	"crypto/subtle"
	"strings"
	"sync"
)

// authGate decides whether a chat is allowed to talk to the bot. It is the lock on
// the front door: the shell tool means anyone who can message the bot can run code
// on the host, so the bot is LOCKED by default.
//
//   - allowAll (config "open": true) -> everyone is admitted (local dev only).
//   - otherwise (default)            -> a chat is admitted only if it is whitelisted
//     (the owner adds the chat ID, learned via /start, to the config) or it sends the
//     optional shared access code once (then that chat is remembered).
type authGate struct {
	allowAll bool
	code     string

	mu      sync.Mutex
	allowed map[string]bool
}

func newAuthGate(open bool, code string, preAllowed []string) *authGate {
	g := &authGate{allowAll: open, code: strings.TrimSpace(code), allowed: map[string]bool{}}
	for _, id := range preAllowed {
		if id = strings.TrimSpace(id); id != "" {
			g.allowed[id] = true
		}
	}
	return g
}

// open reports whether auth is disabled (everyone admitted). Set once at construction.
func (g *authGate) open() bool { return g.allowAll }

// admit classifies one inbound message:
//   - authorized: this chat may proceed and its text should reach the agent.
//   - justGranted: this message WAS the access code; the chat is now unlocked but
//     the code itself must NOT be forwarded to the agent (send a confirmation instead).
//
// A denied chat returns (false, false).
func (g *authGate) admit(chatID, text string) (authorized, justGranted bool) {
	if g.allowAll {
		return true, false
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.allowed[chatID] {
		return true, false
	}
	if g.code != "" && sameSecret(strings.TrimSpace(text), g.code) {
		g.allowed[chatID] = true
		return true, true
	}
	return false, false
}

// sameSecret compares in constant time so a guesser can't learn the code by timing.
func sameSecret(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
