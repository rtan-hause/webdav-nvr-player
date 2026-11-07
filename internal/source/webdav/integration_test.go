package webdav

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveServer runs only when WEBDAV_TEST_URL is set, for example:
//
//	WEBDAV_TEST_URL=https://sftpgo.example.com/dav/ WEBDAV_TEST_USERNAME=nvr \
//	WEBDAV_TEST_PASSWORD=... go test ./internal/source/webdav -run Live -v
//
// It walks down the first folder at each level (year/month/day) and checks
// that files come back with names, sizes and modification times. Read-only.
func TestLiveServer(t *testing.T) {
	u := os.Getenv("WEBDAV_TEST_URL")
	if u == "" {
		t.Skip("set WEBDAV_TEST_URL to run against a real server")
	}
	c, err := New(u, os.Getenv("WEBDAV_TEST_ROOT"), os.Getenv("WEBDAV_TEST_USERNAME"), os.Getenv("WEBDAV_TEST_PASSWORD"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dir := ""
	for depth := 0; depth < 6; depth++ {
		es, err := c.List(ctx, dir)
		if err != nil {
			t.Fatalf("list %q: %v", dir, err)
		}
		var files, next = 0, ""
		for _, e := range es {
			if e.IsDir {
				if next == "" || e.Name > next { // newest folder (date folders sort by name)
					next = e.Name
				}
				continue
			}
			files++
			if e.Size <= 0 || e.ModTime.IsZero() || e.Name == "" {
				t.Errorf("file without size/time/name: %+v", e)
			}
			if files <= 3 {
				t.Logf("%s  %d bytes  %s", e.Path, e.Size, e.ModTime.Format(time.RFC3339))
			}
		}
		t.Logf("%q: %d entries, %d files", dir, len(es), files)
		if files > 0 || next == "" {
			if files == 0 {
				t.Skip("no files found under the newest folders")
			}
			return
		}
		if dir == "" {
			dir = next
		} else {
			dir += "/" + next
		}
	}
}
