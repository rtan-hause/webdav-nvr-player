package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// Remux presents a fragmented MP4 as a regular MP4 with its index at the
// front, the same result as `ffmpeg -c copy -movflags +faststart`, without
// copying any media. The virtual file is Head followed by the source bytes
// [DataStart, DataEnd). Head holds a new ftyp, a moov whose sample tables point
// into that range, and an mdat header that wraps it; the fragment headers left
// inside the range are unused padding.
//
// Browsers need this for Reolink-style uploads: their moov says the clip is 1 s
// long and has no fragment index, so Firefox stops after the first fragment and
// can't seek.
type Remux struct {
	Head      []byte
	DataStart int64
	DataEnd   int64
	Duration  time.Duration // of the longest track
}

// Size is the length of the virtual file.
func (x *Remux) Size() int64 { return int64(len(x.Head)) + x.DataEnd - x.DataStart }

// ErrNotFragmented means the file is a regular MP4 that can be served as is.
var ErrNotFragmented = errors.New("not a fragmented MP4")

const maxSamplesPerTrun = 1 << 20

type rxSample struct {
	size, dur uint32
	cto       int64
	sync      bool
}

// rxChunk is one trun: samples stored back to back in the source.
type rxChunk struct {
	off   int64 // source offset of the first sample
	first int   // index into samples
	n     int
	sdi   uint32
}

type rxTrack struct {
	trackInfo
	samples []rxSample
	chunks  []rxChunk
}

func (t *rxTrack) mediaDuration() uint64 {
	var d uint64
	for _, s := range t.samples {
		d += uint64(s.dur)
	}
	return d
}

