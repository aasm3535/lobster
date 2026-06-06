// Package setup is Lobster's first-run wizard: `lobster setup` walks a new user through
// the Telegram token + provider config, writes ~/.lobster/lobster.json (+ secrets to
// ~/.lobster/.env), and optionally installs the bot to run in the background. It's a
// plain stdin/ANSI TUI — no dependencies, works the same on Linux and Windows.
package setup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// --- colours -----------------------------------------------------------------
// A warm lobster palette: coral/red shades for the banner, salmon for prompts,
// grey for hints. Disabled when output isn't a terminal or NO_COLOR is set.

var colorOK = true

func paint(code int, s string) string {
	if !colorOK {
		return s
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", code, s)
}

func bold(s string) string {
	if !colorOK {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}

const (
	colCoral = 209 // prompts / accents
	colDeep  = 203 // headers
	colGrey  = 245 // hints
	colGreen = 114 // success
	colRed   = 196 // errors
)

// bannerReds shades each line of the wordmark from light coral down to deep red.
var bannerReds = []int{217, 210, 209, 203, 167, 131}

var banner = []string{
	`  ██╗      ██████╗ ██████╗ ███████╗████████╗███████╗██████╗ `,
	`  ██║     ██╔═══██╗██╔══██╗██╔════╝╚══██╔══╝██╔════╝██╔══██╗`,
	`  ██║     ██║   ██║██████╔╝███████╗   ██║   █████╗  ██████╔╝`,
	`  ██║     ██║   ██║██╔══██╗╚════██║   ██║   ██╔══╝  ██╔══██╗`,
	`  ███████╗╚██████╔╝██████╔╝███████║   ██║   ███████╗██║  ██║`,
	`  ╚══════╝ ╚═════╝ ╚═════╝ ╚══════╝   ╚═╝   ╚══════╝╚═╝  ╚═╝`,
}

func printBanner() {
	fmt.Println()
	for i, line := range banner {
		fmt.Println(paint(bannerReds[i%len(bannerReds)], line))
	}
	fmt.Println(paint(colGrey, "        🦞  your AI assistant, right in the terminal"))
	fmt.Println()
}

// --- prompt helpers ----------------------------------------------------------

type prompter struct{ in *bufio.Reader }

// ask reads a line. If required, it re-asks until something is entered; otherwise an
// empty answer falls back to def (shown as a hint).
func (p prompter) ask(label, def string, required bool) string {
	for {
		hint := ""
		if def != "" {
			hint = paint(colGrey, "  ["+def+"]")
		}
		fmt.Print(paint(colCoral, "› ") + label + hint + paint(colGrey, ": "))
		line, err := p.in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			if def != "" {
				return def
			}
			if !required {
				return ""
			}
			// EOF / closed stdin: re-asking would loop forever (this happens when setup is
			// run from a pipe without a real terminal). Bail with a clear hint.
			if err != nil {
				fmt.Println(paint(colRed, "  no input — run setup in a terminal:  lobster setup"))
				os.Exit(1)
			}
			fmt.Println(paint(colRed, "  required"))
			continue
		}
		return line
	}
}

func (p prompter) askInt(label string, def int) int {
	for {
		s := p.ask(label, strconv.Itoa(def), false)
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
			return n
		}
		fmt.Println(paint(colRed, "  enter a whole number"))
	}
}

// askChoice accepts either the number or the literal option text; default is index 0.
func (p prompter) askChoice(label string, opts []string) string {
	fmt.Println(bold(label) + paint(colGrey, "  (default "+opts[0]+")"))
	for i, o := range opts {
		fmt.Printf("   %s %s\n", paint(colCoral, strconv.Itoa(i+1)+")"), o)
	}
	for {
		s := p.ask("choice", opts[0], false)
		s = strings.TrimSpace(s)
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(opts) {
			return opts[n-1]
		}
		for _, o := range opts {
			if strings.EqualFold(o, s) {
				return o
			}
		}
		fmt.Println(paint(colRed, "  pick a number or name from the list"))
	}
}

func (p prompter) askYesNo(label string, def bool) bool {
	d := "y"
	if !def {
		d = "n"
	}
	s := strings.ToLower(p.ask(label+paint(colGrey, " (y/n)"), d, false))
	return s == "y" || s == "yes"
}

func section(title string) {
	fmt.Println()
	fmt.Println(paint(colDeep, "── "+title+" "+strings.Repeat("─", max(0, 40-len([]rune(title))))))
}

// --- wizard ------------------------------------------------------------------

