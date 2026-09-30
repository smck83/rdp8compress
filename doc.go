// Package rdp8compress decompresses RDP 8.0 bulk-compressed data, as sent by
// a Windows RDP server on the Graphics Pipeline dynamic virtual channel
// ("Microsoft::Windows::RDS::Graphics").
//
// The implementation is written solely from Microsoft's Open Specifications,
// [MS-RDPEGFX]: Remote Desktop Protocol: Graphics Pipeline Extension:
//
//   - 2.2.5.1 RDP_SEGMENTED_DATA
//   - 2.2.5.2 RDP_DATA_SEGMENT
//   - 2.2.5.3 RDP8_BULK_ENCODED_DATA
//   - 3.1.9.1 RDP 8.0 bulk compression (3.1.9.1.2.1 De-Blocking,
//     3.1.9.1.2.2 Compressed Segment Header, 3.1.9.1.2.3 Compressed Segment
//     Bit Stream, 3.1.9.1.2.4 Compressed Segment Trailer, 3.1.9.1.2.5 Bit
//     Stream Encoding Examples)
//   - 4.2.1.1 Compression Samples and 4.2.1.2 Sample Code
//
// # Usage
//
// Create one [Decompressor] per graphics channel and feed it every
// RDP_SEGMENTED_DATA message in order. The history buffer (the last
// 2,500,000 bytes of output) persists across calls, as 3.1.9.1.1 requires.
// Call [Decompressor.Reset] when the channel is reopened.
//
// # Handling of malformed input
//
// Input is treated as attacker-controlled. Decompress never panics; every
// malformed message returns an error wrapping [ErrCorrupt] or [ErrTooLarge].
// Where the specification leaves decoder behaviour open, this package makes
// the following choices:
//
//   - A match distance greater than the number of bytes output so far on the
//     channel (capped at the 2,500,000-byte history size) is rejected as
//     corrupt. The sample code would read uninitialised history instead.
//   - Distances above 2,500,000 are rejected. The sample code's 9-bit match
//     prefixes (101111100 to 101111110) are not in the normative token table
//     in 3.1.9.1.2.4 and are treated as reserved, as is the undefined
//     prefix 10000.
//   - A match length prefix of fifteen or more "1" bits (lengths of 65,536
//     and above) is reserved and rejected.
//   - A token that runs past the end of the bit stream, or an unencoded run
//     that runs past the end of the segment, is rejected.
//   - The five reserved high-order bits of the trailer byte are ignored.
//   - A compressed segment whose compression type is not
//     PACKET_COMPR_TYPE_RDP8 (0x04) is rejected. The type of uncompressed
//     segments is not checked.
//   - The 9-bit literal encodings that 3.1.9.1.2.4 reserves for literals with
//     shorter codes are accepted, as the sample code does.
//   - A multipart message's uncompressedSize must equal the real total, and
//     no bytes may follow the last segment.
//   - The 65,535-byte per-segment limit is a requirement on encoders and is
//     not enforced; the per-message limit set by [WithMaxOutput] is.
//
// After an error the decoder's history is left as it was before the failed
// message, but the server's history has moved on, so later messages on the
// channel will not decode correctly. Treat any error as fatal for the
// channel.
//
// A Decompressor is not safe for concurrent use. The package has no mutable
// package-level state, starts no goroutines and does not use unsafe.
//
// [MS-RDPEGFX]: https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-rdpegfx/
package rdp8compress
