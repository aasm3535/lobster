package gateway

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aasm3535/lobster/internal/event"
)

// agentHub tracks the live state of subagents spawned by spawn_agents, so the UI can show
// "N agents working" with what each one is doing right now — a read-only window into the
// orchestration while the main agent blocks on the results. It's process-wide (subagents
// can be spawned from any chat) but each run records which chat owns it.
type agentHub struct {
	mu   sync.Mutex
	runs []*agentRun
	seq  int
}

// agentRun is one subagent's live record.
type agentRun struct {
	mu sync.Mutex

	ID      string
	ChatID  string
	Label   string
	Status  string // "running", "done", "failed"
	Started time.Time
	Ended   time.Time
	last    string   // freshest one-line activity (current tool, etc.)
	lines   []string // full timeline, capped
}

func newAgentHub() *agentHub { return &agentHub{} }

// start registers a new running subagent and returns its record.
func (h *agentHub) start(chatID, label string) *agentRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	r := &agentRun{
		ID:      fmt.Sprintf("a%d", h.seq),
		ChatID:  chatID,
		Label:   label,
		Status:  "running",
		Started: time.Now(),
		last:    "starting…",
	}
	h.runs = append(h.runs, r)
	// Keep the hub bounded: drop the oldest finished runs once it grows.
	if len(h.runs) > 64 {
		h.runs = h.runs[len(h.runs)-64:]
	}
	return r
}

// runningFor reports how many subagents are currently running for a chat.
func (h *agentHub) runningFor(chatID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.runs {
		if r.ChatID == chatID && r.statusIs("running") {
			n++
		}
	}
	return n
}

// activeFor returns the running subagents for a chat (snapshot), newest last.
func (h *agentHub) activeFor(chatID string) []*agentRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*agentRun
	for _, r := range h.runs {
		if r.ChatID == chatID && r.statusIs("running") {
			out = append(out, r)
		}
	}
	return out
}

// recentFor returns the last n runs for a chat (running or finished), newest first.
func (h *agentHub) recentFor(chatID string, n int) []*agentRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*agentRun
	for i := len(h.runs) - 1; i >= 0 && len(out) < n; i-- {
		if h.runs[i].ChatID == chatID {
			out = append(out, h.runs[i])
		}
	}
	return out
}

func (r *agentRun) statusIs(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Status == s
}

// log appends a timeline line and updates the freshest-activity summary.
func (r *agentRun) log(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = line
	r.lines = append(r.lines, line)
	if len(r.lines) > 200 {
		r.lines = r.lines[len(r.lines)-200:]
	}
}

func (r *agentRun) finish(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Status = status
	r.Ended = time.Now()
}

// elapsed is how long the run has taken (live if still running).
func (r *agentRun) elapsed() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Ended.IsZero() {
		return time.Since(r.Started)
	}
	return r.Ended.Sub(r.Started)
}

// view returns a stable snapshot of the fields the UI reads.
func (r *agentRun) view() (id, label, status, last string, el time.Duration, lines []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	el = time.Since(r.Started)
	if !r.Ended.IsZero() {
		el = r.Ended.Sub(r.Started)
	}
	return r.ID, r.Label, r.Status, r.last, el, append([]string(nil), r.lines...)
}

// agentCard is a UI-facing snapshot of one subagent (no locks held by the caller).
type agentCard struct {
	ID, Label, Status, Last string
	Elapsed                 time.Duration
	Lines                   []string
}

// cardsFor returns up to n recent subagents for a chat as snapshots, newest first —
// what the TUI's interactive agents view renders.
func (h *agentHub) cardsFor(chatID string, n int) []agentCard {
	runs := h.recentFor(chatID, n)
	cards := make([]agentCard, 0, len(runs))
	for _, r := range runs {
		id, label, status, last, el, lines := r.view()
		cards = append(cards, agentCard{ID: id, Label: label, Status: status, Last: last, Elapsed: el, Lines: lines})
	}
	return cards
}

// panelLines renders the compact "agents working" plashka for the TUI: a header with the
// live count, then one line per running subagent showing its label, elapsed time and
// freshest activity. Empty when nothing is running.
func (h *agentHub) panelLines(chatID string) []string {
	active := h.activeFor(chatID)
	if len(active) == 0 {
		return nil
	}
	head := fmt.Sprintf("🤖 %d агент(а/ов) работают — ждём… · /agents подробнее", len(active))
	out := []string{tcol(colTool, head)}
	for _, r := range active {
		_, label, _, last, el, _ := r.view()
		out = append(out, tdim(fmt.Sprintf("   ▸ %s · %s · %s", label, fmtDur(el), oneLine(last, 48))))
	}
	return out
}

// detailLines renders a full read-only view of recent subagents for /agents: each one's
// label, status, elapsed time and recent timeline. tail caps how many timeline lines per
// agent are shown.
func (h *agentHub) detailLines(chatID string, tail int) []string {
	runs := h.recentFor(chatID, 8)
	if len(runs) == 0 {
		return []string{tdim("  пока не запускал саб-агентов (spawn_agents)")}
	}
	var out []string
	for _, r := range runs {
		id, label, status, _, el, lines := r.view()
		icon := "⠿"
		col := colTool
		switch status {
		case "done":
			icon, col = "✓", 78
		case "failed":
			icon, col = "✗", colErr
		}
		out = append(out, tcol(col, fmt.Sprintf("  %s %s [%s] · %s · %s", icon, id, status, label, fmtDur(el))))
		start := 0
		if len(lines) > tail {
			start = len(lines) - tail
		}
		for _, l := range lines[start:] {
			out = append(out, tdim("      "+l))
		}
	}
	return out
}

// plainDetail is the colour-free /agents report for Telegram.
func (h *agentHub) plainDetail(chatID string) string {
	runs := h.recentFor(chatID, 8)
	if len(runs) == 0 {
		return "🤖 Пока не запускал саб-агентов. Я делаю это сам через spawn_agents, когда задачу можно распараллелить."
	}
	var b strings.Builder
	b.WriteString("🤖 Саб-агенты:\n")
	for _, r := range runs {
		id, label, status, last, el, _ := r.view()
		icon := "⏳"
		switch status {
		case "done":
			icon = "✅"
		case "failed":
			icon = "❌"
		}
		fmt.Fprintf(&b, "%s %s [%s] %s · %s\n      %s\n", icon, id, status, label, fmtDur(el), oneLine(last, 60))
	}
	return strings.TrimRight(b.String(), "\n")
}

// hubSink records a subagent's event stream into its agentRun (live timeline) and also
// captures the final reply/error so spawn_agents can return the report. It's the bridge
// between the agent's event.Sink and the orchestration UI.
type hubSink struct {
	run   *agentRun
	reply string
	fail  string
	mu    sync.Mutex
}

func (s *hubSink) Emit(ev event.Event) {
	switch ev.Kind {
	case event.KindToolCall:
		line := "● " + ev.Tool
		if p := argPreview(ev.Args); p != "" {
			line += "  " + p
		}
		s.run.log(line)
	case event.KindToolResult:
		// keep the timeline readable: only note slow/!empty results briefly
		if ev.Text != "" {
			s.run.log("  ↳ " + oneLine(ev.Text, 60))
		}
	case event.KindReply:
		s.mu.Lock()
		s.reply = ev.Text
		s.mu.Unlock()
		s.run.log("✓ done")
	case event.KindError:
		s.mu.Lock()
		s.fail = ev.Text
		s.mu.Unlock()
		s.run.log("✗ " + oneLine(ev.Text, 60))
	}
}

func (s *hubSink) result() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reply, s.fail
}
