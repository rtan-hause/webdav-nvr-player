// Package ingest keeps the index in step with the recordings server.
//
// Files enter the index three ways: the upload webhook (Notify), a frequent
// rescan of today's and yesterday's folders, and a periodic full crawl that
// also notices deletions. Indexed rows are the work queue: a dispatcher hands
// due rows to a small worker pool that probes clips, pairs snapshots with
// clips, and makes thumbnails.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/rtan-hause/webdav-nvr-player/internal/media"
	"github.com/rtan-hause/webdav-nvr-player/internal/profile"
	"github.com/rtan-hause/webdav-nvr-player/internal/source/webdav"
	"github.com/rtan-hause/webdav-nvr-player/internal/store"
	"github.com/rtan-hause/webdav-nvr-player/internal/thumbs"
)

// Source is the recordings server (implemented by *webdav.Client).
type Source interface {
	List(ctx context.Context, dir string) ([]webdav.Entry, error)
	Stat(ctx context.Context, relPath string) (webdav.Entry, error)
	ReaderAt(ctx context.Context, relPath string) io.ReaderAt
	Fetch(ctx context.Context, relPath string, max int64) ([]byte, error)
}

// Options configures an Ingester.
type Options struct {
	Store    *store.Store
	Source   Source
	Profile  profile.Profile
	Location *time.Location // camera time zone for file names
	Thumbs   *thumbs.Maker
	// FrameURL returns a URL ffmpeg can read the recording from (see httpapi's loopback route).
	FrameURL func(relPath string) string

	Workers     int           // default 2
	RecentEvery time.Duration // rescan today+yesterday, default 5m
	FullEvery   time.Duration // full crawl, default 24h; also runs at startup
	Settle      time.Duration // scans skip files modified this recently (still uploading), default 60s; negative disables
	MaxAttempts int           // give up on a file after this many errors, default 5
	Now         func() time.Time
	Log         *slog.Logger
}

// Ingester runs scans and background work. Create with New, then Run.
type Ingester struct {
	o    Options
	wake chan struct{}

	pairMu sync.Mutex // pairing decisions are made one at a time
	scanMu sync.Mutex // one scan at a time

	mu       sync.Mutex
	inflight map[string]bool
	cameras  map[string]int64
	status   Status
}

// ScanKind says how much of the server a scan crawls.
type ScanKind string

const (
	ScanFull   ScanKind = "full"   // the whole tree (ScanAll)
	ScanRecent ScanKind = "recent" // today's and yesterday's folders (ScanRecent)
)

// Status describes scan activity for the status page.
type Status struct {
	Scanning       ScanKind  `json:"scanning,omitempty"` // set while a scan runs
	LastFullScan   time.Time `json:"last_full_scan"`
	LastRecentScan time.Time `json:"last_recent_scan"`
	LastScanError  string    `json:"last_scan_error,omitempty"`
	LastScanAdded  int       `json:"last_scan_added"`
	Working        int       `json:"working"` // items being processed right now
}

