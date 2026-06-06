// Command lobster is a fast, native, single-binary personal AI assistant.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aasm3535/lobster/internal/config"
	"github.com/aasm3535/lobster/internal/gateway"
	"github.com/aasm3535/lobster/internal/setup"
)

const version = "0.1.0"

func main() {
	// `lobster -r <code>` (resume a terminal session) is a top-level shortcut — handle it
	// before flag parsing, since it starts with a dash.
	if len(os.Args) > 1 && (os.Args[1] == "-r" || os.Args[1] == "--resume" || os.Args[1] == "-resume") {
		code := ""
		if len(os.Args) > 2 {
			code = os.Args[2]
		}
		if err := runTerminalChat(code); err != nil {
			log.Fatalf("tui: %v", err)
		}
		return
	}

	// Subcommands come before flags: `lobster setup` runs the wizard, `lobster run`
	// (or bare `lobster`) starts the bot. Strip a recognised subcommand so the flag
	// parser below still sees --config/--version.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "setup", "init":
			if err := setup.Run(version); err != nil {
				log.Fatalf("setup: %v", err)
			}
			return
		case "tui", "chat":
			if err := runTerminalChat(resumeArg(os.Args[2:])); err != nil {
				log.Fatalf("tui: %v", err)
			}
			return
		case "resume":
			code := ""
			if len(os.Args) > 2 {
				code = os.Args[2]
			}
			if err := runTerminalChat(code); err != nil {
				log.Fatalf("tui: %v", err)
			}
			return
		case "do", "ask", "p":
			if err := runOneShot(os.Args[2:]); err != nil {
				log.Fatalf("do: %v", err)
			}
			return
		case "run":
			os.Args = append(os.Args[:1], os.Args[2:]...)
		case "help":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
			usage()
			os.Exit(2)
		}
	}

	cfgPath := flag.String("config", "lobster.json", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("lobster", version)
		return
	}

	path := resolveConfigPath(*cfgPath)
	if !fileExists(path) {
		fmt.Print("🦞 No config found. Run setup:\n\n    lobster setup\n\n")
		return
	}

	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	setConsoleTitle("LOBSTER")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw, err := gateway.New(cfg)
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}

	log.Printf("🦞 lobster %s starting (%d model(s), default=%s/%s)", version, len(cfg.Models), cfg.Models[0].Type, cfg.Models[0].Model)
	if err := gw.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("run: %v", err)
	}
	log.Printf("lobster stopped")
}

// loadTerminalConfig loads the config for a terminal-side run (tui / do), where a missing
// Telegram token is fine — a placeholder is filled in if it's the only thing missing.
// Returns (nil, nil) when no config exists at all, after printing the setup hint.
func loadTerminalConfig() (*config.Config, error) {
	path := resolveConfigPath("lobster.json")
	if !fileExists(path) {
		fmt.Print("🦞 No config found. Run setup:\n\n    lobster setup\n\n")
		return nil, nil
	}
	cfg, err := config.Load(path)
	if err != nil && strings.Contains(err.Error(), "telegram.token") {
		_ = os.Setenv("LOBSTER_TELEGRAM_TOKEN", "terminal") // not used in terminal mode
		cfg, err = config.Load(path)
	}
	return cfg, err
}

// resumeArg extracts a session code from `tui` args: either `-r <code>` or a bare positional
// code (`lobster tui work`). Empty means the default session.
func resumeArg(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-r" || args[i] == "--resume" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if !strings.HasPrefix(args[i], "-") {
			return args[i]
		}
	}
	return ""
}

// runOneShot executes one prompt from the command line (`lobster do "fix the failing tests"`)
// and exits — same agent, memory and tools as the TUI, but scriptable. A piped stdin is
// appended to the prompt as context, so `git diff | lobster do "review this"` works.
func runOneShot(args []string) error {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		if data, err := io.ReadAll(os.Stdin); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			piped := strings.TrimSpace(string(data))
			if prompt == "" {
				prompt = piped
			} else {
				prompt += "\n\n--- piped input ---\n" + piped
			}
		}
	}
	if prompt == "" {
		fmt.Print("usage: lobster do \"<prompt>\"   (or pipe input:  git diff | lobster do \"review this\")\n")
		return nil
	}

	cfg, err := loadTerminalConfig()
	if err != nil || cfg == nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw, err := gateway.NewTerminal(cfg)
	if err != nil {
		return err
	}
	return gw.RunOnce(ctx, prompt)
}

// runTerminalChat starts the in-terminal agent (`lobster tui` / `lobster chat`): the same
// agent the Telegram bot runs, but typed in the terminal — no Telegram needed. sessionCode
// names which conversation to resume (empty = the default "local" session).
func runTerminalChat(sessionCode string) error {
	cfg, err := loadTerminalConfig()
	if err != nil || cfg == nil {
		return err
	}

	setConsoleTitle("LOBSTER")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw, err := gateway.NewTerminal(cfg)
	if err != nil {
		return err
	}
	return gw.RunTerminal(ctx, sessionCode)
}

// resolveConfigPath uses the given path if it exists, otherwise falls back to
// ~/.lobster/lobster.json so settings can live in a stable home directory.
func resolveConfigPath(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if home, err := config.Home(); err == nil {
		if fallback := filepath.Join(home, "lobster.json"); fileExists(fallback) {
			return fallback
		}
	}
	return path
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func usage() {
	fmt.Print(`🦞 lobster — personal AI assistant in your terminal

Usage:
  lobster setup     interactive first-run setup (token, provider, background)
  lobster tui       chat with the agent in your terminal (alias: lobster chat)
  lobster tui <code>   open/resume a named session (e.g. lobster tui work)
  lobster -r <code> resume a session by code (history + context restored)
  lobster do "..."  run one prompt and exit (scriptable; stdin is piped in as context)
  lobster           run the Telegram bot (alias: lobster run)
  lobster --version print version
  lobster help      show this help

Flags:
  --config <path>   config file (default: ./lobster.json, then ~/.lobster/lobster.json)
`)
}
