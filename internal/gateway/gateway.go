// Package gateway wires the channel, provider, tools and agent together, and runs
// one independent agent goroutine per chat.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aasm3535/lobster/internal/agent"
	"github.com/aasm3535/lobster/internal/bgproc"
	"github.com/aasm3535/lobster/internal/channel"
	"github.com/aasm3535/lobster/internal/channel/telegram"
	"github.com/aasm3535/lobster/internal/config"
	"github.com/aasm3535/lobster/internal/history"
	"github.com/aasm3535/lobster/internal/llm"
	"github.com/aasm3535/lobster/internal/mcp"
	"github.com/aasm3535/lobster/internal/memory"
	"github.com/aasm3535/lobster/internal/session"
	"github.com/aasm3535/lobster/internal/skills"
	"github.com/aasm3535/lobster/internal/tools"
)

// historyBudgetChars caps how much of a conversation is kept in context (and on disk).
// Older turns scroll out of this rolling window; durable facts survive via remember.
const historyBudgetChars = 120_000

// Verbosity levels control how much of the working timeline reaches the user.
const (
	verbosityQuiet   = "quiet"
	verbosityNormal  = "normal"
	verbosityVerbose = "verbose"
)

// verbosity returns the effective level for a chat: its saved preference, else the
// global default from config, else "normal".
func (g *Gateway) verbosity(chatID string) string {
	if v := g.mem.Pref(chatID, "verbosity"); v != "" {
		return v
	}
	if g.cfg.Verbosity != "" {
		return g.cfg.Verbosity
	}
	return verbosityNormal
}

// streamingOn reports whether replies type out live for a chat (per-chat pref, else the
// global config default, else on).
func (g *Gateway) streamingOn(chatID string) bool {
	if v := g.mem.Pref(chatID, "streaming"); v != "" {
		return v != "off"
	}
	return g.cfg.Streaming != "off"
}

type Gateway struct {
	cfg          *config.Config
	providers    map[string]llm.Provider // model name -> provider
	modelOrder   []string                // model names in config order (for /model listing)
	defaultModel string
	ch           channel.Channel
	auth         *authGate
	mem          *memory.Store
	hist         *history.Store
	sessions     *session.Store
	skills       *skills.Store
	mcp          *mcp.Manager
	bg           *bgproc.Manager
	appCtx       context.Context

	mu    sync.Mutex
	chats map[string]*chatSession
}

// chatSession is one chat's running agent: the channel we feed it, plus a cancel
// to tear it down (e.g. on /reset) so a fresh conversation starts cleanly.
type chatSession struct {
	inbound chan agent.Input
	cancel  context.CancelFunc
}

func New(cfg *config.Config) (*Gateway, error) {
	providers := map[string]llm.Provider{}
	var order []string
	for _, m := range cfg.Models {
		p, err := newProvider(m.ProviderConfig)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", m.Name, err)
		}
		if _, dup := providers[m.Name]; !dup {
			order = append(order, m.Name)
		}
		providers[m.Name] = p
	}

	mem, err := memory.Open(cfg.MemoryFile)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	hist, err := history.Open(cfg.HistoryDir)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	sess, err := session.Open(cfg.SessionsDir)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	sk, err := skills.Open(cfg.SkillsDir)
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	log.Printf("🧠 memory: %s | 📜 history: %s | 🗂 sessions: %s | 🧩 skills: %s (%d)",
		cfg.MemoryFile, cfg.HistoryDir, cfg.SessionsDir, cfg.SkillsDir, len(sk.List()))

	mcpMgr := connectMCP(cfg.MCP)

	gate := newAuthGate(cfg.Auth.Open, cfg.Auth.AccessCode, cfg.Auth.AllowedChats)
	switch {
	case gate.open():
		log.Printf("⚠️  auth: OPEN — anyone who messages the bot can run host commands; remove auth.open to lock it down")
	case len(cfg.Auth.AllowedChats) == 0 && cfg.Auth.AccessCode == "":
		log.Printf("🔒 auth: locked, no chats allowed yet — send /start to the bot to get your chat ID, add it to auth.allowed_chats, then restart")
	default:
		log.Printf("🔒 auth: locked (%d allowed chat(s), access code %s)", len(cfg.Auth.AllowedChats), maskCode(cfg.Auth.AccessCode))
	}

	g := &Gateway{
		cfg:          cfg,
		providers:    providers,
		modelOrder:   order,
		defaultModel: order[0],
		ch:           telegram.New(cfg.Telegram.Token),
		auth:         gate,
		mem:          mem,
		hist:         hist,
		sessions:     sess,
		skills:       sk,
		mcp:          mcpMgr,
		bg:           bgproc.NewManager(),
		appCtx:       context.Background(),
		chats:        map[string]*chatSession{},
	}
	// When a background job finishes, ping the chat that started it.
	g.bg.OnFinish = g.notifyJobDone
	return g, nil
}

