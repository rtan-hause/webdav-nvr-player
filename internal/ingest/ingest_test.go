package ingest

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rtan-hause/webdav-nvr-player/internal/profile"
	"github.com/rtan-hause/webdav-nvr-player/internal/profile/reolink"
	"github.com/rtan-hause/webdav-nvr-player/internal/source/webdav"
	"github.com/rtan-hause/webdav-nvr-player/internal/store"
	"github.com/rtan-hause/webdav-nvr-player/internal/thumbs"
)

// ---- fake recordings server ----

type fakeFile struct {
	data []byte
	mod  time.Time
}

type fakeSource struct {
	mu    sync.Mutex
	files map[string]fakeFile
}

func (f *fakeSource) put(p string, data []byte, mod time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[p] = fakeFile{data, mod}
}

func (f *fakeSource) remove(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, p)
}

func (f *fakeSource) get(p string) (fakeFile, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.files[p]
	return ff, ok
}

func (f *fakeSource) List(_ context.Context, dir string) ([]webdav.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	dirs := map[string]bool{}
	var out []webdav.Entry
	for p, ff := range f.files {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		if i := strings.Index(rest, "/"); i >= 0 {
			if d := prefix + rest[:i]; !dirs[d] {
				dirs[d] = true
				out = append(out, webdav.Entry{Name: rest[:i], Path: d, IsDir: true})
			}
			continue
		}
		out = append(out, webdav.Entry{Name: rest, Path: p, Size: int64(len(ff.data)), ModTime: ff.mod})
	}
	if len(out) == 0 && dir != "" {
		return nil, webdav.ErrNotFound
	}
	return out, nil
}

func (f *fakeSource) Stat(_ context.Context, p string) (webdav.Entry, error) {
	ff, ok := f.get(p)
	if !ok {
		return webdav.Entry{}, webdav.ErrFileNotFound
	}
	return webdav.Entry{Name: path.Base(p), Path: p, Size: int64(len(ff.data)), ModTime: ff.mod}, nil
}

type fakeReaderAt struct {
	f *fakeSource
	p string
}

func (r fakeReaderAt) ReadAt(b []byte, off int64) (int, error) {
	ff, ok := r.f.get(r.p)
	if !ok {
		return 0, webdav.ErrFileNotFound
	}
	return bytes.NewReader(ff.data).ReadAt(b, off)
}

func (f *fakeSource) ReaderAt(_ context.Context, p string) io.ReaderAt { return fakeReaderAt{f, p} }

func (f *fakeSource) Fetch(_ context.Context, p string, _ int64) ([]byte, error) {
	ff, ok := f.get(p)
	if !ok {
		return nil, webdav.ErrFileNotFound
	}
	return ff.data, nil
}

// ---- fixtures ----

var (
	la, _   = time.LoadLocation("America/Los_Angeles")
	fixedAt = time.Date(2026, 11, 6, 22, 0, 0, 0, la)
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testClip(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	out := filepath.Join(t.TempDir(), "c.mp4")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration=3",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-y", out).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, b)
	}
	b, _ := os.ReadFile(out)
	return b
}

func testJPEG(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for i := range img.Pix {
		img.Pix[i] = 128
	}
	img.Set(1, 1, color.White)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type harness struct {
	in  *Ingester
	st  *store.Store
	src *fakeSource
	clk *clock
	dir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), "", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	src := &fakeSource{files: map[string]fakeFile{}}
	clk := &clock{t: fixedAt}

	// ffmpeg reads frames over HTTP, like the real loopback route.
	frames := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ff, ok := src.get(r.URL.Query().Get("p"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "clip.mp4", ff.mod, bytes.NewReader(ff.data))
	}))
	t.Cleanup(frames.Close)

	in := New(Options{
		Store: st, Source: src, Profile: reolink.Profile{}, Location: la,
		Thumbs:   &thumbs.Maker{Dir: dir},
		FrameURL: func(p string) string { return frames.URL + "/?p=" + url.QueryEscape(p) },
		Now:      clk.Now,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return &harness{in: in, st: st, src: src, clk: clk, dir: dir}
}

func (h *harness) run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.in.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// waitFor polls the index until cond holds (work happens on background goroutines).
func (h *harness) waitFor(t *testing.T, what string, cond func(store.Stats) bool) store.Stats {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, err := h.st.Stats(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; stats = %+v", what, st)
		}
		h.in.poke()
		time.Sleep(20 * time.Millisecond)
	}
}

