package android

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	goa11y "github.com/easydev-org/easyworker/internal/a11y"
)

// Recorder records the screen using Android's native `screenrecord`, which
// captures the display once (a single process) and encodes with the hardware
// H.264 encoder. This is far better than repeated `screencap`: one process,
// ~10fps+, hardware encoding, small output.
type Recorder struct{}

func NewRecorder() *Recorder { return &Recorder{} }

func (r *Recorder) Name() string { return "android-screenrecord" }

func (r *Recorder) RecordNative(ctx context.Context, opts goa11y.NativeRecordOptions) ([]byte, string, error) {
	dur := opts.Duration
	if dur <= 0 {
		dur = 5 * time.Second
	}
	// screenrecord caps at 180s per invocation.
	secs := int(dur / time.Second)
	if secs <= 0 {
		secs = 1
	}
	out := "/data/local/tmp/.goa11y-rec.mp4"
	os.Remove(out)

	args := []string{"--time-limit", strconv.Itoa(secs)}
	if opts.Width > 0 && opts.Height > 0 {
		args = append(args, "--size", fmt.Sprintf("%dx%d", opts.Width, opts.Height))
	}
	br := opts.BitRate
	if br <= 0 {
		br = 8000000
	}
	args = append(args, "--bit-rate", strconv.Itoa(br))
	args = append(args, out)

	cmd := exec.CommandContext(ctx, "screenrecord", args...)
	if err := cmd.Run(); err != nil {
		// screenrecord exits non-zero if interrupted; still check output.
		if _, statErr := os.Stat(out); statErr != nil {
			return nil, "", goa11y.PlatformErr(-1, "screenrecord: "+err.Error())
		}
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, "", goa11y.PlatformErr(-1, "read recording: "+err.Error())
	}
	os.Remove(out)
	return data, "video/mp4", nil
}
