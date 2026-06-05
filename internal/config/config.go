// Package config loads Lobster's single JSON config file, with env overrides for secrets.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// envRef matches ${NAME} references that get expanded from the environment in the config.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// loadDotEnv reads a KEY=VALUE file into the process environment so secrets (API keys,
// MCP server tokens) live there instead of the config. A real environment variable
// already set wins, and spawned MCP subprocesses inherit these automatically. A missing
// file is fine.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}

// expandEnvRefs replaces ${NAME} in s with the environment value (empty if unset). Only
// the braced form is touched, so a literal "$" in a token is never mangled.
func expandEnvRefs(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(m[2 : len(m)-1])
	})
}

// Home returns Lobster's persistent state directory (~/.lobster), creating it. This is
// where memory and config live so they survive restarts regardless of the working dir.
func Home() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(h, ".lobster")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

type Config struct {
	Telegram    TelegramConfig `json:"telegram"`
	Provider    ProviderConfig `json:"provider"`
	Auth        AuthConfig     `json:"auth"`
	MCP         MCPConfig      `json:"mcp"`
	System      string         `json:"system"`
	MemoryFile  string         `json:"memory_file"`
	HistoryDir  string         `json:"history_dir"`
	SessionsDir string         `json:"sessions_dir"`
	SkillsDir   string         `json:"skills_dir"`
	Verbosity   string         `json:"verbosity"` // default working-timeline level: quiet | normal | verbose
	Streaming   string         `json:"streaming"` // "on" (type replies out live) or "off"
	MaxSteps    int            `json:"max_steps"` // -1 = no limit (full autonomy)

	// Path is the file this config was loaded from (so the agent can find and edit it).
	Path string `json:"-"`
}

type TelegramConfig struct {
	Token string `json:"token"`
}

// AuthConfig gates who may talk to the bot. The bot is LOCKED by default: a chat is
// admitted only if its ID is in AllowedChats (the normal path — a user runs /start to
// learn their ID and you add it here). Set Open to disable the gate entirely for local
// dev. AccessCode is an optional shared-secret shortcut to unlock a chat without a
// config edit + restart.
type AuthConfig struct {
	// Open disables auth completely — anyone who messages the bot is admitted.
	Open bool `json:"open"`
	// AllowedChats are chat IDs that are authorized. This is the primary mechanism:
	// a user sends /start, the bot replies with their chat ID, you paste it here.
	AllowedChats []string `json:"allowed_chats"`
	// AccessCode is an optional shared secret; sending it as a message unlocks that
	// chat for the rest of the run. Leave empty to rely on AllowedChats alone.
	AccessCode string `json:"access_code"`
}

// MCPConfig lists Model Context Protocol servers to connect at startup. Each server is
// spawned over stdio and its tools become available to the agent alongside the natives.
type MCPConfig struct {
	Servers []MCPServer `json:"servers"`
}

type MCPServer struct {
	Name     string            `json:"name"`    // short label, prefixes the server's tools
	Command  string            `json:"command"` // executable, e.g. "npx"
	Args     []string          `json:"args"`    // e.g. ["-y","@modelcontextprotocol/server-filesystem","/path"]
	Env      map[string]string `json:"env"`     // extra environment variables
	Disabled bool              `json:"disabled"`
}

// ProviderConfig points at any OpenAI- or Anthropic-compatible endpoint. Type selects
// the wire protocol; AuthScheme and Headers let you adapt to a specific host (e.g. a
// gateway that wants a Bearer token, or extra org/version headers) — so a "custom
// provider" is just the right protocol plus the right auth, no special-casing needed.
type ProviderConfig struct {
	Type       string            `json:"type"`        // "openai", "anthropic", or "minimax" (preset)
	BaseURL    string            `json:"base_url"`    // endpoint root
	APIKey     string            `json:"api_key"`     // supports ${ENV_VAR} references
	Model      string            `json:"model"`       //
	MaxTokens  int               `json:"max_tokens"`  //
	AuthScheme string            `json:"auth_scheme"` // "bearer", "x-api-key", or "none" (default per type)
	Headers    map[string]string `json:"headers"`     // extra static request headers
}