// Run executes the interactive setup. version is shown in the header.
func Run(version string) error {
	colorOK = colorsEnabled()
	enableANSI() // no-op except on Windows, where it turns on ANSI escape handling

	printBanner()
	fmt.Println(paint(colGrey, "  setup "+version+" — a few questions and you're live. Press Enter to accept the [bracketed] value.\n"))

	home, err := homeDir()
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(home, "lobster.json")
	envPath := filepath.Join(home, ".env")

	if fileExists(cfgPath) {
		fmt.Println(paint(colGrey, "  Config already exists: "+cfgPath))
		if !(prompter{bufio.NewReader(os.Stdin)}).askYesNo("Overwrite it?", false) {
			fmt.Println(paint(colGrey, "  Ok, leaving everything as is."))
			return nil
		}
		_ = os.Rename(cfgPath, cfgPath+".bak") // keep a backup just in case
		fmt.Println(paint(colGrey, "  (old one saved to "+cfgPath+".bak)"))
	}

	p := prompter{bufio.NewReader(os.Stdin)}

	section("Telegram")
	fmt.Println(paint(colGrey, "  Get a bot token from @BotFather (/newbot)."))
	token := p.ask("Telegram bot token", "", true)

	section("Model provider")
	ptype := p.askChoice("API type", []string{"anthropic", "openai", "fireworks", "minimax"})
	name := p.ask("Preset name (shown in /model)", ptype, false)
	baseURL := p.ask("Base URL", defaultBaseURL(ptype), false)
	apiKey := p.ask("API token", "", true)
	model := p.ask("Model ID", defaultModel(ptype), false)
	maxTokens := p.askInt("Max response tokens (max_tokens)", 4096)

	section("Access")
	fmt.Println(paint(colGrey, "  The bot is locked by default. Enter your chat ID now,"))
	fmt.Println(paint(colGrey, "  or send the bot /start later and it'll tell you your ID."))
	chatID := p.ask("Your Telegram chat ID (Enter to skip)", "", false)

	// Secrets → .env; the config references them as ${VAR} so the file stays shareable.
	env := map[string]string{
		"LOBSTER_TELEGRAM_TOKEN": token,
		"LOBSTER_API_KEY":        apiKey,
	}
	if err := upsertEnv(envPath, env); err != nil {
		return fmt.Errorf("write .env: %w", err)
	}

	cfg := fileConfig{}
	cfg.Telegram.Token = "${LOBSTER_TELEGRAM_TOKEN}"
	if chatID != "" {
		cfg.Auth.AllowedChats = []string{chatID}
	} else {
		cfg.Auth.AllowedChats = []string{}
	}
	cfg.Models = []fileModel{{
		Name:      name,
		Type:      ptype,
		BaseURL:   baseURL,
		APIKey:    "${LOBSTER_API_KEY}",
		Model:     model,
		MaxTokens: maxTokens,
	}}
	if err := writeJSON(cfgPath, cfg); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Println()
	fmt.Println(paint(colGreen, "  ✓ Done!"))
	fmt.Println(paint(colGrey, "    config:   ") + cfgPath)
	fmt.Println(paint(colGrey, "    secrets:  ") + envPath + paint(colGrey, "  (don't commit!)"))
	fmt.Println()

	section("Run in background")
	if p.askYesNo("Start the bot in the background now?", true) {
		exe, _ := os.Executable()
		installBackground(exe, cfgPath, home)
	} else {
		fmt.Println(paint(colGrey, "  Start it manually:  ") + bold("lobster"))
	}
	fmt.Println()
	if chatID == "" {
		fmt.Println(paint(colCoral, "  → ") + "Send the bot " + bold("/start") + " on Telegram, add your ID to allowed_chats, then restart.")
	} else {
		fmt.Println(paint(colCoral, "  → ") + "Message the bot on Telegram — it already knows you.")
	}
	return nil
}

// --- background install ------------------------------------------------------

func installBackground(exe, cfgPath, home string) {
	if exe == "" {
		fmt.Println(paint(colRed, "  couldn't find my own path — start it manually: lobster"))
		return
	}
	switch runtime.GOOS {
	case "linux":
		installSystemd(exe, cfgPath, home)
	case "windows":
		installWindows(exe, cfgPath, home)
	default:
		spawnDetached(exe, cfgPath, home)
		fmt.Println(paint(colGreen, "  ✓ running in the background"))
	}
}

