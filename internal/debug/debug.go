// Package debug is an opt-in diagnostic log. Set LOBSTER_DEBUG=1 to write a timestamped
// trace to ~/.lobster/debug.log (raw model responses, the reason-act step trace, etc.) so
// behaviour like "the agent ended a turn with no text" can be diagnosed from real data
// instead of guessed at. It's a no-op when the env var is unset, so there's zero overhead
// in normal use.
package debug

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	mu      sync.Mutex
	enabled bool
	w       *os.File
)

func init() {
	if os.Getenv("LOBSTER_DEBUG") == "" {
		return
	}
	enabled = true
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".lobster")
		_ = os.MkdirAll(dir, 0o700)
		if f, e := os.OpenFile(filepath.Join(dir, "debug.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); e == nil {
			w = f
		}
	}
}

// Enabled reports whether debug logging is on.
func Enabled() bool { return enabled }

// Logf writes one timestamped line to the debug log (or stderr if the file can't open).
func Logf(format string, a ...any) {
	if !enabled {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	line := time.Now().Format("15:04:05.000") + " " + fmt.Sprintf(format, a...) + "\n"
	if w != nil {
		_, _ = w.WriteString(line)
		return
	}
	fmt.Fprint(os.Stderr, "[debug] "+line)
}

// Clip shortens s for logging (responses can be large).
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[+" + fmt.Sprint(len(s)-n) + "B]"
}
