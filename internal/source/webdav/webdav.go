// Package webdav is a minimal read-only WebDAV client: directory listings via
// PROPFIND, ranged reads for probing, and pass-through requests for streaming.
package webdav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrUnauthorized means the server rejected the credentials.
var ErrUnauthorized = errors.New("the WebDAV server rejected the username or password")

// ErrNotFound means the requested folder doesn't exist on the server.
var ErrNotFound = errors.New("folder not found on the WebDAV server")

// Entry is one file or folder in a listing.
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"` // relative to the client root, "/"-separated, no leading slash
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	ETag    string    `json:"etag,omitempty"`
}

// Client talks to one WebDAV share. Paths passed to it are relative to root.
type Client struct {
	base     *url.URL // share URL + root, always ending in "/"
	username string
	password string
	hc       *http.Client
}

// New returns a client for the share at rawURL, scoped to the folder root inside it.
// hc may be nil to use a client with a 30s timeout.
func New(rawURL, root, username, password string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("webdav url: %w", err)
	}
	u.Path = path.Join("/", u.Path, root) + "/"
	u.RawPath = ""
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: u, username: username, password: password, hc: hc}, nil
}

// URL returns the share URL including the root folder (never includes credentials).
func (c *Client) URL() string { return c.base.String() }

func (c *Client) fileURL(relPath string) string {
	u := *c.base
	u.Path = c.base.Path + strings.TrimLeft(relPath, "/")
	return u.String() // escapes spaces and other reserved characters per segment
}

func (c *Client) newRequest(ctx context.Context, method, relPath string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.fileURL(relPath), nil)
	if err != nil {
		return nil, err
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	return req, nil
}

func statusErr(resp *http.Response, relPath string) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrFileNotFound, relPath)
	}
	return fmt.Errorf("GET %s: unexpected HTTP %d", relPath, resp.StatusCode)
}

// ErrFileNotFound means the file is gone from the server (deleted by retention, for example).
var ErrFileNotFound = errors.New("file not found on the WebDAV server")

// passHeaders are the request headers forwarded by Stream.
var passHeaders = []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"}

// Stream sends a GET or HEAD for relPath, forwarding range and caching headers
// from in. The caller must close the response body. Error statuses from the
// server (404, 416, ...) are returned as responses, not errors, so they can be
// passed through; only transport failures are errors.
func (c *Client) Stream(ctx context.Context, method, relPath string, in http.Header) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, relPath)
	if err != nil {
		return nil, err
	}
	for _, h := range passHeaders {
		if v := in.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	// Streams can run longer than the client's default timeout; the request context bounds them.
	hc := *c.hc
	hc.Timeout = 0
	return hc.Do(req)
}

// Fetch downloads a whole small file (a snapshot), refusing anything over max bytes.
func (c *Client) Fetch(ctx context.Context, relPath string, max int64) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodGet, relPath)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach the WebDAV server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusErr(resp, relPath)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", relPath, max)
	}
	return b, nil
}

// ReaderAt returns an io.ReaderAt that reads relPath with one HTTP range request per call.
func (c *Client) ReaderAt(ctx context.Context, relPath string) io.ReaderAt {
	return &rangeReader{c: c, ctx: ctx, path: relPath}
}

