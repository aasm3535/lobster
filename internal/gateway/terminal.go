package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aasm3535/lobster/internal/agent"
	"github.com/aasm3535/lobster/internal/channel"
	"github.com/aasm3535/lobster/internal/config"
)

// terminalChatID is the single synthetic chat the terminal session uses. Memory, history
// and skills all key off it, so a terminal conversation persists and is searchable just
// like a Telegram one.
const terminalChatID = "local"

// --- colours -----------------------------------------------------------------

var termColor = true

const (
	colReply  = 209 // the agent's text
	colPrompt = 203 // input caret (# ) — coral, matching the banner
	colHead   = 203 // banner / headers
	colDim    = 245 // tool lines, hints
	colErr    = 196 // errors
	colTool   = 78  // the green "tool called" dot
	colRule   = 238 // the thin separator rules around the TUI input box (darker than colDim)
)

func tcol(code int, s string) string {
	if !termColor {
		return s
	}
	// Reset only the foreground (\x1b[39m), not all attributes, so a colour span nested
	// inside bold/italic doesn't cancel them — important for markdown like **`code`**.
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[39m", code, s)
}

func tdim(s string) string  { return tcol(colDim, s) }
func tcode(s string) string { return tcol(180, s) } // inline `code` / code blocks

// shimmer renders s in a dim base with a bright highlight band sweeping across it —
// the "shiny" working label (think Claude Code's Working…). frame advances the sweep.
func shimmer(s string, frame int) string {
	if !termColor {
		return s
	}
	r := []rune(s)
	period := len(r) + 8 // a little dark gap before the sweep wraps around
	if period < 1 {
		return s
	}
	pos := frame % period
	var b strings.Builder
	for i, ch := range r {
		d := i - pos
		if d < 0 {
			d = -d
		}
		switch d {
		case 0:
			b.WriteString("\x1b[38;5;231m") // white-hot center
		case 1:
			b.WriteString("\x1b[38;5;217m") // soft pink
		case 2:
			b.WriteString("\x1b[38;5;209m") // coral falloff
		default:
			b.WriteString("\x1b[38;5;245m") // dim base (readable on the grey plashka too)
		}
		b.WriteRune(ch)
	}
	b.WriteString("\x1b[39m")
	return b.String()
}

func tbold(s string) string {
	if !termColor {
		return s
	}
	return "\x1b[1m" + s + "\x1b[22m"
}

func titalic(s string) string {
	if !termColor {
		return s
	}
	return "\x1b[3m" + s + "\x1b[23m"
}

func tunder(s string) string {
	if !termColor {
		return s
	}
	return "\x1b[4m" + s + "\x1b[24m"
}

// NewTerminal builds a gateway whose "channel" is the local terminal instead of Telegram,
// so `lobster tui` runs the exact same agent — tools, memory, skills, scheduler, models —
// without needing Telegram at all.
func NewTerminal(cfg *config.Config) (*Gateway, error) {
	g, err := New(cfg)
	if err != nil {
		return nil, err
	}
	g.ch = newTerminalChannel() // replace the Telegram channel; tools/proactive msgs print to stdout
	return g, nil
}

// RunTerminal launches the in-terminal agent. When stdout is a real ANSI console and raw
// keyboard input can be enabled, it runs the full-screen TUI (fixed banner on top, the chat
// scrolling above a bottom input box). Otherwise — piped output, NO_COLOR, or a console we
// can't put in raw mode — it falls back to the line-based REPL below.
func (g *Gateway) RunTerminal(ctx context.Context) error {
	enableANSIConsole()
	termColor = terminalColorEnabled()

	g.appCtx = ctx
	defer g.mcp.Close()

	if termColor {
		if restore, ok := enableRawInput(); ok {
			defer restore()
			return g.runTUI(ctx)
		}
	}
	return g.runSimpleREPL(ctx)
}

// terminalSinkFns builds the three closures every terminal sink needs: the live verbosity
// (the local chat shows the tool timeline by default), whether streaming is on, and where to
// archive each turn. Shared by the TUI and the plain REPL.
func (g *Gateway) terminalSinkFns() (func() string, func() bool, func(role, text string)) {
	return func() string {
			if v := g.mem.Pref(terminalChatID, "verbosity"); v != "" {
				return v
			}
			return verbosityNormal
		},
		func() bool { return g.streamingOn(terminalChatID) },
		func(role, text string) { _ = g.sessions.Append(terminalChatID, role, text) }
}

// runSimpleREPL is the line-based read-eval-print loop: a banner, then prompt → answer
// lines. Slash-commands (/model, /reset, /help, /exit) are handled locally. The agent
// goroutine owns the session and can be restarted in place for /model and /reset.
func (g *Gateway) runSimpleREPL(ctx context.Context) error {
	go g.sched.Run(ctx) // scheduled tasks still fire (their output prints here)

	if termColor {
		fmt.Print("\x1b[2J\x1b[H") // wipe the startup logs into a clean screen
	}
	printTerminalBanner(g, terminalChatID)
	g.loadMCP(ctx)

	verb, stream, arch := g.terminalSinkFns()
	r := &termREPL{
		g:      g,
		ctx:    ctx,
		chatID: terminalChatID,
		sess:   agent.NewSession(terminalChatID, g.hist, historyBudgetChars),
		out:    os.Stdout,
		sink:   newTerminalSink(os.Stdout, verb, stream, arch),
	}
	r.startAgent()

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\n" + tcol(colPrompt, "# "))
		line, err := reader.ReadString('\n')
		if err != nil { // EOF (Ctrl-D / Ctrl-Z)
			fmt.Println()
			r.stopAgent()
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if cmd, ok := commandName(line); ok {
			if quit := r.command(cmd, line); quit {
				r.stopAgent()
				return nil
			}
			continue
		}
		_ = g.sessions.Append(r.chatID, "user", line)
		g.resetGoalRuns(r.chatID)
		if !r.send(line) {
			return nil
		}
	}
}

// loadMCP connects MCP servers behind the animated loader and prints a one-line summary.
func (g *Gateway) loadMCP(ctx context.Context) {
	ld := startLoader("loading…")
	g.dialMCP(ctx, func(name string, n int, err error) {
		if err != nil {
			ld.setStage("mcp " + name + " ✗")
			return
		}
		ld.setStage(fmt.Sprintf("mcp %s ✓ (%d)", name, n))
	})
	ready := fmt.Sprintf("ready · %d mcp tool(s) · %d skill(s)", len(g.mcp.Tools()), len(g.skills.List()))
	_, cols := terminalSize()
	ld.finish(centerPad(cols, len([]rune(ready))) + tdim(ready))
}

// termREPL holds the restartable agent state for the terminal session.
type termREPL struct {
	g      *Gateway
	ctx    context.Context
	chatID string
	sess   *agent.Session
	sink   *terminalSink

	// out is where slash-command output (/help, /model, lists…) is written: os.Stdout in
	// the plain REPL, or a line writer feeding the TUI transcript in full-screen mode.
	out io.Writer
	// tui is set only in full-screen mode; commands that touch the screen (/clear) use it.
	tui *tui

	inbound chan agent.Input
	cancel  context.CancelFunc

	// busy tracks whether a turn is in flight (TUI mode). Guarded by busyMu because the
	// key loop, the done-watcher goroutine and goal continuations all touch it.
	busyMu sync.Mutex
	busy   bool
}

// submitAsync hands a message to the agent WITHOUT blocking the caller (the TUI key
// loop). If the agent is idle this starts a fresh turn; if it's mid-turn the message
// becomes a live steering interrupt — the agent folds it in and changes course.
func (r *termREPL) submitAsync(text string) {
	r.busyMu.Lock()
	starting := !r.busy
	if starting {
		r.busy = true
		r.sink.begin()
	}
	d := r.sink.done
	r.busyMu.Unlock()

	if starting {
		go func() {
			select {
			case <-d:
			case <-r.ctx.Done():
			}
			r.busyMu.Lock()
			r.busy = false
			r.busyMu.Unlock()
		}()
	}

	// Never block the key loop, even if the inbound buffer is momentarily full.
	in := agent.Input{Text: text}
	select {
	case r.inbound <- in:
	default:
		go func() {
			select {
			case r.inbound <- in:
			case <-r.ctx.Done():
			}
		}()
	}
}

// isBusy reports whether a turn is currently in flight (TUI mode).
func (r *termREPL) isBusy() bool {
	r.busyMu.Lock()
	defer r.busyMu.Unlock()
	return r.busy
}

// send hands a message to the agent and blocks until the turn finishes. Returns false if
// the context was cancelled (time to exit).
func (r *termREPL) send(line string) bool {
	r.sink.begin()
	d := r.sink.done
	select {
	case r.inbound <- agent.Input{Text: line}:
	case <-r.ctx.Done():
		return false
	}
	select {
	case <-d:
	case <-r.ctx.Done():
		return false
	}
	return true
}

// startAgent spins up the agent goroutine for the current session. The system prompt is
// rebuilt each turn (composeSystem) so remembered facts and config changes take effect.
func (r *termREPL) startAgent() {
	actx, cancel := context.WithCancel(r.ctx)
	r.cancel = cancel
	r.inbound = make(chan agent.Input, 8)
	self := r.g.selfInfo()
	systemFn := func() string {
		return composeSystem(r.g.cfg.System, self, r.g.prefsLine(r.chatID), r.g.promptSections(), r.g.mem.Notes(r.chatID))
	}
	ag := agent.New(r.g.activeProvider(r.chatID), r.g.chatTools(r.chatID), systemFn, r.g.cfg.MaxSteps)
	// Goal mode: a finished turn with an active goal feeds its continuation back in.
	// No transcript noise per continuation — the working plashka shows the agent is on it.
	wrapped := &goalSink{Sink: r.sink, g: r.g, chatID: r.chatID, resubmit: r.submitAsync}
	go ag.Run(actx, r.sess, r.inbound, wrapped)
}

func (r *termREPL) stopAgent() {
	if r.cancel != nil {
		r.cancel()
	}
	// If a turn was in flight, its KindReply will never arrive — release anyone waiting
	// on the sink (and the TUI busy flag) so the next message starts cleanly.
	r.sink.finish()
}

// restartAgent tears down and respawns the agent on the SAME session (used by /model, so
// the conversation is kept but the provider changes).
func (r *termREPL) restartAgent() {
	r.stopAgent()
	r.startAgent()
}

// command handles a slash-command locally; returns true to quit the REPL.
func (r *termREPL) command(cmd, text string) bool {
	switch cmd {
	case "exit", "quit", "q":
		fmt.Fprintln(r.out, tdim("  bye 🦞"))
		return true
	case "help", "h":
		printTerminalHelp(r.out)
	case "clear", "cls":
		if r.tui != nil {
			r.tui.clearLines()
		} else {
			if termColor {
				fmt.Print("\x1b[2J\x1b[H")
			}
			printTerminalBanner(r.g, r.chatID)
		}
	case "reset", "new":
		r.stopAgent()
		_ = r.g.hist.Clear(r.chatID)
		_ = r.g.sessions.Close(r.chatID)
		r.sess = agent.NewSession(r.chatID, r.g.hist, historyBudgetChars)
		r.startAgent()
		if r.tui != nil {
			r.tui.clearLines()
		}
		fmt.Fprintln(r.out, tdim("  🧹 fresh conversation"))
	case "model":
		r.modelCommand(strings.TrimSpace(commandArg(text)))
	case "goal":
		r.goalCommand(strings.TrimSpace(commandArg(text)))
	case "agents", "agent":
		if r.tui != nil {
			r.tui.openAgents() // interactive: dots + ←→/⏎/esc
		} else {
			for _, l := range r.g.hub.detailLines(r.chatID, 12) {
				fmt.Fprintln(r.out, l)
			}
		}
	case "workflow", "workflows":
		r.workflowCommand(strings.TrimSpace(commandArg(text)))
	case "skills":
		r.list("skills", func() []string {
			var out []string
			for _, sk := range r.g.skills.List() {
				out = append(out, sk.Name+" — "+sk.Description)
			}
			return out
		})
	case "sessions":
		r.list("past sessions", func() []string {
			var out []string
			for _, m := range r.g.sessions.List(r.chatID) {
				t := m.Title
				if t == "" {
					t = "(untitled)"
				}
				out = append(out, m.LastActive.Format("2006-01-02 15:04")+" — "+t)
			}
			return out
		})
	case "schedules":
		r.list("scheduled tasks", func() []string {
			var out []string
			for _, j := range r.g.sched.List(r.chatID) {
				kind := "once"
				if j.Recurring() {
					kind = "every " + j.Every.String()
				}
				out = append(out, fmt.Sprintf("%s [%s] next %s: %s", j.ID, kind, j.NextAt.Format("01-02 15:04"), oneLine(j.Prompt, 60)))
			}
			return out
		})
	case "mcp":
		r.list("MCP tools", func() []string {
			var out []string
			for _, h := range r.g.mcp.Tools() {
				out = append(out, h.QualifiedName)
			}
			return out
		})
	default:
		fmt.Fprintln(r.out, tdim("  unknown command — try /help"))
	}
	return false
}

func (r *termREPL) modelCommand(name string) {
	if name == "" {
		fmt.Fprintln(r.out, tcol(colHead, "  models:"))
		active := r.g.activeModel(r.chatID)
		for _, n := range r.g.modelOrder {
			mark := "  • "
			if n == active {
				mark = "  ✅ "
			}
			fmt.Fprintln(r.out, mark+n)
		}
		fmt.Fprintln(r.out, tdim("  switch with /model <name>"))
		return
	}
	if _, ok := r.g.providers[name]; !ok {
		fmt.Fprintln(r.out, tdim("  no such model: "+name))
		return
	}
	_ = r.g.mem.SetPref(r.chatID, "model", name)
	r.restartAgent()
	if r.tui != nil {
		r.tui.setModel(name)
	}
	fmt.Fprintln(r.out, tdim("  🔀 switched to "+name+" (conversation kept)"))
}

// dispatchInput hands a synthetic input (goal kickoff, workflow run) to the agent: async
// in the TUI (the key loop must not block), blocking in the plain REPL (like a normal turn).
func (r *termREPL) dispatchInput(text string) {
	if r.tui != nil {
		r.submitAsync(text)
		return
	}
	r.send(text)
}

// goalCommand handles /goal in the terminal: show, clear, or set + kick off.
func (r *termREPL) goalCommand(arg string) {
	low := strings.ToLower(arg)
	switch {
	case arg == "":
		if goal := r.g.activeGoal(r.chatID); goal != "" {
			fmt.Fprintln(r.out, tcol(colHead, "  ⛳ active goal: ")+goal)
			fmt.Fprintln(r.out, tdim("  clear it with /goal clear"))
		} else {
			fmt.Fprintln(r.out, tdim("  no active goal — set one with /goal <what to achieve>"))
		}
	case low == "clear" || low == "done" || low == "stop" || low == "стоп" || low == "отмена":
		r.g.clearGoal(r.chatID)
		fmt.Fprintln(r.out, tdim("  ⛳ goal cleared"))
	default:
		r.g.setGoal(r.chatID, arg)
		fmt.Fprintln(r.out, tdim("  ⛳ goal pinned — работаю, пока не сделаю (/goal clear чтобы снять)"))
		r.dispatchInput(goalKickoff(arg))
	}
}

// workflowCommand handles /workflow and /workflows in the terminal.
func (r *termREPL) workflowCommand(arg string) {
	if arg == "" {
		r.list("workflows", func() []string {
			var out []string
			for _, m := range r.g.wf.List() {
				out = append(out, m.Name+" — "+m.Description)
			}
			return out
		})
		fmt.Fprintln(r.out, tdim("  run one with /workflow <name>"))
		return
	}
	name, extra, _ := strings.Cut(arg, " ")
	body, ok := r.g.wf.Get(name)
	if !ok {
		fmt.Fprintln(r.out, tdim("  no workflow named "+name+" — see /workflows"))
		return
	}
	fmt.Fprintln(r.out, tdim("  ▶️ running workflow «"+name+"»…"))
	r.dispatchInput(workflowKickoff(name, body, extra))
}

func (r *termREPL) list(title string, items func() []string) {
	got := items()
	if len(got) == 0 {
		fmt.Fprintln(r.out, tdim("  no "+title))
		return
	}
	fmt.Fprintln(r.out, tcol(colHead, "  "+title+":"))
	for _, it := range got {
		fmt.Fprintln(r.out, "  • "+it)
	}
}

// --- loader (animated startup) -----------------------------------------------

// loader animates a small spinner with a one-line status while a slow step (MCP connect)
// runs, so the startup never looks frozen. It's a no-op when colour is off (piped), where
// it just records the final summary.
type loader struct {
	mu    sync.Mutex
	stage string
	stop  chan struct{}
	done  chan struct{}
}

func startLoader(initial string) *loader {
	l := &loader{stage: initial, stop: make(chan struct{}), done: make(chan struct{})}
	if !termColor {
		close(l.done)
		return l
	}
	go l.run()
	return l
}

// loadFrames is a smooth braille spinner.
var loadFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (l *loader) run() {
	defer close(l.done)
	shimmer := []int{160, 196, 203, 209, 216} // the spinner gently shifts through coral
	t := time.NewTicker(90 * time.Millisecond)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.mu.Lock()
			stage := l.stage
			l.mu.Unlock()
			col := shimmer[(i/2)%len(shimmer)]
			fmt.Printf("\r  %s  %s\x1b[K", tcol(col, loadFrames[i%len(loadFrames)]), tcol(colPrompt, stage))
		}
	}
}