const defaultSystem = `You are Lobster (🦞 "Крабик"), a personal AI assistant that lives on the user's own machine and talks to them through Telegram. You have REAL tools — you run shell commands and read/write files on the host — so you actually do things instead of just describing them.

# Personality & vibe
- You're warm, sharp, and a little playful: a capable friend who happens to live in the terminal, not a corporate help desk.
- Mirror the user's language and energy. If they write Russian, reply in Russian. Match their register — casual when they're casual, focused when they're focused.
- You don't have a fixed "vibe" with this person yet — co-create it. Learn how they want you to talk to them and lean into it more over time.
- Be concise. This is a chat, not an essay: short messages, get to the point, no filler.

# Getting to know them (lightly — never a form)
- In your first interactions, if you don't already know it, casually find out their name and a little about them — ONE small question at a time, woven into the conversation. Never interrogate.
- Ask what vibe they want from you ("как тебе удобнее, чтобы я с тобой общался?"). If they're not sure, tell them you'll figure it out together and adapt as you go.
- When you learn something durable — their name, how they like to be addressed, the vibe they want, their projects, preferences, recurring tasks, OR a useful discovery (where a project lives on disk, a decision you both made) — save it with the remember tool right away, without being asked. The full conversation is kept across restarts, but it scrolls; remember is for the facts that must never be lost. Save facts worth recalling, not small talk.
- Use what you already know (it's given to you below) to greet them by name and match their style. Never re-ask something you already know.
- You also have a PERMANENT, searchable archive of every past conversation. The current context is just a rolling window — older turns scroll out of it but are NOT lost. So if the user refers to something from before that you can't see right now, search_sessions for it BEFORE saying you don't remember. Never tell them "you didn't say anything before" without checking the archive first.

# How you work — autonomous, quiet, decisive
- You are fully autonomous. Make your own decisions and carry tasks through to the end yourself — there is no step limit. Don't stop halfway to ask "should I continue?"; just continue. Only ask the user when you genuinely hit a choice that's truly theirs to make (irreversible, a real preference, or you're blocked).
- Be quiet while you work. Send ONE short acknowledgement at the very start ("ок, гляну…") and then DON'T narrate every step — no "сейчас гляну", "сейчас догляну", "сейчас соберу" between tool calls. Work, then give one clear final result. (How much progress detail shows is controlled by the verbosity setting; respect it.)
- Report what actually happened — concretely, with real results, not guesses.
- Shell: prefer bash (shell:"bash") for searching code and Unix tooling (grep -r, find, rg, sed). Use PowerShell only when it's genuinely better. Don't fight a shell's syntax — switch.
- background_run is ONLY for genuinely long work (30s+: full-disk scans, big builds/downloads). Do NOT background quick checks like tsc, eslint, or a single grep — just run them inline. Don't spawn a pile of background jobs.

# Configuring yourself & reading the person
- You're a highly configurable agent: the user shapes how you work so you're maximally useful for THEM. The deep version lives behind /setup — a warm, one-question-at-a-time interview about who they are, their name, what they want you for (their goals), how techy they are, the tone they like, and how much detail to show. On first contact you can lightly offer it ("хочешь, подстроюсь под тебя — пару вопросов?"), but offer once and don't pester; it's occasional, not every message.
- Calibrate to the person. Gauge how technical they are and adapt: for a non-technical user, drop all jargon, explain in plain words, and NEVER ask them about shells, CLIs, verbosity or settings — just choose sensible defaults yourself. For a technical user you can be terse and precise. Save what you learn with configure (verbosity, tech_level, shell) and remember (name, goals, tone, facts), and honour it afterwards.
- Lead with their goals. The point of knowing them is to actually help with what THEY care about — keep their goals in mind and bring them up.

# Telegram
- Write replies in normal Markdown — **bold**, *italic*, ` + "`code`" + `, fenced code blocks, - bullet lists, [links](url). It's converted to Telegram formatting for you, so just write naturally.
- You can attach images with the send_photo tool (a local file path or an http(s) URL, plus an optional caption).
- The user can send YOU photos, and you can actually see them. When an image comes in, look at it and respond to what's in it (describe, read text, answer their question about it).

When a request needs the system, use your tools and report what actually happened.`

// Load reads the config file and applies env overrides + defaults.
func Load(path string) (*Config, error) {
	// Pull secrets in from ~/.lobster/.env first, so ${VAR} references below resolve and
	// MCP subprocesses inherit them.
	if home, herr := Home(); herr == nil {
		loadDotEnv(filepath.Join(home, ".env"))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	data = []byte(expandEnvRefs(string(data))) // resolve ${VAR} from the environment / .env

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.Path = path

	// Secrets can also come from the environment (keeps keys out of the file).
	if v := os.Getenv("LOBSTER_TELEGRAM_TOKEN"); v != "" {
		c.Telegram.Token = v
	}
	if v := os.Getenv("LOBSTER_API_KEY"); v != "" {
		c.Provider.APIKey = v
	}
	if v := os.Getenv("LOBSTER_BASE_URL"); v != "" {
		c.Provider.BaseURL = v
	}
	if v := os.Getenv("LOBSTER_MODEL"); v != "" {
		c.Provider.Model = v
	}
	if v := os.Getenv("LOBSTER_ACCESS_CODE"); v != "" {
		c.Auth.AccessCode = v
	}

	if c.MaxSteps == 0 {
		c.MaxSteps = 40 // sensible default; set max_steps to -1 for no limit (full autonomy)
	}
	if c.Verbosity == "" {
		c.Verbosity = "normal"
	}
	if c.Streaming == "" {
		c.Streaming = "on"
	}
	if c.Provider.MaxTokens == 0 {
		c.Provider.MaxTokens = 4096
	}
	if c.System == "" {
		c.System = defaultSystem
	}
	if c.MemoryFile == "" {
		// Default the persistent profile into ~/.lobster so it outlives the cwd.
		if home, herr := Home(); herr == nil {
			c.MemoryFile = filepath.Join(home, "memory.json")
		} else {
			c.MemoryFile = "lobster.memory.json"
		}
	}
	if c.HistoryDir == "" {
		if home, herr := Home(); herr == nil {
			c.HistoryDir = filepath.Join(home, "history")
		} else {
			c.HistoryDir = "lobster.history"
		}
	}
	if c.SessionsDir == "" {
		if home, herr := Home(); herr == nil {
			c.SessionsDir = filepath.Join(home, "sessions")
		} else {
			c.SessionsDir = "lobster.sessions"
		}
	}
	if c.SkillsDir == "" {
		if home, herr := Home(); herr == nil {
			c.SkillsDir = filepath.Join(home, "skills")
		} else {
			c.SkillsDir = "lobster.skills"
		}
	}

	if c.Telegram.Token == "" {
		return nil, fmt.Errorf("telegram.token is required (set it in the config or LOBSTER_TELEGRAM_TOKEN)")
	}
	if c.Provider.Type == "" {
		return nil, fmt.Errorf("provider.type is required (\"openai\", \"anthropic\" or \"minimax\")")
	}
	if c.Provider.Model == "" {
		return nil, fmt.Errorf("provider.model is required")
	}
	return &c, nil
}
