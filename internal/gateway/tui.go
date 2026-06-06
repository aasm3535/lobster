package gateway

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aasm3535/lobster/internal/agent"
)

// This file implements `lobster tui`'s full-screen interface: the LOBSTER banner pinned at
// the top, the conversation scrolling in the middle, and a bottom input box (a growing,
// wrapping TextArea). It's built on raw-mode keyboard input + ANSI redraws — no external
// TUI library, to keep Lobster a single dependency-free binary. enableRawInput / terminalSize
// are platform-specific (tui_plat_*.go); everything here is platform-neutral.

// --- model -------------------------------------------------------------------

// tui owns all on-screen state. Mutators (appendLine, setWorking, input edits…) lock the
// mutex and flag the render goroutine via markDirty; a single render goroutine owns stdout so
// agent output and the live spinner never interleave mid-line.
type tui struct {
	mu sync.Mutex

	header   []string                // fixed banner block (coral art + tagline + model line)
	headerFn func(cols int) []string // re-renders the header (it's centered, so width matters)
	compact  string                  // one-line header used when the window is too short for the banner
	model    string                  // active model name, shown in the compact header

	lines     []string  // chat transcript, as logical lines (may contain ANSI); wrapped at draw
	input     []rune    // current input buffer (the TextArea contents)
	cursor    int       // caret position, a rune index into input
	scroll    int       // how many display rows we're scrolled up from the bottom (0 = follow)
	working   string    // spinner label while the agent thinks / a tool runs; "" when idle
	workStart time.Time // when the current working stretch began (for the elapsed counter)
	frame     int       // spinner animation frame
	task      string    // short summary of the current job, shown in the window title while busy

	lastTitle string // last OSC title emitted, to skip redundant writes

	// Interactive agents view: a navigable list of subagents under the input. cardsFn
	// supplies the live snapshot; agentsView toggles the mode, agentSel is the highlighted
	// agent, agentOpen shows that one's full timeline.
	cardsFn    func() []agentCard
	agentsView bool
	agentSel   int
	agentOpen  bool

	rows, cols int
	out        *bufio.Writer
	dirty      chan struct{}
}

func newTUI() *tui {
	return &tui{
		rows:  24,
		cols:  80,
		out:   bufio.NewWriter(os.Stdout),
		dirty: make(chan struct{}, 1),
	}
}

func (u *tui) markDirty() {
	select {
	case u.dirty <- struct{}{}:
	default: // a redraw is already pending; coalesce
	}
}

// appendLine adds one finished transcript line and snaps back to the bottom so new output is
// always visible. Multi-line strings are split so wrapping/scrolling stay correct.
func (u *tui) appendLine(s string) {
	u.mu.Lock()
	for _, ln := range strings.Split(s, "\n") {
		u.lines = append(u.lines, ln)
	}
	if len(u.lines) > 4000 { // keep memory bounded on very long sessions
		u.lines = u.lines[len(u.lines)-4000:]
	}
	u.scroll = 0
	u.mu.Unlock()
	u.markDirty()
}

// appendUser echoes the user's submitted message into the transcript (raw mode has no
// terminal echo, so we draw it ourselves), separated from the previous turn by a blank line.
func (u *tui) appendUser(text string) {
	u.mu.Lock()
	if len(u.lines) > 0 {
		u.lines = append(u.lines, "")
	}
	for i, ln := range strings.Split(text, "\n") {
		if i == 0 {
			u.lines = append(u.lines, "  "+tcol(colPrompt, "#  ")+ln)
		} else {
			u.lines = append(u.lines, "     "+ln)
		}
	}
	u.scroll = 0
	u.mu.Unlock()
	u.markDirty()
}

func (u *tui) clearLines() {
	u.mu.Lock()
	u.lines = nil
	u.scroll = 0
	u.mu.Unlock()
	u.markDirty()
}

func (u *tui) setWorking(label string) {
	u.mu.Lock()
	if label != "" && u.working == "" {
		u.workStart = time.Now() // a fresh working stretch starts now
	}
	u.working = label
	u.mu.Unlock()
	u.markDirty()
}

// --- interactive agents view -------------------------------------------------

