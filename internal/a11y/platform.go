package goa11y

// Platform bundles the three capability providers for the current OS. The
// concrete instance is selected by build tags (platform_<os>.go).
type Platform interface {
	Provider() Provider
	Input() InputProvider
	Screenshot() ScreenshotProvider
	// NativeRecorder returns a platform-native screen recorder, or nil when the
	// backend has none (the CLI then falls back to frame-by-frame capture).
	NativeRecorder() NativeRecorder
	// FrameSource returns a BGRA frame source for WebRTC streaming, or nil.
	FrameSource() FrameSource
	// H264Encoder returns a streaming H.264 encoder for the given size/fps.
	H264Encoder(width, height, fps, bitrate int) (VideoEncoder, error)
	// NALSource returns a direct H.264 NAL source (bypasses frame+encoder), or
	// nil. Used on Android where screenrecord both captures and encodes.
	NALSource() NALSource
	// Clipboard returns the text-clipboard backend, or nil when unavailable.
	Clipboard() Clipboard
	OSName() string
}