// New returns an Ingester with defaults applied.
func New(o Options) *Ingester {
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.RecentEvery <= 0 {
		o.RecentEvery = 5 * time.Minute
	}
	if o.FullEvery <= 0 {
		o.FullEvery = 24 * time.Hour
	}
	if o.Settle < 0 {
		o.Settle = 0
	} else if o.Settle == 0 {
		o.Settle = 60 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Location == nil {
		o.Location = time.UTC
	}
	return &Ingester{o: o, wake: make(chan struct{}, 1), inflight: map[string]bool{}, cameras: map[string]int64{}}
}

// Status returns a snapshot of scan activity.
func (in *Ingester) Status() Status {
	in.mu.Lock()
	defer in.mu.Unlock()
	st := in.status
	st.Working = len(in.inflight)
	return st
}

func (in *Ingester) poke() {
	select {
	case in.wake <- struct{}{}:
	default:
	}
}

// Run starts the scheduler, dispatcher and workers and blocks until ctx ends.
// A full crawl runs right away, then on the configured schedule.
func (in *Ingester) Run(ctx context.Context) {
	jobs := make(chan job)
	var wg sync.WaitGroup
	for range in.o.Workers {
		wg.Go(func() {
			for j := range jobs {
				j.run(ctx)
				in.mu.Lock()
				delete(in.inflight, j.key)
				in.mu.Unlock()
				in.poke()
			}
		})
	}
	wg.Go(func() { in.schedule(ctx) })
	in.dispatch(ctx, jobs)
	close(jobs)
	wg.Wait()
}

func (in *Ingester) schedule(ctx context.Context) {
	in.runScan(ctx, ScanFull)
	recent := time.NewTicker(in.o.RecentEvery)
	full := time.NewTicker(in.o.FullEvery)
	defer recent.Stop()
	defer full.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-recent.C:
			in.runScan(ctx, ScanRecent)
		case <-full.C:
			in.runScan(ctx, ScanFull)
		}
	}
}

// Rescan starts a full crawl in the background unless a scan is already running.
func (in *Ingester) Rescan(ctx context.Context) bool {
	if !in.scanMu.TryLock() {
		return false
	}
	in.scanMu.Unlock()
	go in.runScan(context.WithoutCancel(ctx), ScanFull)
	return true
}

func (in *Ingester) runScan(ctx context.Context, kind ScanKind) {
	if !in.scanMu.TryLock() {
		return // another scan is running; it covers this one
	}
	defer in.scanMu.Unlock()
	in.setStatus(func(s *Status) { s.Scanning = kind })
	start := in.o.Now()
	var added int
	var err error
	if kind == ScanFull {
		added, err = in.ScanAll(ctx)
	} else {
		added, err = in.ScanRecent(ctx)
	}
	in.setStatus(func(s *Status) {
		s.Scanning = ""
		s.LastScanAdded = added
		s.LastScanError = ""
		if err != nil {
			s.LastScanError = err.Error()
		}
		if kind == ScanFull {
			s.LastFullScan = start
		} else {
			s.LastRecentScan = start
		}
	})
	if err != nil && ctx.Err() == nil {
		in.o.Log.Error("scan failed", "kind", kind, "err", err)
	} else if added > 0 || kind == ScanFull {
		in.o.Log.Info("scan finished", "kind", kind, "added", added, "took", in.o.Now().Sub(start).Round(time.Millisecond))
	}
	in.poke()
}

func (in *Ingester) setStatus(fn func(*Status)) {
	in.mu.Lock()
	fn(&in.status)
	in.mu.Unlock()
}

// ---- finding files ----

// Notify indexes one newly uploaded file (the webhook). relPath is relative to
// the WebDAV root. It returns the file's kind; Ignore means nothing was done.
func (in *Ingester) Notify(ctx context.Context, relPath string) (profile.Kind, error) {
	relPath = path.Clean("/" + relPath)[1:]
	m := in.o.Profile.Classify(relPath)
	if m.Kind == profile.Ignore {
		return profile.Ignore, nil
	}
	e, err := in.o.Source.Stat(ctx, relPath)
	if err != nil {
		return m.Kind, err
	}
	if _, err := in.add(ctx, []webdav.Entry{e}); err != nil {
		return m.Kind, err
	}
	in.poke()
	return m.Kind, nil
}

// ScanRecent indexes new files in today's and yesterday's folders (camera time)
// and notices deletions there.
func (in *Ingester) ScanRecent(ctx context.Context) (int, error) {
	now := in.o.Now().In(in.o.Location)
	total := 0
	for _, d := range []time.Time{now.AddDate(0, 0, -1), now} {
		for _, dir := range in.o.Profile.DateDirs(d.Year(), d.Month(), d.Day()) {
			n, _, err := in.scanDir(ctx, dir, true)
			if errors.Is(err, webdav.ErrNotFound) {
				continue // the camera hasn't created this folder yet
			}
			if err != nil {
				return total, err
			}
			total += n
		}
	}
	return total, nil
}

