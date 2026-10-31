package job

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/easydev-org/easyworker/internal/script/lang"
)

// TestMaxRunningBoundsConcurrency is the whole point: with a bound of N, more than
// N interpreters must never be alive at once, even when the caller submits a burst.
//
// Each job writes its OWN start/end timestamps. A shared append log was the first
// attempt and it lied: `echo >> file` from nine processes interleaves, so the
// replay showed 9 "starts" before any "end" even when the bound held. Per-job
// files cannot interleave.
func TestMaxRunningBoundsConcurrency(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	// $1 = marker dir, $2 = this job's own file name.
	code := `echo start >> "$1/$2"
sleep 0.15
echo end  >> "$1/$2"`

	const limit = 3
	const total = 9
	r := NewRunner(p, os.Environ())
	r.SetMaxRunning(limit)

	jobs := make([]*Job, 0, total)
	for i := 0; i < total; i++ {
		j, err := r.Start(context.Background(), Spec{
			Lang: "bash", Code: code, Dir: dir,
			Args: []string{dir, "job" + itoaN(i)},
		})
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		jobs = append(jobs, j)
	}
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j *Job) { defer wg.Done(); <-j.Done() }(j)
	}
	// Sample the runner's slot usage while the burst is in flight. This is the
	// measurement that matters: not "how many did I submit" but "how many were
	// actually holding a slot at the same moment".
	peak := 0
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	for {
		if n := r.InFlight(); n > peak {
			peak = n
		}
		select {
		case <-done:
		default:
			time.Sleep(time.Millisecond)
			continue
		}
		if n := r.InFlight(); n > peak {
			peak = n
		}
		break
	}

	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != total {
		t.Fatalf("%d marker files, want %d (every job must have run)", len(entries), total)
	}
	for _, j := range jobs {
		if st := j.Status(); st.State != StateExited || st.ExitCode != 0 {
			t.Fatalf("job %s finished badly: %+v", j.ID, st)
		}
	}
	// The decisive check is peak slots held, sampled while the burst was running.
	if peak > limit {
		t.Fatalf("peak slots = %d, limit was %d", peak, limit)
	}
	if peak < 2 {
		t.Fatalf("peak slots = %d; the bound serialised jobs that should have overlapped", peak)
	}
	t.Logf("peak %d concurrent (limit %d) over %d jobs", peak, limit, total)
}

// itoaN renders a small int without pulling strconv into this test file twice.
func itoaN(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for i > 0 {
		d = append([]byte{byte('0' + i%10)}, d...)
		i /= 10
	}
	return string(d)
}

// TestQueuedJobsAreVisible: a job over the bound comes back as "queued" at once,
// so the caller is never blocked and can see why nothing is running yet.
func TestQueuedJobsAreVisible(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
	r := NewRunner(p, os.Environ())
	r.SetMaxRunning(1)

	first, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "sleep 0.4", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "echo hi", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// The second must be queued immediately (Start did not block).
	if st := second.Status().State; st != StateQueued {
		t.Fatalf("second job state = %s, want queued", st)
	}
	if st := first.Status().State; st != StateRunning {
		t.Fatalf("first job state = %s, want running", st)
	}
	// It starts once the first finishes.
	select {
	case <-second.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("queued job never ran")
	}
	if st := second.Status(); st.State != StateExited || st.ExitCode != 0 {
		t.Fatalf("queued job finished badly: %+v", st)
	}
	if got := joined(second.Snapshot()); !strings.Contains(got, "hi") {
		t.Fatalf("queued job did not produce output: %q", got)
	}
}

// TestKillQueuedJob: a job killed while queued must never start.
func TestKillQueuedJob(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	r := NewRunner(p, os.Environ())
	r.SetMaxRunning(1)

	first, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "sleep 0.5", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Start(context.Background(), Spec{
		Lang: "bash", Code: `echo SHOULD-NOT-RUN > "$1/ran"`, Dir: dir, Args: []string{dir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st := second.Status().State; st != StateQueued {
		t.Fatalf("state = %s, want queued", st)
	}
	second.Kill()
	select {
	case <-second.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("a killed queued job never settled")
	}
	if st := second.Status().State; st != StateKilled {
		t.Fatalf("state = %s, want killed", st)
	}
	<-first.Done()
	// Give the (now free) slot a moment: the killed job must not have used it.
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(dir + "/ran"); err == nil {
		t.Fatal("a queued job ran after being killed")
	}
}

// TestUnlimitedWhenZero: 0 means unlimited, and jobs start immediately.
func TestUnlimitedWhenZero(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
	r := NewRunner(p, os.Environ())
	r.SetMaxRunning(0)
	j, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "sleep 0.2", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if st := j.Status().State; st != StateRunning {
		t.Fatalf("state = %s, want running (no bound)", st)
	}
	<-j.Done()
}

// TestMaxRunningReports: the runner exposes its bound (the daemon logs it, the API
// can show it).
func TestMaxRunningReports(t *testing.T) {
	r := NewRunner(nil, nil)
	if got := r.MaxRunning(); got != 0 {
		t.Fatalf("unset = %d, want 0 (unlimited)", got)
	}
	r.SetMaxRunning(7)
	if got := r.MaxRunning(); got != 7 {
		t.Fatalf("= %d, want 7", got)
	}
}

// TestInFlightCountsRunningJobs: InFlight must report the number of jobs holding a
// slot right now. It read 0 during a 10-job burst in the daemon, which would make
// the "3/50 running" display useless — so this pins it at the unit level.
func TestInFlightCountsRunningJobs(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
	r := NewRunner(p, os.Environ())
	r.SetMaxRunning(2)

	j1, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "sleep 0.4", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	j2, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "sleep 0.4", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.InFlight(); got != 2 {
		t.Fatalf("InFlight right after 2 starts = %d, want 2", got)
	}
	<-j1.Done()
	<-j2.Done()
	// Slots are released asynchronously just after Done; allow a moment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.InFlight() != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.InFlight(); got != 0 {
		t.Fatalf("InFlight after completion = %d, want 0", got)
	}
}
