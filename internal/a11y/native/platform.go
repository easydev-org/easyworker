package native

import (
	"context"
	"sync"

	a11y "github.com/easydev-org/easyworker/internal/a11y"
)

// shared is the process-wide est connection (one daemon per sandbox). Both the
// stream (video + input) and the computer driver use it.
var (
	sharedMu sync.Mutex
	shared   *Conn
)

// Shared returns the process-wide est connection, dialing on first use.
func Shared(ctx context.Context) (*Conn, error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared != nil {
		return shared, nil
	}
	c, err := Dial(ctx)
	if err != nil {
		return nil, err
	}
	shared = c
	return shared, nil
}

// InputProvider implements a11y.InputProvider over est. Input is owned by the
// daemon (same process as capture), so there is no cross-backend deadlock.
type InputProvider struct{}

func NewInputProvider() *InputProvider { return &InputProvider{} }

func (p *InputProvider) Name() string { return "native-a11y-est" }

func (p *InputProvider) conn() (*Conn, error) { return Shared(context.Background()) }

func (p *InputProvider) PointerMove(to a11y.Point) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Move(to.X, to.Y)
}

func (p *InputProvider) PointerDown(b a11y.MouseButton) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Button(btnNum(b), true)
}

func (p *InputProvider) PointerUp(b a11y.MouseButton) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Button(btnNum(b), false)
}

func (p *InputProvider) PointerClick(at a11y.Point, b a11y.MouseButton, count uint32) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	if err := c.Move(at.X, at.Y); err != nil {
		return err
	}
	n := int(count)
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		if err := c.Button(btnNum(b), true); err != nil {
			return err
		}
		if err := c.Button(btnNum(b), false); err != nil {
			return err
		}
	}
	return nil
}

func (p *InputProvider) PointerScroll(at a11y.Point, d a11y.ScrollDelta) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Scroll(at.X, at.Y, d.PixelX+d.LineX*40, d.PixelY+d.LineY*40)
}

// TouchProvider: native touchscreen events (finger), distinct from the mouse.
func (p *InputProvider) TouchDown(to a11y.Point) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Touch(0, to.X, to.Y)
}

func (p *InputProvider) TouchMove(to a11y.Point) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Touch(1, to.X, to.Y)
}

func (p *InputProvider) TouchUp() error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Touch(2, 0, 0)
}

func (p *InputProvider) KeyDown(k a11y.Key) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Key(k.Name, k.Char, true)
}

func (p *InputProvider) KeyUp(k a11y.Key) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Key(k.Name, k.Char, false)
}

func (p *InputProvider) TypeText(text string) error {
	c, err := p.conn()
	if err != nil {
		return err
	}
	return c.Text(text)
}

func btnNum(b a11y.MouseButton) int {
	switch b {
	case a11y.ButtonRight:
		return 3
	case a11y.ButtonMiddle:
		return 2
	default:
		return 1
	}
}

// Clipboard implements a11y.Clipboard over est.
type Clipboard struct{}

func NewClipboard() *Clipboard { return &Clipboard{} }

func (cl *Clipboard) Name() string { return "native-a11y-est" }

func (cl *Clipboard) Get() (string, error) {
	c, err := Shared(context.Background())
	if err != nil {
		return "", err
	}
	b, err := c.ClipboardGet()
	if err != nil {
		return "", err
	}
	// resp: u32 id, u32 len, text[len]
	if len(b) < 8 {
		return "", nil
	}
	return string(b[8:]), nil
}

func (cl *Clipboard) Set(s string) error {
	c, err := Shared(context.Background())
	if err != nil {
		return err
	}
	return c.ClipboardSet(s)
}

// NALSource streams the est video channel as a11y.NALSource.
type NALSource struct {
	conn   *Conn
	codec  a11y.VideoCodec
	fps    int
	width  int
	height int
}

// NewNALSource dials (or reuses) the est connection and returns a NAL source.
func NewNALSource(ctx context.Context) (*NALSource, error) {
	c, err := Shared(ctx)
	if err != nil {
		return nil, err
	}
	return &NALSource{conn: c, codec: a11y.CodecH264, fps: c.FPS, width: c.Width, height: c.Height}, nil
}

func (s *NALSource) Codec() a11y.VideoCodec { return s.codec }
// CodecString is the WebCodecs codec string the daemon reported in its hello
// (e.g. "avc1.42E01F"), or "" when unknown.
func (s *NALSource) CodecString() string { return s.conn.Codec }
func (s *NALSource) FPS() int               { return s.fps }
func (s *NALSource) Width() int             { return s.width }
func (s *NALSource) Height() int            { return s.height }
func (s *NALSource) Close() error           { return nil }

func (s *NALSource) NALs(ctx context.Context) (<-chan []byte, error) {
	return s.conn.Video(), nil
}
