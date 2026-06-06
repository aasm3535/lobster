// Package history persists each chat's conversation transcript so a restart doesn't
// wipe what was said. It complements memory: memory holds distilled durable facts (the
// remember tool), history holds the raw rolling conversation that the agent trims to a
// budget. One small JSON file per chat keeps it simple and avoids rewriting everything.
package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aasm3535/lobster/internal/llm"
)

// Store is a thread-safe, file-per-chat transcript store under a directory.
type Store struct {
	dir string
	mu  sync.Mutex
}

// Open prepares the store directory (created if missing).
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(chatID string) string {
	return filepath.Join(s.dir, safeName(chatID)+".json")
}

// Load returns a chat's saved messages (nil if it has none yet).
func (s *Store) Load(chatID string) ([]llm.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadFile(s.path(chatID))
}

// loadFile reads and decodes one transcript file (caller holds the lock).
func (s *Store) loadFile(path string) ([]llm.Message, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var msgs []llm.Message
	if len(b) > 0 {
		if err := json.Unmarshal(b, &msgs); err != nil {
			return nil, err
		}
	}
	return msgs, nil
}

// Meta describes one saved session (a coded conversation, one JSON file).
type Meta struct {
	ID       string
	Modified time.Time
	Bytes    int64
	Messages int
	Title    string // first user message, for a human-readable handle
}

// List returns saved sessions newest-first. The newest few are enriched with a message
// count and title (read from disk); the rest carry just file metadata, to stay cheap.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		metas = append(metas, Meta{
			ID:       strings.TrimSuffix(e.Name(), ".json"),
			Modified: fi.ModTime(),
			Bytes:    fi.Size(),
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Modified.After(metas[j].Modified) })
	for i := range metas {
		if i >= 60 {
			break
		}
		msgs, err := s.loadFile(filepath.Join(s.dir, metas[i].ID+".json"))
		if err != nil {
			continue
		}
		metas[i].Messages = len(msgs)
		metas[i].Title = firstUserText(msgs)
	}
	return metas
}

// firstUserText returns the first user message, trimmed to a short handle.
func firstUserText(msgs []llm.Message) string {
	for _, m := range msgs {
		if m.Role != llm.RoleUser {
			continue
		}
		t := strings.TrimSpace(strings.ReplaceAll(m.Content, "\n", " "))
		// Skip the synthetic kickoffs (start/setup/goal/workflow) so the title is real.
		if t == "" || strings.HasPrefix(t, "(") {
			continue
		}
		if r := []rune(t); len(r) > 60 {
			t = string(r[:60]) + "…"
		}
		return t
	}
	return ""
}

// Save overwrites a chat's transcript.
func (s *Store) Save(chatID string, msgs []llm.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(msgs)
	if err != nil {
		return err
	}
	// 0600: transcripts can hold personal content, keep them owner-only.
	return os.WriteFile(s.path(chatID), b, 0o600)
}

// Clear forgets a chat's transcript (used by /reset). Missing file is not an error.
func (s *Store) Clear(chatID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(chatID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// safeName keeps a chat id usable as a filename (Telegram ids are numeric, but be safe).
func safeName(id string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, id)
	if mapped == "" {
		return "chat"
	}
	return mapped
}
