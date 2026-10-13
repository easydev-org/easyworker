package goa11y

// Screenshot carries raw pixel data (or an encoded image) from a capture.
type Screenshot struct {
	// Format is the encoding of Data, e.g. "png", "rgba8".
	Format string
	// Data is the encoded image bytes.
	Data []byte
	// Width, Height are set when known (0 when the format is opaque).
	Width, Height int
}

// ScreenshotProvider captures pixels. Backends that only capture do not need
// to read the a11y tree or synthesize input.
type ScreenshotProvider interface {
	CaptureFull() (*Screenshot, error)
	CaptureRegion(r Rect) (*Screenshot, error)
	Name() string
}
