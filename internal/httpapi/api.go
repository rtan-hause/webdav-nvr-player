package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/rtan-hause/webdav-nvr-player/internal/httpx"
	"github.com/rtan-hause/webdav-nvr-player/internal/ingest"
	"github.com/rtan-hause/webdav-nvr-player/internal/media"
	"github.com/rtan-hause/webdav-nvr-player/internal/profile"
	"github.com/rtan-hause/webdav-nvr-player/internal/signedurl"
	"github.com/rtan-hause/webdav-nvr-player/internal/source/webdav"
	"github.com/rtan-hause/webdav-nvr-player/internal/store"
	"github.com/rtan-hause/webdav-nvr-player/internal/thumbs"
)

// Index is everything the clip API needs. All fields are required.
type Index struct {
	Store    *store.Store
	Ingest   Ingester
	Source   Streamer
	Thumbs   *thumbs.Maker
	Signer   *signedurl.Signer
	Location *time.Location // camera time zone, for day boundaries
	// IngestSecret protects POST /api/ingest and /api/rescan. Empty disables both.
	IngestSecret string
}

// Ingester is the part of *ingest.Ingester the API uses.
type Ingester interface {
	Notify(ctx context.Context, relPath string) (profile.Kind, error)
	Rescan(ctx context.Context) bool
	Status() ingest.Status
}

// Streamer is the part of *webdav.Client used to stream recordings.
type Streamer interface {
	Stream(ctx context.Context, method, relPath string, in http.Header) (*http.Response, error)
	ReaderAt(ctx context.Context, relPath string) io.ReaderAt
}

type api struct {
	Index
	remuxes remuxCache
}

func (a *api) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", a.status)
	mux.HandleFunc("GET /api/cameras", a.cameras)
	mux.HandleFunc("GET /api/clips", a.clips)
	mux.HandleFunc("GET /api/clips/{id}", a.clip)
	mux.HandleFunc("GET /api/clips/{id}/thumb", a.thumb)
	mux.HandleFunc("GET /api/clips/{id}/stream", a.stream)
	mux.HandleFunc("HEAD /api/clips/{id}/stream", a.stream)
	mux.HandleFunc("GET /api/days", a.days)
	mux.HandleFunc("POST /api/ingest", a.ingest)
	mux.HandleFunc("POST /api/rescan", a.rescan)
	mux.HandleFunc("GET /internal/raw/{token}", a.raw)
	mux.HandleFunc("HEAD /internal/raw/{token}", a.raw)
}

// ---- reading the index ----

// ClipJSON is a clip as the UI sees it. Times are Unix milliseconds.
type ClipJSON struct {
	ID        int64           `json:"id"`
	CameraID  int64           `json:"camera_id"`
	Start     int64           `json:"start"`
	End       int64           `json:"end"`
	Codec     string          `json:"codec"` // display name, e.g. "HEVC"
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Size      int64           `json:"size"`
	File      string          `json:"file"`
	Thumb     bool            `json:"thumb"`
	ThumbKind store.ThumbKind `json:"thumb_kind,omitempty"`
}

func clipJSON(c store.Clip) ClipJSON {
	return ClipJSON{
		ID: c.ID, CameraID: c.CameraID, Start: c.StartMS, End: c.EndMS, Codec: media.CodecName(c.Codec),
		Width: c.Width, Height: c.Height, Size: c.Size, File: path.Base(c.Path),
		Thumb: c.ThumbPath != "", ThumbKind: c.ThumbKind,
	}
}

// SnapshotJSON is a snapshot not paired with any clip (shown as a dot).
type SnapshotJSON struct {
	ID       int64 `json:"id"`
	CameraID int64 `json:"camera_id"`
	TS       int64 `json:"ts"`
}

func (a *api) status(w http.ResponseWriter, r *http.Request) {
	st, err := a.Store.Stats(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"index":     st,
		"ingest":    a.Ingest.Status(),
		"database":  a.Store.Dialect(),
		"time_zone": a.Location.String(),
		"webhook":   a.IngestSecret != "",
	})
}

func (a *api) cameras(w http.ResponseWriter, r *http.Request) {
	cams, err := a.Store.Cameras(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cams == nil {
		cams = []store.Camera{}
	}
	httpx.JSON(w, http.StatusOK, cams)
}

const maxRange = 8 * 24 * time.Hour

func (a *api) clips(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err1 := strconv.ParseInt(q.Get("from"), 10, 64)
	to, err2 := strconv.ParseInt(q.Get("to"), 10, 64)
	if err1 != nil || err2 != nil || to <= from {
		httpx.Error(w, http.StatusBadRequest, "from and to must be Unix milliseconds with from < to")
		return
	}
	if to-from > maxRange.Milliseconds() {
		httpx.Error(w, http.StatusBadRequest, "ask for at most 8 days at a time")
		return
	}
	var cam int64
	if c := q.Get("camera"); c != "" {
		if cam, err1 = strconv.ParseInt(c, 10, 64); err1 != nil {
			httpx.Error(w, http.StatusBadRequest, "camera must be a camera id")
			return
		}
	}
	cs, err := a.Store.ClipsBetween(r.Context(), from, to, cam)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	ss, err := a.Store.LooseSnapshotsBetween(r.Context(), from, to, cam)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := struct {
		Clips     []ClipJSON     `json:"clips"`
		Snapshots []SnapshotJSON `json:"snapshots"`
	}{Clips: make([]ClipJSON, 0, len(cs)), Snapshots: make([]SnapshotJSON, 0, len(ss))}
	for _, c := range cs {
		out.Clips = append(out.Clips, clipJSON(c))
	}
	for _, s := range ss {
		out.Snapshots = append(out.Snapshots, SnapshotJSON{ID: s.ID, CameraID: s.CameraID, TS: s.TSMS})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *api) loadClip(w http.ResponseWriter, r *http.Request) (store.Clip, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "clip id must be a number")
		return store.Clip{}, false
	}
	c, err := a.Store.Clip(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && c.Status == store.StatusDeleted) {
		httpx.Error(w, http.StatusNotFound, "no such clip")
		return c, false
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return c, false
	}
	return c, true
}

