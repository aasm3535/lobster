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
	"syscall"

	"lobster/internal/config"
	"lobster/internal/gateway"
)

const version = "0.1.0"

func main() {
	cfgPath := flag.String("config", "lobster.json", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("lobster", version)
		return
	}

	cfg, err := config.Load(resolveConfigPath(*cfgPath))
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw, err := gateway.New(cfg)
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}

	log.Printf("🦞 lobster %s starting (provider=%s model=%s)", version, cfg.Provider.Type, cfg.Provider.Model)
	if err := gw.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("run: %v", err)
	}
	log.Printf("lobster stopped")
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
