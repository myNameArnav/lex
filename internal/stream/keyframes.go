package stream

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sort"
)

// Matroska element IDs used to find the keyframe index (the Cues element).
const (
	ebmlSegment       = 0x18538067
	ebmlSeekHead      = 0x114D9B74
	ebmlSeek          = 0x4DBB
	ebmlSeekID        = 0x53AB
	ebmlSeekPosition  = 0x53AC
	ebmlInfo          = 0x1549A966
	ebmlTimecodeScale = 0x2AD7B1
	ebmlTracks        = 0x1654AE6B
	ebmlTrackEntry    = 0xAE
	ebmlTrackNumber   = 0xD7
	ebmlTrackType     = 0x83
	ebmlCues          = 0x1C53BB6B
	ebmlCuePoint      = 0xBB
	ebmlCueTime       = 0xB3
	ebmlCueTrackPos   = 0xB7
	ebmlCueTrack      = 0xF7
	ebmlCluster       = 0x1F43B675
)

var errNoCues = errors.New("no keyframe index")

// MatroskaKeyframes returns the video keyframe times (seconds, ascending)
// listed in a Matroska/WebM file's Cues, reading only the file's index
// rather than its media data, so it's quick even on a hard drive.
func MatroskaKeyframes(path string) ([]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := ebmlReader{f}
	// EBML header, then the Segment.
	id, size, hdr, err := r.header(0)
	if err != nil || id != 0x1A45DFA3 {
		return nil, errors.New("not a Matroska file")
	}
	off := int64(hdr) + size
	id, _, hdr, err = r.header(off)
	if err != nil || id != ebmlSegment {
		return nil, errors.New("no Matroska segment")
	}
	seg := off + int64(hdr) // SeekPosition values are relative to this
	pos := map[uint32]int64{}
	// Walk top-level elements up to the first Cluster; the SeekHead says
	// where the rest (usually Cues at the end of the file) live.
	for off = seg; ; {
		id, size, hdr, err := r.header(off)
		if err != nil || size < 0 {
			break
		}
		if _, seen := pos[id]; !seen {
			pos[id] = off
		}
		if id == ebmlSeekHead {
			body, err := r.body(off+int64(hdr), size, 1<<20)
			if err != nil {
				return nil, err
			}
			eachChild(body, func(id uint32, b []byte) {
				if id != ebmlSeek {
					return
				}
				var target uint32
				var at int64 = -1
				eachChild(b, func(id uint32, v []byte) {
					switch id {
					case ebmlSeekID:
						target = uint32(readUint(v))
					case ebmlSeekPosition:
						at = int64(readUint(v))
					}
				})
				if _, seen := pos[target]; !seen && at >= 0 {
					pos[target] = seg + at
				}
			})
		}
		if id == ebmlCluster {
			break
		}
		off += int64(hdr) + size
	}
	cuesAt, ok := pos[ebmlCues]
	if !ok {
		return nil, errNoCues
	}
	scale := uint64(1000000) // ns per timestamp unit
	if at, ok := pos[ebmlInfo]; ok {
		if b, err := r.element(at, ebmlInfo, 1<<20); err == nil {
			eachChild(b, func(id uint32, v []byte) {
				if id == ebmlTimecodeScale && readUint(v) > 0 {
					scale = readUint(v)
				}
			})
		}
	}
	videoTrack := uint64(0)
	if at, ok := pos[ebmlTracks]; ok {
		if b, err := r.element(at, ebmlTracks, 4<<20); err == nil {
			eachChild(b, func(id uint32, e []byte) {
				if id != ebmlTrackEntry || videoTrack != 0 {
					return
				}
				var num, kind uint64
				eachChild(e, func(id uint32, v []byte) {
					switch id {
					case ebmlTrackNumber:
						num = readUint(v)
					case ebmlTrackType:
						kind = readUint(v)
					}
				})
				if kind == 1 {
					videoTrack = num
				}
			})
		}
	}
	cues, err := r.element(cuesAt, ebmlCues, 64<<20)
	if err != nil {
		return nil, err
	}
	var times []float64
	eachChild(cues, func(id uint32, cp []byte) {
		if id != ebmlCuePoint {
			return
		}
		var t uint64
		video := videoTrack == 0
		eachChild(cp, func(id uint32, v []byte) {
			switch id {
			case ebmlCueTime:
				t = readUint(v)
			case ebmlCueTrackPos:
				eachChild(v, func(id uint32, tv []byte) {
					if id == ebmlCueTrack && readUint(tv) == videoTrack {
						video = true
					}
				})
			}
		})
		if video {
			times = append(times, float64(t)*float64(scale)/1e9)
		}
	})
	if len(times) == 0 {
		return nil, errNoCues
	}
	sort.Float64s(times)
	out := times[:1]
	for _, t := range times[1:] {
		if t > out[len(out)-1] {
			out = append(out, t)
		}
	}
	return out, nil
}