// Defragment reads the moov and every moof of a fragmented MP4 (one small
// read per fragment) and builds a Remux. A file cut off mid-fragment keeps the
// samples that are complete. Regular files return ErrNotFragmented.
func Defragment(r io.ReaderAt, size int64) (*Remux, error) {
	var (
		mvhd       []byte
		extra      [][]byte // other moov children (udta, meta), kept as whole boxes
		tracks     []*rxTrack
		byID       = map[uint32]*rxTrack{}
		movieScale uint32
	)
	wr := &windowReader{r: r, size: size}
	err := walkTop(wr, func(b topBox) error {
		if b.bad {
			if tracks != nil && (b.typ == "mdat" || b.typ == "moof") {
				return errStopWalk // upload cut off: keep the complete samples
			}
			return nil
		}
		switch b.typ {
		case "moov":
			body, err := wr.body(b, maxMoov)
			if err != nil {
				return err
			}
			// Copy: the tracks keep pointing into it after the walk.
			m, err := parseMoov(append([]byte(nil), body...))
			if err != nil {
				return err
			}
			if err := remuxable(m); err != nil {
				return err
			}
			if movieScale, _, err = parseTimeHeader(m.mvhd); err != nil {
				return fmt.Errorf("mvhd: %w", err)
			}
			mvhd, extra = m.mvhd, m.other
			for _, ti := range m.tracks {
				t := &rxTrack{trackInfo: *ti}
				tracks = append(tracks, t)
				byID[t.id] = t
			}
		case "moof":
			if tracks == nil {
				return errors.New("fragment before moov")
			}
			body, err := wr.body(b, maxMoof)
			if err != nil {
				return err
			}
			cut, err := readMoof(body, b.off, size, byID)
			if err != nil {
				return err
			}
			if cut {
				return errStopWalk
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if tracks == nil {
		return nil, errors.New("no moov box (incomplete upload?)")
	}
	if movieScale == 0 {
		return nil, errors.New("mvhd has no timescale")
	}

	// Drop tracks with no samples; a video track is required.
	kept := tracks[:0]
	hasVideo := false
	for _, t := range tracks {
		if len(t.samples) == 0 || t.timescale == 0 {
			continue
		}
		kept = append(kept, t)
		hasVideo = hasVideo || t.video
	}
	tracks = kept
	if !hasVideo {
		return nil, errors.New("fragmented file has no video samples (incomplete upload?)")
	}

	x := &Remux{DataStart: math.MaxInt64}
	var movieDur uint64
	for _, t := range tracks {
		for _, c := range t.chunks {
			end := c.off
			for _, s := range t.samples[c.first : c.first+c.n] {
				end += int64(s.size)
			}
			x.DataStart = min(x.DataStart, c.off)
			x.DataEnd = max(x.DataEnd, end)
		}
		d := scale(t.mediaDuration(), t.timescale, movieScale)
		movieDur = max(movieDur, d)
		x.Duration = max(x.Duration, time.Duration(float64(t.mediaDuration())/float64(t.timescale)*float64(time.Second)))
	}

	ftyp := box("ftyp", []byte("isom"), be32(0x200), []byte("isomiso2mp41"))
	payload := x.DataEnd - x.DataStart
	mdatHdr := box("mdat")
	if payload+8 > math.MaxUint32 {
		mdatHdr = append(be32(1), "mdat"...)
		mdatHdr = binary.BigEndian.AppendUint64(mdatHdr, uint64(payload+16))
	} else {
		binary.BigEndian.PutUint32(mdatHdr, uint32(payload+8))
	}
	build := func(base int64, co64 bool) []byte {
		parts := [][]byte{box("mvhd", patchDuration(mvhd, 16, 24, movieDur))}
		for _, t := range tracks {
			parts = append(parts, t.trak(movieScale, base, x.DataStart, co64))
		}
		parts = append(parts, extra...)
		return box("moov", parts...)
	}
	// stco entries have a fixed width, so the moov's size doesn't depend on base.
	co64 := false
	moov := build(0, false)
	if int64(len(ftyp)+len(moov)+len(mdatHdr))+payload > math.MaxUint32 {
		co64 = true
		moov = build(0, true)
	}
	base := int64(len(ftyp) + len(moov) + len(mdatHdr))
	moov = build(base, co64)
	x.Head = make([]byte, 0, base)
	x.Head = append(append(append(x.Head, ftyp...), moov...), mdatHdr...)
	return x, nil
}

// remuxable reports why Defragment can't rebuild m, if it can't. Files
// without mvex aren't fragmented and can be served as they are.
func remuxable(m *movie) error {
	if !m.fragmented {
		return ErrNotFragmented
	}
	if m.mvhd == nil {
		return errors.New("moov has no mvhd")
	}
	for _, t := range m.tracks {
		if t.stsd == nil {
			return fmt.Errorf("track %d has no sample description", t.id)
		}
		if t.inMoov {
			return errors.New("fragmented file also has samples in moov; not supported")
		}
	}
	return nil
}

// readMoof appends the samples of one fragment. It reports cut when a sample
// runs past the end of the file, so the caller stops there.
func readMoof(moof []byte, moofOff, fileSize int64, byID map[uint32]*rxTrack) (cut bool, err error) {
	firstTraf := true
	var prevEnd int64 // end of the previous traf's data, the default base for the next
	err = boxes(moof, func(typ string, traf []byte) error {
		if typ != "traf" {
			return nil
		}
		var (
			t                     *rxTrack
			base, next            int64
			sdi, dur, size, flags uint32
			empty                 bool
		)
		err := boxes(traf, func(typ string, b []byte) error {
			switch typ {
			case "tfhd":
				if len(b) < 8 {
					return errors.New("tfhd is truncated")
				}
				tf := u32(b, 0) & 0xffffff
				t = byID[u32(b, 4)]
				if t == nil {
					return nil
				}
				sdi, dur, size, flags = t.trex.sdi, t.trex.dur, t.trex.size, t.trex.flags
				i := 8
				need := 8
				for _, f := range []struct {
					bit uint32
					n   int
				}{{0x1, 8}, {0x2, 4}, {0x8, 4}, {0x10, 4}, {0x20, 4}} {
					if tf&f.bit != 0 {
						need += f.n
					}
				}
				if len(b) < need {
					return errors.New("tfhd is truncated")
				}
				switch {
				case tf&0x1 != 0:
					base = int64(binary.BigEndian.Uint64(b[i:]))
					i += 8
				case tf&0x20000 != 0 || firstTraf:
					base = moofOff
				default:
					base = prevEnd
				}
				if tf&0x2 != 0 {
					sdi = u32(b, i)
					i += 4
				}
				if tf&0x8 != 0 {
					dur = u32(b, i)
					i += 4
				}
				if tf&0x10 != 0 {
					size = u32(b, i)
					i += 4
				}
				if tf&0x20 != 0 {
					flags = u32(b, i)
				}
				empty = tf&0x10000 != 0
				next = base
			case "trun":
				if t == nil || empty || cut {
					return nil
				}
				if len(b) < 8 {
					return errors.New("trun is truncated")
				}
				version := b[0]
				tr := u32(b, 0) & 0xffffff
				count := u32(b, 4)
				if count > maxSamplesPerTrun {
					return fmt.Errorf("trun has %d samples, more than supported", count)
				}
				i := 8
				pos := next
				if tr&0x1 != 0 {
					pos = base + int64(int32(u32(b, i)))
					i += 4
				}
				hasFirst := tr&0x4 != 0
				var firstFlags uint32
				if hasFirst {
					firstFlags = u32(b, i)
					i += 4
				}
				stride := 0
				for _, f := range []uint32{0x100, 0x200, 0x400, 0x800} {
					if tr&f != 0 {
						stride += 4
					}
				}
				if i > len(b) || uint64(len(b)-i) < uint64(count)*uint64(stride) {
					return errors.New("trun is truncated")
				}
				// Writers that set first_sample_flags and nothing per sample mean
				// "only the first sample is a keyframe", even when the defaults say
				// every sample is (Reolink does this). Players agree; so do we.
				firstOnlySync := t.video && hasFirst && tr&0x400 == 0
				c := rxChunk{off: pos, first: len(t.samples), sdi: sdi}
				for s := uint32(0); s < count; s++ {
					smp := rxSample{dur: dur, size: size}
					fl := flags
					if tr&0x100 != 0 {
						smp.dur = u32(b, i)
						i += 4
					}
					if tr&0x200 != 0 {
						smp.size = u32(b, i)
						i += 4
					}
					if tr&0x400 != 0 {
						fl = u32(b, i)
						i += 4
					} else if s == 0 && hasFirst {
						fl = firstFlags
					}
					if tr&0x800 != 0 {
						if version == 0 {
							smp.cto = int64(u32(b, i))
						} else {
							smp.cto = int64(int32(u32(b, i)))
						}
						i += 4
					}
					// sample_is_non_sync_sample, or sample_depends_on == 1 (depends on others)
					smp.sync = fl&0x10000 == 0 && (fl>>24)&3 != 1
					if firstOnlySync && s > 0 {
						smp.sync = false
					}
					if pos < 0 || pos+int64(smp.size) > fileSize {
						cut = true
						break
					}
					t.samples = append(t.samples, smp)
					pos += int64(smp.size)
				}
				if c.n = len(t.samples) - c.first; c.n > 0 {
					t.chunks = append(t.chunks, c)
				}
				next = pos
			}
			return nil
		})
		firstTraf = false
		prevEnd = next
		return err
	})
	return cut, err
}

// trak rebuilds a track with sample tables. Chunk offsets are virtual:
// base + (source offset - dataStart).
func (t *rxTrack) trak(movieScale uint32, base, dataStart int64, co64 bool) []byte {
	mediaDur := t.mediaDuration()
	movieDur := scale(mediaDur, t.timescale, movieScale)
	var parts [][]byte
	_ = boxes(t.body, func(typ string, body []byte) error {
		switch typ {
		case "tkhd":
			parts = append(parts, box("tkhd", patchDuration(body, 20, 28, movieDur)))
		case "edts":
			parts = append(parts, box("edts", fixEdits(body, movieDur)))
		case "mdia":
			var mdia [][]byte
			_ = boxes(body, func(typ string, body []byte) error {
				switch typ {
				case "mdhd":
					mdia = append(mdia, box("mdhd", patchDuration(body, 16, 24, mediaDur)))
				case "minf":
					var minf [][]byte
					_ = boxes(body, func(typ string, body []byte) error {
						if typ == "stbl" {
							minf = append(minf, t.stbl(base, dataStart, co64))
						} else {
							minf = append(minf, box(typ, body))
						}
						return nil
					})
					mdia = append(mdia, box("minf", minf...))
				default:
					mdia = append(mdia, box(typ, body))
				}
				return nil
			})
			parts = append(parts, box("mdia", mdia...))
		default:
			parts = append(parts, box(typ, body))
		}
		return nil
	})
	return box("trak", parts...)
}

func (t *rxTrack) stbl(base, dataStart int64, co64 bool) []byte {
	n := len(t.samples)
	parts := [][]byte{box("stsd", t.stsd)}

	// stts: run-length durations.
	var stts []byte
	entries := uint32(0)
	for i := 0; i < n; {
		j := i
		for j < n && t.samples[j].dur == t.samples[i].dur {
			j++
		}
		stts = append(stts, be32(uint32(j-i), t.samples[i].dur)...)
		entries++
		i = j
	}
	parts = append(parts, box("stts", be32(0, entries), stts))

	// ctts: composition offsets, only when some sample has one.
	anyCTO, negCTO := false, false
	for _, s := range t.samples {
		anyCTO = anyCTO || s.cto != 0
		negCTO = negCTO || s.cto < 0
	}
	if anyCTO {
		var ctts []byte
		entries = 0
		for i := 0; i < n; {
			j := i
			for j < n && t.samples[j].cto == t.samples[i].cto {
				j++
			}
			ctts = append(ctts, be32(uint32(j-i), uint32(t.samples[i].cto))...)
			entries++
			i = j
		}
		version := uint32(0)
		if negCTO {
			version = 1 << 24
		}
		parts = append(parts, box("ctts", be32(version, entries), ctts))
	}

	// stss: keyframe list, omitted when every sample is one.
	var stss []byte
	syncs := uint32(0)
	for i, s := range t.samples {
		if s.sync {
			stss = append(stss, be32(uint32(i+1))...)
			syncs++
		}
	}
	if int(syncs) < n {
		parts = append(parts, box("stss", be32(0, syncs), stss))
	}

	// stsc: one chunk per trun; consecutive chunks of the same shape share an entry.
	var stsc []byte
	entries = 0
	for i, c := range t.chunks {
		if i > 0 && c.n == t.chunks[i-1].n && c.sdi == t.chunks[i-1].sdi {
			continue
		}
		stsc = append(stsc, be32(uint32(i+1), uint32(c.n), c.sdi)...)
		entries++
	}
	parts = append(parts, box("stsc", be32(0, entries), stsc))

	// stsz: one size for all samples when they match.
	same := true
	for _, s := range t.samples {
		if s.size != t.samples[0].size {
			same = false
			break
		}
	}
	if same {
		parts = append(parts, box("stsz", be32(0, t.samples[0].size, uint32(n))))
	} else {
		stsz := make([]byte, 0, 4*n)
		for _, s := range t.samples {
			stsz = binary.BigEndian.AppendUint32(stsz, s.size)
		}
		parts = append(parts, box("stsz", be32(0, 0, uint32(n)), stsz))
	}

	if co64 {
		co := make([]byte, 0, 8*len(t.chunks))
		for _, c := range t.chunks {
			co = binary.BigEndian.AppendUint64(co, uint64(base+c.off-dataStart))
		}
		parts = append(parts, box("co64", be32(0, uint32(len(t.chunks))), co))
	} else {
		co := make([]byte, 0, 4*len(t.chunks))
		for _, c := range t.chunks {
			co = binary.BigEndian.AppendUint32(co, uint32(base+c.off-dataStart))
		}
		parts = append(parts, box("stco", be32(0, uint32(len(t.chunks))), co))
	}
	return box("stbl", parts...)
}

// patchDuration copies a full box body (mvhd, mdhd, tkhd) with its duration
// field set; the field is at off0 in version 0 and off1 in version 1.
func patchDuration(body []byte, off0, off1 int, d uint64) []byte {
	b := append([]byte(nil), body...)
	if len(b) > 0 && b[0] == 1 {
		if len(b) >= off1+8 {
			binary.BigEndian.PutUint64(b[off1:], d)
		}
	} else if len(b) >= off0+4 {
		binary.BigEndian.PutUint32(b[off0:], uint32(min(d, math.MaxUint32)))
	}
	return b
}

// fixEdits stretches the last real edit to the end of the track. Fragmented
// files usually write 0 there, which means "everything" only while fragmented.
func fixEdits(edts []byte, trackDur uint64) []byte {
	var out []byte
	_ = boxes(edts, func(typ string, b []byte) error {
		if typ != "elst" || len(b) < 8 {
			out = append(out, box(typ, b)...)
			return nil
		}
		b = append([]byte(nil), b...)
		v1 := b[0] == 1
		count := int(u32(b, 4))
		entry := 12
		if v1 {
			entry = 20
		}
		if len(b) < 8+count*entry {
			out = append(out, box(typ, b)...)
			return nil
		}
		var used uint64
		for k := 0; k < count; k++ {
			e := b[8+k*entry:]
			var segDur uint64
			var mediaTime int64
			if v1 {
				segDur, mediaTime = binary.BigEndian.Uint64(e), int64(binary.BigEndian.Uint64(e[8:]))
			} else {
				segDur, mediaTime = uint64(u32(e, 0)), int64(int32(u32(e, 4)))
			}
			if k == count-1 && mediaTime != -1 {
				segDur = 0
				if trackDur > used {
					segDur = trackDur - used
				}
				if v1 {
					binary.BigEndian.PutUint64(e, segDur)
				} else {
					binary.BigEndian.PutUint32(e, uint32(min(segDur, math.MaxUint32)))
				}
			}
			used += segDur
		}
		out = append(out, box(typ, b)...)
		return nil
	})
	return out
}

func scale(d uint64, from, to uint32) uint64 {
	if from == 0 {
		return 0
	}
	return uint64(float64(d) * float64(to) / float64(from))
}

func box(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out, uint32(n))
	copy(out[4:], typ)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func be32(vals ...uint32) []byte {
	out := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.BigEndian.PutUint32(out[4*i:], v)
	}
	return out
}
