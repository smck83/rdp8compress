package rdp8compress_test

import (
	"fmt"

	"github.com/smck83/rdp8compress"
)

// Gocamole consumes the package through this interface.
var _ interface {
	Decompress(segmented []byte) ([]byte, error)
} = (*rdp8compress.Decompressor)(nil)

func Example() {
	// One Decompressor per graphics channel; its history persists across
	// messages.
	d := rdp8compress.NewDecompressor()

	// [MS-RDPEGFX] 4.2.1.1.3: "ABC" repeated 20 times.
	msg := []byte{0xE0, 0x24, 0x20, 0x90, 0x88, 0x71, 0x1F, 0xB2, 0x01}
	pdus, err := d.Decompress(msg)
	if err != nil {
		// Treat any error as fatal for the channel.
		fmt.Println(err)
		return
	}
	fmt.Printf("%d bytes: %s...\n", len(pdus), pdus[:9])
	// Output: 60 bytes: ABCABCABC...
}
