package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// virtualFile assembles what the stream endpoint serves for a Remux.
func virtualFile(x *Remux, src []byte) []byte {
	return append(append([]byte(nil), x.Head...), src[x.DataStart:x.DataEnd]...)
}

// packets returns ffmpeg's per-packet hashes (stream, dts, pts, duration, size,
// md5) for a file, which must be identical before and after remuxing.
func packets(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.mp4")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", p, "-map", "0", "-c", "copy", "-f", "framemd5", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg framemd5: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// probeFile reports the container duration and keyframe count ffprobe sees.
func probeFile(t *testing.T, b []byte) (dur string, keyframes int) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe.mp4")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", p).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	flags, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "packet=flags", "-of", "csv=p=0", p).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	return strings.TrimSpace(string(out)), strings.Count(string(flags), "K")
}

// makeAVClip encodes 3 s of video and audio as a fragmented MP4.
func makeAVClip(t *testing.T, vcodec ...string) []byte {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	out := filepath.Join(t.TempDir(), "frag.mp4")
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000:duration=3",
		"-pix_fmt", "yuv420p", "-g", "25"}
	args = append(args, vcodec...)
	args = append(args, "-c:a", "aac", "-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-y", out)
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't encode %v: %v\n%s", vcodec, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDefragmentMatchesSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		vcodec []string
	}{
		{"h264 + aac", []string{"-c:v", "libx264", "-bf", "0"}},
		{"h264 with b-frames (composition offsets)", []string{"-c:v", "libx264", "-bf", "2"}},
		{"hevc hvc1 + aac", []string{"-c:v", "libx265", "-tag:v", "hvc1", "-x265-params", "log-level=error"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := makeAVClip(t, tc.vcodec...)
			x, err := Defragment(bytes.NewReader(src), int64(len(src)))
			if err != nil {
				t.Fatal(err)
			}
			if x.Duration < 2900*time.Millisecond || x.Duration > 3100*time.Millisecond {
				t.Fatalf("duration = %v", x.Duration)
			}
			out := virtualFile(x, src)
			if int64(len(out)) != x.Size() {
				t.Fatalf("Size() = %d, assembled %d", x.Size(), len(out))
			}
			if !bytes.Contains(out[:64], []byte("moov")) {
				t.Fatal("moov must come right after ftyp (faststart)")
			}
			if bytes.Contains(x.Head, []byte("mvex")) {
				t.Fatal("remuxed moov must not be fragmented")
			}
			if a, b := packets(t, ffmpegFaststart(t, src)), packets(t, out); a != b {
				t.Fatalf("packets differ from ffmpeg's faststart remux\nffmpeg:\n%s\n\nours:\n%s", diffHead(a, b), diffHead(b, a))
			}
			dur, keys := probeFile(t, out)
			if !strings.HasPrefix(dur, "3.0") && !strings.HasPrefix(dur, "2.9") {
				t.Fatalf("remuxed duration %q", dur)
			}
			if keys < 3 || keys > 4 {
				t.Fatalf("remuxed file has %d keyframes; want one per second (-g 25)", keys)
			}
		})
	}
}

// diffHead returns a's lines from the first one that differs from b.
func diffHead(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	i := 0
	for i < len(al) && i < len(bl) && al[i] == bl[i] {
		i++
	}
	return fmt.Sprintf("(line %d of %d)\n%s", i+1, len(al), strings.Join(al[i:min(i+4, len(al))], "\n"))
}

