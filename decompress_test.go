package rdp8compress

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/rand"
	"strings"
	"testing"
)

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustDecompress(t testing.TB, d *Decompressor, in []byte) []byte {
	t.Helper()
	out, err := d.Decompress(in)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	return out
}

// The five samples in [MS-RDPEGFX] 4.2.1.1.
func TestSpecExamples(t *testing.T) {
	fox := []byte("The quick brown fox jumps over the lazy dog")
	cases := []struct {
		name string
		in   string
		want []byte
	}{
		{"4.2.1.1.1", "E0 24 CE 9B 19 62 18 00", []byte{0x01, 0x02, 0xFF, 0x65, 0x65, 0x65, 0x65, 0x65}},
		{"4.2.1.1.2", "E0 04" + hex.EncodeToString(fox), fox},
		{"4.2.1.1.3", "E0 24 20 90 88 71 1F B2 01", bytes.Repeat([]byte("ABC"), 20)},
		{"4.2.1.1.4", "E1 03 00 2B 00 00 00 11 00 00 00 04 54 68 65 20" +
			"71 75 69 63 6B 20 62 72 6F 77 6E 20 0E 00 00 00" +
			"04 66 6F 78 20 6A 75 6D 70 73 20 6F 76 65 10 00" +
			"00 00 24 39 08 0E 91 F8 D8 61 3D 1E 44 06 43 79" +
			"9C 02", fox},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mustDecompress(t, NewDecompressor(), unhex(t, c.in))
			if !bytes.Equal(got, c.want) {
				t.Fatalf("got %x, want %x", got, c.want)
			}
		})
	}

	// 4.2.1.1.5 omits most of its 1,002 random bytes, so rebuild it from
	// the documented header bytes and our own payload.
	t.Run("4.2.1.1.5", func(t *testing.T) {
		payload := make([]byte, 1000)
		copy(payload, fox)
		rand.New(rand.NewSource(5)).Read(payload[len(fox):])
		in := append(unhex(t, "E0 24 88 01 F4 00"), payload...)
		in = append(in, 0x00)
		got := mustDecompress(t, NewDecompressor(), in)
		if !bytes.Equal(got, payload) {
			t.Fatal("output mismatch")
		}
	})
}

// The bit stream examples in 3.1.9.1.2.5.
func TestBitStreamExamples(t *testing.T) {
	var w bitWriter
	w.str("0 0100 1001 10001 00001 110 001")
	got := mustDecompress(t, NewDecompressor(), single(compressedSeg(w.finish())))
	if want := bytes.Repeat([]byte{0x49}, 10); !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}

	// "10010 0001100" is distance 44; "110 101" is length 13.
	w = bitWriter{}
	for i := 0; i < 44; i++ {
		w.literal(byte(i + 'a'))
	}
	w.str("10010 0001100 110 101")
	got = mustDecompress(t, NewDecompressor(), single(compressedSeg(w.finish())))
	if len(got) != 57 || !bytes.Equal(got[44:], got[:13]) {
		t.Fatalf("got %q", got)
	}
}

func TestLiterals(t *testing.T) {
	var w bitWriter
	var want []byte
	for c := 0; c < 256; c++ {
		w.literal(byte(c))
		w.literal9(byte(c)) // 9-bit forms of short literals are accepted
		want = append(want, byte(c), byte(c))
	}
	got := mustDecompress(t, NewDecompressor(), single(compressedSeg(w.finish())))
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x", got)
	}
}

func TestMatchLengthClasses(t *testing.T) {
	lengths := []int{3, 4, 7, 8, 15, 16, 31, 32, 63, 64, 127, 128, 255, 256, 511,
		512, 1023, 1024, 2047, 2048, 4095, 4096, 8191, 8192, 16383, 16384,
		32767, 32768, 65535}
	for _, n := range lengths {
		var w bitWriter
		w.literal('x')
		w.literal('y')
		w.match(2, n)
		got := mustDecompress(t, NewDecompressor(), single(compressedSeg(w.finish())))
		want := bytes.Repeat([]byte("xy"), n/2+2)[:n+2]
		if !bytes.Equal(got, want) {
			t.Fatalf("length %d: got %d bytes", n, len(got))
		}
	}
}

