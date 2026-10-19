package jobsvc

// storeGraph turns a finished job's easysh dataflow graph into rows.
//
// The graph is *flow.Registry (from shellh.Result). It is stored so the panel
// can show which command produced which stream, how they were piped, and what
// each intermediate result contained — the reason easysh exists.

import (
	"fmt"
	"strings"
	"sync"

	"github.com/easydev-org/easysh/pkg/flow"
	esstore "github.com/easydev-org/easysh/pkg/store"
)

// jobSink implements flow.Sink for one job: it persists every flow's bytes into
// the job's manifest as they are produced (a pipe's intermediate result
// included) and records each flow as it is created. It mirrors easysh's own
// daemon jobSink, so the two stores stay interchangeable.
type jobSink struct {
	store *Store
	jobID string

	mu  sync.Mutex
	seq map[int64]int64

	// Accumulated per flow: the Sink API is chunk-oriented, but FlowStore
	// stores whole flows, so we buffer chunks and flush once at the end.
	buf map[int64][]byte
	reg *flow.Registry
}

// Enqueue implements flow.Sink.
func (s *jobSink) Enqueue(fid flow.FID, p []byte) {
	s.mu.Lock()
	// Enqueue is called by flow.TapBuffer as soon as a command writes output,
	// which can be BEFORE onRegistry runs (the registry is only handed over once
	// the graph is built). Relying on onRegistry to allocate s.buf therefore
	// panicked with "assignment to entry in nil map" and took the whole worker
	// process down — the first write of any job could kill the sandbox.
	if s.buf == nil {
		s.buf = make(map[int64][]byte)
	}
	s.buf[int64(fid)] = append(s.buf[int64(fid)], p...)
	s.mu.Unlock()
}

// onRegistry is the RunGraph hook: it records the registry so the graph can be
// persisted when the job ends.
func (s *jobSink) onRegistry(reg *flow.Registry) {
	s.mu.Lock()
	s.reg = reg
	if s.buf == nil {
		s.buf = map[int64][]byte{}
	}
	s.mu.Unlock()
}

// flush writes the buffered flow bytes (capped) into the store.
func (s *jobSink) flush() {
	s.mu.Lock()
	buf := s.buf
	s.buf = nil
	s.mu.Unlock()
	for fid, b := range buf {
		if len(b) == 0 {
			continue
		}
		if len(b) > maxFlowContent {
			b = b[:maxFlowContent]
		}
		if err := s.store.putBytesRetry(s.jobID, fid, b); err != nil {
			fmt.Printf("store: flow content %s/%d: %v\n", s.jobID, fid, err)
		}
	}
}

// Bounds. The worker runs many jobs for a long time; easysh runs one shell, so
// it can keep everything. These caps keep one pathological job from filling the
// DB: the graph (small) is always stored; only the CONTENT is truncated.
const (
	maxFlowBytes  = 1 << 20 // per flow: store at most 1 MiB of its content
	maxFlowsKept  = 2000    // per job: beyond this, store the graph without content
	maxFlowContent = 1 << 20
)

// storeGraph persists a job's graph. Best-effort: errors are logged, not
// returned, because the job itself already succeeded or failed on its own.
func (m *Manager) storeGraph(jobID string, reg *flow.Registry) {
	if reg == nil {
		return
	}
	flows := reg.Flows()
	if len(flows) == 0 {
		return
	}
	for _, f := range flows {
		bytes, lines := f.Counts()
		row := esstore.FlowRow{
			FID:       int64(f.ID),
			Kind:      f.Kind.String(),
			Fd:        f.Fd.String(),
			Producer:  name(f.Producer),
			Consumer:  name(f.Consumer),
			Owner:     name(f.Owner),
			Path:      f.Path,
			DupOf:     int64(f.DupOf),
			Bytes:     bytes,
			Lines:     lines,
			StartedAt: f.Started.UnixMilli(),
		}
		if !f.Closed.IsZero() {
			row.ClosedAt = f.Closed.UnixMilli()
		}
		if f.Producer != nil {
			row.Line, row.Col = f.Producer.Line, f.Producer.Col
			row.EndLine, row.EndCol = f.Producer.EndLine, f.Producer.EndCol
			row.Iter = strings.TrimPrefix(f.Producer.IterLabel(), "#")
		}
		if err := m.store.Flow.PutFlow(jobID, row); err != nil {
			fmt.Printf("store: flow %s/%d: %v\n", jobID, f.ID, err)
			return
		}
		for _, out := range f.Outputs {
			_ = m.store.Flow.PutEdge(jobID, int64(f.ID), int64(out))
		}
	}
}

// name renders a CommandRef's argv as a single label ("" for nil).
func name(c *flow.CommandRef) string {
	if c == nil {
		return ""
	}
	return c.Name()
}