func clipByPath(t *testing.T, st *store.Store, p string) store.Clip {
	t.Helper()
	cs, err := st.ClipsBetween(context.Background(), 0, 1<<62, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Path == p {
			return c
		}
	}
	t.Fatalf("clip %s not indexed", p)
	return store.Clip{}
}

// ---- tests ----

func TestFullScanIndexesPairsAndMakesThumbnails(t *testing.T) {
	h := newHarness(t)
	clip, snap := testClip(t), testJPEG(t)
	old := fixedAt.Add(-time.Hour) // uploads finished long ago: no waiting for snapshots
	day := "2026/11/06/"
	h.src.put(day+"Living Room_00_20261106215521.mp4", clip, old)
	h.src.put(day+"Living Room_00_20261106215521.jpg", snap, old) // same second
	h.src.put(day+"Living Room_00_20261106215852.mp4", clip, old)
	h.src.put(day+"Living Room_00_20261106215851.jpg", snap, old) // 1 s early, still within 2 s
	h.src.put(day+"Living Room_00_20261106215549.mp4", clip, old) // no snapshot: frame
	h.src.put(day+"Living Room_00_20261106215354.txt", []byte("This is a test file"), old)
	h.src.put("2026/11/05/Garage_00_20261105080000.mp4", clip, old)
	h.src.put("2026/11/05/Garage_00_20261105080001.jpg", snap, old)
	h.run(t)

	st := h.waitFor(t, "4 clips with thumbnails", func(s store.Stats) bool {
		return s.Clips[store.StatusReady] == 4 && s.Thumbs[store.ThumbDone] == 4
	})
	if st.Cameras != 2 || st.Snapshots[store.StatusReady] != 3 || st.PairedSnaps != 3 {
		t.Fatalf("stats = %+v", st)
	}
	want := map[string]string{
		day + "Living Room_00_20261106215521.mp4": "snapshot",
		day + "Living Room_00_20261106215852.mp4": "snapshot",
		day + "Living Room_00_20261106215549.mp4": "frame",
		"2026/11/05/Garage_00_20261105080000.mp4": "snapshot",
	}
	for p, kind := range want {
		c := clipByPath(t, h.st, p)
		if c.ThumbKind != kind || c.Codec != "avc1" || c.Width != 640 || c.EndMS-c.StartMS < 2900 {
			t.Errorf("%s: %+v", p, c)
		}
		if _, err := os.Stat(filepath.Join(h.dir, c.ThumbPath)); err != nil {
			t.Errorf("%s: thumbnail file missing: %v", p, err)
		}
	}
	// File names are camera-local (PST): 21:55:21 PST is 05:55:21 UTC the next day.
	if got := time.UnixMilli(clipByPath(t, h.st, day+"Living Room_00_20261106215521.mp4").StartMS).UTC().Format(time.RFC3339); got != "2026-11-07T05:55:21Z" {
		t.Errorf("start = %s", got)
	}

	// Retention deletes the Garage clip: the next full scan notices and removes its thumbnail.
	garage := clipByPath(t, h.st, "2026/11/05/Garage_00_20261105080000.mp4")
	h.src.remove("2026/11/05/Garage_00_20261105080000.mp4")
	if _, err := h.in.ScanAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = h.waitFor(t, "deletion", func(s store.Stats) bool { return s.Clips[store.StatusDeleted] == 1 })
	if _, err := os.Stat(filepath.Join(h.dir, garage.ThumbPath)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("deleted clip's thumbnail still on disk: %v", err)
	}
}

