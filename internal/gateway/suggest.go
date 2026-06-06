package gateway

import "strings"

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

// suggestFor computes the palette for the current terminal input: "/" → commands (and the
// model/workflow names for /model and /workflow), "@" → a quick model switcher. Empty when
// nothing applies.
func (g *Gateway) suggestFor(input string) []suggestion {
	switch {
	case strings.HasPrefix(input, "@"):
		return g.modelSuggestions(strings.TrimSpace(input[1:]))

	case strings.HasPrefix(input, "/"):
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
		// Command chosen; complete its argument.
		switch strings.ToLower(name) {
		case "model":
			return g.modelArgSuggestions(strings.TrimSpace(arg))
		case "workflow":
			return g.workflowSuggestions(strings.TrimSpace(arg))
		}
	}
	return nil
}

// modelSuggestions powers the "@" picker: choose a model, switch immediately.
func (g *Gateway) modelSuggestions(partial string) []suggestion {
	active := g.activeModel(terminalChatID)
	var out []suggestion
	for _, n := range g.modelOrder {
		if partial != "" && !strings.Contains(strings.ToLower(n), strings.ToLower(partial)) {
			continue
		}
		desc := "switch to this model"
		if n == active {
			desc = "current model"
		}
		out = append(out, suggestion{insert: "/model " + n, label: n, desc: desc, submit: true})
	}
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
