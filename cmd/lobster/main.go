// Command lobster is a fast, native, single-binary personal AI assistant.
package main

import (
	"context"
	"flag"
	"fmt"
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
			if err := runTerminalChat(); err != nil {
				log.Fatalf("tui: %v", err)
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

// runTerminalChat starts the in-terminal agent (`lobster tui` / `lobster chat`): the same
// agent the Telegram bot runs, but typed in the terminal — no Telegram needed. A config
// without a Telegram token is fine here, so we fill a placeholder if it's the only thing
// missing.
func runTerminalChat() error {
	path := resolveConfigPath("lobster.json")
	if !fileExists(path) {
		fmt.Print("🦞 No config found. Run setup:\n\n    lobster setup\n\n")
		return nil
	}
	cfg, err := config.Load(path)
	if err != nil && strings.Contains(err.Error(), "telegram.token") {
		_ = os.Setenv("LOBSTER_TELEGRAM_TOKEN", "terminal") // not used in terminal mode
		cfg, err = config.Load(path)
	}
	if err != nil {
		return err
	}

	setConsoleTitle("LOBSTER")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw, err := gateway.NewTerminal(cfg)
	if err != nil {
		return err
	}
	return gw.RunTerminal(ctx)
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
  lobster           run the Telegram bot (alias: lobster run)
  lobster --version print version
  lobster help      show this help

Flags:
  --config <path>   config file (default: ./lobster.json, then ~/.lobster/lobster.json)
`)
}
