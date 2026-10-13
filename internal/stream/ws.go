// WebCodecs-over-WebSocket screen streaming.
//
// The browser cannot hand a raw H.264 byte stream to <video>, but it CAN decode
// it directly with the WebCodecs `VideoDecoder`. This streams the platform's
// existing H.264 access units (Annex-B) over ONE WebSocket — the SAME 443/TCP
// connection the page already uses — so there is NO WebRTC / UDP / STUN / TURN /
// ICE. The browser decodes each AU and paints a canvas. Remote input + clipboard
// ride the same socket (the control protocol in internal/a11y/control.go is
// unchanged; only the transport differs from the old WebRTC DataChannel).
//
// Wire format
//
//	server -> client: a TEXT frame {t:"hello",codec,w,h,fps} first, then one
//	  BINARY frame per H.264 access unit (Annex-B).
//	client -> server: BINARY frames = the control protocol.
package stream

import (
	"context"
	"encoding/json"
	"log"
	"time"
	"net/http"

	a11y "github.com/easydev-org/easyworker/internal/a11y"
	"github.com/easydev-org/easyworker/internal/a11y/native"
	"golang.org/x/net/websocket"
)

// codecString is the WebCodecs codec string for the negotiated profile
// (42e01f = Constrained Baseline). x264 is forced to Baseline so this matches
// the bitstream.
const codecString = "avc1.42E01F"

type wsHello struct {
	T     string `json:"t"`
	Codec string `json:"codec"`
	W     int    `json:"w"`
	H     int    `json:"h"`
	FPS   int    `json:"fps"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	websocket.Handler(func(conn *websocket.Conn) { s.serveWS(conn) }).ServeHTTP(w, r)
}

func (s *Server) serveWS(conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The worker runs INSIDE the guest on every platform and talks to the
	// native-a11y est daemon over 127.0.0.1:48079. Input, clipboard and the
	// video stream all ride that one est connection; touch is distinct from the
	// mouse so a device with both routes a finger to the touchscreen.
	in, clip := native.NewInputProvider(), native.NewClipboard()
	ctrl := a11y.NewControlHandler(in, clip)
	ctrl.SetTouch(in)

	nals, err := native.NewNALSource(ctx)
	if err != nil {
		log.Printf("stream/ws: %v", err)
		return
	}
	w, h, fps := nals.Width(), nals.Height(), nals.FPS()
	defer nals.Close()

	auCh, aerr := nals.NALs(ctx)
	if aerr != nil {
		log.Printf("stream/ws: nals: %v", aerr)
		return
	}

	// Read access unit(s) until we have an SPS (to learn the real profile/level)
	// AND an IDR slice. Some encoders (macOS VideoToolbox) emit a
	// parameter-set-only first AU; a decoder that starts on the following delta
	// frame errors out, and a client waiting for a key frame shows nothing.
	// Bound the wait so a platform whose encoder never produces an IDR (or any
	// frame) does not hang the whole session.
	var first []byte
	gotSPS := false
	deadline := time.After(4 * time.Second)
	select {
	case <-ctx.Done():
		return
	case <-deadline:
		log.Printf("stream/ws: no first AU within 4s (proceeding)")
	case au, ok := <-auCh:
		if !ok {
			return
		}
		first = au
		gotSPS = nalTypeIn(au, 7)
	}
	for first != nil && (!gotSPS || !nalTypeIn(first, 5)) {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			log.Printf("stream/ws: no IDR within 4s (proceeding with %d bytes)", len(first))
			goto ready
		case au, ok := <-auCh:
			if !ok {
				return
			}
			first = append(first, au...)
			if nalTypeIn(au, 7) {
				gotSPS = true
			}
		}
	}
ready:

	// Prefer the codec string the daemon reported in its est hello (reliable);
	// fall back to parsing the SPS out of the first access unit.
	codec := codecStringFromAU(first)
	if cs, ok := any(nals).(interface{ CodecString() string }); ok {
		if v := cs.CodecString(); v != "" && v != "avc1.000000" {
			codec = v
		}
	}
	hello, _ := json.Marshal(wsHello{T: "hello", Codec: codec, W: w, H: h, FPS: fps})
	if werr := websocket.Message.Send(conn, string(hello)); werr != nil {
		return
	}
	if werr := websocket.Message.Send(conn, first); werr != nil {
		return
	}
	s.mu.Lock()
	s.wsFrames++
	s.mu.Unlock()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var msg []byte
		for {
			if rerr := websocket.Message.Receive(conn, &msg); rerr != nil {
				cancel()
				return
			}
			if len(msg) == 0 {
				continue
			}
			if reply, ok := ctrl.Handle(msg); ok && reply != nil {
				_ = websocket.Message.Send(conn, reply)
			}
		}
	}()

	s.mu.Lock()
	s.activeWS++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.activeWS--
		s.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-readerDone:
			return
		case au, ok := <-auCh:
			if !ok {
				return
			}
			if serr := websocket.Message.Send(conn, au); serr != nil {
				return
			}
			s.mu.Lock()
			s.wsFrames++
			s.mu.Unlock()
		}
	}
}

// codecStringFromAU derives the WebCodecs codec string (avc1.PPCCLL) from the
// SPS in an Annex-B access unit, so the client configures its VideoDecoder for
// the ACTUAL profile/level (x264 picks the level from resolution/fps; the SDP's
// fixed level would otherwise disagree and strict decoders reject it).
func codecStringFromAU(au []byte) string {
	for i := 0; i+3 < len(au); i++ {
		// start code
		if au[i] == 0 && au[i+1] == 0 && (au[i+2] == 1 || (au[i+2] == 0 && au[i+3] == 1)) {
			off := i + 3
			if au[i+2] == 0 {
				off = i + 4
			}
			if off+3 >= len(au) {
				break
			}
			if au[off]&0x1f == 7 { // SPS
				// [nal hdr][profile_idc][constraint flags][level_idc]
				p, c, l := au[off+1], au[off+2], au[off+3]
				return "avc1." + hex2(p) + hex2(c) + hex2(l)
			}
			i = off
		}
	}
	return ""
}

func hex2(b byte) string {
	const h = "0123456789abcdef"
	return string([]byte{h[b>>4], h[b&0xf]})
}

// nalTypeIn reports whether an Annex-B access unit contains a NAL of type t.
func nalTypeIn(au []byte, t byte) bool {
	for i := 0; i+3 < len(au); i++ {
		if au[i] == 0 && au[i+1] == 0 && (au[i+2] == 1 || (au[i+2] == 0 && au[i+3] == 1)) {
			off := i + 3
			if au[i+2] == 0 {
				off = i + 4
			}
			if off < len(au) && au[off]&0x1f == t {
				return true
			}
			i = off
		}
	}
	return false
}
