package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maxToolOutput = 16000

// RegisterBuiltins adds Lobster's core native tools: shell + basic file access.
func RegisterBuiltins(r *Registry) {
	r.Register(Tool{
		Name:        "shell",
		Description: shellDescription(),
		Schema: object(map[string]any{
			"command":     prop("string", "The shell command to run."),
			"shell":       enumProp("string", "Which shell to use: \"bash\" for Unix-style commands (grep, find, rg), or \"powershell\". Defaults to the host's native shell.", "bash", "powershell"),
			"timeout_sec": prop("integer", "Optional timeout in seconds (default 60)."),
		}, "command"),
		Run: runShell,
	})

	r.Register(Tool{
		Name:        "read_file",
		Description: "Read the contents of a text file.",
		Schema: object(map[string]any{
			"path": prop("string", "Path to the file."),
		}, "path"),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			data, err := os.ReadFile(a.Path)
			if err != nil {
				return "", err
			}
			return clip(string(data)), nil
		},
	})

	r.Register(Tool{
		Name:        "write_file",
		Description: "Write (create or overwrite) a text file. Creates parent directories as needed.",
		Schema: object(map[string]any{
			"path":    prop("string", "Path to the file."),
			"content": prop("string", "Full file contents."),
		}, "path", "content"),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if dir := filepath.Dir(a.Path); dir != "" && dir != "." {
				_ = os.MkdirAll(dir, 0o755)
			}
			if err := os.WriteFile(a.Path, []byte(a.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path), nil
		},
	})

	r.Register(Tool{
		Name:        "list_dir",
		Description: "List the entries in a directory.",
		Schema: object(map[string]any{
			"path": prop("string", "Directory path (default current directory)."),
		}),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(args, &a)
			if a.Path == "" {
				a.Path = "."
			}
			entries, err := os.ReadDir(a.Path)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			for _, e := range entries {
				suffix := ""
				if e.IsDir() {
					suffix = "/"
				}
				fmt.Fprintf(&b, "%s%s\n", e.Name(), suffix)
			}
			if b.Len() == 0 {
				return "[empty directory]", nil
			}
			return clip(b.String()), nil
		},
	})
}

// shellDescription tells the model which shells it can drive, so it doesn't reach for
// the wrong syntax (e.g. bash's && on a Windows PowerShell host).
func shellDescription() string {
	base := "Run a shell command on the host and return combined stdout+stderr. " +
		"Use this for any system task: inspecting files, running programs, git, package managers, etc."
	if runtime.GOOS == "windows" {
		desc := base + " The default host shell is Windows PowerShell 5.1 — in PowerShell, chain commands with ';' (NOT '&&'/'||'), " +
			"list files with Get-ChildItem, read env vars as $env:VAR."
		if findBash() != "" {
			desc += " Bash (Git Bash) is ALSO available — pass shell:\"bash\" to use Unix tools like grep -r, find, rg, sed, awk; " +
				"this is usually easier than PowerShell for searching code."
		}
		return desc
	}
	return base + " The host shell is POSIX sh (or pass shell:\"bash\")."
}

func runShell(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Command    string `json:"command"`
		Shell      string `json:"shell"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("command is empty")
	}
	if a.TimeoutSec <= 0 {
		a.TimeoutSec = 60
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(a.TimeoutSec)*time.Second)
	defer cancel()

	cmd, err := shellCommand(cctx, strings.ToLower(strings.TrimSpace(a.Shell)), a.Command)
	if err != nil {
		return "[" + err.Error() + "]", nil
	}
	out, err := cmd.CombinedOutput()
	res := clip(string(out))
	if cctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("command timed out after %ds", a.TimeoutSec)
	}
	if err != nil {
		// A non-zero exit is normal information for the agent, not a transport error.
		if res == "" {
			return fmt.Sprintf("[command failed: %v]", err), nil
		}
		return res + fmt.Sprintf("\n[exit: %v]", err), nil
	}
	if res == "" {
		res = "[no output]"
	}
	return res, nil
}

// shellCommand builds the exec.Cmd for the chosen shell. "bash" runs Git Bash / sh;
// otherwise the host's native shell is used (PowerShell on Windows, sh elsewhere).
func shellCommand(ctx context.Context, shell, command string) (*exec.Cmd, error) {
	if shell == "bash" {
		bash := findBash()
		if bash == "" {
			return nil, fmt.Errorf("bash not found on host")
		}
		return exec.CommandContext(ctx, bash, "-lc", command), nil
	}
	if runtime.GOOS == "windows" {
		// Force UTF-8 on the redirected stream; PowerShell 5.1 otherwise emits the
		// console's OEM code page (e.g. cp866), which turns Cyrillic into mojibake.
		wrapped := "$OutputEncoding=[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " + command
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", wrapped), nil
	}
	return exec.CommandContext(ctx, "sh", "-c", command), nil
}

// findBash locates a real bash interpreter. On Windows it prefers Git Bash over a
// bare "bash" on PATH, because the latter is usually the WSL launcher stub in
// WindowsApps, which errors out when no WSL distro is installed.
func findBash() string {
	for _, p := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("bash"); err == nil && !strings.Contains(strings.ToLower(p), `windowsapps`) {
		return p
	}
	return ""
}

func clip(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	return s[:maxToolOutput] + fmt.Sprintf("\n…[truncated %d bytes]", len(s)-maxToolOutput)
}

// --- tiny JSON Schema helpers ---

func object(props map[string]any, required ...string) map[string]any {
	m := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func enumProp(typ, desc string, values ...string) map[string]any {
	return map[string]any{"type": typ, "description": desc, "enum": values}
}
