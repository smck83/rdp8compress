package rdp8compress

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

var (
	ErrCorrupt  = errors.New("rdp8compress: corrupt data")
	ErrTooLarge = errors.New("rdp8compress: output exceeds limit")
)

const (
	// DefaultMaxOutput is the default bound on the decompressed size of one
	// message.
	DefaultMaxOutput = 32 << 20

	// historySize is the history window, the largest distance a match can
	// reach (3.1.9.1.2.4; the Sample Code's m_historyBuffer).
	historySize = 2500000

	// RDP_SEGMENTED_DATA descriptor values (2.2.5.1).
	descriptorSingle    = 0xE0
	descriptorMultipart = 0xE1

	// RDP8_BULK_ENCODED_DATA header bits (2.2.5.3).
	packetCompressed    = 0x20
	compressionTypeMask = 0x0F
	compressionTypeRDP8 = 0x04

	// multipartHeaderLen is descriptor + segmentCount + uncompressedSize.
	multipartHeaderLen = 1 + 2 + 4

	// maxLengthOnes is the most "1" bits a match length prefix may contain
	// (111111111111110, lengths 32768 to 65535).
	maxLengthOnes = 14

	// keepScratch is the largest scratch buffer kept between calls.
	keepScratch = 4 << 20
)

// Decompressor holds the history for one graphics channel. It is not safe
// for concurrent use; use one per connection.
type Decompressor struct {
	maxOutput int

	// hist is a ring buffer of the last historySize bytes of output from
	// earlier messages. It is allocated on first use.
	hist  []byte
	hpos  int // index in hist where the next byte goes
	hfill int // bytes of hist that hold output, at most historySize

	// out is scratch space for the message being decoded. Bytes are added
	// to hist only once the whole message has decoded.
	out []byte

	limit    int   // output bound for the current message
	limitErr error // error returned when limit is exceeded
}

// Option configures a Decompressor.
type Option func(*Decompressor)

// WithMaxOutput bounds the decompressed size of a single message
// (default 32 MiB). Messages exceeding it return ErrTooLarge. A value of zero
// or less selects the default.
func WithMaxOutput(n int) Option {
	return func(d *Decompressor) {
		if n <= 0 {
			n = DefaultMaxOutput
		}
		d.maxOutput = n
	}
}

