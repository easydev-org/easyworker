package jobsvc

// Flow writes racing the line writer.
//
// The store has ONE writer goroutine that batches job_lines into a transaction.
// The dataflow graph is flushed from the JOB's goroutine when the job ends, so
// two writers can collide: SQLite (WAL) allows a single writer at a time, so the
// loser gets SQLITE_BUSY ("database is locked").
//
// busy_timeout handles short waits, but a large line batch can hold the write
// lock longer than that. Retrying the flow write is the smallest fix that does
// not couple the two paths: the graph is not latency-critical (it is read after
// the job finishes), so waiting is fine.

import (
	"strings"
	"time"
)

// putBytesRetry writes a flow's content, retrying while SQLite reports the
// database as busy/locked. It gives up after ~30s so a genuinely stuck DB does
// not hang the job's completion forever.
func (s *Store) putBytesRetry(jobID string, fid int64, data []byte) error {
	var err error
	delay := 20 * time.Millisecond
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = s.Flow.PutBytes(jobID, fid, data)
		if err == nil {
			return nil
		}
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "locked") && !strings.Contains(msg, "busy") {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 500*time.Millisecond {
			delay *= 2
		}
	}
}
