package rdp8compress

// A minimal RDP 8.0 bulk encoder for tests only, written from the token
// tables in [MS-RDPEGFX] 3.1.9.1.2.4. It favours clarity over compression.

import (
	"encoding/binary"
	"math/bits"
)

// bitWriter builds a compressed segment bit stream, most-significant bit
// first, and appends the trailer byte on finish.
type bitWriter struct {
	buf []byte
	acc uint64 // pending bits, right-aligned
	n   uint   // pending bit count (< 8 between calls)
}

func (w *bitWriter) bits(v uint64, n uint) {
	for n > 0 {
		k := min(n, 32)
		n -= k
		w.acc = w.acc<<k | (v>>n)&(1<<k-1)
		w.n += k
		for w.n >= 8 {
			w.n -= 8
			w.buf = append(w.buf, byte(w.acc>>w.n))
		}
		w.acc &= 1<<w.n - 1
	}
}

// str writes a string of '0' and '1' characters; other characters are
// ignored, so spaces may be used for readability.
func (w *bitWriter) str(s string) {
	for _, c := range s {
		switch c {
		case '0':
			w.bits(0, 1)
		case '1':
			w.bits(1, 1)
		}
	}
}

func (w *bitWriter) align() {
	if w.n > 0 {
		w.bits(0, 8-w.n)
	}
}

// finish pads the last byte and appends the trailer giving the unused bit
// count. It returns the segment data field (without the header byte).
func (w *bitWriter) finish() []byte {
	unused := byte(0)
	if w.n > 0 {
		unused = byte(8 - w.n)
		w.bits(0, 8-w.n)
	}
	return append(w.buf, unused)
}

// literal writes the shortest code for c; the spec requires the short codes.
func (w *bitWriter) literal(c byte) {
	for _, t := range specTokens {
		if t.kind == kindLiteral && t.valueBits == 0 && byte(t.base) == c {
			w.bits(uint64(t.prefix), uint(t.prefixLen))
			return
		}
	}
	w.bits(0, 1)
	w.bits(uint64(c), 8)
}

// literal9 writes c with the "0" prefix even when a short code exists.
func (w *bitWriter) literal9(c byte) {
	w.bits(0, 1)
	w.bits(uint64(c), 8)
}

func (w *bitWriter) distance(d int) {
	for _, t := range specTokens {
		if t.kind == kindMatch && d >= int(t.base) && d < int(t.base)+1<<t.valueBits {
			w.bits(uint64(t.prefix), uint(t.prefixLen))
			w.bits(uint64(d-int(t.base)), uint(t.valueBits))
			return
		}
	}
	panic("distance out of range")
}

func (w *bitWriter) length(l int) {
	if l == 3 {
		w.bits(0, 1)
		return
	}
	ones := uint(bits.Len(uint(l)) - 2) // 2<<ones <= l < 4<<ones
	w.bits(1<<ones-1, ones)
	w.bits(0, 1)
	w.bits(uint64(l-2<<ones), ones+1)
}

func (w *bitWriter) match(dist, length int) {
	w.distance(dist)
	w.length(length)
}

func (w *bitWriter) unencoded(raw []byte) {
	w.bits(0b10001, 5)
	w.bits(0, 5)
	w.bits(uint64(len(raw)), 15)
	w.align()
	w.buf = append(w.buf, raw...)
}

// Framing helpers (2.2.5.1-2.2.5.3).

func compressedSeg(data []byte) []byte { return append([]byte{0x24}, data...) }
func rawSeg(data []byte) []byte        { return append([]byte{0x04}, data...) }

func single(seg []byte) []byte { return append([]byte{descriptorSingle}, seg...) }

func multipart(size int, segs ...[]byte) []byte {
	b := []byte{descriptorMultipart}
	b = binary.LittleEndian.AppendUint16(b, uint16(len(segs)))
	b = binary.LittleEndian.AppendUint32(b, uint32(size))
	for _, s := range segs {
		b = binary.LittleEndian.AppendUint32(b, uint32(len(s)))
		b = append(b, s...)
	}
	return b
}

// testEncoder is a greedy LZ encoder that keeps the whole stream so matches
// can reach into earlier messages, as a server's would.
type testEncoder struct {
	stream []byte
	last   map[uint32]int // 3-byte prefix -> latest position in stream
	// rawEvery, when > 0, emits every rawEvery-th segment uncompressed.
	rawEvery int
	// runs, when true, emits literal stretches as unencoded runs.
	runs bool
	segs int
}

func newTestEncoder() *testEncoder { return &testEncoder{last: map[uint32]int{}} }

const maxSegment = 65535

// encode returns one RDP_SEGMENTED_DATA message carrying msg.
func (e *testEncoder) encode(msg []byte) []byte {
	var segs [][]byte
	for off := 0; off < len(msg) || (off == 0 && len(segs) == 0); off += maxSegment {
		chunk := msg[off:min(off+maxSegment, len(msg))]
		segs = append(segs, e.segment(chunk))
	}
	if len(segs) == 1 && len(msg) <= maxSegment && e.segs%2 == 0 {
		return single(segs[0])
	}
	return multipart(len(msg), segs...)
}

func (e *testEncoder) segment(chunk []byte) []byte {
	e.segs++
	if e.rawEvery > 0 && e.segs%e.rawEvery == 0 {
		e.addAll(chunk)
		return rawSeg(chunk)
	}
	var w bitWriter
	var pending []byte // literals not yet written, when e.runs is set
	flush := func() {
		if len(pending) > 0 {
			w.unencoded(pending)
			pending = nil
		}
	}
	for i := 0; i < len(chunk); {
		if dist, n := e.find(chunk[i:]); n >= 3 {
			flush()
			w.match(dist, n)
			e.addAll(chunk[i : i+n])
			i += n
			continue
		}
		if e.runs {
			pending = append(pending, chunk[i])
		} else {
			w.literal(chunk[i])
		}
		e.add(chunk[i])
		i++
	}
	flush()
	return compressedSeg(w.finish())
}

func key(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 }

// find returns the latest match for the start of b within the history
// window, extended as far as it goes (overlaps allowed).
func (e *testEncoder) find(b []byte) (dist, n int) {
	if len(b) < 3 {
		return 0, 0
	}
	p, ok := e.last[key(b)]
	if !ok {
		return 0, 0
	}
	dist = len(e.stream) - p
	if dist > historySize {
		return 0, 0
	}
	for n < len(b) && n < 65535 {
		// Byte at stream position p+n; positions at or past len(stream)
		// come from b itself (an overlapping match).
		var c byte
		if q := p + n; q < len(e.stream) {
			c = e.stream[q]
		} else {
			c = b[q-len(e.stream)]
		}
		if c != b[n] {
			break
		}
		n++
	}
	return dist, n
}

func (e *testEncoder) add(c byte) {
	e.stream = append(e.stream, c)
	if n := len(e.stream); n >= 3 {
		e.last[key(e.stream[n-3:])] = n - 3
	}
}

func (e *testEncoder) addAll(b []byte) {
	for _, c := range b {
		e.add(c)
	}
}
