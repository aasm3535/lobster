//go:build !windows

package gateway

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

// On Unix we drive the terminal through stty (always present, no cgo/deps): save the current
// settings, switch to a raw-ish mode (character-at-a-time, no echo), and restore on exit.
// Arrow/nav keys already arrive as ANSI escape sequences, and Ctrl-C still raises SIGINT
// (isig stays on), which the signal handler turns into context cancellation.

func enableRawInput() (func(), bool) {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return nil, false // not a tty (piped) — caller falls back to the line REPL
	}
	saved, err := sttyOutput("-g")
	if err != nil {
		return nil, false
	}
	if err := stty("-echo", "-icanon", "min", "1", "time", "0"); err != nil {
		return nil, false
	}
	restore := func() { _ = stty(strings.TrimSpace(saved)) }
	return restore, true
}

func terminalSize() (int, int) {
	out, err := sttyOutput("size")
	if err != nil {
		return 24, 80
	}
	var rows, cols int
	if _, err := fmt.Sscan(strings.TrimSpace(out), &rows, &cols); err != nil {
		return 24, 80
	}
	if rows < 8 {
		rows = 24
	}
	if cols < 20 {
		cols = 80
	}
	return rows, cols
}

// watchResize reports the size now and on every SIGWINCH (terminal resize). Returns a stop
// func that unregisters the handler.
func watchResize(cb func(rows, cols int)) func() {
	r, c := terminalSize()
	cb(r, c)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				signal.Stop(ch)
				return
			case <-ch:
				cb(terminalSize())
			}
		}
	}()
	return func() { close(stop) }
}

func stty(args ...string) error {
	c := exec.Command("stty", args...)
	c.Stdin = os.Stdin
	return c.Run()
}

func sttyOutput(args ...string) (string, error) {
	c := exec.Command("stty", args...)
	c.Stdin = os.Stdin
	b, err := c.Output()
	return string(b), err
}
