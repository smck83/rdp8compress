# Golden fixtures

`TestGolden` decodes every `NAME.bin` in this directory and compares the
result with `NAME.out`.

- `NAME.bin` is the sequence of `RDP_SEGMENTED_DATA` messages received on one
  graphics channel, in order, from channel open.
- `NAME.out` holds the expected decompressed output of each message.

In both files each record is a 4-byte little-endian length followed by that
many bytes. The two files must have the same number of records. One
`Decompressor` decodes all the messages in a file, so the history carries
over between them as it does on a live channel.

Suggested names: `win10-<scenario>.bin`, `win11-<scenario>.bin`.

`testdata/fuzz/` holds the fuzzer's regression corpus (`go test` runs it
automatically).
