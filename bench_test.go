package rdp8compress

import (
	"math/rand"
	"testing"
)

func runBench(b *testing.B, gen func(*rand.Rand, int) []byte, rawLen int) {
	rng := rand.New(rand.NewSource(42))
	enc := newTestEncoder()
	d := NewDecompressor()
	// Warm both histories so matches can reach earlier messages, as in a
	// long-running session.
	if _, err := d.Decompress(enc.encode(gen(rng, 256<<10))); err != nil {
		b.Fatal(err)
	}
	in := gen(rng, rawLen)
	msg := enc.encode(in)
	b.Logf("compressed %d bytes -> %d bytes", len(msg), len(in))

	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Rewinding the history keeps every iteration identical. The bytes
		// the previous iteration wrote lie beyond hfill, so no valid match
		// can see them.
		pos, fill := d.hpos, d.hfill
		if _, err := d.Decompress(msg); err != nil {
			b.Fatal(err)
		}
		d.hpos, d.hfill = pos, fill
	}
}

// BenchmarkDecompress decodes a ~64 KiB compressed message of bitmap-like
// data.
func BenchmarkDecompress(b *testing.B) {
	runBench(b, graphicsLike, 640<<10)
}

// BenchmarkDecompressLiterals decodes ~64 KiB of incompressible data coded
// as literals, the slowest path.
func BenchmarkDecompressLiterals(b *testing.B) {
	random := func(rng *rand.Rand, n int) []byte {
		p := make([]byte, n)
		rng.Read(p)
		return p
	}
	runBench(b, random, 58<<10)
}
