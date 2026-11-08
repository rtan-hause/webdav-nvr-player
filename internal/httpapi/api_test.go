package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rtan-hause/webdav-nvr-player/internal/auth"
	"github.com/rtan-hause/webdav-nvr-player/internal/config"
	"github.com/rtan-hause/webdav-nvr-player/internal/ingest"
	"github.com/rtan-hause/webdav-nvr-player/internal/media"
	"github.com/rtan-hause/webdav-nvr-player/internal/profile"
	"github.com/rtan-hause/webdav-nvr-player/internal/signedurl"
	"github.com/rtan-hause/webdav-nvr-player/internal/source/webdav"
	"github.com/rtan-hause/webdav-nvr-player/internal/store"
	"github.com/rtan-hause/webdav-nvr-player/internal/thumbs"
)

var la, _ = time.LoadLocation("America/Los_Angeles")

// 2026-11-06 21:58:52 PST
var t0 = time.Date(2026, 11, 6, 21, 58, 52, 0, la).UnixMilli()

type fakeIngest struct {
	notified []string
	rescans  int
}

func (f *fakeIngest) Notify(_ context.Context, p string) (profile.Kind, error) {
	f.notified = append(f.notified, p)
	switch {
	case strings.HasSuffix(p, ".txt"):
		return profile.Ignore, nil
	case strings.Contains(p, "missing"):
		return profile.Clip, webdav.ErrFileNotFound
	}
	return profile.Clip, nil
}
func (f *fakeIngest) Rescan(context.Context) bool { f.rescans++; return f.rescans == 1 }
func (f *fakeIngest) Status() ingest.Status       { return ingest.Status{} }

// fakeStream serves one in-memory file with real Range handling.
type fakeStream struct{ files map[string][]byte }

func (f fakeStream) Stream(_ context.Context, method, p string, in http.Header) (*http.Response, error) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/x", nil)
	req.Header = in.Clone()
	if b, ok := f.files[p]; ok {
		rec.Header().Set("ETag", `"v1"`)
		http.ServeContent(rec, req, "clip.mp4", time.Unix(1_800_000_000, 0), bytes.NewReader(b))
	} else {
		rec.WriteHeader(http.StatusNotFound)
	}
	return rec.Result(), nil
}

func (f fakeStream) ReaderAt(_ context.Context, p string) io.ReaderAt {
	if b, ok := f.files[p]; ok {
		return bytes.NewReader(b)
	}
	return missingFile{}
}

type missingFile struct{}

func (missingFile) ReadAt([]byte, int64) (int, error) { return 0, webdav.ErrFileNotFound }

type fixture struct {
	h       http.Handler
	ing     *fakeIngest
	signer  *signedurl.Signer
	clip    store.Clip
	pending store.Clip
	index   Index
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cam, _ := st.EnsureCamera(ctx, "Living Room", "00")
	st.AddClips(ctx, []store.NewFile{
		{CameraID: cam, Path: "2026/11/06/Living Room_00_20261106215852.mp4", TimeMS: t0, Size: 10},
		{CameraID: cam, Path: "2026/11/06/Living Room_00_20261106220000.mp4", TimeMS: t0 + 68_000, Size: 10},
	})
	probeQ, _ := st.ClipsToProbe(ctx, t0, 10)
	var ready, pending store.Clip
	for _, c := range probeQ {
		if c.StartMS == t0 {
			ready = c
		} else {
			pending = c
		}
	}
	st.SetClipProbed(ctx, ready.ID, t0+28_000, "hvc1", 3840, 2160)
	tm := &thumbs.Maker{Dir: dir}
	thumbRel := thumbs.RelPath(ready.ID)
	os.MkdirAll(filepath.Join(dir, filepath.Dir(thumbRel)), 0o750)
	os.WriteFile(filepath.Join(dir, thumbRel), []byte("\xff\xd8jpeg"), 0o600)
	st.SetThumb(ctx, ready.ID, thumbRel, store.ThumbKindSnapshot)
	ready, _ = st.Clip(ctx, ready.ID)

	ing := &fakeIngest{}
	signer := signedurl.New()
	idx := &Index{
		Store: st, Ingest: ing, Thumbs: tm, Signer: signer, Location: la, IngestSecret: "s3cret",
		Source: fakeStream{files: map[string][]byte{ready.Path: []byte("0123456789")}},
	}
	h := New(Options{UI: testUI(), Index: idx})
	return fixture{h: h, ing: ing, signer: signer, clip: ready, pending: pending, index: *idx}
}

