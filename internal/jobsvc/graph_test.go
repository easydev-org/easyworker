package jobsvc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/easydev-org/easysh/pkg/flow"
)

// TestJobSinkEnqueueBeforeRegistry is the regression test for a panic that
// killed the whole worker process.
//
// Enqueue writes to s.buf, but buf used to be allocated only in onRegistry.
// The first write of a command reaches Enqueue first — flow.TapBuffer taps
// output as soon as it appears, before the graph is handed over — so the map
// was nil and Go's "assignment to entry in nil map" panic terminated the
// daemon. One job write took the entire sandbox down.
func TestJobSinkEnqueueBeforeRegistry(t *testing.T) {
	s := &jobSink{jobID: "job-1", seq: map[int64]int64{}}

	s.Enqueue(flow.FID(4), []byte("a b c d e\n")) // must not panic

	s.mu.Lock()
	got := string(s.buf[int64(4)])
	s.mu.Unlock()
	if got != "a b c d e\n" {
		t.Fatalf("buffered %q, want %q", got, "a b c d e\n")
	}
}

// TestJobSinkConcurrentEnqueue checks the lazy allocation under the sink's
// mutex: stdout and stderr taps call Enqueue concurrently.
func TestJobSinkConcurrentEnqueue(t *testing.T) {
	s := &jobSink{jobID: "job-2", seq: map[int64]int64{}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.Enqueue(flow.FID(1), []byte("x"))
				s.Enqueue(flow.FID(2), []byte("y"))
			}
		}()
	}
	wg.Wait()

	s.mu.Lock()
	n1, n2 := len(s.buf[int64(1)]), len(s.buf[int64(2)])
	s.mu.Unlock()
	if n1 != 1600 || n2 != 1600 {
		t.Fatalf("buffered %d/%d bytes, want 1600/1600", n1, n2)
	}
}

// TestGraphPersisted runs a pipeline through the real executor and asserts the
// dataflow graph (flows + edges + intermediate bytes) landed in the store.
func TestGraphPersisted(t *testing.T) {
	m, _ := newTestManager(t)
	store := m.Store()

	// A pipe: its intermediate result is exactly what the graph should capture.
	id, err := m.Execute(context.Background(), `echo "a b c d e" | wc -w`, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the job to finish (the graph is stored at the end, after the
	// job's done channel closes — give the store a moment to catch up).
	j, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-j.done:
	case <-time.After(10 * time.Second):
		t.Fatal("job did not finish")
	}
	time.Sleep(200 * time.Millisecond)

	flows, err := store.Flow.Flows(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) == 0 {
		t.Fatal("no flows stored for the job")
	}
	edges, err := store.Flow.Edges(id)
	if err != nil {
		t.Fatal(err)
	}
	// The pipe must have at least one edge (producer -> consumer).
	if len(edges) == 0 {
		t.Errorf("no edges stored (flows=%d)", len(flows))
	}
	// At least one flow must carry the intermediate bytes.
	var withContent int
	for _, f := range flows {
		b, err := store.Flow.Content(id, f.FID)
		if err == nil && len(b) > 0 {
			withContent++
		}
	}
	if withContent == 0 {
		t.Errorf("no flow content stored (flows=%d)", len(flows))
	}
}
