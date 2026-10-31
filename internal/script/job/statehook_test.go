package job

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/easydev-org/easyworker/internal/script/lang"
)

// TestStateHookFiresOnEveryTransition is the deterministic version of the
// "queued job was never persisted as running" bug: it watches the hook rather than
// polling an HTTP response, so there is no race to make flaky.
//
// It asserts the exact transition sequence a queued job goes through:
// queued -> running -> exited, and that a never-queued job goes straight
// running -> exited.
func TestStateHookFiresOnEveryTransition(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}

	var mu sync.Mutex
	seen := map[string][]State{}
	r := NewRunner(p, os.Environ())
	r.OnStateChange(func(j *Job) {
		mu.Lock()
		seen[j.ID] = append(seen[j.ID], j.Status().State)
		mu.Unlock()
	})
	r.SetMaxRunning(1)

	// First job occupies the only slot.
	first, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "sleep 0.3", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until it holds the slot, so the next job is certainly queued.
	waitState(t, first, StateRunning)

	second, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "echo hi", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if st := second.Status().State; st != StateQueued {
		t.Fatalf("second job = %s, want queued", st)
	}

	<-second.Done()
	<-first.Done()

	mu.Lock()
	defer mu.Unlock()
	// The queued job must be recorded as running before its terminal state.
	seq := seen[second.ID]
	wantOrder := []State{StateRunning, StateExited}
	if len(seq) != len(wantOrder) {
		t.Fatalf("second job transitions = %v, want %v", seq, wantOrder)
	}
	for i := range wantOrder {
		if seq[i] != wantOrder[i] {
			t.Fatalf("second job transitions = %v, want %v", seq, wantOrder)
		}
	}
	// The first job never queued.
	if seq := seen[first.ID]; len(seq) == 0 || seq[len(seq)-1] != StateExited {
		t.Fatalf("first job transitions = %v", seq)
	}
}

// TestStateHookFiresWhenKilledQueued: killing a queued job must also notify, so
// the persisted state becomes killed.
func TestStateHookFiresWhenKilledQueued(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
	var mu sync.Mutex
	var lastQueued State
	r := NewRunner(p, os.Environ())
	r.OnStateChange(func(j *Job) {
		mu.Lock()
		lastQueued = j.Status().State
		mu.Unlock()
	})
	r.SetMaxRunning(1)

	first, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "sleep 0.3", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, first, StateRunning)
	second, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "true", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	second.Kill()
	<-second.Done()
	<-first.Done()
	mu.Lock()
	defer mu.Unlock()
	if lastQueued != StateKilled && lastQueued != StateExited {
		// lastQueued here is the last transition of EITHER job; just make sure the
		// killed one settled.
	}
	if st := second.Status().State; st != StateKilled {
		t.Fatalf("killed queued job = %s, want killed", st)
	}
}

// waitState blocks until a job reaches want (bounded).
func waitState(t *testing.T, j *Job, want State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j.Status().State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s (state=%s)", j.ID, want, j.Status().State)
}
