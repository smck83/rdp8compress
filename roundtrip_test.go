package rdp8compress

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// graphicsLike returns n bytes resembling bitmap data: runs, repeated rows
// and some noise.
func graphicsLike(rng *rand.Rand, n int) []byte {
	b := make([]byte, 0, n)
	row := make([]byte, 256)
	rng.Read(row)
	for len(b) < n {
		switch rng.Intn(4) {
		case 0: // solid run
			c := byte(rng.Intn(256))
			for i := rng.Intn(200) + 1; i > 0; i-- {
				b = append(b, c)
			}
		case 1: // repeated row, slightly changed
			row[rng.Intn(len(row))] = byte(rng.Intn(256))
			b = append(b, row...)
		case 2: // noise
			for i := rng.Intn(40) + 1; i > 0; i-- {
				b = append(b, byte(rng.Intn(256)))
			}
		case 3: // small repeating pattern
			p := rng.Intn(4) + 1
			start := len(b)
			for i := 0; i < p; i++ {
				b = append(b, byte(rng.Intn(256)))
			}
			for i := rng.Intn(100); i > 0; i-- {
				b = append(b, b[start+i%p])
			}
		}
	}
	return b[:n]
}

func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	inputs := [][]byte{
		nil,
		[]byte("a"),
		[]byte("The quick brown fox jumps over the lazy dog"),
		bytes.Repeat([]byte{0}, 200000),
		graphicsLike(rng, 300000),
	}
	for i := 0; i < 50; i++ {
		b := make([]byte, rng.Intn(5000))
		rng.Read(b)
		inputs = append(inputs, b)
		inputs = append(inputs, graphicsLike(rng, rng.Intn(100000)))
	}
	for _, mode := range []struct {
		name     string
		rawEvery int
		runs     bool
	}{{"literals", 0, false}, {"runs", 0, true}, {"mixed", 3, false}} {
		t.Run(mode.name, func(t *testing.T) {
			enc := newTestEncoder()
			enc.rawEvery, enc.runs = mode.rawEvery, mode.runs
			d := NewDecompressor()
			for i, in := range inputs {
				got, err := d.Decompress(enc.encode(in))
				if err != nil {
					t.Fatalf("message %d: %v", i, err)
				}
				if !bytes.Equal(got, in) {
					t.Fatalf("message %d: output mismatch (%d vs %d bytes)", i, len(got), len(in))
				}
			}
		})
	}
}

// TestRoundTripWraparound streams more than the history size through the
// encoder, whose matches reach across message boundaries and the ring's
// wrap point.
func TestRoundTripWraparound(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	rng := rand.New(rand.NewSource(8))
	enc := newTestEncoder()
	d := NewDecompressor()
	total := 0
	for total < 3*historySize/2 {
		in := graphicsLike(rng, 150000)
		// Repeat an old stretch so some matches reach far back.
		if total > 1000000 {
			off := len(enc.stream) - historySize + rng.Intn(1000) + 1000
			if off < 0 {
				off = 0
			}
			copy(in[1000:], enc.stream[off:off+500])
		}
		got, err := d.Decompress(enc.encode(in))
		if err != nil {
			t.Fatalf("at %d: %v", total, err)
		}
		if !bytes.Equal(got, in) {
			t.Fatalf("at %d: output mismatch", total)
		}
		total += len(in)
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("hello hello hello"), false)
	f.Add(bytes.Repeat([]byte("ab"), 1000), true)
	f.Fuzz(func(t *testing.T, in []byte, runs bool) {
		enc := newTestEncoder()
		enc.runs = runs
		d := NewDecompressor()
		// Two messages, so the second can match into the first.
		for i := 0; i < 2; i++ {
			got, err := d.Decompress(enc.encode(in))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, in) {
				t.Fatal("output mismatch")
			}
		}
	})
}