func (l *loader) setStage(s string) {
	l.mu.Lock()
	l.stage = s
	l.mu.Unlock()
}

func (l *loader) finish(summary string) {
	close(l.stop)
	<-l.done
	if termColor {
		fmt.Print("\r\x1b[K") // erase the animated line
	}
	if summary != "" {
		fmt.Println(summary)
	}
}

// --- banner / help -----------------------------------------------------------

// lobsterMascot is a small, clean ASCII lobster — Clawd-style: a little friendly face with
// two raised claws. No big wordmark; the name lives in the small title line below.
var lobsterMascot = []string{
	`  (\         /)`,
	`   \\  ___  //`,
	`   (  o   o  )`,
	`    \   ^   /`,
	`     '-----'`,
}

// centerBlock pads every line of an ASCII-art block by the SAME left margin (so the art's
// internal alignment is preserved) to center it as a unit in cols. color is applied per line.
func centerBlock(lines []string, cols int, color func(i int, s string) string) []string {
	w := 0
	for _, l := range lines {
		if n := len([]rune(strings.TrimRight(l, " "))); n > w {
			w = n
		}
	}
	pad := centerPad(cols, w)
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		l = strings.TrimRight(l, " ")
		out = append(out, pad+color(i, l))
	}
	return out
}

// terminalHeaderLines renders the small lobster mascot, a one-line title, and a status line
// (model · mcp · skills), all centered. The plain REPL prints it once; the TUI pins it as
// its fixed header (re-rendered on resize / model switch / after MCP connects).
func terminalHeaderLines(g *Gateway, chatID string, cols int) []string {
	out := []string{""}
	out = append(out, centerBlock(lobsterMascot, cols, func(_ int, s string) string { return tcol(colReply, s) })...)
	out = append(out, "")

	title := "LOBSTER" + "  ·  terminal chat"
	out = append(out, centerPad(cols, len([]rune(title)))+tbold(tcol(colHead, "LOBSTER"))+tdim("  ·  terminal chat"))

	// Status line folds in the live tool/skill counts so they're part of the header, not a
	// stray chat message.
	model := g.activeModel(chatID)
	tail := fmt.Sprintf("  ·  %d mcp · %d skills  ·  /help · /exit", len(g.mcp.Tools()), len(g.skills.List()))
	info := "model: " + model + tail
	out = append(out, centerPad(cols, len([]rune(info)))+tdim("model: ")+model+tdim(tail))
	return out
}

