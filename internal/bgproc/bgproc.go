// Package bgproc runs long-lived shell commands in the background so the agent never
// blocks on them. The agent starts a job, keeps talking to the user, and polls the
// job's status; when a job finishes, the manager fires OnFinish so the chat can be
// pinged. This is what makes "scan my whole PC for X" practical instead of a 60s
// timeout.
package bgproc

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

const maxOutput = 64 * 1024 // keep only the tail of a chatty job's output

// Job is one background command.
type Job struct {
	ID        string
	ChatID    string
	Command   string
	StartedAt time.Time

	out *tailBuffer

	mu      sync.Mutex
	done    bool
	endedAt time.Time
	exitErr error
	cancel  context.CancelFunc
}

// Status is an immutable snapshot of a job, safe to format and send.
type Status struct {
	ID      string
	Command string
	Running bool
	Elapsed time.Duration
	ExitErr error
	Output  string
}

func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	end := j.endedAt
	if !j.done {
		end = time.Now()
	}
	return Status{
		ID:      j.ID,
		Command: j.Command,
		Running: !j.done,
		Elapsed: end.Sub(j.StartedAt),
		ExitErr: j.exitErr,
		Output:  j.out.String(),
	}
}

func (j *Job) finish(err error) {
	j.mu.Lock()
	j.done = true
	j.endedAt = time.Now()
	j.exitErr = err
	j.mu.Unlock()
	if j.cancel != nil {
		j.cancel()
	}
}

// Manager owns the set of background jobs.
type Manager struct {
	// OnFinish, if set, is called once per job when it exits (in its own goroutine).
	OnFinish func(*Job)

	mu    sync.Mutex
	jobs  map[string]*Job
	order []string
	seq   int
}

func NewManager() *Manager {
	return &Manager{jobs: map[string]*Job{}}
}

// Start launches command detached from any request, tied to ctx for lifetime (so a
// bot shutdown kills its background jobs rather than orphaning them).
func (m *Manager) Start(ctx context.Context, chatID, command string) *Job {
	m.mu.Lock()
	m.seq++
	id := "bg" + strconv.Itoa(m.seq)
	m.mu.Unlock()

	cctx, cancel := context.WithCancel(ctx)
	job := &Job{
		ID:        id,
		ChatID:    chatID,
		Command:   command,
		StartedAt: time.Now(),
		out:       &tailBuffer{max: maxOutput},
		cancel:    cancel,
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		wrapped := "$OutputEncoding=[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " + command
		cmd = exec.CommandContext(cctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", wrapped)
	} else {
		cmd = exec.CommandContext(cctx, "sh", "-c", command)
	}
	cmd.Stdout = job.out
	cmd.Stderr = job.out

	m.mu.Lock()
	m.jobs[id] = job
	m.order = append(m.order, id)
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		job.finish(err)
		m.fireFinish(job)
		return job
	}
	go func() {
		err := cmd.Wait()
		job.finish(err)
		m.fireFinish(job)
	}()
	return job
}

func (m *Manager) fireFinish(j *Job) {
	if m.OnFinish != nil {
		go m.OnFinish(j)
	}
}

// Get returns a job by id.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// List returns all jobs in start order.
func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.jobs[id])
	}
	return out
}

// Stop cancels a running job.
func (m *Manager) Stop(id string) bool {
	j, ok := m.Get(id)
	if !ok {
		return false
	}
	j.mu.Lock()
	running := !j.done
	j.mu.Unlock()
	if running && j.cancel != nil {
		j.cancel()
	}
	return running
}

// tailBuffer is an io.Writer that retains only the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
