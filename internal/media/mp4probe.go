// Package media reads what the index needs from video files.
package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// Info is what Probe learns from an MP4/MOV file.
type Info struct {
	Duration   time.Duration
	Codec      string // sample entry fourcc of the first video track, e.g. "hvc1", "avc1"
	Width      int
	Height     int
	Fragmented bool // fragmented MP4 (moov has mvex; samples live in moof boxes)
	Fragments  int  // number of moof boxes counted (fragmented files only)
	MoovAtEnd  bool // moov comes after mdat, so players need an extra range request to start
}

// CodecName returns a short display name for a fourcc.
func CodecName(fourcc string) string {
	switch fourcc {
	case "hvc1", "hev1":
		return "HEVC"
	case "avc1", "avc3":
		return "H.264"
	case "av01":
		return "AV1"
	case "vp09":
		return "VP9"
	case "":
		return ""
	default:
		return fourcc
	}
}

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

// track is what we need from one trak to sum fragment durations.
type track struct {
	id         uint32
	timescale  uint32
	isVideo    bool
	codec      string
	w, h       int
	defaultDur uint32 // from trex
}

// Probe reads the boxes of an MP4 of the given size. For regular files it
// reads the top-level headers and moov. For fragmented files (Reolink, many
// NVRs) the header carries no real duration, so it sums the sample durations
// of every fragment, reading only the small moof boxes.
func Probe(r io.ReaderAt, size int64) (Info, error) {
	var info Info
	wr := &windowReader{r: r, size: size}
	var off int64
	sawMdat, sawMoov, first := false, false, true
	var movieDur time.Duration
	var tracks []*track
	var video *track
	var fragTicks uint64

	for off+8 <= size {
		hdr, err := wr.at(off, 16)
		if err != nil {
			return info, fmt.Errorf("reading box header at %d: %w", off, err)
		}
		if len(hdr) < 8 {
			break
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		hdrLen := int64(8)
		switch boxSize {
		case 1:
			if len(hdr) < 16 {
				return info, fmt.Errorf("truncated 64-bit box header at %d", off)
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
			hdrLen = 16
		case 0:
			boxSize = size - off // extends to end of file
		}
		if first {
			if !knownTopLevel(typ) {
				return info, ErrNotMP4
			}
			first = false
		}
		if boxSize < hdrLen || off+boxSize > size {
			if sawMoov && info.Fragmented && typ == "mdat" {
				break // last fragment still uploading/truncated: count what we have
			}
			return info, fmt.Errorf("box %q at %d has bad size %d (incomplete upload?)", typ, off, boxSize)
		}
		switch typ {
		case "mdat":
			sawMdat = true
		case "moov":
			if boxSize > maxMoov {
				return info, fmt.Errorf("moov is %d bytes, larger than supported", boxSize)
			}
			body, err := wr.at(off+hdrLen, int(boxSize-hdrLen))
			if err != nil {
				return info, fmt.Errorf("reading moov: %w", err)
			}
			if int64(len(body)) < boxSize-hdrLen {
				return info, errors.New("moov is truncated (incomplete upload?)")
			}
			info.MoovAtEnd = sawMdat
			sawMoov = true
			movieDur, tracks, err = parseMoov(body, &info)
			if err != nil {
				return info, err
			}
			for _, t := range tracks {
				if t.isVideo && video == nil {
					video = t
				}
			}
			if video != nil {
				info.Codec, info.Width, info.Height = video.codec, video.w, video.h
			}
			if !info.Fragmented {
				if movieDur <= 0 {
					return info, errors.New("file has no duration")
				}
				info.Duration = movieDur
				return info, nil
			}
		case "moof":
			if !sawMoov || video == nil {
				break // fragments before moov: nothing to attribute them to
			}
			if boxSize > maxMoof {
				return info, fmt.Errorf("moof is %d bytes, larger than supported", boxSize)
			}
			body, err := wr.at(off+hdrLen, int(boxSize-hdrLen))
			if err != nil {
				return info, fmt.Errorf("reading moof: %w", err)
			}
			ticks, err := moofTicks(body, video)
			if err != nil {
				return info, err
			}
			fragTicks += ticks
			info.Fragments++
		}
		off += boxSize
	}

	switch {
	case first:
		return info, ErrNotMP4
	case !sawMoov:
		return info, errors.New("no moov box (incomplete upload?)")
	case video == nil || video.timescale == 0:
		return info, errors.New("no video track")
	}
	if fragTicks > 0 {
		info.Duration = time.Duration(float64(fragTicks) / float64(video.timescale) * float64(time.Second))
	} else {
		info.Duration = movieDur // mehd, if the writer provided one
	}
	if info.Duration <= 0 {
		return info, errors.New("fragmented file has no samples (incomplete upload?)")
	}
	return info, nil
}

func knownTopLevel(t string) bool {
	switch t {
	case "ftyp", "moov", "mdat", "free", "skip", "wide", "pdin", "uuid", "styp", "sidx", "moof", "mfra":
		return true
	}
	return false
}

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

func parseMoov(moov []byte, info *Info) (time.Duration, []*track, error) {
	var movieTimescale uint32
	var movieDuration, fragDuration uint64
	var tracks []*track
	trex := map[uint32]uint32{}
	err := boxes(moov, func(typ string, body []byte) error {
		switch typ {
		case "mvhd":
			ts, dur, err := parseTimeHeader(body)
			if err != nil {
				return fmt.Errorf("mvhd: %w", err)
			}
			movieTimescale, movieDuration = ts, dur
		case "mvex":
			info.Fragmented = true
			return boxes(body, func(t string, b []byte) error {
				switch t {
				case "mehd":
					if len(b) >= 12 && b[0] == 1 {
						fragDuration = binary.BigEndian.Uint64(b[4:12])
					} else if len(b) >= 8 {
						fragDuration = uint64(u32(b, 4))
					}
				case "trex": // version/flags, track_ID, default_sample_description_index, default_sample_duration
					trex[u32(b, 4)] = u32(b, 12)
				}
				return nil
			})
		case "trak":
			t, err := parseTrak(body)
			if err != nil {
				return err
			}
			tracks = append(tracks, t)
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	for _, t := range tracks {
		t.defaultDur = trex[t.id]
	}
	// In a fragmented file mvhd only covers samples in moov (usually none); mehd covers all.
	if info.Fragmented {
		movieDuration = fragDuration
	}
	var d time.Duration
	if movieTimescale > 0 {
		d = time.Duration(float64(movieDuration) / float64(movieTimescale) * float64(time.Second))
	}
	return d, tracks, nil
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

func parseTrak(trak []byte) (*track, error) {
	t := &track{}
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
					t.isVideo = len(body) >= 12 && string(body[8:12]) == "vide"
				case "minf":
					return boxes(body, func(typ string, body []byte) error {
						if typ != "stbl" {
							return nil
						}
						return boxes(body, func(typ string, body []byte) error {
							if typ != "stsd" || len(body) < 16 {
								return nil
							}
							entry := body[8:] // skip version/flags + entry_count
							t.codec = string(entry[4:8])
							// VisualSampleEntry: 6 reserved + 2 data_reference_index + 16 pre_defined/reserved, then width, height
							if len(entry) >= 8+28 {
								t.w = int(binary.BigEndian.Uint16(entry[8+24 : 8+26]))
								t.h = int(binary.BigEndian.Uint16(entry[8+26 : 8+28]))
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

// moofTicks sums the sample durations of track t in one moof, in t's timescale.
func moofTicks(moof []byte, t *track) (uint64, error) {
	var total uint64
	err := boxes(moof, func(typ string, traf []byte) error {
		if typ != "traf" {
			return nil
		}
		var trackID, defDur uint32
		var mine bool
		return boxes(traf, func(typ string, b []byte) error {
			switch typ {
			case "tfhd":
				flags := u32(b, 0) & 0xffffff
				trackID = u32(b, 4)
				mine = trackID == t.id
				defDur = t.defaultDur
				i := 8
				if flags&0x1 != 0 {
					i += 8 // base_data_offset
				}
				if flags&0x2 != 0 {
					i += 4 // sample_description_index
				}
				if flags&0x8 != 0 {
					defDur = u32(b, i)
				}
			case "trun":
				if !mine {
					return nil
				}
				flags := u32(b, 0) & 0xffffff
				count := u32(b, 4)
				i := 8
				if flags&0x1 != 0 {
					i += 4 // data_offset
				}
				if flags&0x4 != 0 {
					i += 4 // first_sample_flags
				}
				if flags&0x100 == 0 {
					total += uint64(count) * uint64(defDur)
					return nil
				}
				stride := 4
				for _, f := range []uint32{0x200, 0x400, 0x800} {
					if flags&f != 0 {
						stride += 4
					}
				}
				if i+int(count)*stride > len(b) {
					return errors.New("trun is truncated")
				}
				for s := uint32(0); s < count; s++ {
					total += uint64(u32(b, i))
					i += stride
				}
			}
			return nil
		})
	})
	return total, err
}
