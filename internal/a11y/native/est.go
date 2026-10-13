// Package native is a thin client for the native-a11y daemon's easystream
// (`est://`) protocol (see abc-agent/worker-native/EST.md). ONE TCP connection
// multiplexes the H.264 video stream, input injection and a11y queries, so the
// Go worker owns no a11y logic — it only forwards.
package native

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Message types (EST.md).
const (
	tHello = 0x01
	tVideo = 0x02

	tMove   = 0x10
	tButton = 0x11
	tScroll = 0x12
	tKey    = 0x13
	tText   = 0x14
	tTouch  = 0x15 // sub:u8 (0=down,1=move,2=up) x:u16 y:u16 (x,y ignored on up)

	tAppsReq  = 0x20
	tAppsResp = 0x21
	tTreeReq  = 0x22
	tTreeResp = 0x23
	tFindReq  = 0x24
	tFindResp = 0x25
	tActReq   = 0x26
	tActResp  = 0x27

	tClipGet  = 0x30
	tClipData = 0x31
	tClipSet  = 0x32

	tShotReq  = 0x40
	tShotResp = 0x41
)

// Addr is the daemon address (EST.md).
func Addr() string {
	if a := os.Getenv("NATIVE_A11Y_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:48079"
}

// Enabled reports whether the worker should use the native-a11y backend. The Go
// worker has no a11y backend of its own: native is the ONLY mode.
func Enabled() bool { return true }

// spawnDaemon starts the native-a11y daemon if it is not already listening.
// The binary is NATIVE_A11Y_BIN, or `native-a11y[.exe]` next to the worker.
// This lets the worker bring up the daemon itself, so a guest whose baked
// launcher predates native-a11y still works (no golden rebake needed).
func spawnDaemon() error {
	if _, err := os.Stat("/proc/net/tcp"); err == nil {
		// (cheap guard; the dial already failed, so we know it is down)
	}
	bin := os.Getenv("NATIVE_A11Y_BIN")
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		dir := filepath.Dir(exe)
		name := "native-a11y"
		if runtime.GOOS == "windows" {
			name = "native-a11y.exe"
		}
		bin = filepath.Join(dir, name)
	}
	// If the binary is missing, fetch it from the container's nginx (the same
	// place the guest launcher boot-fetches the worker). This makes the worker
	// self-sufficient even on a guest whose baked launcher predates native-a11y.
	if _, err := os.Stat(bin); err != nil {
		url := os.Getenv("NATIVE_A11Y_URL")
		if url == "" {
			// Same endpoint the guest launcher boot-fetches from.
			url = "http://host.lan:8090/native-a11y"
		}
		if url != "" {
			if derr := downloadFile(url, bin); derr != nil {
				return derr
			}
		}
	}
	if _, err := os.Stat(bin); err != nil {
		return err
	}
	cmd := exec.Command(bin, "serve")
	// Send the daemon's output somewhere we can read it back on the guest.
	if logPath := os.Getenv("NATIVE_A11Y_LOG"); logPath != "" {
		if lf, lerr := os.Create(logPath); lerr == nil {
			cmd.Stdout = lf
			cmd.Stderr = lf
		}
	} else {
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Detach: we do not Wait, so the daemon outlives this call.
	go func() { _ = cmd.Wait() }()
	return nil
}

// downloadFile fetches url into path (used to boot-fetch the daemon binary).
func downloadFile(url, path string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	tmp := path + ".dl"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// The fetched binary must be executable (the guest launcher chmods its own
	// download; do the same here, or cmd.Start() fails with EACCES).
	if err := os.Chmod(path, 0o755); err != nil {
		return err
	}
	return nil
}

// Conn is one est connection. Reads are serialized by a single goroutine that
// fans out to per-request channels; writes are serialized by wmu.
type Conn struct {
	c   net.Conn
	wmu sync.Mutex

	Codec  string
	Width  int
	Height int
	FPS    int

	mu      sync.Mutex
	nextID  uint32
	pending map[uint32]chan []byte
	video   chan []byte
	closed  chan struct{}
}

// Dial connects and performs the est handshake.
func Dial(ctx context.Context) (*Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", Addr())
	if err != nil {
		// The daemon may not be running yet (e.g. a guest whose launcher predates
		// native-a11y). Try to start it ourselves, then retry once.
		if serr := spawnDaemon(); serr == nil {
			for i := 0; i < 20; i++ {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(500 * time.Millisecond):
				}
				if nc, err = d.DialContext(ctx, "tcp", Addr()); err == nil {
					break
				}
			}
		}
		if err != nil {
			return nil, err
		}
	}
	c := &Conn{
		c:       nc,
		pending: map[uint32]chan []byte{},
		video:   make(chan []byte, 8),
		closed:  make(chan struct{}),
	}
	typ, _, body, err := readFrame(nc)
	if err != nil || typ != tHello || len(body) < 7 {
		nc.Close()
		return nil, fmt.Errorf("native-a11y: bad hello (%v)", err)
	}
	if body[1] == 1 {
		c.Codec = "avc1.42E01F"
	}
	c.Width = int(binary.BigEndian.Uint16(body[2:4]))
	c.Height = int(binary.BigEndian.Uint16(body[4:6]))
	c.FPS = int(body[6])
	go c.readLoop()
	return c, nil
}