// ffmpegFaststart is what the old player did to each fragmented upload
// (ffmpeg -c copy -movflags +faststart); the remux must match it packet for packet.
func ffmpegFaststart(t *testing.T, src []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.mp4"), filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(in, src, 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", in, "-map", "0", "-c", "copy", "-movflags", "+faststart", "-y", out).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg remux: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDefragmentRegularFile(t *testing.T) {
	b := makeClip(t, "libx264", "-movflags", "+faststart")
	if _, err := Defragment(bytes.NewReader(b), int64(len(b))); !errors.Is(err, ErrNotFragmented) {
		t.Fatalf("err = %v, want ErrNotFragmented", err)
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	if _, err := Defragment(bytes.NewReader(jpeg), int64(len(jpeg))); !errors.Is(err, ErrNotMP4) {
		t.Fatalf("jpeg: %v", err)
	}
}

// TestDefragmentReolinkLayout builds the box layout of a real Reolink upload:
// absolute base_data_offset, no data_offset on the video run, first-sample
// flags only, and default flags that call every sample a keyframe.
func TestDefragmentReolinkLayout(t *testing.T) {
	const fragments, vSize, aSize = 13, 3000, 400
	var f bytes.Buffer
	visual := append(be32(0, 0, 0, 0, 0, 0), []byte{0x0f, 0x00, 0x08, 0x70}...)
	trak := func(id uint32, handler string, timescale uint32, codec string) []byte {
		return box("trak",
			box("tkhd", be32(0x0f, 0, 0, id, 0, 1000)),
			box("mdia",
				box("mdhd", be32(0, 0, 0, timescale, 1000, 0)),
				box("hdlr", be32(0, 0), []byte(handler), be32(0, 0, 0)),
				box("minf", box("stbl",
					box("stsd", be32(0, 1), box(codec, visual)),
					box("stts", be32(0, 0)), box("stsc", be32(0, 0)), box("stsz", be32(0, 0, 0)), box("stco", be32(0, 0))))))
	}
	f.Write(box("ftyp", []byte("iso4"), be32(1), []byte("iso4hvc1")))
	f.Write(box("moov",
		box("mvhd", be32(0, 0, 0, 1000, 1000)),
		trak(1, "vide", 150000, "hvc1"),
		trak(2, "soun", 16000, "mp4a"),
		box("mvex", box("trex", be32(0, 1, 1, 0, 0, 0)), box("trex", be32(0, 2, 1, 1024, 0, 0)))))
	var wantVideo, wantAudio [][]byte
	for i := 0; i < fragments; i++ {
		vrun := be32(0x000304, 50, 0)
		for s := 0; s < 50; s++ {
			vrun = append(vrun, be32(6000, vSize)...)
		}
		arun := be32(0x000201, 31, 50*vSize)
		for s := 0; s < 31; s++ {
			arun = append(arun, be32(aSize)...)
		}
		moof := box("moof",
			box("mfhd", be32(0, uint32(i+1))),
			box("traf", box("tfhd", be32(0x1, 1, 0, 0)), box("trun", vrun)),
			box("traf", box("tfhd", be32(0x21, 2, 0, 0, 0x02000000)), box("trun", arun)))
		dataAt := uint64(f.Len() + len(moof) + 8)
		// Patch both tfhd base_data_offset fields to the absolute mdat payload offset.
		n := 0
		for k := 0; k+8 < len(moof); k++ {
			if string(moof[k:k+4]) == "tfhd" {
				binary.BigEndian.PutUint64(moof[k+12:], dataAt)
				n++
			}
		}
		if n != 2 {
			t.Fatal("test setup: tfhd not found")
		}
		f.Write(moof)
		payload := make([]byte, 50*vSize+31*aSize)
		for s := 0; s < 50; s++ { // tag each sample so misplaced offsets show
			binary.BigEndian.PutUint32(payload[s*vSize:], uint32(i*1000+s))
			wantVideo = append(wantVideo, payload[s*vSize:(s+1)*vSize])
		}
		for s := 0; s < 31; s++ {
			o := 50*vSize + s*aSize
			binary.BigEndian.PutUint32(payload[o:], uint32(0xa0000000+i*1000+s))
			wantAudio = append(wantAudio, payload[o:o+aSize])
		}
		f.Write(box("mdat", payload))
	}
	src := f.Bytes()

	cr := &countingReader{r: bytes.NewReader(src)}
	x, err := Defragment(cr, int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	if x.Duration != 26*time.Second {
		t.Fatalf("duration = %v", x.Duration)
	}
	if cr.reads > fragments+2 {
		t.Fatalf("%d reads for %d fragments", cr.reads, fragments)
	}
	out := virtualFile(x, src)
	tables := readTables(t, out)
	if got := tables[1].duration; got != 26000 {
		t.Fatalf("video tkhd duration = %d ms", got)
	}
	if got := tables[0].duration; got != 26000 {
		t.Fatalf("mvhd duration = %d ms", got)
	}
	if want := []uint32{1, 51, 101}; len(tables[1].stss) != fragments || tables[1].stss[1] != want[1] || tables[1].stss[2] != want[2] {
		t.Fatalf("video keyframes = %v, want one per fragment starting %v", tables[1].stss, want)
	}
	if tables[2].stss != nil {
		t.Fatal("audio track must not have a keyframe table")
	}
	checkSamples(t, out, tables[1], wantVideo)
	checkSamples(t, out, tables[2], wantAudio)

	// Upload cut off partway through the last fragment's video.
	cut := src[:len(src)-31*aSize-20*vSize]
	x, err = Defragment(bytes.NewReader(cut), int64(len(cut)))
	if err != nil {
		t.Fatal(err)
	}
	tables = readTables(t, virtualFile(x, cut))
	if got := len(tables[1].offsets); got != fragments {
		t.Fatalf("truncated: %d video chunks", got)
	}
	if got := tables[1].count; got != 12*50+30 {
		t.Fatalf("truncated: %d video samples, want %d", got, 12*50+30)
	}
}

// tables is what the remux test reads back from a non-fragmented moov.
type tables struct {
	duration uint32 // mvhd or tkhd, movie timescale
	count    int
	sizes    []uint32
	stsc     [][3]uint32
	offsets  []uint32
	stss     []uint32
}

// readTables parses the remuxed moov: index 0 holds mvhd, then one per track ID.
func readTables(t *testing.T, b []byte) map[uint32]*tables {
	t.Helper()
	res := map[uint32]*tables{}
	err := boxes(b, func(typ string, moov []byte) error {
		if typ != "moov" {
			return nil
		}
		return boxes(moov, func(typ string, body []byte) error {
			switch typ {
			case "mvhd":
				res[0] = &tables{duration: u32(body, 16)}
			case "trak":
				tb := &tables{}
				var walk func(string, []byte) error
				walk = func(typ string, b []byte) error {
					switch typ {
					case "tkhd":
						tb.duration = u32(b, 20)
						res[u32(b, 12)] = tb
					case "mdia", "minf", "stbl":
						return boxes(b, walk)
					case "stsz":
						tb.count = int(u32(b, 8))
						for i := 0; i < tb.count && u32(b, 4) == 0; i++ {
							tb.sizes = append(tb.sizes, u32(b, 12+4*i))
						}
						for i := 0; i < tb.count && u32(b, 4) != 0; i++ {
							tb.sizes = append(tb.sizes, u32(b, 4))
						}
					case "stsc":
						for i := 0; i < int(u32(b, 4)); i++ {
							tb.stsc = append(tb.stsc, [3]uint32{u32(b, 8+12*i), u32(b, 12+12*i), u32(b, 16+12*i)})
						}
					case "stco":
						for i := 0; i < int(u32(b, 4)); i++ {
							tb.offsets = append(tb.offsets, u32(b, 8+4*i))
						}
					case "stss":
						for i := 0; i < int(u32(b, 4)); i++ {
							tb.stss = append(tb.stss, u32(b, 8+4*i))
						}
					}
					return nil
				}
				return boxes(body, walk)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// checkSamples resolves every sample through stsc/stco/stsz and compares bytes.
func checkSamples(t *testing.T, file []byte, tb *tables, want [][]byte) {
	t.Helper()
	if tb.count != len(want) {
		t.Fatalf("%d samples, want %d", tb.count, len(want))
	}
	s := 0
	for ci, off := range tb.offsets {
		per := uint32(0)
		for _, e := range tb.stsc {
			if e[0] <= uint32(ci+1) {
				per = e[1]
			}
		}
		pos := int(off)
		for k := uint32(0); k < per; k++ {
			got := file[pos : pos+int(tb.sizes[s])]
			if !bytes.Equal(got, want[s]) {
				t.Fatalf("sample %d (chunk %d) has the wrong bytes: starts %x, want %x", s, ci, got[:4], want[s][:4])
			}
			pos += int(tb.sizes[s])
			s++
		}
	}
	if s != len(want) {
		t.Fatalf("chunks cover %d samples, want %d", s, len(want))
	}
}

// TestDefragmentSampleFile checks a real camera upload when one is provided:
//
//	NVR_REMUX_SAMPLE=/path/to/Camera_00_20261107002146.mp4 go test ./internal/media -run SampleFile
func TestDefragmentSampleFile(t *testing.T) {
	p := os.Getenv("NVR_REMUX_SAMPLE")
	if p == "" {
		t.Skip("set NVR_REMUX_SAMPLE to a fragmented camera clip")
	}
	src, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	x, err := Defragment(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	out := virtualFile(x, src)
	if dir := os.Getenv("NVR_REMUX_OUT"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "remuxed.mp4"), out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if a, b := packets(t, ffmpegFaststart(t, src)), packets(t, out); a != b {
		t.Fatalf("packets differ from ffmpeg's faststart remux\nffmpeg:\n%s\n\nours:\n%s", diffHead(a, b), diffHead(b, a))
	}
	dur, keys := probeFile(t, out)
	t.Logf("duration %v (ffprobe %s), %d keyframes, head %d bytes", x.Duration, dur, keys, len(x.Head))
}
