package gateway

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// copyToClipboard puts text on the system clipboard using the platform's native tool, so
// copying a reply is reliable even though the alt-screen TUI makes mouse selection awkward.
func copyToClipboard(text string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "windows":
		name, args = "clip", nil
	case "darwin":
		name, args = "pbcopy", nil
	default: // linux/bsd — try Wayland then X11
		if p, _ := exec.LookPath("wl-copy"); p != "" {
			name = "wl-copy"
		} else if p, _ := exec.LookPath("xclip"); p != "" {
			name, args = "xclip", []string{"-selection", "clipboard"}
		} else if p, _ := exec.LookPath("xsel"); p != "" {
			name, args = "xsel", []string{"--clipboard", "--input"}
		} else {
			return fmt.Errorf("no clipboard tool found (install wl-copy, xclip or xsel)")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
