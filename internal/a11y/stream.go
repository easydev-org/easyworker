package goa11y

import (
	"context"
	"time"
)

// VideoCodec identifies a WebRTC-compatible video codec.
type VideoCodec string

const (
	CodecH264 VideoCodec = "H264"
)

// VideoEncoder converts raw BGRA frames into an encoded H.264 elementary
// stream (Annex-B NAL units) suitable for RTP/WebRTC.
type VideoEncoder interface {
	// Encode encodes one BGRA frame (top-down, stride = width*4) and returns
	// zero or more encoded access units (each a self-contained frame).
	Encode(bgra []byte) ([][]byte, error)
	// Codec returns the codec this encoder produces.
	Codec() VideoCodec
	// Width/Height/FPS describe the stream.
	Width() int
	Height() int
	FPS() int
	// Close flushes and releases resources.
	Close() error
}

// FrameSource produces BGRA frames at a target fps. Backends (WGC, CGWindowList,
// screencap, ...) implement it.
type FrameSource interface {
	// Frames returns a channel of BGRA frames. The channel is closed when the
	// source stops.
	Frames(ctx context.Context) (<-chan []byte, error)
	// Size returns the frame dimensions (valid after Frames starts).
	Size() (width, height int)
	// Close stops capture and releases resources.
	Close() error
}

// StreamStats reports live streaming statistics.
type StreamStats struct {
	FramesSent    uint64
	FramesDropped uint64
	Bitrate       int
	StartedAt     time.Time
}

// NALSource produces already-encoded H.264 access units (Annex-B) directly,
// bypassing the frame+encoder pipeline. Used on platforms whose native recorder
// both captures and encodes (e.g. Android screenrecord --output-format=h264).
type NALSource interface {
	// NALs returns a channel of H.264 access units (each an Annex-B AU).
	NALs(ctx context.Context) (<-chan []byte, error)
	// Codec reports the codec (H264).
	Codec() VideoCodec
	// FPS returns the target frame rate.
	FPS() int
	// Close stops the source.
	Close() error
}
