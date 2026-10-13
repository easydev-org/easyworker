package goa11y

import (
	"context"
	"time"
)

// NativeRecorder is implemented by backends that can record the screen with a
// platform-native, single-process capture path (e.g. Android screenrecord,
// macOS CGDisplayStream, Windows DXGI). It is far more efficient than the
// generic frame-by-frame Recorder: one process/stream, higher fps, and often
// hardware encoding.
type NativeRecorder interface {
	// RecordNative records for the given duration (or until ctx is cancelled
	// when duration is 0) and returns the encoded video bytes + MIME type.
	RecordNative(ctx context.Context, opts NativeRecordOptions) ([]byte, string, error)
	Name() string
}

// NativeRecordOptions configures a native recording.
type NativeRecordOptions struct {
	Duration time.Duration
	FPS      int
	Width    int
	Height   int
	BitRate  int // bits per second (0 = backend default)
	Region   *Rect
}
