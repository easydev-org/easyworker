package desktopproxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInjectsTokenOnHTTP covers the reason the desktop was read-only: the proxy
// must add ?token= to every request it forwards, because the easystream daemon
// authorizes View by default and only upgrades to Control when it sees the
// token. Without this, driving the desktop (keyboard/mouse) is refused.
func TestInjectsTokenOnHTTP(t *testing.T) {
	var got string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		io.WriteString(w, "ok")
	}))
	defer daemon.Close()

	addr := strings.TrimPrefix(daemon.URL, "http://")
	srv := httptest.NewServer(Handler("/desktop", addr, "tok123"))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/desktop/a11y/action?action=click")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.Contains(got, "token=tok123") {
		t.Fatalf("daemon saw query %q, want it to contain token=tok123", got)
	}
	if !strings.Contains(got, "action=click") {
		t.Fatalf("daemon saw query %q, want it to keep the original params", got)
	}
}

// TestInjectsTokenOnWebSocket covers the subprotocol path: the UI opens its
// socket with "est", and the daemon reads the token from the SECOND entry.
func TestInjectsTokenOnWebSocket(t *testing.T) {
	var proto string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto = r.Header.Get("Sec-WebSocket-Protocol")
		io.WriteString(w, "ok")
	}))
	defer daemon.Close()

	addr := strings.TrimPrefix(daemon.URL, "http://")
	srv := httptest.NewServer(Handler("/desktop", addr, "tok123"))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/desktop/est/ws", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "est")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.Contains(proto, "tok123") {
		t.Fatalf("daemon saw protocol %q, want it to contain the token", proto)
	}
}

// TestNoTokenMeansNoInjection keeps the "auth disabled" case honest.
func TestNoTokenMeansNoInjection(t *testing.T) {
	var got string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		io.WriteString(w, "ok")
	}))
	defer daemon.Close()

	addr := strings.TrimPrefix(daemon.URL, "http://")
	srv := httptest.NewServer(Handler("/desktop", addr, ""))
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/desktop/x?a=1")
	resp.Body.Close()
	if strings.Contains(got, "token=") {
		t.Fatalf("daemon saw %q, want no token when none is configured", got)
	}
}

var _ = net.Dial