// ScanAll walks the whole tree, newest folders first, indexing new files, then
// marks anything indexed before the walk began but no longer present as deleted.
func (in *Ingester) ScanAll(ctx context.Context) (int, error) {
	before, err := in.o.Store.AllLivePaths(ctx)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(before))
	total := 0
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, entries, err := in.scanDir(ctx, dir, false)
		if err != nil {
			return err
		}
		total += n
		var dirs []string
		for _, e := range entries {
			if e.IsDir {
				dirs = append(dirs, e.Path)
			} else {
				seen[e.Path] = true
			}
		}
		if depth >= 8 {
			return nil
		}
		slices.Sort(dirs)
		slices.Reverse(dirs) // date folders sort by name, so this is newest first
		for _, d := range dirs {
			if err := walk(d, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("", 0); err != nil {
		return total, err
	}
	var gone []string
	for _, p := range before {
		if !seen[p] {
			gone = append(gone, p)
		}
	}
	return total, in.markDeleted(ctx, gone)
}

// scanDir indexes the files in one folder. With reconcile set, indexed files
// missing from the folder are marked deleted.
func (in *Ingester) scanDir(ctx context.Context, dir string, reconcile bool) (int, []webdav.Entry, error) {
	entries, err := in.o.Source.List(ctx, dir)
	if err != nil {
		return 0, nil, err
	}
	cutoff := in.o.Now().Add(-in.o.Settle)
	var settled []webdav.Entry
	for _, e := range entries {
		if !e.IsDir && (e.ModTime.IsZero() || e.ModTime.Before(cutoff)) {
			settled = append(settled, e)
		}
	}
	n, err := in.add(ctx, settled)
	if err != nil || !reconcile {
		return n, entries, err
	}
	present := map[string]bool{}
	for _, e := range entries {
		present[e.Path] = true
	}
	live, err := in.o.Store.LivePathsIn(ctx, dir)
	if err != nil {
		return n, entries, err
	}
	var gone []string
	for _, p := range live {
		if !present[p] {
			gone = append(gone, p)
		}
	}
	return n, entries, in.markDeleted(ctx, gone)
}

func (in *Ingester) add(ctx context.Context, entries []webdav.Entry) (int, error) {
	var clips, snaps []store.NewFile
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		m := in.o.Profile.Classify(e.Path)
		if m.Kind == profile.Ignore {
			continue
		}
		cam, err := in.cameraID(ctx, m.Camera, m.Channel)
		if err != nil {
			return 0, err
		}
		f := store.NewFile{CameraID: cam, Path: e.Path, TimeMS: m.In(in.o.Location).UnixMilli(), Size: e.Size, ETag: e.ETag, ModMS: e.ModTime.UnixMilli()}
		if e.ModTime.IsZero() {
			f.ModMS = 0
		}
		if m.Kind == profile.Clip {
			clips = append(clips, f)
		} else {
			snaps = append(snaps, f)
		}
	}
	a, err := in.o.Store.AddSnapshots(ctx, snaps)
	if err != nil {
		return 0, err
	}
	b, err := in.o.Store.AddClips(ctx, clips)
	return a + b, err
}

func (in *Ingester) cameraID(ctx context.Context, name, channel string) (int64, error) {
	key := name + "\x00" + channel
	in.mu.Lock()
	id, ok := in.cameras[key]
	in.mu.Unlock()
	if ok {
		return id, nil
	}
	id, err := in.o.Store.EnsureCamera(ctx, name, channel)
	if err != nil {
		return 0, err
	}
	in.mu.Lock()
	in.cameras[key] = id
	in.mu.Unlock()
	return id, nil
}

