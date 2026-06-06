// Package workflows stores saved multi-step playbooks: plain markdown files the agent
// executes on demand. A workflow is to a task what a skill is to knowledge — "/workflow
// deploy" replays a whole procedure (build, test, ship, verify) the user or the agent
// wrote down once. Files are flat <name>.md under one directory and are re-read on every
// access, so editing them (by hand or by the agent) needs no reload step.
package workflows

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Meta is one workflow's listing entry.
type Meta struct {
	Name        string
	Description string
}

// Store reads and writes workflows in one directory.
type Store struct {
	dir string
}

// Open ensures the directory exists and returns the store.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

// List returns the available workflows, sorted by name. The description is the first
// heading or non-empty line of the file.
func (s *Store) List() []Meta {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		desc := ""
		if body, err := os.ReadFile(filepath.Join(s.dir, e.Name())); err == nil {
			desc = firstLine(string(body))
		}
		out = append(out, Meta{Name: name, Description: desc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a workflow's full markdown body.
func (s *Store) Get(name string) (string, bool) {
	name = sanitize(name)
	if name == "" {
		return "", false
	}
	body, err := os.ReadFile(filepath.Join(s.dir, name+".md"))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// Save writes (creates or overwrites) a workflow.
func (s *Store) Save(name, content string) error {
	name = sanitize(name)
	if name == "" {
		return fmt.Errorf("workflow name is empty or invalid")
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("workflow content is empty")
	}
	return os.WriteFile(filepath.Join(s.dir, name+".md"), []byte(content), 0o644)
}

// Delete removes a workflow; reports whether it existed.
func (s *Store) Delete(name string) bool {
	name = sanitize(name)
	if name == "" {
		return false
	}
	return os.Remove(filepath.Join(s.dir, name+".md")) == nil
}

// sanitize reduces a requested name to a safe flat filename: lowercase, spaces to
// dashes, and only [a-z0-9._-] kept — so a name can never escape the directory.
func sanitize(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".md")
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), ".")
}

// firstLine extracts a one-line description: the first non-empty line, minus any
// markdown heading marks, clipped to a listing-friendly length.
func firstLine(body string) string {
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "#"))
		if ln == "" {
			continue
		}
		if r := []rune(ln); len(r) > 100 {
			ln = string(r[:100]) + "…"
		}
		return ln
	}
	return ""
}
