// Package scheduler lets the agent set itself wake-ups instead of burning tokens on a
// busy-loop. Jobs are persisted to disk and driven by a single timer that sleeps until
// the next one is due — so an idle schedule costs nothing. When a job fires, onFire is
// called with the chat and the prompt the agent saved, which the gateway runs and uses
// to message the user proactively ("papus is down — waking you").
package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Job is one scheduled task.
type Job struct {
	ID      string        `json:"id"`
	ChatID  string        `json:"chat_id"`
	Prompt  string        `json:"prompt"`
	Label   string        `json:"label,omitempty"`
	NextAt  time.Time     `json:"next_at"`
	Every   time.Duration `json:"every"` // 0 = one-shot
	Created time.Time     `json:"created"`
}

// Recurring reports whether the job re-arms after firing.
func (j *Job) Recurring() bool { return j.Every > 0 }

type persisted struct {
	Seq  int    `json:"seq"`
	Jobs []*Job `json:"jobs"`
}

// Store holds the scheduled jobs and drives them.
type Store struct {
	path   string
	onFire func(chatID, prompt string)
	now    func() time.Time

	mu   sync.Mutex
	jobs map[string]*Job
	seq  int
	wake chan struct{}
}

// Open loads the schedule from path (absent is fine). onFire is called when a job fires.
func Open(path string, onFire func(chatID, prompt string)) (*Store, error) {
	s := &Store{path: path, onFire: onFire, now: time.Now, jobs: map[string]*Job{}, wake: make(chan struct{}, 1)}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		var p persisted
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		s.seq = p.Seq
		for _, j := range p.Jobs {
			s.jobs[j.ID] = j
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// Add registers a job. after is the delay to the first run (defaults to every for a pure
// recurring job); every>0 makes it recurring.
func (s *Store) Add(chatID, prompt, label string, after, every time.Duration) *Job {
	s.mu.Lock()
	s.seq++
	first := after
	if first <= 0 {
		first = every
	}
	job := &Job{
		ID:      "t" + strconv.Itoa(s.seq),
		ChatID:  chatID,
		Prompt:  prompt,
		Label:   label,
		NextAt:  s.now().Add(first),
		Every:   every,
		Created: s.now(),
	}
	s.jobs[job.ID] = job
	s.saveLocked()
	s.mu.Unlock()
	s.kick()
	return job
}

// List returns a chat's jobs, soonest first.
func (s *Store) List(chatID string) []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Job
	for _, j := range s.jobs {
		if j.ChatID == chatID {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].NextAt.Before(out[k].NextAt) })
	return out
}

// Remove cancels a job if it belongs to chatID.
func (s *Store) Remove(chatID, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok && j.ChatID == chatID {
		delete(s.jobs, id)
		s.saveLocked()
		s.kick()
		return true
	}
	return false
}

// Run drives the schedule until ctx is cancelled. It sleeps until the next due job, or
// until a change wakes it — never a busy-poll.
func (s *Store) Run(ctx context.Context) {
	for {
		t := time.NewTimer(s.nextWait())
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wake:
			t.Stop()
		case <-t.C:
			s.fireDue()
		}
	}
}

func (s *Store) nextWait() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) == 0 {
		return time.Hour // nothing scheduled; re-evaluated whenever a job is added
	}
	earliest := s.now().Add(time.Hour)
	for _, j := range s.jobs {
		if j.NextAt.Before(earliest) {
			earliest = j.NextAt
		}
	}
	if d := earliest.Sub(s.now()); d > 0 {
		return d
	}
	return 0
}

func (s *Store) fireDue() {
	now := s.now()
	var toFire []*Job
	s.mu.Lock()
	for id, j := range s.jobs {
		if j.NextAt.After(now) {
			continue
		}
		toFire = append(toFire, &Job{ChatID: j.ChatID, Prompt: j.Prompt})
		if j.Recurring() {
			j.NextAt = now.Add(j.Every) // skip any missed intervals — fire once, not N times
		} else {
			delete(s.jobs, id)
		}
	}
	if len(toFire) > 0 {
		s.saveLocked()
	}
	s.mu.Unlock()

	// onFire is called synchronously; the gateway's handler returns immediately (it spawns
	// its own goroutine), so the scheduler loop isn't blocked.
	for _, j := range toFire {
		if s.onFire != nil {
			s.onFire(j.ChatID, j.Prompt)
		}
	}
}

func (s *Store) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Store) saveLocked() {
	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].ID < jobs[k].ID })
	b, err := json.MarshalIndent(persisted{Seq: s.seq, Jobs: jobs}, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	_ = os.WriteFile(s.path, b, 0o600)
}