// NewDecompressor returns a Decompressor with an empty history.
func NewDecompressor(opts ...Option) *Decompressor {
	d := &Decompressor{maxOutput: DefaultMaxOutput}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Reset clears the history, as when the graphics channel is reopened.
func (d *Decompressor) Reset() {
	d.hpos = 0
	d.hfill = 0
	d.out = d.out[:0]
}

// Decompress decodes one RDP_SEGMENTED_DATA message and returns the
// concatenated payload (one or more RDPGFX PDUs). The returned slice is
// owned by the caller.
func (d *Decompressor) Decompress(segmented []byte) ([]byte, error) {
	d.out = d.out[:0]
	err := d.deblock(segmented)
	if err == nil {
		d.commit()
	}
	var res []byte
	if err == nil {
		res = make([]byte, len(d.out))
		copy(res, d.out)
	}
	if cap(d.out) > keepScratch {
		d.out = nil
	} else {
		d.out = d.out[:0]
	}
	return res, err
}

// deblock parses RDP_SEGMENTED_DATA (2.2.5.1, 3.1.9.1.2.1) and decodes each
// segment into d.out.
func (d *Decompressor) deblock(b []byte) error {
	if len(b) == 0 {
		return corrupt("empty message")
	}
	d.limit, d.limitErr = d.maxOutput, ErrTooLarge
	switch b[0] {
	case descriptorSingle:
		return d.segment(b[1:])
	case descriptorMultipart:
		if len(b) < multipartHeaderLen {
			return corrupt("short multipart header")
		}
		count := int(binary.LittleEndian.Uint16(b[1:]))
		size := binary.LittleEndian.Uint32(b[3:])
		if uint64(size) > uint64(d.maxOutput) {
			return fmt.Errorf("%w: uncompressedSize %d, limit %d", ErrTooLarge, size, d.maxOutput)
		}
		// Output beyond the declared size means the header is wrong.
		d.limit, d.limitErr = int(size), ErrCorrupt
		p := b[multipartHeaderLen:]
		for i := 0; i < count; i++ {
			// RDP_DATA_SEGMENT (2.2.5.2).
			if len(p) < 4 {
				return corrupt("short segment header")
			}
			n := binary.LittleEndian.Uint32(p)
			p = p[4:]
			if uint64(n) > uint64(len(p)) {
				return corrupt("segment size exceeds input")
			}
			if err := d.segment(p[:n]); err != nil {
				return err
			}
			p = p[n:]
		}
		if len(p) != 0 {
			return corrupt("trailing bytes after last segment")
		}
		if len(d.out) != int(size) {
			return corrupt("uncompressedSize does not match output")
		}
		return nil
	default:
		return corrupt("unknown descriptor")
	}
}

// segment decodes one RDP8_BULK_ENCODED_DATA structure (2.2.5.3, 3.1.9.1.2.2).
func (d *Decompressor) segment(b []byte) error {
	if len(b) == 0 {
		return corrupt("missing segment header")
	}
	header, data := b[0], b[1:]
	if header&packetCompressed == 0 {
		if len(data) > d.limit-len(d.out) {
			return d.overLimit()
		}
		d.out = append(d.out, data...)
		return nil
	}
	if header&compressionTypeMask != compressionTypeRDP8 {
		return corrupt("unsupported compression type")
	}
	return d.decode(data)
}

// decode Huffman-decodes one compressed segment's data field
// (3.1.9.1.2.3, 3.1.9.1.2.4).
func (d *Decompressor) decode(data []byte) error {
	if len(data) == 0 {
		return corrupt("missing trailer byte")
	}
	// The last byte gives the number of unused bits (0-7) in the byte before
	// it. Its five high-order bits are reserved and ignored.
	end := len(data) - 1
	total := end*8 - int(data[end]&7)
	if total < 0 {
		return corrupt("trailer exceeds bit stream")
	}
	r := bitReader{src: data[:end]}
	// Work on a local copy of d.out so the literal path stays in registers;
	// store it back before calling helpers that use d.out. On error d.out is
	// discarded, so it need not be current.
	out, limit := d.out, d.limit

	for r.consumed < total {
		if r.nbits < 32 {
			r.refill()
		}
		e := lut[r.acc>>(64-lutBits)]
		switch e.kind {
		case kindLiteral:
			r.consume(uint(e.length))
			if len(out) >= limit {
				return d.overLimit()
			}
			out = append(out, byte(e.base))

		case kindMatch:
			r.consume(uint(e.length))
			distance := int(e.base) + int(r.read(uint(e.valueBits)))
			if r.nbits < 32 {
				r.refill()
			}
			if distance == 0 {
				// An unencoded run: a 15-bit count, padding to the next
				// byte boundary, then count raw bytes.
				count := int(r.read(15))
				start := (r.consumed + 7) / 8
				stop := start + count
				if r.consumed > total || stop*8 > total {
					return corrupt("unencoded run exceeds segment")
				}
				if count > limit-len(out) {
					return d.overLimit()
				}
				out = append(out, r.src[start:stop]...)
				r.seek(stop)
				continue
			}
			var length int
			if r.acc>>63 == 0 {
				r.consume(1)
				length = 3
			} else {
				ones := bits.LeadingZeros64(^r.acc)
				if ones > maxLengthOnes {
					return corrupt("reserved match length prefix")
				}
				r.consume(uint(ones) + 1)
				length = 2<<ones + int(r.read(uint(ones)+1))
			}
			if r.consumed > total {
				return corrupt("match runs past end of bit stream")
			}
			d.out = out
			err := d.match(distance, length)
			out = d.out
			if err != nil {
				return err
			}

		default:
			return corrupt("reserved token")
		}
	}
	if r.consumed != total {
		return corrupt("token runs past end of bit stream")
	}
	d.out = out
	return nil
}

// match appends length bytes starting distance bytes back in the output
// stream. The source may lie in the history, in this message's output, or
// both, and may overlap the bytes being written, which then repeat.
func (d *Decompressor) match(distance, length int) error {
	if distance > historySize || distance > d.hfill+len(d.out) {
		return corrupt("match distance exceeds history")
	}
	if length > d.limit-len(d.out) {
		return d.overLimit()
	}
	// src indexes d.out; negative values reach back into d.hist.
	src := len(d.out) - distance
	if src < 0 {
		back := -src
		n := min(length, back)
		i := d.hpos - back
		if i < 0 {
			i += historySize
		}
		first := min(n, historySize-i)
		d.out = append(d.out, d.hist[i:i+first]...)
		d.out = append(d.out, d.hist[:n-first]...)
		length -= n
		src = 0
	}
	// Copy in chunks no longer than what already exists, so overlapping
	// matches replicate as in the Sample Code's byte-by-byte loop.
	for length > 0 {
		n := min(length, len(d.out)-src)
		d.out = append(d.out, d.out[src:src+n]...)
		src += n
		length -= n
	}
	return nil
}

// commit appends the decoded message to the history ring.
func (d *Decompressor) commit() {
	b := d.out
	if len(b) == 0 {
		return
	}
	if d.hist == nil {
		d.hist = make([]byte, historySize)
	}
	if len(b) > historySize {
		b = b[len(b)-historySize:]
	}
	n := copy(d.hist[d.hpos:], b)
	copy(d.hist, b[n:])
	d.hpos = (d.hpos + len(b)) % historySize
	d.hfill = min(d.hfill+len(d.out), historySize)
}

func (d *Decompressor) overLimit() error {
	if d.limitErr == ErrCorrupt {
		return corrupt("output exceeds uncompressedSize")
	}
	return fmt.Errorf("%w: limit %d", ErrTooLarge, d.limit)
}

func corrupt(why string) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, why)
}