// connectMCP dials every enabled MCP server from config, best-effort: a server that
// fails to start is logged and skipped rather than taking the whole bot down.
func connectMCP(cfg config.MCPConfig) *mcp.Manager {
	m := mcp.NewManager()
	for _, srv := range cfg.Servers {
		if srv.Disabled || strings.TrimSpace(srv.Command) == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		n, err := m.AddStdio(ctx, srv.Name, srv.Command, srv.Args, srv.Env)
		cancel()
		if err != nil {
			log.Printf("⚠️  mcp: %q failed to connect: %v", srv.Name, err)
			continue
		}
		log.Printf("🔌 mcp: %q connected (%d tools)", srv.Name, n)
	}
	return m
}

// botCommands is the CLI-style command menu advertised in Telegram's "/" picker.
func botCommands() []channel.Command {
	return []channel.Command{
		{Name: "start", Description: "Show help and get started"},
		{Name: "setup", Description: "Tune how I work with you"},
		{Name: "model", Description: "List / switch the model"},
		{Name: "skills", Description: "List my installed skills"},
		{Name: "sessions", Description: "Browse our past conversations"},
		{Name: "mcp", Description: "Show connected MCP servers"},
		{Name: "help", Description: "Show the list of commands"},
		{Name: "id", Description: "Show your chat ID"},
		{Name: "reset", Description: "Start a fresh conversation"},
	}
}

// maskCode shows only enough of the access code to recognize it in logs.
func maskCode(code string) string {
	if code == "" {
		return "none"
	}
	if len(code) <= 2 {
		return "****"
	}
	return code[:2] + "****"
}

func newProvider(p config.ProviderConfig) (llm.Provider, error) {
	switch p.Type {
	case "openai":
		return llm.NewOpenAI(p.BaseURL, p.APIKey, p.Model, p.AuthScheme, p.Headers), nil
	case "anthropic":
		return llm.NewAnthropic(p.BaseURL, p.APIKey, p.Model, p.MaxTokens, p.AuthScheme, p.Headers), nil
	case "minimax":
		return llm.NewMiniMax(p.BaseURL, p.APIKey, p.Model, p.MaxTokens, p.AuthScheme, p.Headers), nil
	default:
		return nil, fmt.Errorf("unknown provider type %q (use \"openai\", \"anthropic\" or \"minimax\")", p.Type)
	}
}

// activeModel returns the model name a chat is using: its saved choice if still valid,
// else the default.
func (g *Gateway) activeModel(chatID string) string {
	if name := g.mem.Pref(chatID, "model"); name != "" {
		if _, ok := g.providers[name]; ok {
			return name
		}
	}
	return g.defaultModel
}

func (g *Gateway) activeProvider(chatID string) llm.Provider {
	return g.providers[g.activeModel(chatID)]
}

func (g *Gateway) Run(ctx context.Context) error {
	g.appCtx = ctx // background jobs live for the app's lifetime, not a single request
	defer g.mcp.Close()

	// Advertise the command menu; non-fatal if it fails (e.g. transient network).
	if err := g.ch.SetCommands(ctx, botCommands()); err != nil {
		log.Printf("warning: could not register bot commands: %v", err)
	}
	return g.ch.Start(ctx, func(in channel.Inbound) {
		g.dispatch(ctx, in)
	})
}

// notifyJobDone pings the chat that started a background job when it finishes — unless
// the chat is in quiet mode, where the agent reports results itself.
func (g *Gateway) notifyJobDone(j *bgproc.Job) {
	if g.verbosity(j.ChatID) == verbosityQuiet {
		return
	}
	st := j.Status()
	head := fmt.Sprintf("✅ Background job %s finished in %s", st.ID, fmtDur(st.Elapsed))
	if st.ExitErr != nil {
		head = fmt.Sprintf("⚠️ Background job %s failed after %s: %v", st.ID, fmtDur(st.Elapsed), st.ExitErr)
	}
	tail := strings.TrimSpace(st.Output)
	if tail != "" {
		head += "\n\n" + oneLine(tail, 300)
	}
	_, _ = g.ch.SendText(context.Background(), j.ChatID, head)
}

// dispatch routes an inbound message to its per-chat agent, spawning one on first contact.
func (g *Gateway) dispatch(ctx context.Context, in channel.Inbound) {
	text := strings.TrimSpace(in.Text)

	// Front door: an unauthorized chat never reaches the agent (and thus never the
	// shell). We answer it directly — /start (or anything else) gets an onboarding
	// message that shows this chat's ID so the owner can whitelist it — but we never
	// spawn a session or forward anything to the model.
	authorized, justGranted := g.auth.admit(in.ChatID, text)
	if justGranted {
		g.reply(ctx, in.ChatID, "✅ Access granted. Send me a message and I'll get to work.")
		return
	}
	if !authorized {
		g.replyMarkdown(ctx, in.ChatID, onboardMessage(in.ChatID))
		return
	}

	// Authorized. Slash-commands are handled here and never reach the model.
	if cmd, isCmd := commandName(text); isCmd {
		g.runCommand(ctx, in.ChatID, cmd, text)
		return
	}

	// A photo with no caption still needs a non-empty turn for the archive/history.
	userText := in.Text
	if userText == "" && len(in.Images) > 0 {
		userText = "📷 (фото)"
	}
	archived := userText
	if len(in.Images) > 0 {
		archived += fmt.Sprintf(" [+%d image(s)]", len(in.Images))
	}
	_ = g.sessions.Append(in.ChatID, "user", archived)
	g.enqueue(ctx, in.ChatID, agent.Input{Text: userText, Images: in.Images})
}

