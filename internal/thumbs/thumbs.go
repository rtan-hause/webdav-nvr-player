// Package thumbs makes small JPEG thumbnails, from a camera's snapshot (in Go)
// or from a frame of the video (with ffmpeg), and stores them under DATA_DIR.
package thumbs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
)

// Width is the thumbnail width in pixels; height follows the source aspect ratio.
const Width = 480

// Maker writes thumbnails into Dir/thumbs/.
type Maker struct {
	Dir    string // DATA_DIR
	FFmpeg string // ffmpeg binary, default "ffmpeg"
}

// RelPath is where a clip's thumbnail lives, relative to Dir. Clips are
// spread over folders of 1000 so no folder grows huge.
func RelPath(clipID int64) string {
	return filepath.ToSlash(filepath.Join("thumbs", strconv.FormatInt(clipID/1000, 10), strconv.FormatInt(clipID, 10)+".jpg"))
}

// Abs resolves a path returned by FromJPEG/FromVideo. It refuses paths outside Dir.
func (m *Maker) Abs(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if !strings.HasPrefix(clean, "thumbs"+string(filepath.Separator)) {
		return "", fmt.Errorf("bad thumbnail path %q", rel)
	}
	return filepath.Join(m.Dir, clean), nil
}

// Remove deletes a thumbnail; a missing file is not an error.
func (m *Maker) Remove(rel string) error {
	p, err := m.Abs(rel)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// FromJPEG resizes a camera snapshot and stores it as the clip's thumbnail.
func (m *Maker) FromJPEG(clipID int64, data []byte) (string, error) {
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("decoding snapshot: %w", err)
	}
	b := src.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return "", errors.New("snapshot has no pixels")
	}
	w := min(Width, b.Dx())
	h := max(1, b.Dy()*w/b.Dx())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 78}); err != nil {
		return "", err
	}
	return m.write(clipID, buf.Bytes())
}

// FromVideo grabs one frame at seekSec from a video URL with ffmpeg and stores
// it as the clip's thumbnail. The URL should carry no credentials (it shows up
// in the process list); see the loopback endpoint in httpapi.
func (m *Maker) FromVideo(ctx context.Context, clipID int64, url string, seekSec float64) (string, error) {
	bin := m.FFmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, bin,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-ss", strconv.FormatFloat(seekSec, 'f', 3, 64),
		"-i", url,
		"-frames:v", "1",
		"-vf", "scale="+strconv.Itoa(Width)+":-2",
		"-q:v", "4", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("ffmpeg isn't installed (needed for clips without a camera snapshot): %w", err)
		}
		return "", fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if out.Len() == 0 {
		return "", fmt.Errorf("ffmpeg produced no frame at %.1fs: %s", seekSec, strings.TrimSpace(stderr.String()))
	}
	return m.write(clipID, out.Bytes())
}

func (m *Maker) write(clipID int64, data []byte) (string, error) {
	rel := RelPath(clipID)
	abs := filepath.Join(m.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".thumb-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return rel, nil
}
