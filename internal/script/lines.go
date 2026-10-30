package script

import (
	"strings"
	"sync"
)

// LineRec is one completed output line with its job-global sequence number.
type LineRec struct {
	Seq  int64
	Line string
}

// LineBuffer is an io.Writer that splits into lines as they are written. It is
// a bounded ring: when full the OLDEST lines are dropped (the sqlite store is
// the unbounded authority).
//
// It lives here, next to the runner, because it is how a job's output becomes
// lines: the runner writes to it and jobsvc reads from it. It has nothing to do
// with which engine produces the bytes.
type LineBuffer struct {
	max     int
	nextSeq func() int64
	onLine  func(LineRec) int64

	mu        sync.Mutex
	recs      []LineRec
	cur       strings.Builder
	dropped   int
	pendingCR bool
}

// NewLineBuffer builds a line buffer holding at most max complete lines.
//
// nextSeq numbers a line for callers that want the buffer to allocate it. Pass
// nil when someone ELSE owns the numbering: a job with two streams has two
// buffers fed by two goroutines, so a counter inside a buffer is a race, not an
// order. In that mode onLine returns the authoritative number and the ring stores
// it, which keeps the ring, the live stream and the persisted rows on one
// sequence.
func NewLineBuffer(max int, nextSeq func() int64, onLine func(LineRec) int64) *LineBuffer {
	if max <= 0 {
		max = 10000
	}
	return &LineBuffer{max: max, nextSeq: nextSeq, onLine: onLine}
}

// number assigns the line's sequence and returns the record carrying it. The
// order matters: when a caller owns the numbering (onLine returns it), that number
// must be written back into the record, because the ring is what a history query
// slices on and what the replay sorts on.
func (lb *LineBuffer) number(rec LineRec) LineRec {
	if lb.nextSeq != nil {
		rec.Seq = lb.nextSeq()
	}
	if lb.onLine != nil {
		rec.Seq = lb.onLine(rec)
	}
	return rec
}

// Write splits p into lines (normalizing CRLF) as they are written.
func (lb *LineBuffer) Write(p []byte) (int, error) {
	// Phase 1 (under mu): split the bytes into complete lines. Nothing outside
	// this struct runs here.
	var lines []string
	lb.mu.Lock()
	complete := func() {
		lines = append(lines, lb.cur.String())
		lb.cur.Reset()
	}
	i := 0
	if lb.pendingCR && len(p) > 0 && p[0] == '\n' {
		i = 1
	}
	lb.pendingCR = false
	for ; i < len(p); i++ {
		switch p[i] {
		case '\r':
			complete()
			if i+1 < len(p) && p[i+1] == '\n' {
				i++
			} else if i+1 == len(p) {
				lb.pendingCR = true
			}
		case '\n':
			complete()
		default:
			lb.cur.WriteByte(p[i])
		}
	}
	lb.mu.Unlock()

	// Phase 2 (outside mu): number each line and record it. Deliberately NOT
	// inside the lock: onLine may take another lock (the job's publish lock), and
	// calling out while holding mu is how a lock-order inversion starts.
	for _, line := range lines {
		lb.record(LineRec{Line: line})
	}
	return len(p), nil
}

// record numbers one completed line and appends it to the ring.
func (lb *LineBuffer) record(rec LineRec) {
	rec = lb.number(rec)
	lb.mu.Lock()
	lb.recs = append(lb.recs, rec)
	if len(lb.recs) > lb.max {
		lb.recs = lb.recs[1:]
		lb.dropped++
	}
	lb.mu.Unlock()
}

// Recs returns a copy of the ring window (complete lines only).
func (lb *LineBuffer) Recs() []LineRec {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	out := make([]LineRec, len(lb.recs))
	copy(out, lb.recs)
	return out
}

// Dropped reports how many complete lines fell off the ring front.
func (lb *LineBuffer) Dropped() int {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.dropped
}

// FlushLine promotes a partial trailing line (output that never ended in \n)
// into the ring and returns it ("" when nothing pending).
func (lb *LineBuffer) FlushLine() string {
	lb.mu.Lock()
	if lb.cur.Len() == 0 {
		lb.mu.Unlock()
		return ""
	}
	line := lb.cur.String()
	lb.cur.Reset()
	lb.mu.Unlock()
	lb.record(LineRec{Line: line})
	return line
}

// Tail returns the last n lines joined with \n (plus any partial line).
func (lb *LineBuffer) Tail(n int) string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if n <= 0 || n > len(lb.recs) {
		n = len(lb.recs)
	}
	lines := make([]string, 0, n+1)
	for _, r := range lb.recs[len(lb.recs)-n:] {
		lines = append(lines, r.Line)
	}
	if lb.cur.Len() > 0 {
		lines = append(lines, lb.cur.String())
	}
	return strings.Join(lines, "\n")
}