func TestMatchDistanceClasses(t *testing.T) {
	// Fill the history with a known pattern, then reach back each distance.
	hist := make([]byte, historySize)
	rand.New(rand.NewSource(1)).Read(hist)
	d := NewDecompressor()
	for off := 0; off < len(hist); off += 1 << 20 {
		chunk := hist[off:min(off+1<<20, len(hist))]
		mustDecompress(t, d, single(rawSeg(chunk)))
	}
	dists := []int{1, 31, 32, 159, 160, 671, 672, 1695, 1696, 5791, 5792,
		22175, 22176, 54943, 54944, 317087, 317088, 1365663, 1365664,
		2414239, 2414240, historySize}
	for _, dist := range dists {
		var w bitWriter
		w.match(dist, 3)
		got := mustDecompress(t, d, single(compressedSeg(w.finish())))
		hist = append(hist, got...)
		start := len(hist) - 3 - dist
		if want := hist[start : start+3]; !bytes.Equal(got, want) {
			t.Fatalf("distance %d: got %x, want %x", dist, got, want)
		}
	}
}

func TestUnencodedRun(t *testing.T) {
	for _, n := range []int{0, 1, 7, 1000, 32767} {
		raw := make([]byte, n)
		rand.New(rand.NewSource(int64(n))).Read(raw)
		var w bitWriter
		w.literal('a') // misalign before the run
		w.unencoded(raw)
		w.literal('b') // decoding continues after the run
		w.match(n+2, 3)
		got := mustDecompress(t, NewDecompressor(), single(compressedSeg(w.finish())))
		want := append(append([]byte("a"), raw...), 'b')
		for i := 0; i < 3; i++ {
			want = append(want, want[len(want)-(n+2)])
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("n=%d: mismatch", n)
		}
	}
}

func TestTrailerBits(t *testing.T) {
	// Streams of every length mod 8, so the trailer takes every value.
	for n := 1; n <= 16; n++ {
		var w bitWriter
		for i := 0; i < n; i++ {
			w.str("11000") // literal 0x00, 5 bits
		}
		data := w.finish()
		if want := byte((8 - (5*n)%8) % 8); data[len(data)-1] != want {
			t.Fatalf("trailer %d, want %d", data[len(data)-1], want)
		}
		got := mustDecompress(t, NewDecompressor(), single(compressedSeg(data)))
		if !bytes.Equal(got, make([]byte, n)) {
			t.Fatalf("n=%d: got %x", n, got)
		}
		// Reserved high bits of the trailer are ignored.
		data[len(data)-1] |= 0xF8
		got = mustDecompress(t, NewDecompressor(), single(compressedSeg(data)))
		if !bytes.Equal(got, make([]byte, n)) {
			t.Fatalf("n=%d reserved bits: got %x", n, got)
		}
	}
	// A segment with only a zero trailer decodes to nothing.
	got := mustDecompress(t, NewDecompressor(), single(compressedSeg([]byte{0})))
	if len(got) != 0 {
		t.Fatalf("got %x", got)
	}
}

func TestHistoryWraparound(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	d := NewDecompressor()
	var stream []byte
	// 2,400,000 bytes, then 300,000 more: the ring wraps once.
	for _, n := range []int{2400000, 300000} {
		msg := make([]byte, n)
		rng.Read(msg)
		var segs [][]byte
		for off := 0; off < n; off += maxSegment {
			segs = append(segs, rawSeg(msg[off:min(off+maxSegment, n)]))
		}
		stream = append(stream, mustDecompress(t, d, multipart(n, segs...))...)
	}
	if d.hpos != len(stream)-historySize {
		t.Fatalf("hpos %d, want %d", d.hpos, len(stream)-historySize)
	}
	// Matches whose source straddles the wrap point, the far end of the
	// window, and a source that runs from history into the current message.
	// Stream position historySize is at ring index 0.
	straddle := len(stream) - (historySize - 10)
	var w bitWriter
	w.match(straddle, 20)
	w.match(historySize, 5)
	w.literal('q')
	w.match(4, 40)
	got := mustDecompress(t, d, single(compressedSeg(w.finish())))
	var want []byte
	emit := func(dist, n int) {
		for i := 0; i < n; i++ {
			j := len(stream) + len(want) - dist
			if j < len(stream) {
				want = append(want, stream[j])
			} else {
				want = append(want, want[j-len(stream)])
			}
		}
	}
	emit(straddle, 20)
	emit(historySize, 5)
	want = append(want, 'q')
	emit(4, 40)
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x\nwant %x", got, want)
	}
}

