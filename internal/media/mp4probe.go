// Package media reads what the index needs from video files.
package media

import (
	"errors"
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

// Probe reads the boxes of an MP4 of the given size. For regular files it
// reads the top-level headers and moov. For fragmented files (Reolink, many
// NVRs) the header carries no real duration, so it sums the sample durations
// of every fragment, reading only the small moof boxes.
func Probe(r io.ReaderAt, size int64) (Info, error) {
	var (
		info             Info
		sawMdat, sawMoov bool
		movieDur         time.Duration
		video            *trackInfo
		fragTicks        uint64
	)
	wr := &windowReader{r: r, size: size}
	err := walkTop(wr, func(b topBox) error {
		if b.bad {
			if sawMoov && info.Fragmented && b.typ == "mdat" {
				return errStopWalk // last fragment still uploading/truncated: count what we have
			}
			return nil
		}
		switch b.typ {
		case "mdat":
			sawMdat = true
		case "moov":
			body, err := wr.body(b, maxMoov)
			if err != nil {
				return err
			}
			m, err := parseMoov(body)
			if err != nil {
				return err
			}
			sawMoov = true
			info.MoovAtEnd, info.Fragmented = sawMdat, m.fragmented
			if movieDur, err = m.duration(); err != nil {
				return err
			}
			if video = m.firstVideo(); video != nil {
				info.Codec, info.Width, info.Height = sampleEntry(video.stsd)
			}
			if !info.Fragmented {
				return errStopWalk // the headers have everything
			}
		case "moof":
			if !sawMoov || video == nil {
				return nil // fragments before moov: nothing to attribute them to
			}
			body, err := wr.body(b, maxMoof)
			if err != nil {
				return err
			}
			ticks, err := moofTicks(body, video)
			if err != nil {
				return err
			}
			fragTicks += ticks
			info.Fragments++
		}
		return nil
	})

	switch {
	case err != nil:
		return info, err
	case !sawMoov:
		return info, errors.New("no moov box (incomplete upload?)")
	case !info.Fragmented:
		if movieDur <= 0 {
			return info, errors.New("file has no duration")
		}
		info.Duration = movieDur
		return info, nil
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

// moofTicks sums the sample durations of track t in one moof, in t's timescale.
func moofTicks(moof []byte, t *trackInfo) (uint64, error) {
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
				defDur = t.trex.dur
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
