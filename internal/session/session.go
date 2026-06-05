// Package session is the permanent, searchable archive of conversations — the long-term
// half of Lobster's memory. Where history.Store keeps a trimmed rolling window for the
// live model context, session.Store keeps EVERYTHING: every conversation is recorded as
// an append-only log under ~/.lobster/sessions/<chat>/, split into sessions (a /reset
// starts a new one), with an index of titles and timestamps. The agent can search across
// all of it to recall "what did we talk about last week".
package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is one archived message (only human-meaningful turns are stored — user prompts
// and the assistant's replies, not raw tool output).
type Entry struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
}

// Meta describes one session for the index/listing.
type Meta struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Started    time.Time `json:"started"`
	LastActive time.Time `json:"last_active"`
	Messages   int       `json:"messages"`
}

// Hit is a single search match.
type Hit struct {
	SessionID string
	Title     string
	At        time.Time
	Role      string
	Snippet   string
}

type index struct {
	Current  string `json:"current"`
	Sessions []Meta `json:"sessions"`
}

// Store archives conversations per chat under a root directory.
type Store struct {
	dir string
	mu  sync.Mutex
	now func() time.Time // injectable for tests
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, now: time.Now}, nil
}

// Append records a message in the chat's current session, opening a new session if none
// is active. Title is auto-derived from the first user message.
func (s *Store) Append(chatID, role, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if err := os.MkdirAll(s.chatDir(chatID), 0o700); err != nil {
		return err
	}
	idx := s.loadIndex(chatID)

	if idx.Current == "" {
		id := now.Format("2006-01-02_150405")
		idx.Current = id
		idx.Sessions = append(idx.Sessions, Meta{ID: id, Started: now, LastActive: now})
	}

	cur := -1
	for i := range idx.Sessions {
		if idx.Sessions[i].ID == idx.Current {
			cur = i
			break
		}
	}
	if cur == -1 { // index lost the current meta; recreate it
		idx.Sessions = append(idx.Sessions, Meta{ID: idx.Current, Started: now, LastActive: now})
		cur = len(idx.Sessions) - 1
	}

	line, err := json.Marshal(Entry{Role: role, Content: content, At: now})
	if err != nil {
		return err
	}
	if err := appendLine(s.sessionPath(chatID, idx.Current), line); err != nil {
		return err
	}

	idx.Sessions[cur].LastActive = now
	idx.Sessions[cur].Messages++
	if idx.Sessions[cur].Title == "" && role == "user" {
		idx.Sessions[cur].Title = title(content)
	}
	return s.saveIndex(chatID, idx)
}

// Close ends the chat's current session so the next message starts a fresh one.
func (s *Store) Close(chatID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.loadIndex(chatID)
	if idx.Current == "" {
		return nil
	}
	idx.Current = ""
	return s.saveIndex(chatID, idx)
}

// List returns a chat's sessions, newest first.
func (s *Store) List(chatID string) []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.loadIndex(chatID)
	out := make([]Meta, len(idx.Sessions))
	for i, m := range idx.Sessions {
		out[len(idx.Sessions)-1-i] = m
	}
	return out
}

// Search scans all of a chat's sessions (newest first) for content matching query
// (case-insensitive substring), returning up to limit hits.
func (s *Store) Search(chatID, query string, limit int) []Hit {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	idx := s.loadIndex(chatID)

	var hits []Hit
	for i := len(idx.Sessions) - 1; i >= 0; i-- {
		meta := idx.Sessions[i]
		for _, e := range s.readEntries(chatID, meta.ID) {
			if strings.Contains(strings.ToLower(e.Content), q) {
				hits = append(hits, Hit{
					SessionID: meta.ID,
					Title:     meta.Title,
					At:        e.At,
					Role:      e.Role,
					Snippet:   snippet(e.Content, q),
				})
				if len(hits) >= limit {
					return hits
				}
			}
		}
	}
	return hits
}

// --- internals ---

func (s *Store) chatDir(chatID string) string { return filepath.Join(s.dir, safeName(chatID)) }
func (s *Store) indexPath(chatID string) string {
	return filepath.Join(s.chatDir(chatID), "index.json")
}
func (s *Store) sessionPath(chatID, id string) string {
	return filepath.Join(s.chatDir(chatID), id+".jsonl")
}

func (s *Store) loadIndex(chatID string) index {
	var idx index
	if b, err := os.ReadFile(s.indexPath(chatID)); err == nil {
		_ = json.Unmarshal(b, &idx)
	}
	return idx
}

func (s *Store) saveIndex(chatID string, idx index) error {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.indexPath(chatID), b, 0o600)
}

func (s *Store) readEntries(chatID, id string) []Entry {
	f, err := os.Open(s.sessionPath(chatID, id))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// title derives a short, single-line session title from the first user message.
func title(content string) string {
	t := strings.Join(strings.Fields(content), " ")
	r := []rune(t)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return t
}

// snippet returns a window of content around the first match of q, single-lined.
func snippet(content, q string) string {
	flat := strings.Join(strings.Fields(content), " ")
	low := strings.ToLower(flat)
	i := strings.Index(low, q)
	if i < 0 {
		r := []rune(flat)
		if len(r) > 160 {
			return string(r[:160]) + "…"
		}
		return flat
	}
	start := i - 40
	if start < 0 {
		start = 0
	}
	end := i + len(q) + 100
	if end > len(flat) {
		end = len(flat)
	}
	out := flat[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(flat) {
		out += "…"
	}
	return out
}

// safeName keeps a chat id usable as a directory name.
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
