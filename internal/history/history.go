// Package history persists each chat's conversation transcript so a restart doesn't
// wipe what was said. It complements memory: memory holds distilled durable facts (the
// remember tool), history holds the raw rolling conversation that the agent trims to a
// budget. One small JSON file per chat keeps it simple and avoids rewriting everything.
package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	b, err := os.ReadFile(s.path(chatID))
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