// centerPad returns the left margin that centers content of visible width w in cols.
func centerPad(cols, w int) string {
	pad := (cols - w) / 2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad)
}

func printTerminalBanner(g *Gateway, chatID string) {
	_, cols := terminalSize()
	for _, line := range terminalHeaderLines(g, chatID, cols) {
		fmt.Println(line)
	}
}

func printTerminalHelp(w io.Writer) {
	fmt.Fprintln(w, tcol(colHead, "  commands:"))
	for _, l := range []string{
		"/model [name]    list or switch the model (conversation kept)",
		"/goal <цель>     pin a goal — the agent keeps working until it's done (/goal clear)",
		"/agents          show what spawned subagents are doing (read-only)",
		"/workflow [name] run a saved playbook (no name = list them)",
		"/skills          list installed skills",
		"/sessions        list past conversations",
		"/schedules       list scheduled tasks",
		"/mcp             list connected MCP tools",
		"/reset           start a fresh conversation",
		"/clear           clear the screen",
		"/exit            quit",
	} {
		fmt.Fprintln(w, "  "+l)
	}
	fmt.Fprintln(w, tdim("  anything else is sent to the agent — it can run shell, read/write files, etc."))
}

// --- terminal channel --------------------------------------------------------

// terminalChannel satisfies channel.Channel by printing to stdout. RunTerminal drives the
// REPL itself, so Start is unused; the Send* methods exist for the tools (send_photo) and
// proactive paths (background-job / scheduled notifications) that talk through g.ch.
type terminalChannel struct{ out io.Writer }

