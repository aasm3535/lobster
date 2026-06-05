// Package memory gives each chat a small persistent profile — what the assistant has
// learned about that user (their name, the vibe they want, durable facts). It is
// deliberately simple: a JSON file on disk, loaded at startup and rewritten on every
// change. This is what lets the agent get better at being THIS person's assistant
// across restarts, instead of meeting them fresh every time.
package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Note is one durable thing the assistant chose to remember about a user.
type Note struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Profile is everything known about a single chat's user: durable notes plus tunable
// preferences (verbosity, preferred shell, …) the user set during setup.
type Profile struct {
	Notes []Note            `json:"notes"`
	Prefs map[string]string `json:"prefs,omitempty"`
}

// Store is a thread-safe, file-backed map of chatID -> profile.
type Store struct {
	path string
	mu   sync.Mutex
	data map[string]*Profile
}

// Open loads the store from path (an absent file is fine — it starts empty).
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]*Profile{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, err
		}
	}
	if s.data == nil {
		s.data = map[string]*Profile{}
	}
	return s, nil
}

// Remember appends a durable note about a chat's user and persists immediately.
func (s *Store) Remember(chatID, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.data[chatID]
	if p == nil {
		p = &Profile{}
		s.data[chatID] = p
	}
	p.Notes = append(p.Notes, Note{Text: text, At: time.Now()})
	return s.flushLocked()
}

// Notes returns what is known about a chat's user, oldest first.
func (s *Store) Notes(chatID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.data[chatID]
	if p == nil {
		return nil
	}
	out := make([]string, len(p.Notes))
	for i, n := range p.Notes {
		out[i] = n.Text
	}
	return out
}

// SetPref stores a tunable preference for a chat (e.g. verbosity=quiet) and persists.
func (s *Store) SetPref(chatID, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.data[chatID]
	if p == nil {
		p = &Profile{}
		s.data[chatID] = p
	}
	if p.Prefs == nil {
		p.Prefs = map[string]string{}
	}
	p.Prefs[key] = value
	return s.flushLocked()
}

// Pref returns a chat's preference for key, or "" if unset.
func (s *Store) Pref(chatID, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.data[chatID]; p != nil {
		return p.Prefs[key]
	}
	return ""
}

// Forget drops everything known about a chat's user.
func (s *Store) Forget(chatID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, chatID)
	return s.flushLocked()
}

func (s *Store) flushLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	// 0600: the profile may hold personal details, keep it owner-only.
	return os.WriteFile(s.path, b, 0o600)
}