type rangeReader struct {
	c    *Client
	ctx  context.Context
	path string
}

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	req, err := r.c.newRequest(r.ctx, http.MethodGet, r.path)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
	resp, err := r.c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("couldn't reach the WebDAV server: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusRequestedRangeNotSatisfiable:
		return 0, io.EOF
	case http.StatusOK:
		return 0, fmt.Errorf("the WebDAV server ignored a range request for %s; range support is required", r.path)
	default:
		return 0, statusErr(resp, r.path)
	}
	n, err := io.ReadFull(resp.Body, p)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return n, io.EOF
	}
	return n, err
}

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop>
<D:resourcetype/><D:getcontentlength/><D:getlastmodified/><D:getetag/>
</D:prop></D:propfind>`

// List returns the direct children of dir ("" for the root), folders first, then by name.
func (c *Client) List(ctx context.Context, dir string) ([]Entry, error) {
	dir = strings.Trim(dir, "/")
	target := *c.base
	if dir != "" {
		target.Path = c.base.Path + dir + "/"
	}
	ms, err := c.propfind(ctx, &target, "1")
	if err != nil {
		return nil, err
	}
	return c.entries(&target, ms, false)
}

// Stat returns one file's details (size, modification time, ETag).
func (c *Client) Stat(ctx context.Context, relPath string) (Entry, error) {
	target := *c.base
	target.Path = c.base.Path + strings.Trim(relPath, "/")
	ms, err := c.propfind(ctx, &target, "0")
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Entry{}, fmt.Errorf("%w: %s", ErrFileNotFound, relPath)
		}
		return Entry{}, err
	}
	es, err := c.entries(&target, ms, true)
	if err != nil {
		return Entry{}, err
	}
	if len(es) == 0 {
		return Entry{}, fmt.Errorf("%w: %s", ErrFileNotFound, relPath)
	}
	return es[0], nil
}

func (c *Client) propfind(ctx context.Context, target *url.URL, depth string) (multistatus, error) {
	var ms multistatus
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", target.String(), strings.NewReader(propfindBody))
	if err != nil {
		return ms, err
	}
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return ms, fmt.Errorf("couldn't reach the WebDAV server: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ms, fmt.Errorf("%w (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return ms, fmt.Errorf("%w: %s", ErrNotFound, target.Path)
	case resp.StatusCode != http.StatusMultiStatus:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return ms, fmt.Errorf("PROPFIND %s: unexpected HTTP %d (is this a WebDAV URL?): %s", target.Path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&ms); err != nil {
		return ms, fmt.Errorf("PROPFIND %s: reading response: %w", target.Path, err)
	}
	return ms, nil
}

func (c *Client) entries(listed *url.URL, ms multistatus, includeSelf bool) ([]Entry, error) {
	out := make([]Entry, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		href, err := url.Parse(strings.TrimSpace(r.Href))
		if err != nil {
			return nil, fmt.Errorf("bad href %q: %w", r.Href, err)
		}
		p := listed.ResolveReference(href).Path // decoded
		if !includeSelf && strings.TrimSuffix(p, "/") == strings.TrimSuffix(listed.Path, "/") {
			continue // the folder itself
		}
		rel, ok := strings.CutPrefix(p, c.base.Path)
		if !ok {
			continue // outside our root; shouldn't happen with Depth: 1
		}
		prop, ok := r.okProp()
		if !ok {
			continue
		}
		e := Entry{
			Path:  strings.Trim(rel, "/"),
			IsDir: prop.ResourceType.Collection != nil || strings.HasSuffix(p, "/"),
			ETag:  strings.Trim(prop.ETag, `"`),
		}
		e.Name = path.Base(e.Path)
		if n, err := strconv.ParseInt(strings.TrimSpace(prop.ContentLength), 10, 64); err == nil {
			e.Size = n
		}
		if t, err := http.ParseTime(strings.TrimSpace(prop.LastModified)); err == nil {
			e.ModTime = t
		}
		out = append(out, e)
	}
	sortEntries(out)
	return out, nil
}

func sortEntries(es []Entry) {
	slices.SortFunc(es, func(a, b Entry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

type multistatus struct {
	Responses []response `xml:"DAV: response"`
}

type response struct {
	Href      string     `xml:"DAV: href"`
	Propstats []propstat `xml:"DAV: propstat"`
}

type propstat struct {
	Prop   prop   `xml:"DAV: prop"`
	Status string `xml:"DAV: status"`
}

type prop struct {
	ResourceType struct {
		Collection *struct{} `xml:"DAV: collection"`
	} `xml:"DAV: resourcetype"`
	ContentLength string `xml:"DAV: getcontentlength"`
	LastModified  string `xml:"DAV: getlastmodified"`
	ETag          string `xml:"DAV: getetag"`
}

// okProp merges the properties from every 200 propstat (servers split found and
// missing properties into separate propstats).
func (r response) okProp() (prop, bool) {
	var merged prop
	found := false
	for _, ps := range r.Propstats {
		if !strings.Contains(ps.Status, " 200") {
			continue
		}
		found = true
		if ps.Prop.ResourceType.Collection != nil {
			merged.ResourceType = ps.Prop.ResourceType
		}
		if ps.Prop.ContentLength != "" {
			merged.ContentLength = ps.Prop.ContentLength
		}
		if ps.Prop.LastModified != "" {
			merged.LastModified = ps.Prop.LastModified
		}
		if ps.Prop.ETag != "" {
			merged.ETag = ps.Prop.ETag
		}
	}
	return merged, found
}