// bitReader reads a bit stream most-significant bit first (3.1.9.1.2.3).
// Reads past the end of src return zero bits; callers compare consumed with
// the stream length to detect overruns.
type bitReader struct {
	src      []byte
	pos      int    // next byte of src to load into acc
	acc      uint64 // unread bits, left-aligned; bits below nbits may be preloaded
	nbits    uint   // valid bits in acc
	consumed int    // bits consumed from the start of src
}

// refill loads bytes until at least 56 bits are available.
func (r *bitReader) refill() {
	if r.pos+8 <= len(r.src) {
		// Load 8 bytes; keep the whole ones. Bits of the partial byte land
		// where the next refill will OR the same values again.
		r.acc |= binary.BigEndian.Uint64(r.src[r.pos:]) >> r.nbits
		n := (63 - r.nbits) >> 3
		r.pos += int(n)
		r.nbits += n * 8
		return
	}
	for r.nbits <= 56 {
		var b byte
		if r.pos < len(r.src) {
			b = r.src[r.pos]
		}
		r.pos++
		r.acc |= uint64(b) << (56 - r.nbits)
		r.nbits += 8
	}
}

func (r *bitReader) consume(n uint) {
	r.acc <<= n
	r.nbits -= n
	r.consumed += int(n)
}

// read returns the next n bits (0 < n <= 32, and n <= nbits).
func (r *bitReader) read(n uint) uint32 {
	v := uint32(r.acc >> (64 - n))
	r.consume(n)
	return v
}

// seek positions the reader at byte i of src.
func (r *bitReader) seek(i int) {
	r.pos = i
	r.acc = 0
	r.nbits = 0
	r.consumed = i * 8
}
