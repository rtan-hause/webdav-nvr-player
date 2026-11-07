package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// countingReader records how many reads Probe makes (each is one HTTP range request in production).
type countingReader struct {
	r     *bytes.Reader
	reads int
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	c.reads++
	return c.r.ReadAt(p, off)
}

// makeClip encodes a short test video with ffmpeg; the test skips if ffmpeg or the encoder is missing.
func makeClip(t *testing.T, encoder string, extra ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	out := filepath.Join(t.TempDir(), "clip.mp4")
	args := append([]string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=3",
		"-c:v", encoder, "-pix_fmt", "yuv420p"}, extra...)
	args = append(args, "-y", out)
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't encode with %s: %v\n%s", encoder, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProbeEncodedClips(t *testing.T) {
	for _, tc := range []struct {
		name      string
		encoder   string
		extra     []string
		codec     string
		moovAtEnd bool
		frag      bool
	}{
		{"h264 moov at end (ffmpeg default)", "libx264", nil, "avc1", true, false},
		{"h264 faststart", "libx264", []string{"-movflags", "+faststart"}, "avc1", false, false},
		{"hevc hvc1 faststart (Reolink-style)", "libx265", []string{"-tag:v", "hvc1", "-movflags", "+faststart", "-x265-params", "log-level=error"}, "hvc1", false, false},
		{"h264 fragmented", "libx264", []string{"-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-frag_duration", "1000000"}, "avc1", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := makeClip(t, tc.encoder, tc.extra...)
			cr := &countingReader{r: bytes.NewReader(b)}
			info, err := Probe(cr, int64(len(b)))
			if err != nil {
				t.Fatal(err)
			}
			if info.Codec != tc.codec || info.Width != 320 || info.Height != 180 || info.MoovAtEnd != tc.moovAtEnd || info.Fragmented != tc.frag {
				t.Fatalf("info = %+v", info)
			}
			// Fragmented: ffmpeg's empty_moov output has no total duration, so it must come from summing fragments.
			if info.Duration < 2900*time.Millisecond || info.Duration > 3100*time.Millisecond {
				t.Fatalf("duration = %v", info.Duration)
			}
			if tc.frag && info.Fragments < 2 {
				t.Fatalf("expected several fragments, got %d", info.Fragments)
			}
			if cr.reads > 6 {
				t.Fatalf("probe made %d reads; small test files should need only a few", cr.reads)
			}
		})
	}
}

// TestProbeReolinkLayout mimics a real Reolink upload: ftyp, a moov whose mvhd
// claims 1 s (timescale 1000, duration 1000) with mvex, then 2 s fragments of 50
// samples at 25 fps in a 150000 timescale, interleaved with audio, and no
// mehd/sidx/mfra. Large mdat bodies check that probing only reads moof boxes.
func TestProbeReolinkLayout(t *testing.T) {
	const fragments = 13
	var f bytes.Buffer
	box := func(typ string, body ...[]byte) []byte {
		n := 8
		for _, b := range body {
			n += len(b)
		}
		out := make([]byte, 8, n)
		binary.BigEndian.PutUint32(out, uint32(n))
		copy(out[4:], typ)
		for _, b := range body {
			out = append(out, b...)
		}
		return out
	}
	be := func(vals ...uint32) []byte {
		out := make([]byte, 4*len(vals))
		for i, v := range vals {
			binary.BigEndian.PutUint32(out[4*i:], v)
		}
		return out
	}
	visual := append(be(0, 0, 0, 0, 0, 0), []byte{0x0f, 0x00, 0x08, 0x70}...) // reserved..., width 3840, height 2160
	trak := func(id uint32, handler string, timescale uint32, codec string) []byte {
		return box("trak",
			box("tkhd", be(0, 0, 0, id, 0)),
			box("mdia",
				box("mdhd", be(0, 0, 0, timescale, 1000, 0)),
				box("hdlr", be(0, 0), []byte(handler), be(0, 0, 0)),
				box("minf", box("stbl", box("stsd", be(0, 1), box(codec, visual))))))
	}
	f.Write(box("ftyp", []byte("iso4"), be(1), []byte("iso4hvc1")))
	f.Write(box("moov",
		box("mvhd", be(0, 0, 0, 1000, 1000)),
		trak(1, "vide", 150000, "hvc1"),
		trak(2, "soun", 16000, "mp4a"),
		box("mvex", box("trex", be(0, 1, 1, 0, 0, 0)), box("trex", be(0, 2, 1, 1024, 0, 0)))))
	for i := 0; i < fragments; i++ {
		// Video trun flags 0x304: first-sample-flags + per-sample duration + size.
		vrun := be(0x000304, 50, 0)
		for s := 0; s < 50; s++ {
			vrun = append(vrun, be(6000, 30000)...) // 6000/150000 s = 40 ms
		}
		// Audio trun flags 0x201: data offset + size; duration from trex.
		arun := be(0x000201, 31, 0)
		for s := 0; s < 31; s++ {
			arun = append(arun, be(400)...)
		}
		f.Write(box("moof",
			box("mfhd", be(0, uint32(i+1))),
			box("traf", box("tfhd", be(0x1, 1, 0, 0)), box("trun", vrun)),
			box("traf", box("tfhd", be(0x21, 2, 0, 0, 0)), box("trun", arun))))
		f.Write(box("mdat", make([]byte, 1_500_000)))
	}

	cr := &countingReader{r: bytes.NewReader(f.Bytes())}
	info, err := Probe(cr, int64(f.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Duration != 26*time.Second || info.Fragments != fragments || !info.Fragmented || info.Codec != "hvc1" || info.Width != 3840 || info.Height != 2160 {
		t.Fatalf("info = %+v", info)
	}
	if cr.reads > fragments+2 {
		t.Fatalf("%d reads for %d fragments; each fragment should cost one read", cr.reads, fragments)
	}

	// The same file cut off mid-fragment (upload in progress) counts complete fragments only.
	cut := f.Bytes()[:f.Len()-700_000]
	info, err = Probe(bytes.NewReader(cut), int64(len(cut)))
	if err != nil || info.Fragments != fragments || info.Duration != 26*time.Second {
		t.Fatalf("truncated last mdat: %+v %v", info, err)
	}
}

func TestProbeRejectsNonMP4(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	if _, err := Probe(bytes.NewReader(jpeg), int64(len(jpeg))); !errors.Is(err, ErrNotMP4) {
		t.Fatalf("jpeg: %v", err)
	}
}

func TestProbeTruncatedUpload(t *testing.T) {
	b := makeClip(t, "libx264") // moov at end
	half := b[:len(b)/2]
	if _, err := Probe(bytes.NewReader(half), int64(len(half))); err == nil {
		t.Fatal("a half-uploaded file must not probe successfully")
	}
}

func TestCodecName(t *testing.T) {
	for in, want := range map[string]string{"hvc1": "HEVC", "hev1": "HEVC", "avc1": "H.264", "mp4v": "mp4v", "": ""} {
		if got := CodecName(in); got != want {
			t.Errorf("CodecName(%q) = %q, want %q", in, got, want)
		}
	}
}