// openAgents enters the navigable subagents view (dots under the input, ←→ to select,
// ⏎ to open one, esc to leave).
func (u *tui) openAgents() {
	u.mu.Lock()
	u.agentsView = true
	u.agentOpen = false
	u.agentSel = 0
	u.scroll = 0
	u.mu.Unlock()
	u.markDirty()
}

func (u *tui) agentCount() int {
	if u.cardsFn == nil {
		return 0
	}
	return len(u.cardsFn())
}

// agentsNav moves the selection by d, wrapping around the list.
func (u *tui) agentsNav(d int) {
	n := u.agentCount()
	if n == 0 {
		return
	}
	u.mu.Lock()
	u.agentSel = ((u.agentSel+d)%n + n) % n
	u.scroll = 0
	u.mu.Unlock()
	u.markDirty()
}

// agentsEnter opens the selected agent's full timeline.
func (u *tui) agentsEnter() {
	if u.agentCount() == 0 {
		return
	}
	u.mu.Lock()
	u.agentOpen = true
	u.scroll = 0
	u.mu.Unlock()
	u.markDirty()
}

// agentsBack steps out one level: detail → list, list → exit the agents view.
// Returns true if it stayed inside the agents view.
func (u *tui) agentsBack() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	defer u.markDirty()
	if u.agentOpen {
		u.agentOpen = false
		u.scroll = 0
		return true
	}
	u.agentsView = false
	return false
}

func (u *tui) inAgentsView() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.agentsView
}

// setTask records what the agent is currently doing — it becomes the terminal window
// title while the agent works (like Claude Code's live title).
func (u *tui) setTask(s string) {
	u.mu.Lock()
	u.task = s
	u.mu.Unlock()
	u.markDirty()
}

func (u *tui) setModel(name string) {
	u.mu.Lock()
	u.model = name
	if u.headerFn != nil {
		u.header = u.headerFn(u.cols) // the banner shows the model name — refresh it
	}
	u.mu.Unlock()
	u.markDirty()
}

// refreshHeader re-renders the pinned banner (e.g. after MCP connects, so its counts update).
func (u *tui) refreshHeader() {
	u.mu.Lock()
	if u.headerFn != nil {
		u.header = u.headerFn(u.cols)
	}
	u.mu.Unlock()
	u.markDirty()
}

// --- input editing -----------------------------------------------------------

func (u *tui) insertRune(r rune) {
	u.mu.Lock()
	u.input = append(u.input, 0)
	copy(u.input[u.cursor+1:], u.input[u.cursor:])
	u.input[u.cursor] = r
	u.cursor++
	u.mu.Unlock()
	u.markDirty()
}

func (u *tui) backspace() {
	u.mu.Lock()
	if u.cursor > 0 {
		u.input = append(u.input[:u.cursor-1], u.input[u.cursor:]...)
		u.cursor--
	}
	u.mu.Unlock()
	u.markDirty()
}

func (u *tui) deleteFwd() {
	u.mu.Lock()
	if u.cursor < len(u.input) {
		u.input = append(u.input[:u.cursor], u.input[u.cursor+1:]...)
	}
	u.mu.Unlock()
	u.markDirty()
}

func (u *tui) moveCursor(d int) {
	u.mu.Lock()
	u.cursor += d
	if u.cursor < 0 {
		u.cursor = 0
	}
	if u.cursor > len(u.input) {
		u.cursor = len(u.input)
	}
	u.mu.Unlock()
	u.markDirty()
}

func (u *tui) cursorHome() { u.mu.Lock(); u.cursor = 0; u.mu.Unlock(); u.markDirty() }
func (u *tui) cursorEnd()  { u.mu.Lock(); u.cursor = len(u.input); u.mu.Unlock(); u.markDirty() }

// inputLen returns the current input length under the lock (the key loop must not read
// u.input directly — the render goroutine shares it).
func (u *tui) inputLen() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.input)
}

func (u *tui) killLine() {
	u.mu.Lock()
	u.input = u.input[:0]
	u.cursor = 0
	u.mu.Unlock()
	u.markDirty()
}

func (u *tui) scrollBy(n int) {
	u.mu.Lock()
	u.scroll += n
	if u.scroll < 0 {
		u.scroll = 0
	}
	u.mu.Unlock()
	u.markDirty()
}

