package native

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient points an HTTPClient at a test server.
func newTestClient(t *testing.T, h http.HandlerFunc) *HTTPClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &HTTPClient{base: srv.URL, hc: srv.Client()}
}

func TestHTTPApps(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apps" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte("1234\tFinder\tfocused\n5678\tTerminal\t\n"))
	})
	body, err := c.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Finder") || !strings.Contains(body, "focused") {
		t.Fatalf("apps body = %q", body)
	}
}

func TestHTTPTreeQuery(t *testing.T) {
	var got string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_, _ = w.Write([]byte("window\n└── button"))
	})
	out, err := c.Tree(context.Background(), 42, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "pid=42") || !strings.Contains(got, "depth=5") {
		t.Fatalf("query = %q", got)
	}
	if !strings.Contains(out, "button") {
		t.Fatalf("tree = %q", out)
	}
}

func TestHTTPFindOutput(t *testing.T) {
	var got string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_, _ = w.Write([]byte("100,200"))
	})
	out, err := c.Find(context.Background(), "button", 0, "center")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sel=button") || !strings.Contains(got, "o=center") {
		t.Fatalf("query = %q", got)
	}
	if out != "100,200" {
		t.Fatalf("find = %q", out)
	}
}

// TestHTTPErrorBody checks a 200 body of "error: ..." is surfaced as an error.
func TestHTTPErrorBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("error: accessibility service not connected"))
	})
	if _, err := c.Apps(context.Background()); err == nil {
		t.Fatal("expected an error from an 'error:' body")
	}
}

func TestHTTPScreenshot(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G'}, make([]byte, 4)...) // 8 bytes
	hdr := make([]byte, 16)
	binary.BigEndian.PutUint32(hdr[8:12], 640) // width at 16..20 overall
	binary.BigEndian.PutUint32(hdr[12:16], 480)
	png = append(png, hdr...)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(png)
	})
	got, err := c.Screenshot(context.Background(), "0,0,100,100")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(png) {
		t.Fatalf("png len = %d", len(got))
	}
}