func TestHistoryAcrossCallsAndSegments(t *testing.T) {
	d := NewDecompressor()
	mustDecompress(t, d, single(rawSeg([]byte("hello "))))

	// A second message whose first segment is raw and whose second is
	// compressed, matching into both the previous message and segment.
	var w bitWriter
	w.match(11, 5) // "hello" from the first message
	w.match(11, 6) // " world" from the first segment of this message
	got := mustDecompress(t, d, multipart(16, rawSeg([]byte("world")), compressedSeg(w.finish())))
	if string(got) != "worldhello world" {
		t.Fatalf("got %q", got)
	}

	d.Reset()
	w = bitWriter{}
	w.match(1, 3)
	if _, err := d.Decompress(single(compressedSeg(w.finish()))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("after Reset: err = %v, want ErrCorrupt", err)
	}
}

func TestLargeMessageTrimsHistory(t *testing.T) {
	msg := make([]byte, historySize+12345)
	rand.New(rand.NewSource(3)).Read(msg)
	d := NewDecompressor()
	mustDecompress(t, d, single(rawSeg(msg)))
	if d.hfill != historySize {
		t.Fatalf("hfill = %d", d.hfill)
	}
	var w bitWriter
	w.match(historySize, 3)
	got := mustDecompress(t, d, single(compressedSeg(w.finish())))
	if want := msg[len(msg)-historySize:][:3]; !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestEmptyMessages(t *testing.T) {
	d := NewDecompressor()
	for _, in := range [][]byte{single(rawSeg(nil)), multipart(0), multipart(0, rawSeg(nil))} {
		got, err := d.Decompress(in)
		if err != nil || len(got) != 0 {
			t.Fatalf("%x: got %x, %v", in, got, err)
		}
	}
}

func TestErrors(t *testing.T) {
	bits := func(s string) []byte {
		var w bitWriter
		w.str(s)
		return w.finish()
	}
	lit := func(n int) []byte {
		var w bitWriter
		for i := 0; i < n; i++ {
			w.literal('a')
		}
		return w.finish()
	}
	cases := []struct {
		name string
		in   []byte
		max  int
		want error
	}{
		{"empty", nil, 0, ErrCorrupt},
		{"unknown descriptor", []byte{0xE2, 0x04}, 0, ErrCorrupt},
		{"single without segment header", []byte{0xE0}, 0, ErrCorrupt},
		{"short multipart header", []byte{0xE1, 1, 0, 0}, 0, ErrCorrupt},
		{"short segment size", unhex(t, "E1 0100 01000000 0100"), 0, ErrCorrupt},
		{"segment count too high", unhex(t, "E1 0200 01000000 02000000 0401"), 0, ErrCorrupt},
		{"segment size exceeds input", unhex(t, "E1 0100 01000000 05000000 04 01"), 0, ErrCorrupt},
		{"trailing bytes", append(multipart(1, rawSeg([]byte{1})), 0), 0, ErrCorrupt},
		{"uncompressedSize too big", multipart(2, rawSeg([]byte{1})), 0, ErrCorrupt},
		{"uncompressedSize too small", multipart(1, rawSeg([]byte{1, 2})), 0, ErrCorrupt},
		{"uncompressedSize too small, compressed", multipart(1, compressedSeg(lit(2))), 0, ErrCorrupt},
		{"empty multipart segment", multipart(0, nil), 0, ErrCorrupt},
		{"missing trailer", single([]byte{0x24}), 0, ErrCorrupt},
		{"trailer exceeds stream", single([]byte{0x24, 0x01}), 0, ErrCorrupt},
		{"bad compression type", single([]byte{0x23, 0x00}), 0, ErrCorrupt},
		{"reserved 9-bit prefix", single(compressedSeg(bits("101111100 0000000"))), 0, ErrCorrupt},
		{"reserved 9-bit prefix 2", single(compressedSeg(bits("101111111"))), 0, ErrCorrupt},
		{"reserved 10000 prefix", single(compressedSeg(bits("10000 0000"))), 0, ErrCorrupt},
		{"distance beyond window", single(compressedSeg(bits("10111101 111111111111111111111 0"))), 0, ErrCorrupt},
		{"distance beyond output", single(compressedSeg(bits("0 01100001 10001 00110 0"))), 0, ErrCorrupt},
		{"reserved length prefix", single(compressedSeg(bits("0 01100001 0 01100001 10001 00001 111111111111111 000000000000000 0"))), 0, ErrCorrupt},
		{"literal past end", single([]byte{0x24, 0x30, 0x00}), 0, ErrCorrupt},
		{"truncated literal", single([]byte{0x24, 0x00, 0x04}), 0, ErrCorrupt},
		{"truncated match", single(compressedSeg(bits("0 01100001 10001 000"))), 0, ErrCorrupt},
		{"truncated match length", single(compressedSeg(bits("0 01100001 0 01100001 10001 00001 1111"))), 0, ErrCorrupt},
		{"truncated unencoded count", single(compressedSeg(bits("10001 00000 0000"))), 0, ErrCorrupt},
		{"unencoded run past end", single(compressedSeg(bits("10001 00000 000000000000010 0000000 01100001"))), 0, ErrCorrupt},
		{"raw over limit", single(rawSeg(make([]byte, 11))), 10, ErrTooLarge},
		{"literal over limit", single(compressedSeg(lit(11))), 10, ErrTooLarge},
		{"match over limit", single(compressedSeg(bits("0 01100001 10001 00001 110 000"))), 8, ErrTooLarge},
		{"unencoded over limit", single(compressedSeg(bits("10001 00000 000000000000010 0000000 01100001 01100001"))), 1, ErrTooLarge},
		{"declared size over limit", multipart(11, rawSeg(make([]byte, 11))), 10, ErrTooLarge},
		{"multipart exceeds declared", multipart(4, compressedSeg(bits("0 01100001 10001 00001 110 000"))), 0, ErrCorrupt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := NewDecompressor()
			mustDecompress(t, d, single(rawSeg([]byte("seed"))))
			if c.max > 0 {
				d.maxOutput = c.max
			}
			hist := append([]byte(nil), d.hist[:4]...)
			pos, fill := d.hpos, d.hfill
			out, err := d.Decompress(c.in)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if out != nil {
				t.Fatalf("out = %x on error", out)
			}
			if d.hpos != pos || d.hfill != fill || !bytes.Equal(d.hist[:4], hist) {
				t.Fatal("history changed on error")
			}
		})
	}
}