// enqueue hands a user message to the chat's agent, spawning the agent on first contact.
func (g *Gateway) enqueue(ctx context.Context, chatID string, msg agent.Input) {
	g.mu.Lock()
	cs, ok := g.chats[chatID]
	if !ok {
		cs = g.startSession(ctx, chatID)
		g.chats[chatID] = cs
	}
	g.mu.Unlock()

	// Never block the poller; if the buffer is momentarily full, hand off async so
	// the user's "stop / do it differently" message is never dropped.
	select {
	case cs.inbound <- msg:
	default:
		go func() { cs.inbound <- msg }()
	}
}

// startSession spins up one agent goroutine for a chat. Caller holds g.mu.
func (g *Gateway) startSession(ctx context.Context, chatID string) *chatSession {
	cctx, cancel := context.WithCancel(ctx)
	inbound := make(chan agent.Input, 32)
	// Loads any persisted transcript so the conversation survives a restart.
	sess := agent.NewSession(chatID, g.hist, historyBudgetChars)

	// The system prompt is rebuilt each turn so anything the agent remembers or
	// reconfigures mid-chat shows up on its very next move. The self-info is static.
	self := g.selfInfo()
	systemFn := func() string {
		return composeSystem(g.cfg.System, self, g.prefsLine(chatID), g.skillsSection(), g.mem.Notes(chatID))
	}

	ag := agent.New(g.activeProvider(chatID), g.chatTools(chatID), systemFn, g.cfg.MaxSteps)
	sink := newTelegramSink(g.ch, chatID, cctx,
		func() string { return g.verbosity(chatID) },
		func() bool { return g.streamingOn(chatID) },
		func(role, text string) { _ = g.sessions.Append(chatID, role, text) },
	)
	go ag.Run(cctx, sess, inbound, sink)
	return &chatSession{inbound: inbound, cancel: cancel}
}