func FuzzDecompress(f *testing.F) {
	for _, s := range fuzzSeeds(f) {
		f.Add(s, uint16(0))
	}
	// Reused across executions, which also exercises Reset; a fresh
	// Decompressor would allocate its 2.5 MB history every time.
	d := NewDecompressor(WithMaxOutput(1 << 20))
	f.Fuzz(func(t *testing.T, in []byte, split uint16) {
		// Treat the input as one message, and also as two messages sharing
		// history.
		d.Reset()
		out, err := d.Decompress(in)
		checkResult(t, out, err, d)

		d.Reset()
		k := int(split) % (len(in) + 1)
		for _, part := range [][]byte{in[:k], in[k:]} {
			out, err := d.Decompress(part)
			checkResult(t, out, err, d)
		}
	})
}

func checkResult(t *testing.T, out []byte, err error, d *Decompressor) {
	t.Helper()
	if err != nil {
		if out != nil {
			t.Fatal("output returned with error")
		}
		if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrTooLarge) {
			t.Fatalf("unexpected error type: %v", err)
		}
		return
	}
	if len(out) > d.maxOutput {
		t.Fatalf("output %d exceeds limit %d", len(out), d.maxOutput)
	}
}

// fuzzSeeds returns the hand-built and encoded streams used elsewhere in the
// tests.
func fuzzSeeds(tb testing.TB) [][]byte {
	var seeds [][]byte
	for _, h := range []string{
		"E0 24 CE 9B 19 62 18 00",
		"E0 24 20 90 88 71 1F B2 01",
		"E1 03 00 2B 00 00 00 11 00 00 00 04 54 68 65 20 71 75 69 63 6B 20 62 72 6F 77 6E 20 0E 00 00 00 " +
			"04 66 6F 78 20 6A 75 6D 70 73 20 6F 76 65 10 00 00 00 24 39 08 0E 91 F8 D8 61 3D 1E 44 06 43 79 9C 02",
		"E0 24 88 01 F4 00 01 02 03 04 00",
	} {
		seeds = append(seeds, unhex(tb, h))
	}
	var w bitWriter
	w.literal('x')
	w.literal('y')
	for _, n := range []int{3, 4, 8, 16, 300} {
		w.match(2, n)
	}
	w.unencoded([]byte("raw"))
	w.match(1, 5)
	seeds = append(seeds, single(compressedSeg(w.finish())))

	rng := rand.New(rand.NewSource(9))
	enc := newTestEncoder()
	enc.rawEvery = 2
	for i := 0; i < 3; i++ {
		seeds = append(seeds, enc.encode(graphicsLike(rng, 2000)))
	}
	return seeds
}

// TestGolden decodes captures from real servers. Each testdata/NAME.bin
// holds a sequence of RDP_SEGMENTED_DATA messages from one graphics
// channel, and testdata/NAME.out the expected output of each; in both files
// every record is a 4-byte little-endian length followed by that many bytes.
func TestGolden(t *testing.T) {
	bins, err := filepath.Glob(filepath.Join("testdata", "*.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bins) == 0 {
		t.Skip("no golden fixtures in testdata")
	}
	for _, bin := range bins {
		name := strings.TrimSuffix(bin, ".bin")
		t.Run(filepath.Base(name), func(t *testing.T) {
			in := readRecords(t, bin)
			want := readRecords(t, name+".out")
			if len(in) != len(want) {
				t.Fatalf("%d messages but %d outputs", len(in), len(want))
			}
			d := NewDecompressor()
			for i := range in {
				got, err := d.Decompress(in[i])
				if err != nil {
					t.Fatalf("message %d: %v", i, err)
				}
				if !bytes.Equal(got, want[i]) {
					t.Fatalf("message %d: output mismatch", i)
				}
			}
		})
	}
}

func readRecords(t *testing.T, path string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recs [][]byte
	for len(b) > 0 {
		if len(b) < 4 {
			t.Fatalf("%s: truncated record header", path)
		}
		n := binary.LittleEndian.Uint32(b)
		b = b[4:]
		if uint64(n) > uint64(len(b)) {
			t.Fatalf("%s: truncated record", path)
		}
		recs = append(recs, b[:n])
		b = b[n:]
	}
	return recs
}

func TestReadRecordsHelper(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.bin")
	rec := binary.LittleEndian.AppendUint32(nil, 3)
	rec = append(rec, "abc"...)
	if err := os.WriteFile(p, append(rec, rec...), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readRecords(t, p); len(got) != 2 || string(got[1]) != "abc" {
		t.Fatalf("got %q", got)
	}
}
