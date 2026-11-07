package webdav

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fileServer serves one file at /dav/2026/11/06/Living Room_00_20261106215852.mp4
// with full Range support (via http.ServeContent) and basic auth.
func fileServer(t *testing.T, content []byte) (*httptest.Server, *[]string) {
	t.Helper()
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "nvr" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/dav/2026/11/06/Living Room_00_20261106215852.mp4" {
			http.NotFound(w, r)
			return
		}
		ranges = append(ranges, r.Header.Get("Range"))
		w.Header().Set("ETag", `"abc"`)
		http.ServeContent(w, r, "clip.mp4", time.Unix(1_800_000_000, 0), bytes.NewReader(content))
	}))
	t.Cleanup(srv.Close)
	return srv, &ranges
}

const clipPath = "2026/11/06/Living Room_00_20261106215852.mp4"

func TestReaderAt(t *testing.T) {
	content := []byte("0123456789abcdefghij")
	srv, ranges := fileServer(t, content)
	c, _ := New(srv.URL+"/dav/", "/", "nvr", "pw", nil)
	r := c.ReaderAt(context.Background(), clipPath)

	buf := make([]byte, 5)
	if n, err := r.ReadAt(buf, 10); err != nil || n != 5 || string(buf) != "abcde" {
		t.Fatalf("ReadAt(10) = %d %v %q", n, err, buf)
	}
	if (*ranges)[0] != "bytes=10-14" {
		t.Fatalf("range header = %q", (*ranges)[0])
	}
	// Reading past the end returns what's there plus io.EOF, like os.File.
	n, err := r.ReadAt(buf, 17)
	if n != 3 || !errors.Is(err, io.EOF) || string(buf[:n]) != "hij" {
		t.Fatalf("ReadAt(17) = %d %v %q", n, err, buf[:n])
	}
	if _, err := r.ReadAt(buf, 100); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt beyond end = %v", err)
	}
}

func TestReaderAtMissingFile(t *testing.T) {
	srv, _ := fileServer(t, []byte("x"))
	c, _ := New(srv.URL+"/dav/", "/", "nvr", "pw", nil)
	_, err := c.ReaderAt(context.Background(), "2026/11/06/gone.mp4").ReadAt(make([]byte, 4), 0)
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestStat(t *testing.T) {
	srv := fakeServer(t, func(r *http.Request) (int, string) {
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "0" {
			return http.StatusBadRequest, ""
		}
		if r.URL.Path != "/dav/2026/11/06/Living Room_00_20261106215852.mp4" {
			return http.StatusNotFound, ""
		}
		return http.StatusMultiStatus, `<D:multistatus xmlns:D="DAV:"><D:response>
 <D:href>/dav/2026/11/06/Living%20Room_00_20261106215852.mp4</D:href><D:propstat><D:prop>
 <D:resourcetype/><D:getcontentlength>17486114</D:getcontentlength>
 <D:getlastmodified>Sat, 07 Nov 2026 05:59:20 GMT</D:getlastmodified></D:prop>
 <D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`
	})
	c, _ := New(srv.URL+"/dav/", "/", "", "", nil)
	e, err := c.Stat(context.Background(), clipPath)
	if err != nil || e.Size != 17486114 || e.Path != clipPath || e.IsDir {
		t.Fatalf("Stat = %+v %v", e, err)
	}
	if _, err := c.Stat(context.Background(), "2026/11/06/gone.mp4"); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestFetch(t *testing.T) {
	srv, _ := fileServer(t, []byte("jpeg-bytes"))
	c, _ := New(srv.URL+"/dav", "/", "nvr", "pw", nil)
	b, err := c.Fetch(context.Background(), clipPath, 1024)
	if err != nil || string(b) != "jpeg-bytes" {
		t.Fatalf("Fetch = %q %v", b, err)
	}
	if _, err := c.Fetch(context.Background(), clipPath, 4); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("size limit: %v", err)
	}
	bad, _ := New(srv.URL+"/dav", "/", "nvr", "wrong", nil)
	if _, err := bad.Fetch(context.Background(), clipPath, 1024); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad password: %v", err)
	}
}

func TestStreamForwardsRange(t *testing.T) {
	srv, ranges := fileServer(t, []byte("0123456789"))
	c, _ := New(srv.URL+"/dav/", "/", "nvr", "pw", nil)
	in := http.Header{}
	in.Set("Range", "bytes=2-5")
	in.Set("Cookie", "session=secret") // must not be forwarded
	resp, err := c.Stream(context.Background(), http.MethodGet, clipPath, in)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(body) != "2345" || resp.Header.Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("stream: %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Range"))
	}
	if (*ranges)[0] != "bytes=2-5" {
		t.Fatalf("range not forwarded: %q", (*ranges)[0])
	}

	resp, err = c.Stream(context.Background(), http.MethodGet, "2026/11/06/gone.mp4", http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing file should pass through 404, got %d", resp.StatusCode)
	}
}