type ebmlReader struct{ r io.ReaderAt }

// header reads the element header at off: ID (with its length marker, as
// IDs are written), data size (-1 if unknown) and header length.
func (e ebmlReader) header(off int64) (id uint32, size int64, n int, err error) {
	var b [12]byte
	m, err := e.r.ReadAt(b[:], off)
	if m == 0 {
		return 0, 0, 0, err
	}
	buf := b[:m]
	idLen := vintLen(buf[0])
	if idLen == 0 || idLen > 4 || len(buf) < idLen+1 {
		return 0, 0, 0, errors.New("bad element ID")
	}
	for _, c := range buf[:idLen] {
		id = id<<8 | uint32(c)
	}
	sizeLen := vintLen(buf[idLen])
	if sizeLen == 0 || len(buf) < idLen+sizeLen {
		return 0, 0, 0, errors.New("bad element size")
	}
	v := uint64(buf[idLen]) & (0xFF >> sizeLen)
	allOnes := v == 0xFF>>sizeLen
	for _, c := range buf[idLen+1 : idLen+sizeLen] {
		v = v<<8 | uint64(c)
		allOnes = allOnes && c == 0xFF
	}
	if allOnes {
		return id, -1, idLen + sizeLen, nil
	}
	return id, int64(v), idLen + sizeLen, nil
}

func (e ebmlReader) body(off, size, max int64) ([]byte, error) {
	if size < 0 || size > max {
		return nil, errors.New("element too large")
	}
	b := make([]byte, size)
	if _, err := e.r.ReadAt(b, off); err != nil && !(errors.Is(err, io.EOF) && size == 0) {
		return nil, err
	}
	return b, nil
}

// element reads the body of the element at off, which must have the given ID.
func (e ebmlReader) element(off int64, want uint32, max int64) ([]byte, error) {
	id, size, n, err := e.header(off)
	if err != nil {
		return nil, err
	}
	if id != want {
		return nil, errors.New("unexpected element")
	}
	return e.body(off+int64(n), size, max)
}

// eachChild calls fn for each child element in an element body.
func eachChild(b []byte, fn func(id uint32, body []byte)) {
	for len(b) > 1 {
		idLen := vintLen(b[0])
		if idLen == 0 || idLen > 4 || len(b) < idLen+1 {
			return
		}
		var id uint32
		for _, c := range b[:idLen] {
			id = id<<8 | uint32(c)
		}
		sizeLen := vintLen(b[idLen])
		if sizeLen == 0 || len(b) < idLen+sizeLen {
			return
		}
		v := uint64(b[idLen]) & (0xFF >> sizeLen)
		for _, c := range b[idLen+1 : idLen+sizeLen] {
			v = v<<8 | uint64(c)
		}
		start := idLen + sizeLen
		if v > uint64(len(b)-start) {
			return
		}
		fn(id, b[start:start+int(v)])
		b = b[start+int(v):]
	}
}

// vintLen returns the length of an EBML variable-length integer from its
// first byte (0 if invalid).
func vintLen(c byte) int {
	for i := 0; i < 8; i++ {
		if c&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 0
}

func readUint(b []byte) uint64 {
	if len(b) > 8 {
		return 0
	}
	var buf [8]byte
	copy(buf[8-len(b):], b)
	return binary.BigEndian.Uint64(buf[:])
}
