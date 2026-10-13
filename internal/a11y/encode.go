package goa11y

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"time"
)

// MP4Encoder encodes captured frames into an MP4 video, purely in Go: each
// frame is JPEG-compressed (stdlib image/jpeg) and muxed into an MP4 container
// as an MJPEG track. No external tools, no cgo.
type MP4Encoder struct {
	fps    int
	width  int
	height int
	frames [][]byte // JPEG-encoded frames
	inited bool
}

// NewMP4Encoder creates a pure-Go MP4 encoder for the given fps.
func NewMP4Encoder(fps int) *MP4Encoder {
	if fps <= 0 {
		fps = 10
	}
	return &MP4Encoder{fps: fps}
}

func (e *MP4Encoder) Name() string { return "mp4-mjpeg" }

func (e *MP4Encoder) AddFrame(pngData []byte, at time.Time) error {
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return err
	}
	return e.AddImage(img, at)
}

// AddImage adds an already-decoded image (avoids a PNG round-trip).
func (e *MP4Encoder) AddImage(img image.Image, at time.Time) error {
	if !e.inited {
		b := img.Bounds()
		e.width, e.height = b.Dx(), b.Dy()
		e.inited = true
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return err
	}
	e.frames = append(e.frames, buf.Bytes())
	return nil
}

func (e *MP4Encoder) Finish() ([]byte, string, error) {
	if len(e.frames) == 0 {
		return nil, "", &Error{Kind: ErrInvalidConfig, Message: "no frames encoded"}
	}
	data, err := muxMJPEG(e.frames, e.width, e.height, e.fps)
	if err != nil {
		return nil, "", err
	}
	return data, "video/mp4", nil
}
