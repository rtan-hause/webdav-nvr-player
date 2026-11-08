package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// Reading ISO-BMFF (MP4/MOV) structure, shared by Probe and Defragment.

// ErrNotMP4 means the file isn't an ISO-BMFF (MP4/MOV) file.
var ErrNotMP4 = errors.New("not an MP4 file")

const (
	maxMoov = 64 << 20 // a 30 s clip's moov is tens of KB; refuse absurd sizes
	maxMoof = 4 << 20
	window  = 8 << 10 // one read covers a moof plus the next box header
)

// windowReader serves small reads from a cached window so walking boxes costs
// one range request per window rather than one per header.
type windowReader struct {
	r     io.ReaderAt
	size  int64
	off   int64
	buf   []byte
	reads int
}

// at returns n bytes at off (fewer at end of file).
func (w *windowReader) at(off int64, n int) ([]byte, error) {
	if off >= w.off && off+int64(n) <= w.off+int64(len(w.buf)) {
		return w.buf[off-w.off : off-w.off+int64(n)], nil
	}
	want := max(int64(n), window)
	want = min(want, w.size-off)
	buf := make([]byte, want)
	got, err := w.r.ReadAt(buf, off)
	w.reads++
	if got < int(want) && err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	w.off, w.buf = off, buf[:got]
	return w.buf[:min(n, got)], nil
}

// ---------- top-level boxes ----------

// topBox is one top-level box of a file. bad means its size is impossible or
// runs past the end of the file (an upload still in progress or cut off).
type topBox struct {
	typ       string
	off, size int64 // size includes the header
	hdrLen    int64
	bad       bool
}

// errStopWalk, returned by a walkTop callback, ends the walk without error.
var errStopWalk = errors.New("stop walk")

// walkTop calls fn with each top-level box of the file in order. It returns
// ErrNotMP4 if the file is empty or doesn't start with a known MP4 box.
//
// fn also sees bad boxes, so it can stop cleanly at a cut-off upload by
// returning errStopWalk; if it returns nil instead, the walk fails with a size error.
func walkTop(wr *windowReader, fn func(topBox) error) error {
	first := true
	for off := int64(0); off+8 <= wr.size; {
		hdr, err := wr.at(off, 16)
		if err != nil {
			return fmt.Errorf("reading box header at %d: %w", off, err)
		}
		if len(hdr) < 8 {
			break
		}
		b := topBox{typ: string(hdr[4:8]), off: off, size: int64(binary.BigEndian.Uint32(hdr[0:4])), hdrLen: 8}
		switch b.size {
		case 1:
			if len(hdr) < 16 {
				return fmt.Errorf("truncated 64-bit box header at %d", off)
			}
			b.size, b.hdrLen = int64(binary.BigEndian.Uint64(hdr[8:16])), 16
		case 0:
			b.size = wr.size - off // extends to end of file
		}
		if first {
			if !knownTopLevel(b.typ) {
				return ErrNotMP4
			}
			first = false
		}
		b.bad = b.size < b.hdrLen || off+b.size > wr.size
		switch err := fn(b); {
		case errors.Is(err, errStopWalk):
			return nil
		case err != nil:
			return err
		case b.bad:
			return fmt.Errorf("box %q at %d has bad size %d (incomplete upload?)", b.typ, off, b.size)
		}
		off += b.size
	}
	if first {
		return ErrNotMP4
	}
	return nil
}

func knownTopLevel(t string) bool {
	switch t {
	case "ftyp", "moov", "mdat", "free", "skip", "wide", "pdin", "uuid", "styp", "sidx", "moof", "mfra":
		return true
	}
	return false
}

// body reads the contents of top-level box b, refusing boxes over limit bytes.
func (w *windowReader) body(b topBox, limit int64) ([]byte, error) {
	if b.size > limit {
		return nil, fmt.Errorf("%s is %d bytes, larger than supported", b.typ, b.size)
	}
	body, err := w.at(b.off+b.hdrLen, int(b.size-b.hdrLen))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", b.typ, err)
	}
	if int64(len(body)) < b.size-b.hdrLen {
		return nil, fmt.Errorf("%s is truncated (incomplete upload?)", b.typ)
	}
	return body, nil
}

// ---------- boxes inside a box ----------

// boxes iterates the child boxes in b.
func boxes(b []byte, fn func(typ string, body []byte) error) error {
	for len(b) >= 8 {
		size := uint64(binary.BigEndian.Uint32(b[0:4]))
		typ := string(b[4:8])
		hdr := uint64(8)
		if size == 1 {
			if len(b) < 16 {
				return fmt.Errorf("truncated %q", typ)
			}
			size = binary.BigEndian.Uint64(b[8:16])
			hdr = 16
		} else if size == 0 {
			size = uint64(len(b))
		}
		if size < hdr || size > uint64(len(b)) {
			return fmt.Errorf("box %q has bad size %d", typ, size)
		}
		if err := fn(typ, b[hdr:size]); err != nil {
			return err
		}
		b = b[size:]
	}
	return nil
}

func u32(b []byte, i int) uint32 {
	if i+4 > len(b) {
		return 0
	}
	return binary.BigEndian.Uint32(b[i : i+4])
}

