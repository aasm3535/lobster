package gateway

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aasm3535/lobster/internal/event"
)

// agentHub tracks the live state of subagents spawned by spawn_agents, so the UI can show
// the running agents as little plashki under the input — each with its task — and let you
// open one to watch what it does, like a plain chat. It's process-wide but each run records
// which chat owns it.
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
	Task    string // the prompt/goal the subagent was given (shown on its plashka)
	Status  string // "running", "done", "failed"
	Started time.Time
	Ended   time.Time
	last    string   // freshest one-line activity
	lines   []string // full timeline, capped (clean, no emoji)
	reply   string   // the subagent's final report, shown at the end of its chat
}

func newAgentHub() *agentHub { return &agentHub{} }

// start registers a new running subagent and returns its record.
func (h *agentHub) start(chatID, label, task string) *agentRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	r := &agentRun{
		ID:      fmt.Sprintf("a%d", h.seq),
		ChatID:  chatID,
		Label:   label,
		Task:    task,
		Status:  "running",
		Started: time.Now(),
		last:    "starting",
	}
	h.runs = append(h.runs, r)
	if len(h.runs) > 64 { // keep the hub bounded
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

func (r *agentRun) setReply(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reply = s
}

func (r *agentRun) finish(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Status = status
	r.Ended = time.Now()
}

// agentCard is a UI-facing snapshot of one subagent (no locks held by the caller).
type agentCard struct {
	ID, Label, Task, Status, Last, Reply string
	Elapsed                              time.Duration
	Lines                                []string
}

func (r *agentRun) card() agentCard {
	r.mu.Lock()
	defer r.mu.Unlock()
	el := time.Since(r.Started)
	if !r.Ended.IsZero() {
		el = r.Ended.Sub(r.Started)
	}
	return agentCard{
		ID: r.ID, Label: r.Label, Task: r.Task, Status: r.Status,
		Last: r.last, Reply: r.reply, Elapsed: el,
		Lines: append([]string(nil), r.lines...),
	}
}

// cardsFor returns up to n recent subagents for a chat as snapshots, newest first.
func (h *agentHub) cardsFor(chatID string, n int) []agentCard {
	runs := h.recentFor(chatID, n)
	cards := make([]agentCard, 0, len(runs))
	for _, r := range runs {
		cards = append(cards, r.card())
	}
	return cards
}

// visibleCards returns the subagents worth showing in the live UI: all running ones plus any
// that finished within ttl. Completed agents fade out after ttl so they don't pile up.
func (h *agentHub) visibleCards(chatID string, max int, ttl time.Duration) []agentCard {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []agentCard
	for i := len(h.runs) - 1; i >= 0 && len(out) < max; i-- {
		r := h.runs[i]
		if r.ChatID != chatID {
			continue
		}
		r.mu.Lock()
		hide := r.Status != "running" && !r.Ended.IsZero() && time.Since(r.Ended) > ttl
		r.mu.Unlock()
		if hide {
			continue
		}
		out = append(out, r.card())
	}
	return out
}

// plainDetail is the colour-free /agents report for Telegram.
func (h *agentHub) plainDetail(chatID string) string {
	cards := h.cardsFor(chatID, 8)
	if len(cards) == 0 {
		return "No subagents yet. I spawn them (spawn_agents) when a job parallelizes."
	}
	var b strings.Builder
	b.WriteString("Subagents:\n")
	for _, c := range cards {
		fmt.Fprintf(&b, "- [%s] %s · %s\n    %s\n", c.Status, c.Label, fmtDur(c.Elapsed), oneLine(c.Last, 60))
	}
	return strings.TrimRight(b.String(), "\n")
}

// detailLines renders a colour-free read-only view of recent subagents (REPL /agents).
func (h *agentHub) detailLines(chatID string, tail int) []string {
	cards := h.cardsFor(chatID, 8)
	if len(cards) == 0 {
		return []string{tdim("  no subagents yet (spawn_agents)")}
	}
	var out []string
	for _, c := range cards {
		out = append(out, fmt.Sprintf("  [%s] %s · %s", c.Status, c.Label, fmtDur(c.Elapsed)))
		start := 0
		if len(c.Lines) > tail {
			start = len(c.Lines) - tail
		}
		for _, l := range c.Lines[start:] {
			out = append(out, tdim("      "+l))
		}
	}
	return out
}

// hubSink records a subagent's event stream into its agentRun as clean, chat-like lines
// (no emoji) and captures the final report. It bridges the agent's event.Sink to the UI.
type hubSink struct {
	run   *agentRun
	reply string
	fail  string
	mu    sync.Mutex
}

func (s *hubSink) Emit(ev event.Event) {
	switch ev.Kind {
	case event.KindToolCall:
		line := ev.Tool
		if p := argPreview(ev.Args); p != "" {
			line += "  " + p
		}
		s.run.log(line)
	case event.KindToolResult:
		if ev.Text != "" {
			s.run.log("   " + oneLine(ev.Text, 70))
		}
	case event.KindReply:
		s.mu.Lock()
		s.reply = ev.Text
		s.mu.Unlock()
		s.run.setReply(ev.Text)
	case event.KindError:
		s.mu.Lock()
		s.fail = ev.Text
		s.mu.Unlock()
		s.run.log("error: " + oneLine(ev.Text, 70))
	}
}

func (s *hubSink) result() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reply, s.fail
}
