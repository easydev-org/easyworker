// Package stream exposes the sandbox screen as an H.264 access-unit stream
// over a single WebSocket (WebCodecs in the browser) — no WebRTC / UDP / STUN.
//
// The worker owns no a11y logic: on the VMs the native-a11y daemon captures and
// encodes (the worker forwards Annex-B over the est connection); on Android the
// worker drives the guest's hardware encoder over adb. See ws.go.
package stream

import (
	"net/http"
	"sync"
)

// Prefix is the mount point of the est (easystream) endpoints.
const Prefix = "/est/"

// Server streams the screen over one WebSocket (single viewer).
type Server struct {
	fps      int
	bitrate  int
	mu       sync.Mutex
	activeWS int
	wsFrames uint64
}

// New creates a stream server at the given target fps + bitrate.
func New(fps, bitrate int) *Server {
	if fps <= 0 {
		fps = 10
	}
	if bitrate <= 0 {
		bitrate = 8_000_000
	}
	return &Server{fps: fps, bitrate: bitrate}
}

// Handler returns the mux for /est/*.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/est/ws", s.handleWS)
	return mux
}
