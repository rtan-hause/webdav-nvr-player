package thumbs

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func decodeFile(t *testing.T, path string) image.Image {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("thumbnail isn't a JPEG: %v", err)
	}
	return img
}

func TestFromJPEG(t *testing.T) {
	// A 4K frame like Reolink's snapshots, left half red, right half blue.
	src := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	for y := 0; y < 2160; y++ {
		for x := 0; x < 3840; x++ {
			c := color.RGBA{200, 30, 30, 255}
			if x >= 1920 {
				c = color.RGBA{30, 30, 200, 255}
			}
			src.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, nil); err != nil {
		t.Fatal(err)
	}

	m := &Maker{Dir: t.TempDir()}
	rel, err := m.FromJPEG(1234, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if rel != "thumbs/1/1234.jpg" {
		t.Fatalf("rel = %q", rel)
	}
	abs, _ := m.Abs(rel)
	img := decodeFile(t, abs)
	if img.Bounds().Dx() != 480 || img.Bounds().Dy() != 270 {
		t.Fatalf("size = %v", img.Bounds())
	}
	if r, _, b, _ := img.At(100, 135).RGBA(); r < b {
		t.Fatal("left side should still be red")
	}

	if err := m.Remove(rel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatal("Remove left the file")
	}
	if err := m.Remove(rel); err != nil {
		t.Fatal("removing twice should be fine")
	}
}

func TestFromJPEGRejectsGarbage(t *testing.T) {
	m := &Maker{Dir: t.TempDir()}
	if _, err := m.FromJPEG(1, []byte("<html>not an image</html>")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAbsRefusesEscapes(t *testing.T) {
	m := &Maker{Dir: t.TempDir()}
	for _, bad := range []string{"../etc/passwd", "thumbs/../../x", "/etc/passwd", "nvr.db"} {
		if _, err := m.Abs(bad); err == nil {
			t.Errorf("Abs(%q) should fail", bad)
		}
	}
}

func TestFromVideoOverHTTP(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "testsrc=size=1280x720:rate=25:duration=4", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart", "-y", clip).CombinedOutput(); err != nil {
		t.Skipf("can't make test clip: %v %s", err, out)
	}
	data, _ := os.ReadFile(clip)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "clip.mp4", time.Now(), bytes.NewReader(data))
	}))
	defer srv.Close()

	m := &Maker{Dir: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rel, err := m.FromVideo(ctx, 7, srv.URL+"/clip.mp4", 0.4)
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := m.Abs(rel)
	img := decodeFile(t, abs)
	if img.Bounds().Dx() != 480 || img.Bounds().Dy() != 270 {
		t.Fatalf("size = %v", img.Bounds())
	}

	if _, err := m.FromVideo(ctx, 8, srv.URL+"/clip.mp4", 60); err == nil {
		t.Fatal("seeking past the end should fail, not write an empty thumbnail")
	}
}
