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
	onLine  func(LineRec)

	mu        sync.Mutex
	recs      []LineRec
	cur       strings.Builder
	dropped   int
	pendingCR bool
}

// NewLineBuffer builds a line buffer holding at most max complete lines.
func NewLineBuffer(max int, nextSeq func() int64, onLine func(LineRec)) *LineBuffer {
	if max <= 0 {
		max = 10000
	}
	return &LineBuffer{max: max, nextSeq: nextSeq, onLine: onLine}
}

// Write splits p into lines (normalizing CRLF) as they are written.
func (lb *LineBuffer) Write(p []byte) (int, error) {
	var completed []LineRec
	lb.mu.Lock()
	complete := func() {
		rec := LineRec{Seq: lb.nextSeq(), Line: lb.cur.String()}
		lb.cur.Reset()
		lb.recs = append(lb.recs, rec)
		if len(lb.recs) > lb.max {
			lb.recs = lb.recs[1:]
			lb.dropped++
		}
		completed = append(completed, rec)
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
	if lb.onLine != nil {
		for _, rec := range completed {
			lb.onLine(rec)
		}
	}
	return len(p), nil
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
	rec := LineRec{Seq: lb.nextSeq(), Line: lb.cur.String()}
	lb.cur.Reset()
	lb.recs = append(lb.recs, rec)
	if len(lb.recs) > lb.max {
		lb.recs = lb.recs[1:]
		lb.dropped++
	}
	lb.mu.Unlock()
	if lb.onLine != nil {
		lb.onLine(rec)
	}
	return rec.Line
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