// installSystemd writes a per-user systemd unit and enables it — survives reboot, no root.
func installSystemd(exe, cfgPath, home string) {
	unitDir := filepath.Join(home, "..", ".config", "systemd", "user")
	if h, err := os.UserHomeDir(); err == nil {
		unitDir = filepath.Join(h, ".config", "systemd", "user")
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		fmt.Println(paint(colRed, "  systemd: "+err.Error()))
		spawnDetached(exe, cfgPath, home)
		return
	}
	unit := fmt.Sprintf(`[Unit]
Description=Lobster Telegram AI assistant
After=network-online.target

[Service]
ExecStart=%q run --config %q
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`, exe, cfgPath)
	unitPath := filepath.Join(unitDir, "lobster.service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		fmt.Println(paint(colRed, "  systemd: "+err.Error()))
		spawnDetached(exe, cfgPath, home)
		return
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", "--now", "lobster.service").Run(); err != nil {
		fmt.Println(paint(colGrey, "  unit written: "+unitPath))
		fmt.Println(paint(colGrey, "  run:  systemctl --user enable --now lobster.service"))
		return
	}
	fmt.Println(paint(colGreen, "  ✓ systemd service started"))
	fmt.Println(paint(colGrey, "    logs:  journalctl --user -u lobster -f"))
	fmt.Println(paint(colGrey, "    stop:  systemctl --user stop lobster"))
}

// installWindows starts the bot detached now and registers a logon task for persistence.
func installWindows(exe, cfgPath, home string) {
	spawnDetached(exe, cfgPath, home)
	fmt.Println(paint(colGreen, "  ✓ running in the background"))
	fmt.Println(paint(colGrey, "    logs:  "+filepath.Join(home, "lobster.log")))

	tr := fmt.Sprintf(`"%s" run --config "%s"`, exe, cfgPath)
	err := exec.Command("schtasks", "/Create", "/SC", "ONLOGON", "/TN", "Lobster",
		"/TR", tr, "/RL", "LIMITED", "/F").Run()
	if err != nil {
		fmt.Println(paint(colGrey, "  (auto-start at logon not configured — you can add it via Task Scheduler)"))
		return
	}
	fmt.Println(paint(colGreen, "  ✓ auto-start at logon configured (task \"Lobster\")"))
	fmt.Println(paint(colGrey, "    remove:  schtasks /Delete /TN Lobster /F"))
}

// spawnDetached starts the bot as an independent process, logging to ~/.lobster/lobster.log.
// The child keeps running after this wizard exits.
func spawnDetached(exe, cfgPath, home string) {
	logPath := filepath.Join(home, "lobster.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	cmd := exec.Command(exe, "run", "--config", cfgPath)
	if err == nil {
		cmd.Stdout, cmd.Stderr = f, f
	}
	detach(cmd) // platform-specific flags so it survives the parent (no-op off Windows)
	if err := cmd.Start(); err != nil {
		fmt.Println(paint(colRed, "  couldn't start: "+err.Error()))
		return
	}
	_ = cmd.Process.Release()
}

// --- file writing ------------------------------------------------------------

// fileConfig is the minimal config the wizard emits — just enough to boot. Lobster fills
// in every other default at load time, so we keep the written file small and readable.
type fileConfig struct {
	Telegram struct {
		Token string `json:"token"`
	} `json:"telegram"`
	Auth struct {
		AllowedChats []string `json:"allowed_chats"`
	} `json:"auth"`
	Models []fileModel `json:"models"`
}

type fileModel struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// upsertEnv merges keys into a KEY=VALUE file, preserving any existing lines (other
// secrets, comments) and updating in place — so re-running setup never wipes the file.
func upsertEnv(path string, kv map[string]string) error {
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	}
	done := map[string]bool{}
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, _, ok := strings.Cut(t, "=")
		k = strings.TrimSpace(k)
		if ok {
			if v, want := kv[k]; want {
				lines[i] = k + "=" + v
				done[k] = true
			}
		}
	}
	for k, v := range kv {
		if !done[k] {
			lines = append(lines, k+"="+v)
		}
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o600)
}

// --- small helpers -----------------------------------------------------------

func defaultBaseURL(t string) string {
	switch t {
	case "anthropic":
		return "https://api.anthropic.com"
	case "minimax":
		return "https://api.minimax.io/anthropic"
	case "openai":
		return "https://api.openai.com/v1"
	case "fireworks":
		return "https://api.fireworks.ai/inference/v1"
	}
	return ""
}

func defaultModel(t string) string {
	switch t {
	case "anthropic":
		return "claude-sonnet-4-5"
	case "minimax":
		return "MiniMax-M3"
	case "fireworks":
		return "accounts/fireworks/models/deepseek-v4-flash"
	}
	return ""
}

func homeDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(h, ".lobster")
	return dir, os.MkdirAll(dir, 0o700)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// colorsEnabled is false when NO_COLOR is set or stdout isn't a real terminal (piped),
// so a redirected run produces clean text instead of escape soup.
func colorsEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