func TestWebhookAndLateSnapshot(t *testing.T) {
	h := newHarness(t)
	clip, snap := testClip(t), testJPEG(t)
	h.run(t)
	h.waitFor(t, "startup scan", func(store.Stats) bool { return !h.in.Status().LastFullScan.IsZero() })
	ctx := context.Background()

	if k, err := h.in.Notify(ctx, "2026/11/06/Living Room_00_20261106215354.txt"); err != nil || k != profile.Ignore {
		t.Fatalf("txt: %v %v", k, err)
	}
	if _, err := h.in.Notify(ctx, "2026/11/06/Front of House_00_20261106215900.mp4"); !errors.Is(err, webdav.ErrFileNotFound) {
		t.Fatalf("missing file: %v", err)
	}

	// The clip lands first (just uploaded); it waits for a snapshot.
	p := "2026/11/06/Front of House_00_20261106215900.mp4"
	h.src.put(p, clip, fixedAt)
	if k, err := h.in.Notify(ctx, "/"+p); err != nil || k != profile.Clip {
		t.Fatalf("notify clip: %v %v", k, err)
	}
	h.waitFor(t, "clip waiting for snapshot", func(s store.Stats) bool { return s.Thumbs[store.ThumbWaiting] == 1 })

	// The snapshot arrives 20 s later, stamped 1 s after the clip's name.
	h.clk.Advance(20 * time.Second)
	sp := "2026/11/06/Front of House_00_20261106215901.jpg"
	h.src.put(sp, snap, h.clk.Now())
	if k, err := h.in.Notify(ctx, sp); err != nil || k != profile.Snapshot {
		t.Fatalf("notify snapshot: %v %v", k, err)
	}
	h.waitFor(t, "snapshot thumbnail", func(s store.Stats) bool { return s.Thumbs[store.ThumbDone] == 1 })
	if c := clipByPath(t, h.st, p); c.ThumbKind != "snapshot" {
		t.Fatalf("thumb kind = %q", c.ThumbKind)
	}

	// A clip whose snapshot never comes gets a frame once its 60 s wait is over.
	p2 := "2026/11/06/Front of House_00_20261106220500.mp4"
	h.src.put(p2, clip, h.clk.Now())
	if _, err := h.in.Notify(ctx, p2); err != nil {
		t.Fatal(err)
	}
	h.waitFor(t, "second clip waiting", func(s store.Stats) bool { return s.Thumbs[store.ThumbWaiting] == 1 })
	h.clk.Advance(61 * time.Second)
	h.waitFor(t, "frame fallback", func(s store.Stats) bool { return s.Thumbs[store.ThumbDone] == 2 })
	if c := clipByPath(t, h.st, p2); c.ThumbKind != "frame" {
		t.Fatalf("thumb kind = %q", c.ThumbKind)
	}
}

func TestRecentScanSkipsFilesStillUploading(t *testing.T) {
	h := newHarness(t)
	clip := testClip(t)
	ctx := context.Background()
	h.src.put("2026/11/06/Garage_00_20261106215950.mp4", clip, fixedAt.Add(-10*time.Second)) // still uploading
	h.src.put("2026/11/06/Garage_00_20261106215000.mp4", clip, fixedAt.Add(-10*time.Minute))
	h.src.put("2026/11/04/Garage_00_20261104120000.mp4", clip, fixedAt.Add(-48*time.Hour)) // not a recent folder

	n, err := h.in.ScanRecent(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recent scan added %d (want 1, the settled file): %v", n, err)
	}
	h.clk.Advance(2 * time.Minute)
	if n, _ := h.in.ScanRecent(ctx); n != 1 {
		t.Fatalf("second recent scan should add the now-settled file, added %d", n)
	}
	if n, _ := h.in.ScanAll(ctx); n != 1 {
		t.Fatalf("full scan should add the older day's file, added %d", n)
	}
}