// takeInput returns the current input text and clears the box.
func (u *tui) takeInput() string {
	u.mu.Lock()
	s := string(u.input)
	u.input = u.input[:0]
	u.cursor = 0
	u.mu.Unlock()
	u.markDirty()
	return s
}

// --- rendering ---------------------------------------------------------------

var tuiSpin = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (u *tui) renderLoop(stop chan struct{}) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-u.dirty:
			u.render()
		case <-t.C:
			u.mu.Lock()
			tick := u.working != "" || u.agentsView // both animate / update live
			if tick {
				u.frame++
			}
			u.mu.Unlock()
			if tick {
				u.render()
			}
		}
	}
}

func (u *tui) render() {
	u.mu.Lock()
	rows, cols := u.rows, u.cols
	if cols < 24 {
		cols = 24
	}
	if rows < 8 {
		rows = 8
	}
	header := u.header
	lines := u.lines
	input := append([]rune(nil), u.input...)
	cursor := u.cursor
	working := u.working
	workStart := u.workStart
	frame := u.frame
	scroll := u.scroll
	task := u.task
	agentsView := u.agentsView
	agentOpen := u.agentOpen
	agentSel := u.agentSel

	// Window title mirrors what the agent is doing (OSC 0), Claude Code-style.
	title := "🦞 LOBSTER"
	switch {
	case working != "" && task != "":
		title = "🦞 " + task
	case working != "":
		title = "🦞 " + working
	}
	titleSeq := ""
	if title != u.lastTitle {
		u.lastTitle = title
		titleSeq = "\x1b]0;" + title + "\x07"
	}
	compactPlain := "🦞 LOBSTER  ·  " + u.model + "  ·  /help · /exit"
	compact := centerPad(cols, len([]rune(compactPlain))+1) + // +1: the emoji is two cells wide
		tcol(colHead, "🦞 LOBSTER") + tdim("  ·  "+u.model+"  ·  /help · /exit")
	u.mu.Unlock()

	// Footer: the input box framed by two thin rules (top and bottom), then a hint line.
	inRows, caretLine, caretCol := layoutInput(input, cursor, cols)
	hint := tdim("  ⏎ send · ←→ edit · ↑↓ scroll · /help · /exit")
	if working != "" {
		// Mid-turn the input box stays live: Enter steers the agent instead of queueing.
		hint = tdim("  ⏎ подправить на лету · esc clear · ↑↓ scroll · /exit")
	}

	// In the agents view the footer becomes the dots strip + its controls, and the input
	// box is hidden (you're navigating, not typing).
	var cards []agentCard
	if agentsView && u.cardsFn != nil {
		cards = u.cardsFn()
		if agentSel >= len(cards) {
			agentSel = 0
		}
		inRows = []string{agentDots(cards, agentSel)}
		caretLine, caretCol = 0, 0
		if agentOpen {
			hint = tdim("  ↑↓ scroll · esc back · /exit")
		} else {
			hint = tdim("  ←→ select · ⏎ open · esc back")
		}
	}

	// Passive dots strip under the input whenever subagents have run (not in the agents
	// view, which shows its own dots).
	strip := ""
	if !agentsView && u.cardsFn != nil {
		strip = agentStrip(u.cardsFn())
	}

	rule := tcol(colRule, "  "+strings.Repeat("─", cols-4))
	footerH := 1 + len(inRows) + 1 + 1
	if strip != "" {
		footerH++
	}

	head := header
	chatH := rows - len(head) - footerH
	if chatH < 3 { // window too short for the full banner — collapse to one line
		head = []string{compact}
		chatH = rows - len(head) - footerH
	}
	if chatH < 1 {
		chatH = 1
	}

	// Agents view replaces the transcript with either the agent list or one agent's
	// timeline. Anchored to the top; ↑↓ scrolls the detail.
	if agentsView {
		var av []string
		for _, ln := range agentViewLines(cards, agentSel, agentOpen, cols) {
			av = append(av, wrapLine(ln, cols)...)
		}
		total := len(av)
		first := 0
		if agentOpen && total > chatH { // detail can scroll
			first = total - chatH - scroll
			if first < 0 {
				first = 0
			}
			if first > total-chatH {
				first = total - chatH
			}
		}
		end := first + chatH
		if end > total {
			end = total
		}
		view := av[first:end]
		var b strings.Builder
		b.WriteString(titleSeq)
		b.WriteString("\x1b[?25l\x1b[H")
		row := 1
		put := func(s string) {
			fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[K", row, s)
			row++
		}
		for _, h := range head {
			put(h)
		}
		for i := 0; i < chatH; i++ {
			if i < len(view) {
				put(view[i])
			} else {
				put("")
			}
		}
		put(rule)
		for _, ir := range inRows {
			put(ir)
		}
		put(rule)
		put(hint)
		for row <= rows {
			put("")
		}
		u.out.WriteString(b.String())
		u.out.Flush()
		return
	}

	// Flatten the transcript into wrapped display rows, then the live spinner as a transient
	// last row, and window onto the bottom (newest) portion, honouring any manual scroll.
	var disp []string
	for _, ln := range lines {
		disp = append(disp, wrapLine(ln, cols)...)
	}
	if working != "" {
		// The working state is one tidy grey "plashka": spinner + shimmering label +
		// elapsed time. It's transient — it never lands in the transcript.
		lbl := working
		if el := int(time.Since(workStart).Seconds()); el >= 1 {
			lbl += fmt.Sprintf(" · %ds", el)
		}
		badge := "  \x1b[48;5;236m " + tcol(colReply, tuiSpin[frame%len(tuiSpin)]) + " " + shimmer(lbl, frame) + " \x1b[0m"
		disp = append(disp, "", badge)
	}
	total := len(disp)
	// Clamp scroll to the real backlog so scrolling past the top doesn't need an equal
	// number of opposite presses to come back.
	maxScroll := total - chatH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
		u.mu.Lock()
		if u.scroll > maxScroll {
			u.scroll = maxScroll
		}
		u.mu.Unlock()
	}
	start := 0
	if total > chatH {
		start = total - chatH - scroll
		if start < 0 {
			start = 0
		}
		if start > total-chatH {
			start = total - chatH
		}
	}
	end := start + chatH
	if end > total {
		end = total
	}
	view := disp[start:end]

	// Scrolled up: a small grey badge on the bottom chat row shows how much is below.
	if scroll > 0 && total > end && len(view) > 0 {
		label := fmt.Sprintf(" ↓ %d more ", total-end)
		pad := cols - len([]rune(label)) - 2
		if pad < 0 {
			pad = 0
		}
		badge := strings.Repeat(" ", pad) + "\x1b[48;5;236m\x1b[38;5;250m" + label + "\x1b[0m"
		view = append(append([]string(nil), view[:len(view)-1]...), badge)
	}

	var b strings.Builder
	b.WriteString(titleSeq)
	b.WriteString("\x1b[?25l\x1b[H") // hide cursor, home
	row := 1
	put := func(s string) {
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[K", row, s)
		row++
	}
	for _, h := range head {
		put(h)
	}
	for i := 0; i < chatH; i++ { // chat anchored to the top of its region (just under the banner)
		if i < len(view) {
			put(view[i])
		} else {
			put("")
		}
	}
	put(rule)
	inputTop := row
	for _, ir := range inRows {
		put(ir)
	}
	put(rule)
	if strip != "" {
		put(strip)
	}
	put(hint)
	for row <= rows { // clear any rows left over from a previous, taller frame
		put("")
	}

	// Place the real cursor inside the input box and reveal it.
	fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", inputTop+caretLine, 1+caretCol)
	u.out.WriteString(b.String())
	u.out.Flush()
}

// agentStatusColor maps a subagent status to its dot colour.
func agentStatusColor(status string) int {
	switch status {
	case "done":
		return 78 // green
	case "failed":
		return colErr
	default:
		return colReply // running — coral
	}
}

// agentStrip is the passive, always-on dots line shown under the input box whenever any
// subagent has run: one ● per agent (status-coloured), a count, and a /agents hint. No
// emoji, minimal — press /agents to open the navigable view.
func agentStrip(cards []agentCard) string {
	if len(cards) == 0 {
		return ""
	}
	running := 0
	var dots strings.Builder
	for i, c := range cards {
		if i > 0 {
			dots.WriteByte(' ')
		}
		dots.WriteString(tcol(agentStatusColor(c.Status), "●"))
		if c.Status == "running" {
			running++
		}
	}
	state := tdim("idle")
	if running > 0 {
		state = tcol(colReply, fmt.Sprintf("%d working", running))
	}
	return "  " + tdim("agents ") + dots.String() + "  " + state + tdim("  ·  /agents")
}

// agentDots renders the strip of agent dots shown under the input in the agents view:
// one ● per subagent (coloured by status), the selected one ringed and labelled.
func agentDots(cards []agentCard, sel int) string {
	if len(cards) == 0 {
		return tdim("  no subagents yet — I spawn them with spawn_agents when a job parallelizes")
	}
	var b strings.Builder
	b.WriteString("  " + tcol(colTool, "agents") + "  ")
	for i, c := range cards {
		if i > 0 {
			b.WriteByte(' ')
		}
		dot := tcol(agentStatusColor(c.Status), "●")
		if i == sel {
			b.WriteString("\x1b[1m[" + dot + "]\x1b[22m") // ringed + bold
		} else {
			b.WriteString(" " + dot + " ")
		}
	}
	if sel >= 0 && sel < len(cards) {
		c := cards[sel]
		b.WriteString("   " + tcol(agentStatusColor(c.Status), c.ID) + " " + c.Label)
	}
	return b.String()
}

// agentViewLines builds the chat-area content for the agents view: the list of agents
// (with the selected one highlighted) or, when opened, the selected agent's full timeline.
func agentViewLines(cards []agentCard, sel int, open bool, cols int) []string {
	if len(cards) == 0 {
		return []string{"", tdim("  🤖 Пока не запускал саб-агентов."),
			tdim("  Я делаю это сам (spawn_agents), когда задачу можно распараллелить.")}
	}
	if sel < 0 || sel >= len(cards) {
		sel = 0
	}
	if open {
		c := cards[sel]
		out := []string{"", tcol(agentStatusColor(c.Status), "  🤖 "+c.ID+" · "+c.Label) +
			tdim("  ["+c.Status+" · "+fmtDur(c.Elapsed)+"]"), ""}
		if len(c.Lines) == 0 {
			out = append(out, tdim("    (no activity yet)"))
		}
		for _, l := range c.Lines {
			out = append(out, "    "+tdim(l))
		}
		return out
	}
	out := []string{"", tcol(colHead, "  🤖 Subagents") + tdim("  — ←→ выбрать · ⏎ открыть · esc выйти"), ""}
	for i, c := range cards {
		marker := "   "
		row := tdim
		if i == sel {
			marker = tcol(colReply, " ▸ ")
			row = func(s string) string { return s } // selected row at full brightness
		}
		dot := tcol(agentStatusColor(c.Status), "●")
		head := fmt.Sprintf("%s%s %s %s", marker, dot, c.ID, c.Label)
		tail := fmt.Sprintf("  [%s · %s]", c.Status, fmtDur(c.Elapsed))
		out = append(out, head+row(tail))
		out = append(out, "       "+tdim(oneLine(c.Last, cols-10)))
	}
	return out
}

// layoutInput wraps the input buffer into display rows for the bottom box and reports the
// caret's (row, column) within it. Row 0 carries the "❯ " prompt; wrapped rows are indented
// to line up under it.
func layoutInput(input []rune, cursor, cols int) (rows []string, caretLine, caretCol int) {
	const prefix = "  " // left margin
	promptW := 3        // "#  " — typed text starts at the same column as the agent's reply text
	textW := cols - len(prefix) - promptW
	if textW < 1 {
		textW = 1
	}

	for off := 0; ; off += textW {
		end := off + textW
		if end > len(input) {
			end = len(input)
		}
		seg := string(input[off:end])
		if off == 0 {
			rows = append(rows, prefix+tcol(colPrompt, "#  ")+seg)
		} else {
			rows = append(rows, prefix+strings.Repeat(" ", promptW)+seg)
		}
		if end >= len(input) {
			break
		}
	}
	caretLine = cursor / textW
	caretCol = len(prefix) + promptW + (cursor % textW)
	if cursor > 0 && cursor%textW == 0 && cursor == len(input) {
		// Caret sits exactly at a wrap boundary: show it at the start of a fresh row.
		if caretLine >= len(rows) {
			rows = append(rows, prefix+strings.Repeat(" ", promptW))
		}
	}
	return rows, caretLine, caretCol
}

// wrapLine breaks one logical line into display rows of at most w visible columns. It wraps
// on word boundaries (so a word is never split mid-letter — that was the "криво"), copies
// ANSI escapes through without counting them toward the width, and re-applies the line's
// leading indent to each wrapped continuation row so a paragraph stays aligned under its
// first line instead of jumping to column 0.
func wrapLine(s string, w int) []string {
	if w < 1 {
		w = 1
	}

	// Tokenize into atoms: each is either a zero-width ANSI escape or one visible rune.
	type atom struct {
		s   string
		vis bool
	}
	var atoms []atom
	inEsc := false
	var esc strings.Builder
	for _, r := range s {
		if inEsc {
			esc.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				atoms = append(atoms, atom{esc.String(), false})
				esc.Reset()
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			esc.Reset()
			esc.WriteRune(r)
			continue
		}
		if r == '\t' {
			r = ' '
		}
		atoms = append(atoms, atom{string(r), true})
	}
	if inEsc && esc.Len() > 0 {
		atoms = append(atoms, atom{esc.String(), false})
	}

	// Leading spaces become the continuation indent (guarded so a deep indent on a narrow
	// terminal doesn't squeeze the text to nothing).
	indent := 0
	for _, a := range atoms {
		if !a.vis {
			continue
		}
		if a.s == " " {
			indent++
		} else {
			break
		}
	}
	if indent > w/2 {
		indent = 0
	}
	pad := strings.Repeat(" ", indent)

	render := func(as []atom, prefix string) string {
		var b strings.Builder
		b.WriteString(prefix)
		for _, a := range as {
			b.WriteString(a.s)
		}
		return b.String()
	}

	var rows []string
	var row []atom
	col, limit := 0, w
	prefix := ""
	for _, a := range atoms {
		if a.vis && col >= limit {
			// Break at the last space in the row (word wrap); fall back to a hard break.
			brk := -1
			for i := len(row) - 1; i >= 0; i-- {
				if row[i].vis && row[i].s == " " {
					brk = i
					break
				}
			}
			var carry []atom
			if brk > 0 {
				rows = append(rows, render(row[:brk], prefix))
				carry = append(carry, row[brk+1:]...)
			} else {
				rows = append(rows, render(row, prefix))
			}
			prefix = pad
			limit = w - indent
			if limit < 1 {
				limit = 1
			}
			row = carry
			col = 0
			for _, c := range carry {
				if c.vis {
					col++
				}
			}
		}
		row = append(row, a)
		if a.vis {
			col++
		}
	}
	rows = append(rows, render(row, prefix))
	return rows
}

// --- raw key input -----------------------------------------------------------

type keyKind int

const (
	kRune keyKind = iota
	kEnter
	kBackspace
	kDelete
	kLeft
	kRight
	kHome
	kEnd
	kUp
	kDown
	kPgUp
	kPgDn
	kKill
	kEsc  // lone Escape — clears the input box
	kEOT  // Ctrl-D
	kQuit // Ctrl-C / stream closed
	kNone
)

type keyEvent struct {
	kind keyKind
	r    rune
}

// readKeys decodes raw stdin bytes into key events on its own goroutine: UTF-8 text (so typed
// Cyrillic works), control chars, and ANSI escape sequences for the arrow/navigation keys.
func readKeys(ctx context.Context) <-chan keyEvent {
	raw := make(chan byte, 1024)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				close(raw)
				return
			}
			for i := 0; i < n; i++ {
				raw <- buf[i]
			}
		}
	}()

	keys := make(chan keyEvent, 64)
	go func() {
		defer close(keys)
		for {
			b, ok := <-raw
			if !ok {
				keys <- keyEvent{kind: kQuit}
				return
			}
			switch {
			case b == 0x1b: // ESC — possibly an arrow/nav sequence
				select {
				case b2, ok := <-raw:
					if !ok {
						return
					}
					if b2 == '[' || b2 == 'O' {
						var seq []byte
						for {
							c, ok := <-raw
							if !ok {
								return
							}
							seq = append(seq, c)
							if c >= 0x40 && c <= 0x7e {
								break
							}
						}
						if ev := parseCSI(seq); ev.kind != kNone {
							keys <- ev
						}
					} else {
						keys <- byteKey(b2)
					}
				case <-time.After(40 * time.Millisecond):
					keys <- keyEvent{kind: kEsc} // a lone ESC keypress
				}
			case b < 0x80:
				if ev := byteKey(b); ev.kind != kNone {
					keys <- ev
				}
			default: // start of a multi-byte UTF-8 rune
				n := utf8ExtraBytes(b)
				bs := []byte{b}
				for i := 0; i < n; i++ {
					c, ok := <-raw
					if !ok {
						return
					}
					bs = append(bs, c)
				}
				if r, _ := utf8.DecodeRune(bs); r != utf8.RuneError {
					keys <- keyEvent{kind: kRune, r: r}
				}
			}
		}
	}()
	return keys
}