func readFrame(r io.Reader) (typ byte, flags byte, body []byte, err error) {
	var h [6]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return
	}
	typ, flags = h[0], h[1]
	n := binary.BigEndian.Uint32(h[2:6])
	body = make([]byte, n)
	_, err = io.ReadFull(r, body)
	return
}

func (c *Conn) readLoop() {
	defer close(c.closed)
	for {
		typ, _, body, err := readFrame(c.c)
		if err != nil {
			return
		}
		switch typ {
		case tVideo:
			select {
			case c.video <- body:
			default:
			}
		default:
			if len(body) >= 4 {
				id := binary.BigEndian.Uint32(body[0:4])
				c.mu.Lock()
				ch := c.pending[id]
				delete(c.pending, id)
				c.mu.Unlock()
				if ch != nil {
					ch <- body
				}
			}
		}
	}
}

func (c *Conn) send(typ byte, body []byte) error {
	var h [6]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[2:6], uint32(len(body)))
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.c.Write(h[:]); err != nil {
		return err
	}
	_, err := c.c.Write(body)
	return err
}

// request sends typ+body (first 4 bytes = id slot) and waits for the response
// carrying the same id.
func (c *Conn) request(typ byte, body []byte) ([]byte, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan []byte, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	binary.BigEndian.PutUint32(body[0:4], id)
	if err := c.send(typ, body); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-c.closed:
		return nil, io.EOF
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("native-a11y: request %#x timed out", typ)
	}
}

// Video returns the stream of Annex-B access units.
func (c *Conn) Video() <-chan []byte { return c.video }

// Close tears the connection down.
func (c *Conn) Close() error { return c.c.Close() }

// ---- input (C->S) ----

func (c *Conn) Move(x, y int32) error {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:2], uint16(x))
	binary.BigEndian.PutUint16(b[2:4], uint16(y))
	return c.send(tMove, b)
}

func (c *Conn) Button(button int, down bool) error {
	d := byte(0)
	if down {
		d = 1
	}
	return c.send(tButton, []byte{byte(button), d})
}

func (c *Conn) Scroll(x, y, dx, dy int32) error {
	b := make([]byte, 8)
	binary.BigEndian.PutUint16(b[0:2], uint16(x))
	binary.BigEndian.PutUint16(b[2:4], uint16(y))
	binary.BigEndian.PutUint16(b[4:6], uint16(int16(dx)))
	binary.BigEndian.PutUint16(b[6:8], uint16(int16(dy)))
	return c.send(tScroll, b)
}

// Touch sends a native touchscreen event (finger): sub 0=down, 1=move, 2=up.
func (c *Conn) Touch(sub byte, x, y int32) error {
	b := make([]byte, 5)
	b[0] = sub
	binary.BigEndian.PutUint16(b[1:3], uint16(x))
	binary.BigEndian.PutUint16(b[3:5], uint16(y))
	return c.send(tTouch, b)
}

// Key sends a key event. `name` is the named key ("" for a printable char);
// `ch` is the printable character (0 for a named key). EST.md Key frame:
// nameLen:u8 name nameLen char:u32 down:u8.
func (c *Conn) Key(name string, ch rune, down bool) error {
	d := byte(0)
	if down {
		d = 1
	}
	b := append([]byte{byte(len(name))}, []byte(name)...)
	var cb [4]byte
	binary.LittleEndian.PutUint32(cb[:], uint32(ch))
	b = append(b, cb[:]...)
	b = append(b, d)
	return c.send(tKey, b)
}

func (c *Conn) Text(s string) error { return c.send(tText, []byte(s)) }

// ---- clipboard (discrete, but only est exposes it) ----

func (c *Conn) ClipboardGet() ([]byte, error) { return c.request(tClipGet, make([]byte, 4)) }

func (c *Conn) ClipboardSet(s string) error {
	b := make([]byte, 4+4+len(s))
	binary.BigEndian.PutUint32(b[4:8], uint32(len(s)))
	copy(b[8:], s)
	return c.send(tClipSet, b)
}
