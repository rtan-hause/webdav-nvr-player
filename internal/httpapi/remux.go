package httpapi

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rtan-hause/webdav-nvr-player/internal/media"
	"github.com/rtan-hause/webdav-nvr-player/internal/source/webdav"
	"github.com/rtan-hause/webdav-nvr-player/internal/store"
)

// Fragmented uploads (Reolink and most NVRs) are served as a regular MP4 with
// the index first, built on the fly by media.Defragment; see media.Remux. The
// header for a clip is a few KB and costs one small range request per fragment
// to build, so it's made on first play and kept in memory.

const (
	remuxCacheSize    = 2048
	remuxBuildTimeout = 30 * time.Second
)

type remuxCache struct {
	mu      sync.Mutex
	entries map[string]*remuxEntry
	order   []string // oldest first, for eviction
}

type remuxEntry struct {
	done chan struct{}
	rx   *media.Remux // nil: serve the file as it is
	err  error
}

// get returns the cached result for key, building it once even when several
// requests for the same clip arrive together. Failed builds aren't cached.
func (c *remuxCache) get(ctx context.Context, key string, build func(context.Context) (*media.Remux, error)) (*media.Remux, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*remuxEntry{}
	}
	e, ok := c.entries[key]
	if !ok {
		e = &remuxEntry{done: make(chan struct{})}
		c.entries[key] = e
		c.order = append(c.order, key)
		for len(c.order) > remuxCacheSize {
			delete(c.entries, c.order[0])
			c.order = c.order[1:]
		}
		c.mu.Unlock()
		// Detached from the request: a browser dropping its first request
		// (it often does, to re-request a range) shouldn't waste the work.
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remuxBuildTimeout)
		e.rx, e.err = build(bctx)
		cancel()
		if e.err != nil {
			c.mu.Lock()
			if c.entries[key] == e {
				delete(c.entries, key)
			}
			c.mu.Unlock()
		}
		close(e.done)
	} else {
		c.mu.Unlock()
	}
	select {
	case <-e.done:
		return e.rx, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// errReader remembers whether the source failed, so a network error isn't
// mistaken for a file that can't be remuxed.
type errReader struct {
	r   io.ReaderAt
	err error
}

func (e *errReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := e.r.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) && e.err == nil {
		e.err = err
	}
	return n, err
}

// remuxFor returns how to serve clip c: a Remux, or nil to pass it through.
func (a *api) remuxFor(ctx context.Context, c store.Clip) (*media.Remux, error) {
	key := c.Path + "\x00" + strconv.FormatInt(c.Size, 10) + "\x00" + c.ETag
	return a.remuxes.get(ctx, key, func(ctx context.Context) (*media.Remux, error) {
		start := time.Now()
		src := &errReader{r: a.Source.ReaderAt(ctx, c.Path)}
		rx, err := media.Defragment(src, c.Size)
		switch {
		case src.err != nil:
			return nil, src.err
		case errors.Is(err, media.ErrNotFragmented):
			return nil, nil
		case err != nil:
			slog.Warn("can't remux fragmented clip; serving it as is", "path", c.Path, "err", err)
			return nil, nil
		}
		slog.Debug("remuxed fragmented clip", "path", c.Path, "duration", rx.Duration, "head_bytes", len(rx.Head), "took", time.Since(start))
		return rx, nil
	})
}

func (a *api) stream(w http.ResponseWriter, r *http.Request) {
	c, ok := a.loadClip(w, r)
	if !ok {
		return
	}
	rx, err := a.remuxFor(r.Context(), c)
	switch {
	case err != nil && r.Context().Err() != nil:
		return // client went away
	case errors.Is(err, webdav.ErrFileNotFound):
		errJSON(w, http.StatusNotFound, "this recording is no longer on the server")
		return
	case err != nil:
		slog.Warn("stream failed", "path", c.Path, "err", err)
		errJSON(w, http.StatusBadGateway, "couldn't read this recording from the server")
		return
	case rx == nil:
		a.proxy(w, r, c.Path)
		return
	}
	h := fnv.New64a()
	fmt.Fprint(h, c.Path, c.Size, c.ETag)
	w.Header().Set("ETag", fmt.Sprintf(`"rx1-%x"`, h.Sum64()))
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	f := &remuxFile{ctx: r.Context(), src: a.Source, path: c.Path, rx: rx}
	defer f.Close()
	// ServeContent handles Range, If-Range, conditional GETs and HEAD.
	http.ServeContent(w, r, "", time.UnixMilli(c.ModMS), f)
}

// remuxFile is the virtual MP4 as an io.ReadSeeker: Head from memory, then
// the source's media bytes through one streaming range request per seek.
type remuxFile struct {
	ctx  context.Context
	src  Streamer
	path string
	rx   *media.Remux
	off  int64

	body    io.ReadCloser
	bodyOff int64 // virtual offset body will read next
}

func (f *remuxFile) Read(p []byte) (int, error) {
	size := f.rx.Size()
	if f.off >= size {
		return 0, io.EOF
	}
	if head := int64(len(f.rx.Head)); f.off < head {
		n := copy(p, f.rx.Head[f.off:])
		f.off += int64(n)
		return n, nil
	}
	if f.body == nil || f.bodyOff != f.off {
		if err := f.open(); err != nil {
			return 0, err
		}
	}
	p = p[:min(int64(len(p)), size-f.off)]
	n, err := f.body.Read(p)
	f.off += int64(n)
	f.bodyOff = f.off
	if errors.Is(err, io.EOF) {
		f.closeBody()
		if f.off < size {
			return n, io.ErrUnexpectedEOF
		}
	}
	return n, err
}

func (f *remuxFile) open() error {
	f.closeBody()
	from := f.rx.DataStart + f.off - int64(len(f.rx.Head))
	hdr := http.Header{}
	hdr.Set("Range", fmt.Sprintf("bytes=%d-%d", from, f.rx.DataEnd-1))
	resp, err := f.src.Stream(f.ctx, http.MethodGet, f.path, hdr)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusPartialContent || !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", from)) {
		resp.Body.Close()
		return fmt.Errorf("range request for %s answered %s", f.path, resp.Status)
	}
	f.body, f.bodyOff = resp.Body, f.off
	return nil
}

func (f *remuxFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.off
	case io.SeekEnd:
		offset += f.rx.Size()
	default:
		return 0, errors.New("bad whence")
	}
	if offset < 0 {
		return 0, errors.New("negative position")
	}
	f.off = offset
	return offset, nil
}

func (f *remuxFile) closeBody() {
	if f.body != nil {
		f.body.Close()
		f.body = nil
	}
}

func (f *remuxFile) Close() error {
	f.closeBody()
	return nil
}