func utf8ExtraBytes(b byte) int {
	switch {
	case b >= 0xf0:
		return 3
	case b >= 0xe0:
		return 2
	case b >= 0xc0:
		return 1
	}
	return 0
}

func byteKey(b byte) keyEvent {
	switch b {
	case '\r', '\n':
		return keyEvent{kind: kEnter}
	case 0x7f, 0x08:
		return keyEvent{kind: kBackspace}
	case 0x03:
		return keyEvent{kind: kQuit}
	case 0x04:
		return keyEvent{kind: kEOT}
	case 0x15: // Ctrl-U
		return keyEvent{kind: kKill}
	case 0x01: // Ctrl-A
		return keyEvent{kind: kHome}
	case 0x05: // Ctrl-E
		return keyEvent{kind: kEnd}
	}
	if b >= 0x20 {
		return keyEvent{kind: kRune, r: rune(b)}
	}
	return keyEvent{kind: kNone}
}

func parseCSI(seq []byte) keyEvent {
	final := seq[len(seq)-1]
	params := string(seq[:len(seq)-1])
	switch final {
	case 'A':
		return keyEvent{kind: kUp}
	case 'B':
		return keyEvent{kind: kDown}
	case 'C':
		return keyEvent{kind: kRight}
	case 'D':
		return keyEvent{kind: kLeft}
	case 'H':
		return keyEvent{kind: kHome}
	case 'F':
		return keyEvent{kind: kEnd}
	case '~':
		switch params {
		case "1", "7":
			return keyEvent{kind: kHome}
		case "4", "8":
			return keyEvent{kind: kEnd}
		case "3":
			return keyEvent{kind: kDelete}
		case "5":
			return keyEvent{kind: kPgUp}
		case "6":
			return keyEvent{kind: kPgDn}
		}
	}
	return keyEvent{kind: kNone}
}

