# rdp8compress

[![Go Reference](https://pkg.go.dev/badge/github.com/smck83/rdp8compress.svg)](https://pkg.go.dev/github.com/smck83/rdp8compress)
[![CI](https://github.com/smck83/rdp8compress/actions/workflows/ci.yml/badge.svg)](https://github.com/smck83/rdp8compress/actions/workflows/ci.yml)

A small, dependency-free Go decompressor for **RDP 8.0 bulk compression**: the
`RDP_SEGMENTED_DATA` messages a Windows RDP server sends on the Graphics
Pipeline channel (`Microsoft::Windows::RDS::Graphics`).

It was written for Gocamole, a pure-Go
browser RDP gateway, and should suit any Go RDP client.

- Standard library only, no cgo, no `unsafe`, no goroutines.
- Decoding only. The compressor is out of scope, since clients only
  decompress.
- Not supported: the "RDP 8.0 Lite" variant used by compressed dynamic
  channel PDUs ([MS-RDPEDYC]). Negotiate DVC version 2 to avoid it.

```
go get github.com/smck83/rdp8compress
```

## Usage

```go
// One Decompressor per graphics channel. Its history (the last 2,500,000
// bytes of output) persists across messages, as the spec requires.
d := rdp8compress.NewDecompressor()

for msg := range gfxChannelData {
	pdus, err := d.Decompress(msg) // one or more RDPGFX PDUs
	if err != nil {
		// errors.Is(err, rdp8compress.ErrCorrupt) or ErrTooLarge.
		// The history is now out of step with the server's: close the channel.
		return err
	}
	handleGFX(pdus)
}

// When the graphics channel is reopened:
d.Reset()
```

`Decompress` returns a fresh slice the caller owns. A `Decompressor` is not
safe for concurrent use.

## Specification

Implemented only from Microsoft's Open Specifications, [MS-RDPEGFX]:

| Section | Topic |
|---|---|
| [2.2.5.1] | `RDP_SEGMENTED_DATA` (SINGLE `0xE0`, MULTIPART `0xE1`) |
| [2.2.5.2] | `RDP_DATA_SEGMENT` |
| [2.2.5.3] | `RDP8_BULK_ENCODED_DATA` header (`PACKET_COMPRESSED`, `PACKET_COMPR_TYPE_RDP8`) |
| [3.1.9.1] | RDP 8.0 bulk compression: de-blocking, bit stream, trailer and token tables |
| [4.2.1.1] | Compression samples (all five are unit tests here) |
| [4.2.1.2] | Sample code (token table and reference decompressor) |

### Clean-room statement

This code was written from the Microsoft documents above and nothing else.
No other RDP implementation (FreeRDP, GPL code, gopher-rdp, etc.) was read
or used. The test-only encoder in `encoder_test.go` was also written from the
spec's token tables.

## Limits and choices

The input is treated as attacker-controlled. `Decompress` never panics, and
malformed data returns an error wrapping `ErrCorrupt`.

- **Output limit.** `WithMaxOutput(n)` bounds each message's decompressed
  size (default 32 MiB) and returns `ErrTooLarge` beyond it. A multipart
  header whose `uncompressedSize` exceeds the limit is rejected before any
  decoding. No buffer is sized from an untrusted length: output grows only as
  it is produced.
- **Declared size.** A multipart message's `uncompressedSize` must equal the
  real total, and no bytes may follow the last segment.
- **Match distance.** A distance larger than the output produced so far on
  the channel (capped at the 2,500,000-byte window) is rejected as corrupt;
  the spec's sample code would read uninitialised history instead. The sample
  code's 9-bit match prefixes (distances above 4,500,000), the undefined
  prefix `10000`, and length prefixes of 15 or more `1` bits are reserved, and
  are rejected too.
- **Overlapping matches** replicate, as with the sample code's byte-by-byte
  copy. They are implemented with doubling block copies.
- **Truncation.** A token or unencoded run that runs past the bit count the
  trailer byte gives is rejected. The trailer's five reserved high bits are
  ignored.
- **Lenient where harmless.** The 9-bit forms of literals that have short
  codes are accepted, as the sample code accepts them. The 65,535-byte
  per-segment limit binds encoders and is not enforced.
- **Errors are fatal for the channel.** A failed message leaves the history
  unchanged, but the server's history has moved on.

Memory use is a 2.5 MB history (allocated on the first non-empty message)
plus a scratch buffer reused between calls. Each call makes one allocation,
for the returned slice.

## Performance

`BenchmarkDecompress` decodes a 67 KB compressed message (640 KiB of
bitmap-like data; matches can reach into an earlier message).
`BenchmarkDecompressLiterals` decodes 58 KiB of incompressible data coded as
literals, the worst case.

On an Intel Core i5-7200U (2016 laptop CPU, 2.5 GHz, Go 1.27, Windows),
median of 5 runs:

| Benchmark | Output throughput | Allocations |
|---|---|---|
| `BenchmarkDecompress` | 257 MB/s | 1 per call |
| `BenchmarkDecompressLiterals` | 80 MB/s | 1 per call |

Literal-only data is limited by the serial Huffman decode. A server would
normally send such data as an uncompressed segment, which costs a single
copy.

Run them with:

```
go test -run '^$' -bench . -benchmem
```

## Testing

- Unit tests built from hand-written bit streams: every literal and length
  class, every distance class up to 2,500,000, unencoded runs, every trailer
  value, history wraparound, and each error path.
- All five samples from [4.2.1.1], plus the bit-stream examples from
  3.1.9.1.2.5.
- Round-trip tests through a test-only encoder, including random inputs and
  more than 3.5 MB streamed through one channel.
- Fuzz targets `FuzzDecompress` (arbitrary input, and input split into two
  messages) and `FuzzRoundTrip`.
- Golden captures: `TestGolden` decodes every `testdata/*.bin` / `*.out`
  pair (format in [testdata/README.md](testdata/README.md)).

CI runs gofmt, `go vet`, `go test -race` with a 90% coverage gate, 30 s of
fuzzing, `govulncheck`, a `CGO_ENABLED=0` build, a stdlib-only dependency
check and a benchmark smoke run.

## Licence

[Apache-2.0](LICENSE).

[MS-RDPEGFX]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpegfx/
[MS-RDPEDYC]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpedyc/
[2.2.5.1]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpegfx/1b962dc9-c4f1-404c-a5b8-036bb51656ec
[2.2.5.2]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpegfx/040566f8-4592-48bf-b276-8429038171e1
[2.2.5.3]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpegfx/bfb13bde-5315-4e08-ba8c-3cb30e9571d8
[3.1.9.1]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpegfx/f69980f4-7c92-4846-9c8d-61781362c76f
[4.2.1.1]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpegfx/20331349-723a-4d4d-b204-db17ea82b80b
[4.2.1.2]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpegfx/90d01be7-043f-49c6-be77-9aaa8eb6b4e0