// chatTools builds the tool set for one chat: the shared builtins plus tools that are
// bound to this specific chat (its memory and its Telegram thread).
func (g *Gateway) chatTools(chatID string) *tools.Registry {
	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg)

	reg.Register(tools.Tool{
		Name: "remember",
		Description: "Save a durable fact about this user — their name, how they like to be addressed, " +
			"the vibe they want, their projects, preferences, recurring tasks. Use it for things worth " +
			"recalling next time, not small talk. What you save is shown back to you in future conversations.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"fact": map[string]any{"type": "string", "description": "The thing to remember, in one short sentence."},
			},
			"required": []string{"fact"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Fact string `json:"fact"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Fact) == "" {
				return "", fmt.Errorf("fact is empty")
			}
			if err := g.mem.Remember(chatID, a.Fact); err != nil {
				return "", err
			}
			return "remembered: " + a.Fact, nil
		},
	})

	reg.Register(tools.Tool{
		Name: "send_photo",
		Description: "Send a photo to the user on Telegram. 'photo' is a local file path or an http(s) URL; " +
			"'caption' is optional. Use this to share images, screenshots, or generated pictures.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"photo":   map[string]any{"type": "string", "description": "Local file path or http(s) URL of the image."},
				"caption": map[string]any{"type": "string", "description": "Optional caption."},
			},
			"required": []string{"photo"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Photo   string `json:"photo"`
				Caption string `json:"caption"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Photo) == "" {
				return "", fmt.Errorf("photo is empty")
			}
			id, err := g.ch.SendPhoto(ctx, chatID, a.Photo, a.Caption)
			if err != nil {
				return "", err
			}
			return "sent photo (message " + id + ")", nil
		},
	})

	reg.Register(tools.Tool{
		Name: "background_run",
		Description: "Start a long-running shell command in the BACKGROUND and return immediately with a job id. " +
			"Use this instead of shell for anything slow (full-disk scans, big downloads/builds, long searches) so you " +
			"don't block. Check progress with background_status; the user is auto-notified when the job finishes.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The shell command to run in the background."},
			},
			"required": []string{"command"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Command) == "" {
				return "", fmt.Errorf("command is empty")
			}
			j := g.bg.Start(g.appCtx, chatID, a.Command)
			return fmt.Sprintf("started %s: %s", j.ID, a.Command), nil
		},
	})

	reg.Register(tools.Tool{
		Name: "background_status",
		Description: "Check background jobs. Pass an 'id' (e.g. bg1) for that job's state, elapsed time and latest " +
			"output, or omit it to list all jobs.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Job id like bg1 (optional)."},
			},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(args, &a)
			if id := strings.TrimSpace(a.ID); id != "" {
				j, ok := g.bg.Get(id)
				if !ok {
					return "", fmt.Errorf("no such job: %s", id)
				}
				return formatJob(j.Status(), true), nil
			}
			jobs := g.bg.List()
			if len(jobs) == 0 {
				return "no background jobs", nil
			}
			var b strings.Builder
			for _, j := range jobs {
				b.WriteString(formatJob(j.Status(), false))
				b.WriteByte('\n')
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})

	reg.Register(tools.Tool{
		Name:        "background_stop",
		Description: "Stop (kill) a running background job by its id.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Job id like bg1."},
			},
			"required": []string{"id"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if g.bg.Stop(strings.TrimSpace(a.ID)) {
				return "stopped " + a.ID, nil
			}
			return a.ID + " was not running", nil
		},
	})

	reg.Register(tools.Tool{
		Name: "configure",
		Description: "Save how you should work with THIS user (persisted). Set any of: 'verbosity' — how much working " +
			"detail they see (quiet = only final answers, normal = a short progress line, verbose = full tool timeline); " +
			"'streaming' — whether your reply types out live as it's written (on) or appears all at once when done (off); " +
			"'tech_level' — how technical they are (beginner = avoid jargon & explain simply, never ask them shell/CLI " +
			"questions; intermediate; pro = you can be terse and technical); 'shell' — preferred default shell (bash or " +
			"powershell), only relevant for technical users. Use during /setup or whenever they ask you to adapt.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verbosity":  map[string]any{"type": "string", "enum": []string{"quiet", "normal", "verbose"}, "description": "How much working detail to show."},
				"streaming":  map[string]any{"type": "string", "enum": []string{"on", "off"}, "description": "Type the reply out live (on) or send it all at once (off)."},
				"tech_level": map[string]any{"type": "string", "enum": []string{"beginner", "intermediate", "pro"}, "description": "How technical the user is, so you calibrate explanations."},
				"shell":      map[string]any{"type": "string", "enum": []string{"bash", "powershell"}, "description": "Preferred default shell (technical users only)."},
			},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Verbosity string `json:"verbosity"`
				Streaming string `json:"streaming"`
				TechLevel string `json:"tech_level"`
				Shell     string `json:"shell"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			var changed []string
			if v := strings.ToLower(strings.TrimSpace(a.Verbosity)); v != "" {
				if v != verbosityQuiet && v != verbosityNormal && v != verbosityVerbose {
					return "", fmt.Errorf("verbosity must be quiet, normal or verbose")
				}
				_ = g.mem.SetPref(chatID, "verbosity", v)
				changed = append(changed, "verbosity="+v)
			}
			if st := strings.ToLower(strings.TrimSpace(a.Streaming)); st != "" {
				if st != "on" && st != "off" {
					return "", fmt.Errorf("streaming must be on or off")
				}
				_ = g.mem.SetPref(chatID, "streaming", st)
				changed = append(changed, "streaming="+st)
			}
			if tl := strings.ToLower(strings.TrimSpace(a.TechLevel)); tl != "" {
				_ = g.mem.SetPref(chatID, "tech_level", tl)
				changed = append(changed, "tech_level="+tl)
			}
			if sh := strings.ToLower(strings.TrimSpace(a.Shell)); sh != "" {
				_ = g.mem.SetPref(chatID, "shell", sh)
				changed = append(changed, "shell="+sh)
			}
			if len(changed) == 0 {
				return "nothing to change", nil
			}
			return "updated: " + strings.Join(changed, ", "), nil
		},
	})

	reg.Register(tools.Tool{
		Name: "use_skill",
		Description: "Load a skill's full instructions before doing a task it covers. The available skills (name + " +
			"what they're for) are listed in your system prompt; call this with the skill's name to get its step-by-step " +
			"instructions plus the paths of any bundled scripts/resources, then follow them (run scripts via shell, read " +
			"files via read_file).",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "The skill's name."},
			},
			"required": []string{"name"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			sk, ok := g.skills.Get(strings.TrimSpace(a.Name))
			if !ok {
				return "", fmt.Errorf("no skill named %q (see the skills list in your prompt)", a.Name)
			}
			return formatSkill(sk), nil
		},
	})

	reg.Register(tools.Tool{
		Name: "reload_skills",
		Description: "Rescan the skills folder. Call this after you create or edit a skill (a <name>/SKILL.md folder) " +
			"so it becomes available.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			if err := g.skills.Reload(); err != nil {
				return "", err
			}
			names := make([]string, 0)
			for _, sk := range g.skills.List() {
				names = append(names, sk.Name)
			}
			if len(names) == 0 {
				return "no skills installed", nil
			}
			return fmt.Sprintf("%d skill(s): %s", len(names), strings.Join(names, ", ")), nil
		},
	})

	reg.Register(tools.Tool{
		Name: "search_sessions",
		Description: "Search ALL of this user's past conversations (the permanent session archive) for a word or " +
			"phrase. Use this to recall things from earlier chats that have scrolled out of the current context — e.g. " +
			"\"what did we decide about X\", \"where did I say my project lives\". Returns matching snippets with when and " +
			"which session they're from.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Word or phrase to look for."},
				"limit": map[string]any{"type": "integer", "description": "Max results (default 15)."},
			},
			"required": []string{"query"},
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Limit <= 0 {
				a.Limit = 15
			}
			hits := g.sessions.Search(chatID, a.Query, a.Limit)
			if len(hits) == 0 {
				return "no matches in past conversations", nil
			}
			var b strings.Builder
			for _, h := range hits {
				title := h.Title
				if title == "" {
					title = h.SessionID
				}
				fmt.Fprintf(&b, "[%s | %s | %s] %s\n", h.At.Format("2006-01-02 15:04"), title, h.Role, h.Snippet)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})

	reg.Register(tools.Tool{
		Name:        "list_sessions",
		Description: "List this user's past conversation sessions (newest first) with their titles, dates and message counts.",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			list := g.sessions.List(chatID)
			if len(list) == 0 {
				return "no past sessions yet", nil
			}
			var b strings.Builder
			for _, m := range list {
				title := m.Title
				if title == "" {
					title = "(untitled)"
				}
				fmt.Fprintf(&b, "%s — %s (%d msgs)\n", m.LastActive.Format("2006-01-02 15:04"), title, m.Messages)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})

	// Tools from connected MCP servers, exposed to the model like any native tool.
	for _, h := range g.mcp.Tools() {
		registerMCPTool(reg, h)
	}

	// Connect a NEW MCP server at runtime — the agent can extend itself without a restart.
	reg.Register(tools.Tool{
		Name: "add_mcp_server",
		Description: "Connect a new MCP server to yourself right now (no restart) and make its tools available " +
			"immediately, in this same conversation. It's also saved to your config so it persists. If the server isn't " +
			"installed yet, install it first with shell (e.g. `npx -y <package>` self-installs on first run). Give a short " +
			"name, the launch command (e.g. \"npx\"), and its args.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "Short label for the server (prefixes its tools)."},
				"command": map[string]any{"type": "string", "description": "Executable to launch, e.g. npx or node."},
				"args":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Arguments."},
				"env":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Extra env vars (optional)."},
			},
			"required": []string{"name", "command"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Name    string            `json:"name"`
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			a.Name = strings.TrimSpace(a.Name)
			a.Command = strings.TrimSpace(a.Command)
			if a.Name == "" || a.Command == "" {
				return "", fmt.Errorf("name and command are required")
			}
			cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			n, err := g.mcp.AddStdio(cctx, a.Name, a.Command, a.Args, a.Env)
			if err != nil {
				return "", err
			}
			// Make the new tools usable in THIS session immediately. This runs in the
			// agent's own goroutine (it's a tool call), so registering into its registry
			// is race-free, and the next model step will see them.
			for _, h := range g.mcp.Tools() {
				if h.ServerName == a.Name {
					registerMCPTool(reg, h)
				}
			}
			persisted := "saved to config"
			if err := g.persistMCPServer(config.MCPServer{Name: a.Name, Command: a.Command, Args: a.Args, Env: a.Env}); err != nil {
				persisted = "not saved to config: " + err.Error()
			}
			return fmt.Sprintf("connected %q with %d tool(s) — available now (%s)", a.Name, n, persisted), nil
		},
	})

	return reg
}

// registerMCPTool exposes one MCP tool handle to the model through a registry.
func registerMCPTool(reg *tools.Registry, h *mcp.ToolHandle) {
	schema := h.Schema
	if schema == nil {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	desc := h.Description
	if desc == "" {
		desc = "Tool from MCP server " + h.ServerName
	}
	reg.Register(tools.Tool{
		Name:        h.QualifiedName,
		Description: desc,
		Schema:      schema,
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			return h.Call(ctx, args)
		},
	})
}

// persistMCPServer appends a server to lobster.json (preserving everything else) so a
// runtime-added server survives the next restart.
func (g *Gateway) persistMCPServer(srv config.MCPServer) error {
	g.cfg.MCP.Servers = append(g.cfg.MCP.Servers, srv)
	if g.cfg.Path == "" {
		return nil
	}
	raw, err := os.ReadFile(g.cfg.Path)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	mcpObj, _ := m["mcp"].(map[string]any)
	if mcpObj == nil {
		mcpObj = map[string]any{}
		m["mcp"] = mcpObj
	}
	servers, _ := mcpObj["servers"].([]any)
	entry := map[string]any{"name": srv.Name, "command": srv.Command}
	if len(srv.Args) > 0 {
		entry["args"] = srv.Args
	}
	if len(srv.Env) > 0 {
		entry["env"] = srv.Env
	}
	mcpObj["servers"] = append(servers, entry)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(g.cfg.Path, out, 0o600)
}

// formatSkill renders a loaded skill for the model: where it lives, what it bundles, and
// its full instructions.
func formatSkill(sk *skills.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill: %s\nDirectory: %s\n", sk.Name, sk.Dir)
	if len(sk.Files) > 0 {
		b.WriteString("Bundled files (reference them by joining the directory above):\n")
		for _, f := range sk.Files {
			b.WriteString("  - " + f + "\n")
		}
	}
	b.WriteString("\n--- INSTRUCTIONS ---\n")
	b.WriteString(sk.Body)
	return b.String()
}

// skillsSection lists the installed skills (name + description) for the system prompt —
// the cheap, always-present half of progressive disclosure.
func (g *Gateway) skillsSection() string {
	list := g.skills.List()
	if len(list) == 0 {
		return "# Skills\nNone installed yet. You can create one yourself: write " + g.skills.Dir() +
			"/<name>/SKILL.md (YAML frontmatter with name + description, then markdown instructions; bundle helper " +
			"scripts in the same folder), then call reload_skills."
	}
	var b strings.Builder
	b.WriteString("# Skills (specialized playbooks — load before a matching task)\n")
	for _, sk := range list {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
	}
	b.WriteString("When a task matches one of these, call use_skill with its name FIRST and follow the instructions it " +
		"returns. You can also author new skills (a <name>/SKILL.md folder under " + g.skills.Dir() + "), then reload_skills.")
	return b.String()
}

// prefsLine summarizes a chat's current preferences for the system prompt, so the model
// honours the verbosity/shell the user picked (verbosity is also enforced by the sink).
func (g *Gateway) prefsLine(chatID string) string {
	v := g.verbosity(chatID)
	line := "# Current preferences\n- verbosity: " + v
	switch v {
	case verbosityQuiet:
		line += " (the user only wants final answers — work silently, no step-by-step narration)"
	case verbosityVerbose:
		line += " (the user wants to see everything you do)"
	default:
		line += " (one short acknowledgement, then the result)"
	}
	if tl := g.mem.Pref(chatID, "tech_level"); tl != "" {
		line += "\n- tech level: " + tl
		switch tl {
		case "beginner":
			line += " (NON-technical — avoid jargon, explain in plain language, never ask them about shells/CLI/config; just pick sensible defaults yourself)"
		case "pro":
			line += " (you can be terse and technical)"
		}
	}
	if sh := g.mem.Pref(chatID, "shell"); sh != "" {
		line += "\n- preferred shell: " + sh + " (use shell:\"" + sh + "\" unless another is clearly better)"
	}
	return line
}

// formatJob renders a background job's status for the model. With detail, it includes
// the captured output tail; otherwise it's a one-line summary for a list.
func formatJob(st bgproc.Status, detail bool) string {
	state := "running " + fmtDur(st.Elapsed)
	if !st.Running {
		state = "done in " + fmtDur(st.Elapsed)
		if st.ExitErr != nil {
			state = "failed after " + fmtDur(st.Elapsed) + " (" + st.ExitErr.Error() + ")"
		}
	}
	head := fmt.Sprintf("%s [%s]: %s", st.ID, state, oneLine(st.Command, 80))
	if detail {
		out := strings.TrimSpace(st.Output)
		if out == "" {
			out = "(no output yet)"
		}
		return head + "\n--- output ---\n" + truncateTail(out, 4000)
	}
	return head
}

// selfInfo tells the agent where its own files live and that it may change them. The
// paths don't move during a run, so this is computed once per session.
func (g *Gateway) selfInfo() string {
	exe, _ := os.Executable()
	return fmt.Sprintf("# Your own setup (you can inspect and change this)\n"+
		"- OS / shell: %s\n"+
		"- Config file: %s\n"+
		"- Memory file: %s\n"+
		"- Binary: %s\n"+
		"- State dir: ~/.lobster\n"+
		"You can read and edit your own config with read_file/write_file — e.g. to change the model, "+
		"max_steps, the system persona, or allowed_chats. Most changes take effect after a restart; tell "+
		"the user when a restart is needed. Don't touch secrets unless asked.",
		runtime.GOOS, g.cfg.Path, g.cfg.MemoryFile, exe)
}

// composeSystem assembles the per-turn system prompt: persona + self-info + prefs +
// skills + memory.
func composeSystem(base, self, prefs, skillsList string, notes []string) string {
	var b strings.Builder
	b.WriteString(base)
	// Re-evaluated every turn (composeSystem runs before each model call), so the agent
	// always knows the real "now" instead of guessing from its training cutoff.
	b.WriteString("\n\n# Right now\n- Current date & time: ")
	b.WriteString(time.Now().Format("Monday, 2006-01-02 15:04 -07:00"))
	b.WriteString("\n\n")
	b.WriteString(self)
	if prefs != "" {
		b.WriteString("\n\n")
		b.WriteString(prefs)
	}
	if skillsList != "" {
		b.WriteString("\n\n")
		b.WriteString(skillsList)
	}
	b.WriteString("\n\n# What you know about this user\n")
	if len(notes) == 0 {
		b.WriteString("(Nothing yet — this is a fresh relationship. Get to know them, lightly.)")
		return b.String()
	}
	for _, n := range notes {
		b.WriteString("- ")
		b.WriteString(n)
		b.WriteByte('\n')
	}
	return b.String()
}

// startKickoff is the internal cue we feed the agent when an authorized user sends
// /start, so the greeting comes from the agent itself (in character) rather than a
// canned string. It reads as the user opening the chat.
const startKickoff = "(The user just opened the chat with /start. Greet them as yourself, in character. " +
	"If you already know them, welcome them back by name; if not, say a warm hello and, lightly, " +
	"start getting to know them. Keep it short.)"

const setupKickoff = "(The user ran /setup. Run a warm, GENUINE getting-to-know-you interview so you can be the most " +
	"useful assistant for THEM specifically. Ask ONE question at a time, conversationally — react to each answer before " +
	"the next, never dump a list or a form. Keep it human, not a survey.\n\n" +
	"Cover, roughly in this order, adapting to their answers:\n" +
	"1) Their name / how they'd like you to address them.\n" +
	"2) What they actually want you for — their goals. Ask in plain language ('чем я могу быть тебе полезен? что хочешь, " +
	"чтобы я для тебя делал?'). Give a couple of concrete examples if they seem unsure (помочь с кодом, навести порядок в " +
	"файлах, что-то находить на компе, разобраться в чём-то, просто поболтать).\n" +
	"3) How comfortable they are with computers/tech — gently, no jargon ('ты больше технарь или просто пользователь?'). " +
	"This decides EVERYTHING below: set tech_level via configure.\n" +
	"4) The tone/vibe they want from you (на ты или на вы, по-дружески или по-делу, с юмором или строго). Save with remember.\n" +
	"5) How much you should show while working — for non-tech people frame it simply ('показывать тебе каждый шаг или " +
	"только готовый результат?'), not as 'verbosity'. Set with configure.\n\n" +
	"IMPORTANT for beginners / non-technical people: do NOT ask about shells, bash/powershell, CLIs, or any tech setting — " +
	"they won't understand and it'll feel cold. Just pick sensible defaults yourself. Only ask the technical stuff (preferred " +
	"shell, verbose timeline) if they clearly told you they're technical.\n\n" +
	"As you learn things, save them immediately (configure for verbosity/tech_level/shell, remember for name/goals/tone/" +
	"facts). At the end, give a short warm recap of what you understood and what you'll do differently. Keep the whole thing " +
	"light and encouraging — some people are nervous about this stuff.)"

// runCommand handles the bot's CLI-style slash commands for an authorized chat. text is
// the full message, so commands that take an argument (like /model gpt4o) can read it.
func (g *Gateway) runCommand(ctx context.Context, chatID, cmd, text string) {
	switch cmd {
	case "start":
		// Hand off to the agent so the greeting is the agent's own, not a fixed reply.
		g.enqueue(ctx, chatID, agent.Input{Text: startKickoff})
	case "setup":
		g.enqueue(ctx, chatID, agent.Input{Text: setupKickoff})
	case "model":
		g.switchModel(ctx, chatID, commandArg(text))
	case "skills":
		g.replyMarkdown(ctx, chatID, skillsMessage(g.skills.List()))
	case "sessions":
		g.replyMarkdown(ctx, chatID, sessionsMessage(g.sessions.List(chatID)))
	case "mcp":
		g.replyMarkdown(ctx, chatID, mcpMessage(g.mcp.Tools()))
	case "help":
		g.replyMarkdown(ctx, chatID, helpMessage())
	case "id", "whoami":
		g.replyMarkdown(ctx, chatID, "Your chat ID:\n\n"+mdInline(chatID))
	case "reset", "new":
		g.resetSession(chatID)
		g.reply(ctx, chatID, "🧹 Conversation reset. Starting fresh.")
	default:
		g.reply(ctx, chatID, "Unknown command. Try /help")
	}
}

// switchModel changes the chat's model (no arg → list them). Switching keeps the
// conversation: it just respawns the agent goroutine, and the new one reloads the same
// persisted transcript — only the provider changes.
func (g *Gateway) switchModel(ctx context.Context, chatID, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		g.replyMarkdown(ctx, chatID, modelsMessage(g.modelOrder, g.activeModel(chatID)))
		return
	}
	if _, ok := g.providers[name]; !ok {
		g.replyMarkdown(ctx, chatID, "⚠️ "+mdV2("Unknown model: "+name)+"\n\n"+modelsMessage(g.modelOrder, g.activeModel(chatID)))
		return
	}
	_ = g.mem.SetPref(chatID, "model", name)
	g.respawnSession(chatID)
	g.reply(ctx, chatID, "🔀 Switched to "+name+". (Conversation kept.)")
}

// respawnSession tears down the running agent but KEEPS the transcript and archive, so a
// fresh agent (e.g. on a different model) picks up the same conversation on the next message.
func (g *Gateway) respawnSession(chatID string) {
	g.mu.Lock()
	if cs, ok := g.chats[chatID]; ok {
		cs.cancel()
		delete(g.chats, chatID)
	}
	g.mu.Unlock()
}

// resetSession tears down a chat's running agent AND forgets its saved transcript, so
// the next message truly starts clean. Durable remember-notes are intentionally kept.
func (g *Gateway) resetSession(chatID string) {
	g.mu.Lock()
	if cs, ok := g.chats[chatID]; ok {
		cs.cancel()
		delete(g.chats, chatID)
	}
	g.mu.Unlock()
	_ = g.hist.Clear(chatID)
	_ = g.sessions.Close(chatID) // end the current session; its archive is kept & searchable
}

// reply sends a one-off plain message without blocking the poller goroutine.
func (g *Gateway) reply(ctx context.Context, chatID, text string) {
	go func() { _, _ = g.ch.SendText(ctx, chatID, text) }()
}

// replyMarkdown sends a one-off Markdown (MarkdownV2) message off the poller goroutine.
func (g *Gateway) replyMarkdown(ctx context.Context, chatID, text string) {
	go func() { _, _ = g.ch.SendMarkdown(ctx, chatID, text) }()
}

// commandName extracts a bare command from text like "/reset", "/reset@bot" or
// "/id now", returning ("reset", true). Non-commands return ("", false).
func commandName(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	word := strings.Fields(text)[0]        // "/id@bot"
	word = strings.SplitN(word, "@", 2)[0] // "/id"
	return strings.ToLower(strings.TrimPrefix(word, "/")), true
}

// commandArg returns everything after the command word, e.g. "/model gpt4o" -> "gpt4o".
func commandArg(text string) string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return ""
	}
	return strings.Join(fields[1:], " ")
}

// modelsMessage (MarkdownV2) lists the configured models, marking the current one.
func modelsMessage(names []string, current string) string {
	var b strings.Builder
	b.WriteString("🔀 *" + mdV2("Models") + "*\n\n")
	for _, n := range names {
		mark := "• "
		if n == current {
			mark = "✅ "
		}
		b.WriteString(mark + mdInline(n) + "\n")
	}
	b.WriteString("\n" + mdV2("Switch with /model <name>"))
	return b.String()
}

// skillsMessage (MarkdownV2) lists installed skills for the /skills command.
func skillsMessage(list []*skills.Skill) string {
	if len(list) == 0 {
		return "🧩 *" + mdV2("Skills") + "*\n\n" +
			mdV2("None installed yet. Drop a <name>/SKILL.md folder into my skills directory — or just ask me to make one — and it'll show up here.")
	}
	var b strings.Builder
	b.WriteString("🧩 *" + mdV2("Skills") + "*\n\n")
	for _, sk := range list {
		b.WriteString("• *" + mdV2(sk.Name) + "* — " + mdV2(sk.Description) + "\n")
	}
	return b.String()
}

// sessionsMessage (MarkdownV2) lists past conversations for the /sessions command.
func sessionsMessage(list []session.Meta) string {
	if len(list) == 0 {
		return "🗂 *" + mdV2("Sessions") + "*\n\n" +
			mdV2("No past conversations recorded yet. Everything we talk about is archived here and stays searchable — just ask me to recall something later.")
	}
	var b strings.Builder
	b.WriteString("🗂 *" + mdV2("Past conversations") + "*\n\n")
	for i, m := range list {
		if i >= 20 {
			b.WriteString(mdV2(fmt.Sprintf("…and %d more", len(list)-20)))
			break
		}
		title := m.Title
		if title == "" {
			title = "(untitled)"
		}
		b.WriteString("• " + mdV2(m.LastActive.Format("2006-01-02")) + " — " + mdV2(title) + "\n")
	}
	b.WriteString("\n" + mdV2("Ask me to recall anything from these — I can search across all of them."))
	return b.String()
}

// mcpMessage (MarkdownV2) lists connected MCP servers and their tool counts.
func mcpMessage(handles []*mcp.ToolHandle) string {
	if len(handles) == 0 {
		return "🔌 *" + mdV2("MCP") + "*\n\n" +
			mdV2("No MCP servers connected. Add them under \"mcp\" → \"servers\" in my config (a command + args, like an npx server) and restart me.")
	}
	var order []string
	counts := map[string]int{}
	for _, h := range handles {
		if _, ok := counts[h.ServerName]; !ok {
			order = append(order, h.ServerName)
		}
		counts[h.ServerName]++
	}
	var b strings.Builder
	b.WriteString("🔌 *" + mdV2("MCP servers") + "*\n\n")
	for _, s := range order {
		b.WriteString("• *" + mdV2(s) + "* — " + mdV2(fmt.Sprintf("%d tool(s)", counts[s])) + "\n")
	}
	return b.String()
}

// onboardMessage (MarkdownV2) is what an unauthorized chat sees: a welcome plus this
// chat's ID — shown as tap-to-copy code — so the owner can whitelist it.
func onboardMessage(chatID string) string {
	return "🦞 *Welcome to Lobster* — " + mdV2("a private AI assistant.") + "\n\n" +
		mdV2("This bot is private. Access is granted by chat ID, and yours is:") + "\n\n" +
		mdInline(chatID) + "\n\n" +
		mdV2("Add it to the bot's config and restart:") + "\n\n" +
		mdCodeBlock(`"auth": { "allowed_chats": ["`+chatID+`"] }`) + "\n\n" +
		mdV2("Then send me a message and I'll get to work.")
}

// helpMessage (MarkdownV2) is the CLI-style command reference for authorized users.
func helpMessage() string {
	return "🦞 *Lobster* — " + mdV2("your personal AI assistant") + "\n\n" +
		"*" + mdV2("Commands") + "*\n" +
		mdV2("/start — say hi / (re)introduce myself") + "\n" +
		mdV2("/setup — tune how I work with you") + "\n" +
		mdV2("/model — list / switch the model") + "\n" +
		mdV2("/skills — list my installed skills") + "\n" +
		mdV2("/sessions — browse our past conversations") + "\n" +
		mdV2("/mcp — show connected MCP servers") + "\n" +
		mdV2("/help — show this help") + "\n" +
		mdV2("/id — show your chat ID") + "\n" +
		mdV2("/reset — start a fresh conversation") + "\n\n" +
		mdV2("Just send a normal message and I'll get to work. I can run shell commands, read or write files, and send you photos.")
}