func TestWithMaxOutput(t *testing.T) {
	if d := NewDecompressor(WithMaxOutput(0)); d.maxOutput != DefaultMaxOutput {
		t.Fatalf("maxOutput = %d", d.maxOutput)
	}
	if d := NewDecompressor(WithMaxOutput(5)); d.maxOutput != 5 {
		t.Fatalf("maxOutput = %d", d.maxOutput)
	}
	d := NewDecompressor(WithMaxOutput(4))
	if _, err := d.Decompress(single(rawSeg([]byte("four")))); err != nil {
		t.Fatalf("at limit: %v", err)
	}
}

func TestScratchReleased(t *testing.T) {
	d := NewDecompressor()
	mustDecompress(t, d, single(rawSeg(make([]byte, keepScratch+1))))
	if d.out != nil {
		t.Fatal("large scratch buffer kept")
	}
	mustDecompress(t, d, single(rawSeg(make([]byte, 100))))
	if cap(d.out) == 0 {
		t.Fatal("small scratch buffer dropped")
	}
}

func TestReturnedSliceOwned(t *testing.T) {
	d := NewDecompressor()
	a := mustDecompress(t, d, single(rawSeg([]byte("aaaa"))))
	mustDecompress(t, d, single(rawSeg([]byte("bbbb"))))
	if string(a) != "aaaa" {
		t.Fatalf("first result overwritten: %q", a)
	}
}

func TestTokenTable(t *testing.T) {
	// Every 9-bit pattern is decodable except the prefixes the token table
	// leaves undefined: 10000 and 1011111.
	for i, e := range lut {
		reserved := i>>4 == 0b10000 || i>>2 == 0b1011111
		if (e.kind == kindReserved) != reserved {
			t.Errorf("lut[%09b] kind %d", i, e.kind)
		}
	}
	// Match distance ranges are contiguous and cover 1..historySize.
	next := 0
	for _, tk := range specTokens {
		if tk.kind != kindMatch {
			continue
		}
		if int(tk.base) != next {
			t.Errorf("distance base %d, want %d", tk.base, next)
		}
		next = int(tk.base) + 1<<tk.valueBits
	}
	if next <= historySize {
		t.Errorf("distances end at %d", next)
	}
}
