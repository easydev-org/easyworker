package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHandlerServesPanel(t *testing.T) {
	rec := get(t, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	// A STUB IS SKIPPED, LIKE IN TestHandlerServesBuiltAssets JUST BELOW — the two must agree, and they
	// did not: this one failed on a placeholder while the other skipped. The platform jobs create a stub
	// on purpose (they test the engine on Windows and macOS and have no Node toolchain), so failing there
	// reports "the panel is broken" about a file the job put there itself.
	//
	// The title assertion is real wherever dist/ came from a build — which is the `build` job, and any
	// developer who has run `npm run build`.
	if !strings.Contains(rec.Body.String(), "/assets/") {
		t.Skip("the embedded panel is a stub — the title belongs to a real build")
	}
	if !strings.Contains(rec.Body.String(), "Agent Worker") {
		t.Error("index should carry the Agent Worker title")
	}
}

func TestHandlerServesBuiltAssets(t *testing.T) {
	// The vite build emits /assets/index-*.js + .css; the SPA entry references
	// them. Walk the embedded FS to find one of each and fetch it.
	html := get(t, "/").Body.String()
	// A checked-in placeholder shell has no built assets (the real panel comes
	// from webui/ at image-build time), so there is nothing to fetch. Skip rather
	// than fail: this test is about the HANDLER serving assets that exist, not
	// about whether the panel was built.
	if !strings.Contains(html, "/assets/") {
		t.Skip("no built assets embedded (npm run build produces them)")
	}
	for _, want := range []string{"/assets/", ".js", ".css"} {
		if !strings.Contains(html, want) {
			t.Fatalf("index missing %q", want)
		}
	}
}

func TestHandlerFallsBackToIndex(t *testing.T) {
	rec := get(t, "/some/deep/link")
	// THE SUBJECT IS THE FALLBACK, NOT THE PANEL'S CONTENT. Asserting on the title made this test fail on
	// a stub panel — saying nothing about whether an unknown path falls back — and would have failed
	// again the moment the panel's wording changed. What matters is that an unknown path is answered with
	// the SPA entry and a 200 rather than a 404.
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("fallback content-type = %q, want text/html", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") {
		t.Fatalf("fallback body is not the SPA entry: %q", body)
	}
}

func TestHandlerRejectsNonGet(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d want 405", rec.Code)
	}
}
