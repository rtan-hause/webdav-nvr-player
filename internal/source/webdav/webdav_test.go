package webdav

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Shaped like SFTPGo's responses (golang.org/x/net/webdav): escaped hrefs,
// collections with a trailing slash, sizes only on files.
const dayListing = `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:">
 <D:response><D:href>/dav/2026/11/06/</D:href><D:propstat><D:prop>
  <D:resourcetype><D:collection xmlns:D="DAV:"/></D:resourcetype>
  <D:getlastmodified>Sat, 07 Nov 2026 05:58:52 GMT</D:getlastmodified>
 </D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
 <D:response><D:href>/dav/2026/11/06/Living%20Room_00_20261106215852.mp4</D:href><D:propstat><D:prop>
  <D:resourcetype></D:resourcetype>
  <D:getcontentlength>17486114</D:getcontentlength>
  <D:getlastmodified>Sat, 07 Nov 2026 05:59:20 GMT</D:getlastmodified>
  <D:getetag>"18a5f0c2b9e3d4a01ac8f22"</D:getetag>
 </D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
 <D:response><D:href>/dav/2026/11/06/Living%20Room_00_20261106215851.jpg</D:href>
  <D:propstat><D:prop><D:resourcetype/><D:getcontentlength>453804</D:getcontentlength>
   <D:getlastmodified>Sat, 07 Nov 2026 05:58:55 GMT</D:getlastmodified></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  <D:propstat><D:prop><D:getetag/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>
 </D:response>
 <D:response><D:href>/dav/2026/11/06/extra/</D:href><D:propstat><D:prop>
  <D:resourcetype><D:collection/></D:resourcetype>
 </D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`

func fakeServer(t *testing.T, check func(r *http.Request) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, body := check(r)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListParsesEntries(t *testing.T) {
	var gotPath, gotDepth, gotUser, gotPass string
	srv := fakeServer(t, func(r *http.Request) (int, string) {
		gotPath, gotDepth = r.URL.Path, r.Header.Get("Depth")
		gotUser, gotPass, _ = r.BasicAuth()
		if r.Method != "PROPFIND" {
			return http.StatusMethodNotAllowed, ""
		}
		return http.StatusMultiStatus, dayListing
	})
	c, err := New(srv.URL+"/dav", "/", "nvr", "pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	es, err := c.List(context.Background(), "2026/11/06")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/dav/2026/11/06/" || gotDepth != "1" || gotUser != "nvr" || gotPass != "pw" {
		t.Fatalf("request: path=%q depth=%q user=%q pass=%q", gotPath, gotDepth, gotUser, gotPass)
	}
	if len(es) != 3 {
		t.Fatalf("want 3 entries (self excluded), got %d: %+v", len(es), es)
	}
	if !es[0].IsDir || es[0].Path != "2026/11/06/extra" || es[0].Name != "extra" {
		t.Fatalf("folders should sort first: %+v", es[0])
	}
	mp4 := es[2]
	if mp4.Name != "Living Room_00_20261106215852.mp4" || mp4.Path != "2026/11/06/Living Room_00_20261106215852.mp4" {
		t.Fatalf("names must be unescaped: %+v", mp4)
	}
	if mp4.IsDir || mp4.Size != 17486114 || mp4.ETag != "18a5f0c2b9e3d4a01ac8f22" {
		t.Fatalf("file props: %+v", mp4)
	}
	if want := time.Date(2026, 11, 7, 5, 59, 20, 0, time.UTC); !mp4.ModTime.Equal(want) {
		t.Fatalf("mod time = %v", mp4.ModTime)
	}
	if es[1].Size != 453804 || es[1].ETag != "" {
		t.Fatalf("404 propstat must be ignored, 200 props kept: %+v", es[1])
	}
}

func TestListWithRootFolder(t *testing.T) {
	srv := fakeServer(t, func(r *http.Request) (int, string) {
		if r.URL.Path != "/dav/recordings/reolink/" {
			return http.StatusNotFound, ""
		}
		return http.StatusMultiStatus, `<D:multistatus xmlns:D="DAV:">
 <D:response><D:href>/dav/recordings/reolink/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
 <D:response><D:href>` + "http://" + r.Host + `/dav/recordings/reolink/2026/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`
	})
	c, _ := New(srv.URL+"/dav/", "recordings/reolink", "", "", nil)
	es, err := c.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].Path != "2026" || !es[0].IsDir {
		t.Fatalf("absolute hrefs relative to root: %+v", es)
	}
	if !strings.HasSuffix(c.URL(), "/dav/recordings/reolink/") {
		t.Fatalf("URL() = %s", c.URL())
	}
}

func TestListErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		want   error
	}{
		"bad password": {http.StatusUnauthorized, ErrUnauthorized},
		"forbidden":    {http.StatusForbidden, ErrUnauthorized},
		"missing root": {http.StatusNotFound, ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			srv := fakeServer(t, func(*http.Request) (int, string) { return tc.status, "" })
			c, _ := New(srv.URL, "/", "nvr", "wrong", nil)
			if _, err := c.List(context.Background(), ""); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("not webdav", func(t *testing.T) {
		srv := fakeServer(t, func(*http.Request) (int, string) { return http.StatusOK, "<html>login</html>" })
		c, _ := New(srv.URL, "/", "", "", nil)
		_, err := c.List(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "is this a WebDAV URL?") {
			t.Fatalf("got %v", err)
		}
	})
}
