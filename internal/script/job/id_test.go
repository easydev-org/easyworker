package job

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/easydev-org/easyworker/internal/script/lang"
)

// TestJobIDsAreUniqueAcrossRunners: a job is persisted, so its id must not
// collide with one from a previous daemon. A plain counter (job-1, job-2, ...)
// would restart at job-1 and overwrite the stored log of the old job-1.
func TestJobIDsAreUniqueAcrossRunners(t *testing.T) {
	p := lang.NewProbe(os.Environ(), t.TempDir())
	if _, ok := p.Supported(context.Background())["bash"]; !ok {
		t.Skip("no bash")
	}
	seen := map[string]bool{}
	// Two runners stand in for two daemon lifetimes.
	for i := 0; i < 2; i++ {
		r := NewRunner(p, os.Environ())
		for j := 0; j < 3; j++ {
			jb, err := r.Start(context.Background(), Spec{Lang: "bash", Code: "true", Dir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			<-jb.Done()
			if seen[jb.ID] {
				t.Fatalf("duplicate job id across runners: %q", jb.ID)
			}
			seen[jb.ID] = true
			if !strings.HasPrefix(jb.ID, "job-") {
				t.Fatalf("unexpected id shape: %q", jb.ID)
			}
		}
	}
	if len(seen) != 6 {
		t.Fatalf("expected 6 unique ids, got %d", len(seen))
	}
}
