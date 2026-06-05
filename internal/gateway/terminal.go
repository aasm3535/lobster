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
	colPrompt = 216 // input caret
	colHead   = 203 // banner / headers
	colDim    = 245 // tool lines, hints
	colErr    = 196 // errors
	colTool   = 78  // the green "tool called" dot
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

// RunTerminal is the interactive read-eval-print loop: a clean banner, then prompt → answer
// lines. Slash-commands (/model, /reset, /help, /exit) are handled locally. The agent
// goroutine owns the session and can be restarted in place for /model and /reset.
func (g *Gateway) RunTerminal(ctx context.Context) error {
	enableANSIConsole()
	termColor = terminalColorEnabled()

	g.appCtx = ctx
	defer g.mcp.Close()
	go g.sched.Run(ctx) // scheduled tasks still fire (their output prints here)

	if termColor {
		fmt.Print("\x1b[2J\x1b[H") // wipe the startup logs into a clean screen
	}
	printTerminalBanner(g, terminalChatID)
	g.loadMCP(ctx)

	r := &termREPL{
		g:      g,
		ctx:    ctx,
		chatID: terminalChatID,
		sess:   agent.NewSession(terminalChatID, g.hist, historyBudgetChars),
		sink: newTerminalSink(os.Stdout,
			// The terminal shows the tool timeline by default — that's the whole point of a
			// local session. A global "quiet" (set for Telegram) doesn't hide it here; only
			// an explicit quiet on the local chat does.
			func() string {
				if v := g.mem.Pref(terminalChatID, "verbosity"); v != "" {
					return v
				}
				return verbosityNormal
			},
			func() bool { return g.streamingOn(terminalChatID) },
			func(role, text string) { _ = g.sessions.Append(terminalChatID, role, text) },
		),
	}
	r.startAgent()

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\n" + tcol(colPrompt, "❯ "))
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
	ld.finish(tdim(fmt.Sprintf("  ready · %d mcp tool(s) · %d skill(s)", len(g.mcp.Tools()), len(g.skills.List()))))
}

// termREPL holds the restartable agent state for the terminal session.
type termREPL struct {
	g      *Gateway
	ctx    context.Context
	chatID string
	sess   *agent.Session
	sink   *terminalSink

	inbound chan agent.Input
	cancel  context.CancelFunc
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
		return composeSystem(r.g.cfg.System, self, r.g.prefsLine(r.chatID), r.g.skillsSection(), r.g.mem.Notes(r.chatID))
	}
	ag := agent.New(r.g.activeProvider(r.chatID), r.g.chatTools(r.chatID), systemFn, r.g.cfg.MaxSteps)
	go ag.Run(actx, r.sess, r.inbound, r.sink)
}

func (r *termREPL) stopAgent() {
	if r.cancel != nil {
		r.cancel()
	}
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
		fmt.Println(tdim("  bye 🦞"))
		return true
	case "help", "h":
		printTerminalHelp()
	case "clear", "cls":
		if termColor {
			fmt.Print("\x1b[2J\x1b[H")
		}
		printTerminalBanner(r.g, r.chatID)
	case "reset", "new":
		r.stopAgent()
		_ = r.g.hist.Clear(r.chatID)
		_ = r.g.sessions.Close(r.chatID)
		r.sess = agent.NewSession(r.chatID, r.g.hist, historyBudgetChars)
		r.startAgent()
		fmt.Println(tdim("  🧹 fresh conversation"))
	case "model":
		r.modelCommand(strings.TrimSpace(commandArg(text)))
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
		fmt.Println(tdim("  unknown command — try /help"))
	}
	return false
}

func (r *termREPL) modelCommand(name string) {
	if name == "" {
		fmt.Println(tcol(colHead, "  models:"))
		active := r.g.activeModel(r.chatID)
		for _, n := range r.g.modelOrder {
			mark := "  • "
			if n == active {
				mark = "  ✅ "
			}
			fmt.Println(mark + n)
		}
		fmt.Println(tdim("  switch with /model <name>"))
		return
	}
	if _, ok := r.g.providers[name]; !ok {
		fmt.Println(tdim("  no such model: " + name))
		return
	}
	_ = r.g.mem.SetPref(r.chatID, "model", name)
	r.restartAgent()
	fmt.Println(tdim("  🔀 switched to " + name + " (conversation kept)"))
}

func (r *termREPL) list(title string, items func() []string) {
	got := items()
	if len(got) == 0 {
		fmt.Println(tdim("  no " + title))
		return
	}
	fmt.Println(tcol(colHead, "  "+title+":"))
	for _, it := range got {
		fmt.Println("  • " + it)
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

var termBanner = []string{
	`  ██╗      ██████╗ ██████╗ ███████╗████████╗███████╗██████╗ `,
	`  ██║     ██╔═══██╗██╔══██╗██╔════╝╚══██╔══╝██╔════╝██╔══██╗`,
	`  ██║     ██║   ██║██████╔╝███████╗   ██║   █████╗  ██████╔╝`,
	`  ██║     ██║   ██║██╔══██╗╚════██║   ██║   ██╔══╝  ██╔══██╗`,
	`  ███████╗╚██████╔╝██████╔╝███████║   ██║   ███████╗██║  ██║`,
	`  ╚══════╝ ╚═════╝ ╚═════╝ ╚══════╝   ╚═╝   ╚══════╝╚═╝  ╚═╝`,
}

func printTerminalBanner(g *Gateway, chatID string) {
	reds := []int{217, 210, 209, 203, 167, 131}
	fmt.Println()
	for i, line := range termBanner {
		fmt.Println(tcol(reds[i%len(reds)], line))
	}
	fmt.Println(tdim("        🦞  terminal chat — same agent, no Telegram needed"))
	fmt.Println()
	fmt.Println(tdim("  model: ") + g.activeModel(chatID) + tdim("   ·   /help for commands, /exit to quit"))
}

func printTerminalHelp() {
	fmt.Println(tcol(colHead, "  commands:"))
	for _, l := range []string{
		"/model [name]  list or switch the model (conversation kept)",
		"/skills        list installed skills",
		"/sessions      list past conversations",
		"/schedules     list scheduled tasks",
		"/mcp           list connected MCP tools",
		"/reset         start a fresh conversation",
		"/clear         clear the screen",
		"/exit          quit",
	} {
		fmt.Println("  " + l)
	}
	fmt.Println(tdim("  anything else is sent to the agent — it can run shell, read/write files, etc."))
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