func newTerminalChannel() *terminalChannel { return &terminalChannel{out: os.Stdout} }

func (t *terminalChannel) Name() string { return "terminal" }

func (t *terminalChannel) Start(ctx context.Context, _ func(channel.Inbound)) error {
	<-ctx.Done()
	return nil
}

func (t *terminalChannel) SendText(_ context.Context, _, text string) (string, error) {
	fmt.Fprintln(t.out, text)
	return "0", nil
}

func (t *terminalChannel) SendMarkdown(_ context.Context, _, text string) (string, error) {
	fmt.Fprintln(t.out, text)
	return "0", nil
}

func (t *terminalChannel) SendHTML(_ context.Context, _, text string) (string, error) {
	fmt.Fprintln(t.out, stripTags(text))
	return "0", nil
}

func (t *terminalChannel) SendPhoto(_ context.Context, _, photo, caption string) (string, error) {
	line := tdim("  🖼  " + photo)
	if caption != "" {
		line += " — " + caption
	}
	fmt.Fprintln(t.out, line)
	return "0", nil
}

func (t *terminalChannel) EditText(_ context.Context, _, _, _ string) error         { return nil }
func (t *terminalChannel) EditHTML(_ context.Context, _, _, _ string) error         { return nil }
func (t *terminalChannel) DeleteText(_ context.Context, _, _ string) error          { return nil }
func (t *terminalChannel) SendChatAction(_ context.Context, _, _ string) error      { return nil }
func (t *terminalChannel) SetCommands(_ context.Context, _ []channel.Command) error { return nil }

// stripTags reduces the Telegram HTML produced for proactive messages to readable plain
// text for the terminal (the interactive reply path never goes through here).
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	out := b.String()
	out = strings.ReplaceAll(out, "&lt;", "<")
	out = strings.ReplaceAll(out, "&gt;", ">")
	out = strings.ReplaceAll(out, "&quot;", "\"")
	out = strings.ReplaceAll(out, "&amp;", "&")
	return out
}

// terminalColorEnabled mirrors the wizard: off when NO_COLOR is set or stdout isn't a tty.
func terminalColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
