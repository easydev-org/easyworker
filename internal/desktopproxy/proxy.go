// Package desktopproxy serves the easystream daemon's own web UI under the
// worker's origin, at /desktop/.
//
// Why a proxy instead of a Service or a separate hostname: the worker and the
// daemon are ALWAYS in the same pod (the daemon listens on 127.0.0.1:48078), so
// there is nothing to discover. Mounting it on the worker's own origin means the
// panel needs no CORS, no second host, and no second port to expose — the user
// just opens /desktop/ in a new window.
//
// The easystream UI is written with RELATIVE paths (it derives its websocket as
// location.pathname + 'est/ws'), so it works unchanged under any prefix; the
// proxy only has to strip the prefix and forward.
package desktopproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// DefaultAddr is where the daemon listens (127.0.0.1 because it is same-pod).
const DefaultAddr = "127.0.0.1:48078"

// Handler proxies /desktop/* to the easystream daemon, including its /est/ws
// WebSocket (the live screen + input).
//
// mount is the prefix to strip, normally "/desktop".
//
// token is the CONTROL token. It is injected on the way through so the panel
// never has to hand it to the browser: the daemon authorizes View by default
// and only upgrades to Control (keyboard/mouse injection) when it sees the
// token. Without this the desktop is a read-only picture.
//
// The daemon reads the token from either
//
//	Sec-WebSocket-Protocol: est, <token>   (the browser-safe way, no URL/logs)
//	?token=<token>                          (plain HTTP / a11y calls)
//
// so both are supplied here.
func Handler(mount, addr, token string) http.Handler {
	if addr == "" {
		addr = DefaultAddr
	}
	target := &url.URL{Scheme: "http", Host: addr}
	inject := func(req *http.Request) {
		if token == "" {
			return
		}
		// 1) ?token= for plain HTTP requests and any query the page already had.
		q := req.URL.Query()
		if q.Get("token") == "" {
			q.Set("token", token)
			req.URL.RawQuery = q.Encode()
		}
		// 2) The WebSocket subprotocol list. The browser sends "est"; the daemon
		// expects the token as the second entry. Append it if it is not there.
		if proto := req.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
			parts := strings.Split(proto, ",")
			found := false
			for _, p := range parts {
				if strings.TrimSpace(p) == token {
					found = true
					break
				}
			}
			if !found {
				req.Header.Set("Sec-WebSocket-Protocol", strings.TrimSpace(proto)+", "+token)
			}
		}
	}
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Rewrite /desktop/app.js -> /app.js, /desktop/ -> /.
			p := strings.TrimPrefix(req.URL.Path, mount)
			if p == "" {
				p = "/"
			}
			req.URL.Path = p
			req.URL.Host = target.Host
			req.URL.Scheme = target.Scheme
			req.Host = target.Host
			// The daemon's own origin check is loopback-only, so keep it simple.
			req.Header.Del("Origin")
			inject(req)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "desktop (easystream) unavailable at %s: %v\n", addr, err)
		},
	}
	return &strip{mount: mount, next: rp}
}

// strip redirects a bare mount (no trailing slash) so that the UI's relative
// URLs resolve under the prefix, then delegates.
type strip struct {
	mount string
	next  http.Handler
}

func (s *strip) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == s.mount {
		http.Redirect(w, r, s.mount+"/", http.StatusFound)
		return
	}
	s.next.ServeHTTP(w, r)
}

// DialCheck reports whether the daemon is reachable (used to give the panel a
// clear "desktop not running yet" instead of a blank page).
func DialCheck(addr string) error {
	if addr == "" {
		addr = DefaultAddr
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}
