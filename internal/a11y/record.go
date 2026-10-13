package goa11y

import (
	"context"
	"time"
)

// Recorder captures a screen recording as a sequence of frames, encoded by a
// FrameEncoder. Recording is "capture at a target fps + encode", built on the
// same ScreenshotProvider the rest of goa11y uses.
type Recorder struct {
	shot    ScreenshotProvider
	encoder FrameEncoder
	fps     int
	region  *Rect
}

// FrameEncoder consumes captured frames and produces the final artifact.
type FrameEncoder interface {
	// AddFrame adds one frame (PNG bytes) captured at the given time.
	AddFrame(png []byte, at time.Time) error
	// Finish finalizes the recording and returns the encoded bytes + a MIME type.
	Finish() ([]byte, string, error)
	// Name identifies the encoder.
	Name() string
}

// RecordOptions configures a recording.
type RecordOptions struct {
	FPS      int    // target frames per second (default 10)
	Duration time.Duration // 0 = until ctx cancelled
	Region   *Rect // nil = full screen
	Encoder  FrameEncoder
}

// NewRecorder creates a recorder over a screenshot provider.
func NewRecorder(shot ScreenshotProvider, opts RecordOptions) *Recorder {
	fps := opts.FPS
	if fps <= 0 {
		fps = 10
	}
	return &Recorder{shot: shot, encoder: opts.Encoder, fps: fps, region: opts.Region}
}

// Record captures frames until ctx is cancelled or the duration elapses, then
// finalizes the encoder.
func (r *Recorder) Record(ctx context.Context) ([]byte, string, error) {
	if r.encoder == nil {
		return nil, "", &Error{Kind: ErrInvalidConfig, Message: "no frame encoder"}
	}
	interval := time.Second / time.Duration(r.fps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	capture := func() error {
		shot, err := r.captureFrame()
		if err != nil {
			return err
		}
		return r.encoder.AddFrame(shot.Data, time.Now())
	}
	// first frame immediately
	if err := capture(); err != nil {
		return nil, "", err
	}
	for {
		select {
		case <-ctx.Done():
			return r.encoder.Finish()
		case <-ticker.C:
			if err := capture(); err != nil {
				// skip a bad frame rather than abort the whole recording
				continue
			}
		}
	}
}

func (r *Recorder) captureFrame() (*Screenshot, error) {
	if r.region != nil {
		return r.shot.CaptureRegion(*r.region)
	}
	return r.shot.CaptureFull()
}