func (a *api) clip(w http.ResponseWriter, r *http.Request) {
	c, ok := a.loadClip(w, r)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		ClipJSON
		Path   string       `json:"path"`
		Status store.Status `json:"status"`
		Error  string       `json:"error,omitempty"`
	}{clipJSON(c), c.Path, c.Status, c.Error})
}

func (a *api) thumb(w http.ResponseWriter, r *http.Request) {
	c, ok := a.loadClip(w, r)
	if !ok {
		return
	}
	if c.ThumbPath == "" {
		httpx.Error(w, http.StatusNotFound, "no thumbnail yet")
		return
	}
	abs, err := a.Thumbs.Abs(c.ThumbPath)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Thumbnails can be replaced (frame upgraded to the camera's snapshot), so revalidate.
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, abs)
}

// raw serves a recording to local helpers (ffmpeg) by signed token, so no
// credentials appear in command lines. Only loopback clients may use it.
func (a *api) raw(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		httpx.Error(w, http.StatusForbidden, "local use only")
		return
	}
	p, ok := a.Signer.Verify(r.PathValue("token"), time.Now())
	if !ok {
		httpx.Error(w, http.StatusForbidden, "link expired or invalid")
		return
	}
	a.proxy(w, r, p)
}

// proxy streams a recording from the server as it is, passing range requests
// through so the browser can seek without downloading the whole file.
func (a *api) proxy(w http.ResponseWriter, r *http.Request, relPath string) {
	resp, err := a.Source.Stream(r.Context(), r.Method, relPath, r.Header)
	if err != nil {
		if r.Context().Err() == nil {
			slog.Warn("stream failed", "path", relPath, "err", err)
		}
		httpx.Error(w, http.StatusBadGateway, "couldn't reach the recordings server")
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified, http.StatusRequestedRangeNotSatisfiable:
	case http.StatusNotFound:
		httpx.Error(w, http.StatusNotFound, "this recording is no longer on the server")
		return
	default:
		httpx.Error(w, http.StatusBadGateway, "the recordings server answered "+resp.Status)
		return
	}
	for _, h := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Cache-Control", "private, max-age=3600")
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, resp.Body)
	}
}

func (a *api) days(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err1 := time.ParseInLocation(time.DateOnly, q.Get("from"), a.Location)
	to, err2 := time.ParseInLocation(time.DateOnly, q.Get("to"), a.Location)
	if err1 != nil || err2 != nil || to.Before(from) || to.Sub(from) > 62*24*time.Hour {
		httpx.Error(w, http.StatusBadRequest, "from and to must be dates (YYYY-MM-DD), at most 62 days apart")
		return
	}
	days := []string{}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		ok, err := a.Store.HasClipsBetween(r.Context(), d.UnixMilli(), d.AddDate(0, 0, 1).UnixMilli())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ok {
			days = append(days, d.Format(time.DateOnly))
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"days": days, "time_zone": a.Location.String()})
}

// ---- webhook ----

func (a *api) authorized(w http.ResponseWriter, r *http.Request) bool {
	if a.IngestSecret == "" {
		httpx.Error(w, http.StatusForbidden, "the upload webhook is off; set INGEST_SECRET to turn it on")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-NVR-Secret")), []byte(a.IngestSecret)) != 1 {
		httpx.Error(w, http.StatusUnauthorized, "missing or wrong X-NVR-Secret header")
		return false
	}
	return true
}

func (a *api) ingest(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || body.Path == "" {
		httpx.Error(w, http.StatusBadRequest, `send JSON like {"path": "2026/11/06/Front of House_00_20261106215852.mp4"}`)
		return
	}
	kind, err := a.Ingest.Notify(r.Context(), body.Path)
	switch {
	case errors.Is(err, webdav.ErrFileNotFound):
		httpx.Error(w, http.StatusNotFound, "that file isn't on the recordings server (check the path is relative to WEBDAV_ROOT)")
	case err != nil:
		httpx.Error(w, http.StatusBadGateway, err.Error())
	case kind == profile.Ignore:
		w.WriteHeader(http.StatusNoContent)
	default:
		httpx.JSON(w, http.StatusAccepted, map[string]string{"kind": kind.String()})
	}
}

func (a *api) rescan(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	if !a.Ingest.Rescan(r.Context()) {
		httpx.Error(w, http.StatusConflict, "a scan is already running")
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