// --- line writer -------------------------------------------------------------

// lineWriter adapts the io.Writer the sink and the proactive channel already write to into
// transcript appends: it buffers bytes and flushes a transcript line on each newline.
type lineWriter struct {
	ui  *tui
	mu  sync.Mutex
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	for {
		i := indexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		s := string(line)
		w.buf = w.buf[i+1:]
		w.mu.Unlock()
		w.ui.appendLine(s)
		w.mu.Lock()
	}
	w.mu.Unlock()
	return len(p), nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// --- run ---------------------------------------------------------------------

// runTUI drives the full-screen experience: alternate screen + raw input, a render goroutine,
// the same restartable agent the plain REPL uses (its sink now feeds the transcript), and the
// keyboard loop. enableRawInput has already been called (and its restore deferred) by the
// caller, RunTerminal.
func (g *Gateway) runTUI(ctx context.Context) error {
	ui := newTUI()
	ui.headerFn = func(cols int) []string { return terminalHeaderLines(g, terminalChatID, cols) }
	ui.header = ui.headerFn(80)
	ui.model = g.activeModel(terminalChatID)
	ui.cardsFn = func() []agentCard { return g.hub.cardsFor(terminalChatID, 8) }

	fmt.Print("\x1b[?1049h\x1b[2J\x1b[H") // enter alternate screen
	defer fmt.Print("\x1b[?25h\x1b[?1049l\x1b[0m\x1b]0;LOBSTER\x07")

	stop := make(chan struct{})
	renderDone := make(chan struct{})
	go func() { ui.renderLoop(stop); close(renderDone) }()
	// Join the render goroutine before the deferred leave-alt-screen runs (defers are LIFO),
	// so no stray frame paints into the normal buffer after we've switched back.
	defer func() { close(stop); <-renderDone }()

	// Track the window size (poll on Windows, SIGWINCH on Unix) so the layout reflows on
	// resize without spawning a sizing process on every frame.
	stopResize := watchResize(func(rows, cols int) {
		u := ui
		u.mu.Lock()
		u.rows, u.cols = rows, cols
		u.header = u.headerFn(cols) // the header is centered, so re-render it for the new width
		u.mu.Unlock()
		u.markDirty()
	})
	defer stopResize()
	ui.markDirty()

	go g.sched.Run(ctx) // scheduled tasks still fire; their output lands in the transcript

	// The sink renders into the transcript and drives the TUI's own spinner; proactive
	// messages (background jobs, scheduled pings) go through the channel into the transcript.
	verb, stream, arch := g.terminalSinkFns()
	sink := newTerminalSink(&lineWriter{ui: ui}, verb, stream, arch)
	sink.work = ui.setWorking
	if tc, ok := g.ch.(*terminalChannel); ok {
		tc.out = &lineWriter{ui: ui}
	}

	r := &termREPL{
		g:      g,
		ctx:    ctx,
		chatID: terminalChatID,
		sess:   agent.NewSession(g.termHistID, g.hist, historyBudgetChars),
		out:    &lineWriter{ui: ui},
		tui:    ui,
		sink:   sink,
	}
	r.startAgent()
	defer r.stopAgent()

	// Connect MCP servers, showing progress in the spinner instead of a separate loader.
	ui.setWorking("loading…")
	g.dialMCP(ctx, func(name string, n int, err error) {
		if err != nil {
			ui.setWorking("mcp " + name + " ✗")
		} else {
			ui.setWorking(fmt.Sprintf("mcp %s ✓ (%d)", name, n))
		}
	})
	ui.setWorking("")
	// The tool/skill counts live in the header now — refresh it so they appear once MCP
	// has connected, instead of dropping a "ready …" line into the chat.
	ui.refreshHeader()

	keys := readKeys(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case k, ok := <-keys:
			if !ok {
				return nil
			}
			// In the agents view, the navigation keys drive the picker instead of the
			// input box: ←→ select, ⏎ open, esc steps back / leaves.
			if ui.inAgentsView() {
				switch k.kind {
				case kQuit:
					return nil
				case kEsc:
					ui.agentsBack()
				case kLeft, kUp:
					ui.agentsNav(-1)
				case kRight, kDown:
					ui.agentsNav(1)
				case kEnter:
					ui.agentsEnter()
				case kPgUp:
					ui.scrollBy(10)
				case kPgDn:
					ui.scrollBy(-10)
				}
				continue
			}

			switch k.kind {
			case kQuit:
				return nil
			case kEOT:
				if ui.inputLen() == 0 {
					return nil
				}
			case kEsc:
				ui.killLine()
			case kRune:
				ui.insertRune(k.r)
			case kBackspace:
				ui.backspace()
			case kDelete:
				ui.deleteFwd()
			case kLeft:
				ui.moveCursor(-1)
			case kRight:
				ui.moveCursor(1)
			case kHome:
				ui.cursorHome()
			case kEnd:
				ui.cursorEnd()
			case kKill:
				ui.killLine()
			case kUp:
				ui.scrollBy(1)
			case kDown:
				ui.scrollBy(-1)
			case kPgUp:
				ui.scrollBy(10)
			case kPgDn:
				ui.scrollBy(-10)
			case kEnter:
				if quit := g.tuiSubmit(r, ui); quit {
					return nil
				}
			}
		}
	}
}

// tuiSubmit handles Enter: echo the message, run a slash-command or hand it to the agent.
// It NEVER blocks the key loop: if the agent is mid-turn, the message lands as a live
// steering interrupt (the same trick the Telegram path has). Returns true on /exit.
func (g *Gateway) tuiSubmit(r *termREPL, ui *tui) bool {
	text := ui.takeInput()
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	ui.appendUser(trimmed)
	if cmd, ok := commandName(trimmed); ok {
		return r.command(cmd, trimmed)
	}
	_ = g.sessions.Append(r.chatID, "user", trimmed)
	g.resetGoalRuns(r.chatID) // a real user message re-arms goal-mode auto-continue
	ui.setTask(oneLine(trimmed, 48))
	r.submitAsync(trimmed)
	return false
}