// parseTimeHeader reads timescale and duration from mvhd/mdhd (full boxes).
func parseTimeHeader(b []byte) (timescale uint32, duration uint64, err error) {
	if len(b) < 4 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	if b[0] == 1 {
		if len(b) < 32 {
			return 0, 0, io.ErrUnexpectedEOF
		}
		return binary.BigEndian.Uint32(b[20:24]), binary.BigEndian.Uint64(b[24:32]), nil
	}
	if len(b) < 20 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	return binary.BigEndian.Uint32(b[12:16]), uint64(binary.BigEndian.Uint32(b[16:20])), nil
}

// ---------- moov ----------

// movie is what Probe and Defragment read from a moov. Its byte slices point
// into the moov body passed to parseMoov.
type movie struct {
	mvhd       []byte // mvhd body; nil if missing
	fragmented bool   // has mvex: samples live in moof boxes
	fragDur    uint64 // from mehd, in the movie timescale; 0 if absent
	tracks     []*trackInfo
	other      [][]byte // other children (udta, meta, ...) as whole boxes
}

// trackInfo is what is read from one trak, plus its trex defaults.
type trackInfo struct {
	id        uint32
	timescale uint32
	video     bool
	body      []byte // trak body
	stsd      []byte // stsd body; nil if missing
	inMoov    bool   // moov's own sample tables aren't empty
	trex      trex
}

// trex holds a track's default sample values for fragments.
type trex struct {
	sdi, dur, size, flags uint32
}

func parseMoov(moov []byte) (*movie, error) {
	m := &movie{}
	defaults := map[uint32]trex{}
	err := boxes(moov, func(typ string, body []byte) error {
		switch typ {
		case "mvhd":
			m.mvhd = body
		case "mvex":
			m.fragmented = true
			return boxes(body, func(t string, b []byte) error {
				switch t {
				case "mehd":
					if len(b) >= 12 && b[0] == 1 {
						m.fragDur = binary.BigEndian.Uint64(b[4:12])
					} else if len(b) >= 8 {
						m.fragDur = uint64(u32(b, 4))
					}
				case "trex": // version/flags, track_ID, sample description index, duration, size, flags
					defaults[u32(b, 4)] = trex{sdi: u32(b, 8), dur: u32(b, 12), size: u32(b, 16), flags: u32(b, 20)}
				}
				return nil
			})
		case "trak":
			t, err := parseTrak(body)
			if err != nil {
				return err
			}
			m.tracks = append(m.tracks, t)
		default:
			m.other = append(m.other, box(typ, body))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, t := range m.tracks {
		t.trex = defaults[t.id]
	}
	return m, nil
}

// duration is the movie's length from its headers. In a fragmented file mvhd
// only covers samples in moov (usually none), so mehd's is used instead.
func (m *movie) duration() (time.Duration, error) {
	if m.mvhd == nil {
		return 0, nil
	}
	timescale, d, err := parseTimeHeader(m.mvhd)
	if err != nil {
		return 0, fmt.Errorf("mvhd: %w", err)
	}
	if m.fragmented {
		d = m.fragDur
	}
	if timescale == 0 {
		return 0, nil
	}
	return time.Duration(float64(d) / float64(timescale) * float64(time.Second)), nil
}

// firstVideo returns the first video track, or nil.
func (m *movie) firstVideo() *trackInfo {
	for _, t := range m.tracks {
		if t.video {
			return t
		}
	}
	return nil
}

func parseTrak(trak []byte) (*trackInfo, error) {
	t := &trackInfo{body: trak}
	err := boxes(trak, func(typ string, body []byte) error {
		switch typ {
		case "tkhd": // version 0: flags(4) created(4) modified(4) track_ID; version 1: 8-byte times
			if len(body) > 0 && body[0] == 1 {
				t.id = u32(body, 20)
			} else {
				t.id = u32(body, 12)
			}
		case "mdia":
			return boxes(body, func(typ string, body []byte) error {
				switch typ {
				case "mdhd":
					ts, _, err := parseTimeHeader(body)
					if err != nil {
						return fmt.Errorf("mdhd: %w", err)
					}
					t.timescale = ts
				case "hdlr": // version/flags(4) pre_defined(4) handler_type(4)
					t.video = len(body) >= 12 && string(body[8:12]) == "vide"
				case "minf":
					return boxes(body, func(typ string, body []byte) error {
						if typ != "stbl" {
							return nil
						}
						return boxes(body, func(typ string, body []byte) error {
							switch typ {
							case "stsd":
								t.stsd = body
							case "stsz", "stz2": // sample count after version/flags and sample_size (stz2: field size)
								t.inMoov = t.inMoov || u32(body, 8) > 0
							case "stts": // entry count after version/flags
								t.inMoov = t.inMoov || u32(body, 4) > 0
							}
							return nil
						})
					})
				}
				return nil
			})
		}
		return nil
	})
	return t, err
}

// sampleEntry reads the codec fourcc and, for video, the size from the first
// sample entry of an stsd body. Zero values if it can't.
func sampleEntry(stsd []byte) (codec string, width, height int) {
	if len(stsd) < 16 {
		return "", 0, 0
	}
	entry := stsd[8:] // skip version/flags + entry_count
	codec = string(entry[4:8])
	// VisualSampleEntry: 6 reserved + 2 data_reference_index + 16 pre_defined/reserved, then width, height
	if len(entry) >= 8+28 {
		width = int(binary.BigEndian.Uint16(entry[8+24 : 8+26]))
		height = int(binary.BigEndian.Uint16(entry[8+26 : 8+28]))
	}
	return codec, width, height
}