func (in *Ingester) markDeleted(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	thumbPaths, err := in.o.Store.MarkDeleted(ctx, paths)
	if err != nil {
		return err
	}
	for _, t := range thumbPaths {
		if err := in.o.Thumbs.Remove(t); err != nil {
			in.o.Log.Warn("removing thumbnail", "path", t, "err", err)
		}
	}
	in.o.Log.Info("files removed from the server", "count", len(paths))
	return nil
}

// ---- background work ----

type job struct {
	key string
	run func(ctx context.Context)
}

func (in *Ingester) dispatch(ctx context.Context, jobs chan<- job) {
	for {
		sent, err := in.dispatchDue(ctx, jobs)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			in.o.Log.Error("reading work queue", "err", err)
		}
		if sent > 0 {
			continue
		}
		wait := 30 * time.Second
		nowMS := in.o.Now().UnixMilli()
		if next, err := in.o.Store.NextWakeMS(ctx, nowMS); err == nil && next > 0 {
			wait = min(wait, time.Duration(next-nowMS)*time.Millisecond+50*time.Millisecond)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-in.wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// dispatchDue hands due rows to workers: snapshots first (cheap, and they let
// clips pair), then probes, then thumbnails.
func (in *Ingester) dispatchDue(ctx context.Context, jobs chan<- job) (int, error) {
	const batch = 32
	now := in.o.Now().UnixMilli()
	var todo []job

	snaps, err := in.o.Store.SnapshotsToPair(ctx, batch)
	if err != nil {
		return 0, err
	}
	for _, s := range snaps {
		todo = append(todo, job{fmt.Sprintf("s%d", s.ID), func(ctx context.Context) { in.pairSnapshot(ctx, s) }})
	}
	probes, err := in.o.Store.ClipsToProbe(ctx, now, batch)
	if err != nil {
		return 0, err
	}
	for _, c := range probes {
		todo = append(todo, job{fmt.Sprintf("c%d", c.ID), func(ctx context.Context) { in.probeClip(ctx, c) }})
	}
	thumbJobs, err := in.o.Store.ClipsNeedingThumb(ctx, now, batch)
	if err != nil {
		return 0, err
	}
	for _, c := range thumbJobs {
		todo = append(todo, job{fmt.Sprintf("c%d", c.ID), func(ctx context.Context) { in.makeThumb(ctx, c.ID) }})
	}

	sent := 0
	for _, j := range todo {
		in.mu.Lock()
		busy := in.inflight[j.key]
		if !busy {
			in.inflight[j.key] = true
		}
		in.mu.Unlock()
		if busy {
			continue
		}
		select {
		case jobs <- j:
			sent++
		case <-ctx.Done():
			return sent, ctx.Err()
		}
	}
	return sent, nil
}

func (in *Ingester) backoff(attempts int) int64 {
	d := 30 * time.Second << min(attempts, 6) // 30s, 1m, 2m ... capped at 32m
	return in.o.Now().Add(d).UnixMilli()
}

func (in *Ingester) probeClip(ctx context.Context, c store.Clip) {
	pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	info, err := media.Probe(in.o.Source.ReaderAt(pctx, c.Path), c.Size)
	if err != nil {
		if errors.Is(err, webdav.ErrFileNotFound) {
			_ = in.markDeleted(ctx, []string{c.Path})
			return
		}
		permanent := errors.Is(err, media.ErrNotMP4) || c.Attempts+1 >= in.o.MaxAttempts
		in.o.Log.Warn("couldn't read clip", "path", c.Path, "err", err, "giving_up", permanent)
		_ = in.o.Store.SetClipFailed(ctx, c.ID, err.Error(), in.backoff(c.Attempts), permanent)
		return
	}
	end := c.StartMS + info.Duration.Milliseconds()
	if err := in.o.Store.SetClipProbed(ctx, c.ID, end, info.Codec, info.Width, info.Height); err != nil {
		in.o.Log.Error("saving probe result", "path", c.Path, "err", err)
		return
	}
	// Wait for the snapshot relative to when the upload finished, so a backfill
	// of old files doesn't wait at all.
	uploaded := c.ModMS
	if uploaded == 0 {
		uploaded = in.o.Now().UnixMilli()
	}
	_ = in.pairClip(ctx, c, uploaded+in.o.Profile.Pairing().Wait.Milliseconds())
}

// pairClip looks for the clip's snapshot; without one the clip waits until
// deadlineMS, then falls back to a frame (see store.PairClip). Errors are logged.
func (in *Ingester) pairClip(ctx context.Context, c store.Clip, deadlineMS int64) error {
	in.pairMu.Lock()
	defer in.pairMu.Unlock()
	_, err := in.o.Store.PairClip(ctx, c.ID, in.o.Profile.Pairing().Before.Milliseconds(), deadlineMS, in.o.Now().UnixMilli())
	if err != nil {
		in.o.Log.Error("pairing clip", "path", c.Path, "err", err)
	}
	return err
}

func (in *Ingester) pairSnapshot(ctx context.Context, s store.Snapshot) {
	in.pairMu.Lock()
	defer in.pairMu.Unlock()
	if _, err := in.o.Store.PairSnapshot(ctx, s.ID, in.o.Profile.Pairing().Before.Milliseconds()); err != nil {
		in.o.Log.Error("pairing snapshot", "path", s.Path, "err", err)
	}
}

func (in *Ingester) makeThumb(ctx context.Context, clipID int64) {
	c, err := in.o.Store.Clip(ctx, clipID)
	if err != nil {
		return
	}
	if c.ThumbState == store.ThumbWaiting {
		// Deadline passed: one last look for a snapshot, otherwise use a frame.
		if err := in.pairClip(ctx, c, c.PairDeadlineMS); err != nil {
			return
		}
		if c, err = in.o.Store.Clip(ctx, clipID); err != nil {
			return
		}
	}

	tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var rel string
	var kind store.ThumbKind
	switch c.ThumbState {
	case store.ThumbSnapshot:
		kind = store.ThumbKindSnapshot
		var sn store.Snapshot
		if sn, err = in.o.Store.Snapshot(ctx, c.SnapshotID); err != nil {
			_ = in.o.Store.UnpairForFrame(ctx, c.ID)
			return
		}
		data, ferr := in.o.Source.Fetch(tctx, sn.Path, 32<<20)
		gone := errors.Is(ferr, webdav.ErrFileNotFound)
		if ferr == nil {
			rel, err = in.o.Thumbs.FromJPEG(c.ID, data)
		} else {
			err = ferr // network trouble: retried below with backoff
		}
		if gone || (ferr == nil && err != nil) {
			// The snapshot is gone or isn't a usable JPEG: fall back to a frame next round.
			in.o.Log.Warn("snapshot unusable, using a video frame", "snapshot", sn.Path, "err", err)
			if gone {
				_ = in.markDeleted(ctx, []string{sn.Path})
			}
			_ = in.o.Store.UnpairForFrame(ctx, c.ID)
			return
		}
	case store.ThumbFrame:
		kind = store.ThumbKindFrame
		seek := float64(c.EndMS-c.StartMS) / 1000 * 0.1
		rel, err = in.o.Thumbs.FromVideo(tctx, c.ID, in.o.FrameURL(c.Path), seek)
	default:
		return
	}
	if err != nil {
		permanent := c.Attempts+1 >= in.o.MaxAttempts
		in.o.Log.Warn("thumbnail failed", "path", c.Path, "kind", kind, "err", err, "giving_up", permanent)
		_ = in.o.Store.SetThumbFailed(ctx, c.ID, err.Error(), in.backoff(c.Attempts), permanent)
		return
	}
	if err := in.o.Store.SetThumb(ctx, c.ID, rel, kind); err != nil {
		in.o.Log.Error("saving thumbnail", "path", c.Path, "err", err)
	}
}
