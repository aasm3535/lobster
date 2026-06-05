// Package skills implements Anthropic-style Agent Skills: each skill is a folder with a
// SKILL.md file (YAML frontmatter giving a name + description, then markdown
// instructions) plus any bundled scripts/resources. Discovery is cheap and always on;
// the model sees only each skill's name + description until it asks to load the full
// instructions — "progressive disclosure", so many skills cost little context.
//
// No YAML dependency: the frontmatter we need is a handful of `key: value` lines, which
// a tiny stdlib parser handles.
package skills

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Skill is one discovered skill.
type Skill struct {
	Name        string
	Description string
	Dir         string   // absolute path to the skill's folder
	Body        string   // the markdown instructions (everything after the frontmatter)
	Files       []string // bundled files relative to Dir (scripts, resources), excluding SKILL.md
}

// Store discovers and holds the skills under a directory.
type Store struct {
	dir string

	mu     sync.Mutex
	byName map[string]*Skill
	order  []string
}

// Open prepares the skills directory and loads whatever's there.
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, byName: map[string]*Skill{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Dir is the skills directory (so the agent can be told where to author new skills).
func (s *Store) Dir() string { return s.dir }

// Reload rescans the skills directory. Folders without a SKILL.md are ignored.
func (s *Store) Reload() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	byName := map[string]*Skill{}
	var order []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(s.dir, e.Name())
		raw, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
		if err != nil {
			continue
		}
		meta, body := parseFrontmatter(string(raw))
		name := meta["name"]
		if name == "" {
			name = e.Name()
		}
		byName[name] = &Skill{
			Name:        name,
			Description: meta["description"],
			Dir:         skillDir,
			Body:        body,
			Files:       listFiles(skillDir),
		}
		order = append(order, name)
	}
	s.mu.Lock()
	s.byName = byName
	s.order = order
	s.mu.Unlock()
	return nil
}

// List returns the skills in discovery order.
func (s *Store) List() []*Skill {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Skill, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.byName[n])
	}
	return out
}

// Get returns a skill by name.
func (s *Store) Get(name string) (*Skill, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, ok := s.byName[name]
	return sk, ok
}

// parseFrontmatter splits a SKILL.md into its `key: value` frontmatter and its body. A
// file without a leading `---` fence is treated as all body (no metadata).
func parseFrontmatter(content string) (map[string]string, string) {
	meta := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return meta, strings.TrimSpace(content)
	}
	close := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			close = i
			break
		}
	}
	if close == -1 {
		return meta, strings.TrimSpace(content)
	}
	for _, line := range lines[1:close] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		meta[key] = val
	}
	return meta, strings.TrimSpace(strings.Join(lines[close+1:], "\n"))
}

// listFiles returns the skill's bundled files (relative, slash paths), excluding SKILL.md.
func listFiles(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "SKILL.md" {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files
}
