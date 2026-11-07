package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testUI() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>nvr</title>")},
		"assets/app-1a.js": {Data: []byte("console.log(1)")},
	}
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	rec := get(t, New(Options{UI: testUI()}), "/healthz")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz: %d %q", rec.Code, rec.Body.String())
	}
}

func TestVersion(t *testing.T) {
	rec := get(t, New(Options{Version: "1.2.3", UI: testUI()}), "/api/version")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"1.2.3"`) {
		t.Fatalf("version: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSourceStatus(t *testing.T) {
	rec := get(t, New(Options{UI: testUI()}), "/api/source")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatalf("unconfigured source: %d %s", rec.Code, rec.Body.String())
	}

	h := New(Options{UI: testUI(), Source: func(context.Context) SourceStatus {
		return SourceStatus{Configured: true, OK: true, URL: "https://dav.example/dav/", Folders: []string{"2026"}}
	}})
	rec = get(t, h, "/api/source")
	if !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), `"folders":["2026"]`) {
		t.Fatalf("configured source: %s", rec.Body.String())
	}
}

func TestAPIWithoutServerSays503(t *testing.T) {
	rec := get(t, New(Options{UI: testUI()}), "/api/clips?from=0&to=1")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "WEBDAV_URL") {
		t.Fatalf("unconfigured api: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSPAFallbackAndAssets(t *testing.T) {
	h := New(Options{UI: testUI()})

	for _, p := range []string{"/", "/cameras/front", "/index.html"} {
		rec := get(t, h, p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>nvr</title>") {
			t.Fatalf("%s: want index.html, got %d %q", p, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: index must not be cached long-term", p)
		}
	}

	rec := get(t, h, "/assets/app-1a.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset: %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("fingerprinted assets should be cached as immutable")
	}
}

func TestMissingUIExplainsHowToBuild(t *testing.T) {
	rec := get(t, New(Options{UI: fstest.MapFS{}}), "/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make web") {
		t.Fatalf("missing UI: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSPARejectsWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	New(Options{UI: testUI()}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: %d", rec.Code)
	}
}
