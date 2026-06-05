// Package tools is Lobster's native tool registry. Tools are plain Go functions with
// a JSON-Schema description; they are handed to the model as native function defs.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"lobster/internal/llm"
)

// Tool is one native capability the agent can invoke.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Run         func(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds the available tools in a stable order.
type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	if _, ok := r.tools[t.Name]; !ok {
		r.order = append(r.order, t.Name)
	}
	r.tools[t.Name] = t
}

// Defs returns the model-facing tool definitions.
func (r *Registry) Defs() []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		defs = append(defs, llm.ToolDef{Name: t.Name, Description: t.Description, Parameters: t.Schema})
	}
	return defs
}

// Run executes a tool by name. Missing arguments are treated as an empty object.
func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	if strings.TrimSpace(string(args)) == "" {
		args = json.RawMessage("{}")
	}
	return t.Run(ctx, args)
}