func do(h http.Handler, method, target string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestClipsAndCameras(t *testing.T) {
	f := newFixture(t)
	rec := do(f.h, "GET", "/api/cameras", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Living Room"`) {
		t.Fatalf("cameras: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(f.h, "GET", "/api/clips?from="+itoa(t0-3600_000)+"&to="+itoa(t0+3600_000), nil, "")
	var got struct {
		Clips     []ClipJSON     `json:"clips"`
		Snapshots []SnapshotJSON `json:"snapshots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Clips) != 1 || got.Snapshots == nil {
		t.Fatalf("only the ready clip is listed, with an empty (not null) snapshots list: %s", rec.Body.String())
	}
	c := got.Clips[0]
	if c.Codec != "HEVC" || c.File != "Living Room_00_20261106215852.mp4" || !c.Thumb || c.End-c.Start != 28_000 {
		t.Fatalf("clip json: %+v", c)
	}

	for _, bad := range []string{"/api/clips", "/api/clips?from=5&to=1", "/api/clips?from=0&to=999999999999", "/api/clips?from=0&to=1&camera=x"} {
		if rec := do(f.h, "GET", bad, nil, ""); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}

	if rec := do(f.h, "GET", "/api/clips/"+itoa(f.clip.ID), nil, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"path":"2026/11/06/`) {
		t.Fatalf("clip detail: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(f.h, "GET", "/api/clips/999", nil, ""); rec.Code != 404 {
		t.Fatalf("missing clip: %d", rec.Code)
	}
}

func TestThumb(t *testing.T) {
	f := newFixture(t)
	rec := do(f.h, "GET", "/api/clips/"+itoa(f.clip.ID)+"/thumb", nil, "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || !strings.HasPrefix(rec.Body.String(), "\xff\xd8") {
		t.Fatalf("thumb: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := do(f.h, "GET", "/api/clips/"+itoa(f.pending.ID)+"/thumb", nil, ""); rec.Code != 404 {
		t.Fatalf("clip without thumbnail: %d", rec.Code)
	}
}

func TestStreamSupportsRanges(t *testing.T) {
	f := newFixture(t)
	url := "/api/clips/" + itoa(f.clip.ID) + "/stream"
	rec := do(f.h, "GET", url, map[string]string{"Range": "bytes=2-5"}, "")
	if rec.Code != 206 || rec.Body.String() != "2345" || rec.Header().Get("Content-Range") != "bytes 2-5/10" || rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("range: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	rec = do(f.h, "HEAD", url, nil, "")
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "10" {
		t.Fatalf("head: %d %d %v", rec.Code, rec.Body.Len(), rec.Header())
	}
	// The pending clip's file isn't on the fake server: a clear 404.
	if rec := do(f.h, "GET", "/api/clips/"+itoa(f.pending.ID)+"/stream", nil, ""); rec.Code != 404 {
		t.Fatalf("gone file: %d", rec.Code)
	}
}

func TestRawIsLoopbackAndSigned(t *testing.T) {
	f := newFixture(t)
	tok := f.signer.Sign(f.clip.Path, time.Now().Add(time.Minute))
	req := httptest.NewRequest("GET", "/internal/raw/"+tok, nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "0123456789" {
		t.Fatalf("loopback raw: %d %q", rec.Code, rec.Body.String())
	}

	req.RemoteAddr = "10.0.0.5:50000"
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("remote client must be refused: %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/internal/raw/forged.token", nil)
	req.RemoteAddr = "[::1]:50000"
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("bad token must be refused: %d", rec.Code)
	}
}

func TestDays(t *testing.T) {
	f := newFixture(t)
	rec := do(f.h, "GET", "/api/days?from=2026-11-01&to=2026-11-07", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"days":["2026-11-06"]`) {
		t.Fatalf("days: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(f.h, "GET", "/api/days?from=2026-01-01&to=2026-12-31", nil, ""); rec.Code != 400 {
		t.Fatalf("too long a range: %d", rec.Code)
	}
}

func TestWebhook(t *testing.T) {
	f := newFixture(t)
	secret := map[string]string{"X-NVR-Secret": "s3cret", "Content-Type": "application/json"}
	body := func(p string) string { return `{"path":"` + p + `"}` }

	if rec := do(f.h, "POST", "/api/ingest", nil, body("a.mp4")); rec.Code != 401 {
		t.Fatalf("no secret: %d", rec.Code)
	}
	if rec := do(f.h, "POST", "/api/ingest", map[string]string{"X-NVR-Secret": "nope"}, body("a.mp4")); rec.Code != 401 {
		t.Fatalf("wrong secret: %d", rec.Code)
	}
	if rec := do(f.h, "POST", "/api/ingest", secret, `{}`); rec.Code != 400 {
		t.Fatalf("no path: %d", rec.Code)
	}
	rec := do(f.h, "POST", "/api/ingest", secret, body("/2026/11/06/Living Room_00_20261106221000.mp4"))
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"clip"`) {
		t.Fatalf("clip: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(f.h, "POST", "/api/ingest", secret, body("test.txt")); rec.Code != 204 {
		t.Fatalf("ignored file: %d", rec.Code)
	}
	if rec := do(f.h, "POST", "/api/ingest", secret, body("missing.mp4")); rec.Code != 404 {
		t.Fatalf("missing file: %d", rec.Code)
	}
	if len(f.ing.notified) != 3 || f.ing.notified[0] != "/2026/11/06/Living Room_00_20261106221000.mp4" {
		t.Fatalf("notified: %v", f.ing.notified)
	}

	if rec := do(f.h, "POST", "/api/rescan", secret, ""); rec.Code != 202 {
		t.Fatalf("rescan: %d", rec.Code)
	}
	if rec := do(f.h, "POST", "/api/rescan", secret, ""); rec.Code != 409 {
		t.Fatalf("second rescan while running: %d", rec.Code)
	}
}

func TestWebhookOffWithoutSecret(t *testing.T) {
	st, _ := store.Open(context.Background(), "", t.TempDir())
	defer st.Close()
	h := New(Options{UI: testUI(), Index: &Index{Store: st, Ingest: &fakeIngest{}, Location: la}})
	rec := do(h, "POST", "/api/ingest", map[string]string{"X-NVR-Secret": ""}, `{"path":"a.mp4"}`)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "INGEST_SECRET") {
		t.Fatalf("webhook without secret configured: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStatus(t *testing.T) {
	f := newFixture(t)
	rec := do(f.h, "GET", "/api/status", nil, "")
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != 200 || !strings.Contains(string(body), `"database":"sqlite"`) || !strings.Contains(string(body), `"webhook":true`) {
		t.Fatalf("status: %d %s", rec.Code, body)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestStreamRemuxesFragmentedClip checks that a fragmented upload is served as
// the remuxed file, with byte ranges that straddle the generated header and
// the source's media.
func TestStreamRemuxesFragmentedClip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	out := filepath.Join(t.TempDir(), "frag.mp4")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=3", "-pix_fmt", "yuv420p", "-g", "25", "-c:v", "libx264",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-y", out).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v\n%s", err, b)
	}
	src, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := media.Defragment(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	virtual := append(append([]byte(nil), want.Head...), src[want.DataStart:want.DataEnd]...)

	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cam, _ := st.EnsureCamera(ctx, "Garage", "00")
	path := "2026/11/06/Garage_00_20261106215852.mp4"
	st.AddClips(ctx, []store.NewFile{{CameraID: cam, Path: path, TimeMS: t0, Size: int64(len(src))}})
	q, _ := st.ClipsToProbe(ctx, t0, 10)
	st.SetClipProbed(ctx, q[0].ID, t0+3000, "avc1", 320, 180)
	h := New(Options{UI: testUI(), Index: &Index{
		Store: st, Ingest: &fakeIngest{}, Thumbs: &thumbs.Maker{Dir: dir}, Signer: signedurl.New(), Location: la,
		Source: fakeStream{files: map[string][]byte{path: src}},
	}})
	url := "/api/clips/" + itoa(q[0].ID) + "/stream"

	rec := do(h, "GET", url, nil, "")
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), virtual) {
		t.Fatalf("full GET: %d, %d bytes (want %d, the remuxed file)", rec.Code, rec.Body.Len(), len(virtual))
	}
	if rec.Header().Get("Content-Type") != "video/mp4" || rec.Header().Get("Accept-Ranges") != "bytes" || rec.Header().Get("ETag") == "" {
		t.Fatalf("headers: %v", rec.Header())
	}
	head := len(want.Head)
	for _, r := range [][2]int{{0, 99}, {head - 50, head + 5000}, {len(virtual) - 1000, len(virtual) - 1}} {
		rec = do(h, "GET", url, map[string]string{"Range": "bytes=" + itoa(int64(r[0])) + "-" + itoa(int64(r[1]))}, "")
		if rec.Code != 206 || !bytes.Equal(rec.Body.Bytes(), virtual[r[0]:r[1]+1]) {
			t.Fatalf("range %v: %d, %d bytes", r, rec.Code, rec.Body.Len())
		}
	}
	rec = do(h, "HEAD", url, nil, "")
	if rec.Code != 200 || rec.Header().Get("Content-Length") != strconv.Itoa(len(virtual)) {
		t.Fatalf("HEAD: %d, length %q", rec.Code, rec.Header().Get("Content-Length"))
	}
}

// TestSignInGuardsTheAPI checks the wiring: with sign-in on, clip routes need a
// session while health, version, the web UI and the webhook (its own secret) don't.
func TestSignInGuardsTheAPI(t *testing.T) {
	f := newFixture(t)
	st, err := store.Open(context.Background(), "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	am := auth.New(auth.Options{Mode: config.AuthPassword, Store: st})
	h := New(Options{UI: testUI(), Auth: am, Index: &Index{Store: f.index.Store, Ingest: f.ing, Source: f.index.Source, Thumbs: f.index.Thumbs, Signer: f.signer, Location: la, IngestSecret: "s3cret"}})

	for _, tc := range []struct {
		method, path string
		hdr          map[string]string
		want         int
	}{
		{"GET", "/api/cameras", nil, 401},
		{"GET", "/api/clips/" + itoa(f.clip.ID) + "/stream", nil, 401},
		{"GET", "/api/settings", nil, 401},
		{"GET", "/healthz", nil, 200},
		{"GET", "/api/version", nil, 200},
		{"GET", "/api/auth", nil, 200},
		{"GET", "/", nil, 200},
		{"POST", "/api/ingest", map[string]string{"X-NVR-Secret": "s3cret", "Content-Type": "application/json"}, 202},
	} {
		body := ""
		if tc.method == "POST" {
			body = `{"path":"2026/11/06/Living Room_00_20261106215852.mp4"}`
		}
		if rec := do(h, tc.method, tc.path, tc.hdr, body); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d (%s)", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}
