// Package httpapi wires the HTTP routes: health, JSON API and the single-page UI.
package httpapi

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Options configures the HTTP handler.
type Options struct {
	Version string
	UI      fs.FS // built web UI (index.html + assets/); may be empty in dev builds
	// Source checks the connection to the recordings server. Nil means none is configured.
	Source func(ctx context.Context) SourceStatus
	// Index serves clips. Nil (no recordings server configured) makes those routes answer 503.
	Index *Index
}

// SourceStatus is the result of a live connection check against the recordings server.
type SourceStatus struct {
	Configured bool      `json:"configured"`
	URL        string    `json:"url,omitempty"` // never includes credentials
	Username   string    `json:"username,omitempty"`
	Profile    string    `json:"profile,omitempty"`
	TimeZone   string    `json:"time_zone,omitempty"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	Folders    []string  `json:"folders,omitempty"` // top-level folders under the root
	Files      int       `json:"files"`             // files directly in the root
	CheckedAt  time.Time `json:"checked_at"`
}

// New returns the root handler.
func New(opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": opts.Version})
	})
	mux.HandleFunc("GET /api/source", func(w http.ResponseWriter, r *http.Request) {
		if opts.Source == nil {
			writeJSON(w, http.StatusOK, SourceStatus{CheckedAt: time.Now().UTC()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, opts.Source(ctx))
	})
	if opts.Index != nil {
		(&api{Index: *opts.Index}).routes(mux)
	}
	// Unknown API paths must not fall through to the SPA's index.html.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		if opts.Index == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no recordings server configured; set WEBDAV_URL"})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	mux.Handle("/", spa(opts.UI))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// spa serves static files from ui and falls back to index.html for client-side routes.
func spa(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(ui, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite fingerprints asset names, so they never change.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(ui, "index.html")
		if err != nil {
			http.Error(w, "The web UI isn't built into this binary. Run `make web` and rebuild.", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
