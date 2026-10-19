package jobsvc

import (
	"context"
	"testing"
	"time"
)

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
