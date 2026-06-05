package bgproc

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitDone(t *testing.T, j *Job, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st := j.Status(); !st.Running {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish within %s", j.ID, timeout)
	return Status{}
}

func TestManager_RunCapturesOutputAndFires(t *testing.T) {
	m := NewManager()

	var wg sync.WaitGroup
	wg.Add(1)
	var fired string
	m.OnFinish = func(j *Job) {
		fired = j.ID
		wg.Done()
	}

	j := m.Start(context.Background(), "chatX", "echo bgtest_ok")
	if j.ChatID != "chatX" {
		t.Fatalf("chatID not tracked: %q", j.ChatID)
	}

	st := waitDone(t, j, 10*time.Second)
	if st.Running {
		t.Fatal("expected job to be done")
	}
	if st.ExitErr != nil {
		t.Fatalf("expected clean exit, got %v", st.ExitErr)
	}
	if !strings.Contains(st.Output, "bgtest_ok") {
		t.Fatalf("expected output to contain bgtest_ok, got %q", st.Output)
	}

	wg.Wait()
	if fired != j.ID {
		t.Fatalf("OnFinish fired with %q, want %q", fired, j.ID)
	}

	// It should be findable and listed.
	if _, ok := m.Get(j.ID); !ok {
		t.Fatal("Get could not find the job")
	}
	if len(m.List()) != 1 {
		t.Fatalf("expected 1 job listed, got %d", len(m.List()))
	}
}
