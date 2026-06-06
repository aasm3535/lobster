package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// expandMentions turns "@path" file tags in a message into real context for the agent: each
// referenced file that exists is appended (clipped) after the message, so the visible chat
// keeps just the tag while the agent actually receives the file's contents.
func (g *Gateway) expandMentions(text string) string {
	seen := map[string]bool{}
	var attach []string
	for _, f := range strings.Fields(text) {
		if len(f) < 2 || !strings.HasPrefix(f, "@") {
			continue
		}
		p := strings.TrimRight(f[1:], ".,;:)]}")
		if p == "" || seen[p] {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		seen[p] = true
		content := string(data)
		if len(content) > 16000 {
			content = content[:16000] + "\n…[truncated]"
		}
		attach = append(attach, fmt.Sprintf("--- %s ---\n%s", p, content))
	}
	if len(attach) == 0 {
		return text
	}
	return text + "\n\n(Referenced files — full contents below:)\n\n" + strings.Join(attach, "\n\n")
}

// suggestion is one entry in the TUI's "/" command palette or "@" model picker shown above
// the input. insert is what replaces the input when accepted; submit means Enter runs it
// immediately (arg-less commands, a chosen model/workflow) rather than just completing.
type suggestion struct {
	insert string
	label  string
	desc   string
	submit bool
}

// tuiCommand describes a slash command for the palette.
type tuiCommand struct {
	name string
	desc string
	arg  bool // takes an argument (so completing it leaves a trailing space, not a run)
}

var tuiCommands = []tuiCommand{
	{"model", "switch the model", true},
	{"goal", "pin a goal — work until done", true},
	{"agents", "inspect running subagents", false},
	{"copy", "copy the last reply to the clipboard", false},
	{"select", "selection mode — freeze screen for mouse copy", false},
	{"workflow", "run a saved workflow", true},
	{"skills", "list installed skills", false},
	{"sessions", "past conversations", false},
	{"schedules", "scheduled tasks", false},
	{"mcp", "connected MCP servers", false},
	{"reset", "start a fresh conversation", false},
	{"clear", "clear the screen", false},
	{"help", "show help", false},
	{"exit", "quit", false},
}

// suggestFor computes the palette for the current terminal input: an active "@<partial>"
// mention (anywhere in the text) → file picker; otherwise "/" at the start → command palette
// (with model/workflow name completion). Empty when nothing applies.
func (g *Gateway) suggestFor(input string) []suggestion {
	// File mention: the last "@" with no space after it is the one being typed.
	if at := strings.LastIndex(input, "@"); at >= 0 {
		partial := input[at+1:]
		if !strings.ContainsAny(partial, " \t") {
			return g.fileSuggestions(input[:at], partial)
		}
	}

	if strings.HasPrefix(input, "/") {
		rest := input[1:]
		name, arg, hasSpace := strings.Cut(rest, " ")
		if !hasSpace {
			// Still typing the command name → list matching commands.
			var out []suggestion
			for _, c := range tuiCommands {
				if strings.HasPrefix(c.name, strings.ToLower(name)) {
					ins := "/" + c.name
					if c.arg {
						ins += " "
					}
					out = append(out, suggestion{insert: ins, label: "/" + c.name, desc: c.desc, submit: !c.arg})
				}
			}
			return out
		}
		switch strings.ToLower(name) {
		case "model":
			return g.modelArgSuggestions(strings.TrimSpace(arg))
		case "workflow":
			return g.workflowSuggestions(strings.TrimSpace(arg))
		}
	}
	return nil
}

// fileSuggestions powers the "@" file picker: files under the working dir matching partial.
// prefix is the input text before the "@", so accepting keeps the rest of the message and
// just drops in a "@<path>" mention (a tag), not the file's contents.
func (g *Gateway) fileSuggestions(prefix, partial string) []suggestion {
	files := scanFiles(".", 2500)
	low := strings.ToLower(partial)

	type hit struct {
		path  string
		score int
	}
	var hits []hit
	for _, f := range files {
		base := filepath.Base(f)
		lf, lb := strings.ToLower(f), strings.ToLower(base)
		switch {
		case low == "":
			hits = append(hits, hit{f, 2})
		case strings.HasPrefix(lb, low):
			hits = append(hits, hit{f, 0}) // basename prefix — best
		case strings.Contains(lb, low):
			hits = append(hits, hit{f, 1})
		case strings.Contains(lf, low):
			hits = append(hits, hit{f, 2})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		return len(hits[i].path) < len(hits[j].path)
	})
	if len(hits) > 30 {
		hits = hits[:30]
	}
	out := make([]suggestion, 0, len(hits))
	for _, h := range hits {
		p := filepath.ToSlash(h.path)
		// Accepting inserts a "@path" tag and a trailing space — a mention, not the file.
		out = append(out, suggestion{insert: prefix + "@" + p + " ", label: "@" + filepath.Base(p), desc: p, submit: false})
	}
	return out
}

// scanFiles walks dir (skipping noisy/huge dirs) and returns up to max relative file paths.
func scanFiles(dir string, max int) []string {
	var out []string
	skip := map[string]bool{".git": true, "node_modules": true, "vendor": true, ".lobster": true, "dist": true, "build": true, ".idea": true, ".vscode": true}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != dir) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, e := filepath.Rel(dir, path)
		if e != nil {
			rel = path
		}
		out = append(out, rel)
		if len(out) >= max {
			return filepath.SkipAll
		}
		return nil
	})
	return out
}

// modelArgSuggestions completes "/model <name>".
func (g *Gateway) modelArgSuggestions(partial string) []suggestion {
	active := g.activeModel(terminalChatID)
	var out []suggestion
	for _, n := range g.modelOrder {
		if partial != "" && !strings.Contains(strings.ToLower(n), strings.ToLower(partial)) {
			continue
		}
		desc := ""
		if n == active {
			desc = "current"
		}
		out = append(out, suggestion{insert: "/model " + n, label: n, desc: desc, submit: true})
	}
	return out
}

// workflowSuggestions completes "/workflow <name>".
func (g *Gateway) workflowSuggestions(partial string) []suggestion {
	var out []suggestion
	for _, m := range g.wf.List() {
		if partial != "" && !strings.Contains(strings.ToLower(m.Name), strings.ToLower(partial)) {
			continue
		}
		out = append(out, suggestion{insert: "/workflow " + m.Name, label: m.Name, desc: m.Description, submit: true})
	}
	return out
}
