package rdp8compress

// Token definitions from [MS-RDPEGFX] 3.1.9.1.2.4 (Compressed Segment
// Trailer), which holds the normative token table, cross-checked against the
// table in the 4.2.1.2 Sample Code.

const (
	kindReserved = iota // bit sequence not defined by the spec
	kindLiteral         // base is the literal byte, or valueBits follow
	kindMatch           // base plus valueBits give the match distance
)

// token is one Huffman prefix. For kindLiteral with valueBits == 8 (the "0"
// prefix), the literal is the next 8 bits.
type token struct {
	prefixLen int
	prefix    uint32
	kind      uint8
	valueBits int
	base      uint32
}

// specTokens lists every defined token. Match distance 0 (prefix 10001
// followed by five 0 bits) introduces an unencoded run.
var specTokens = [...]token{
	{1, 0b0, kindLiteral, 8, 0},

	{5, 0b10001, kindMatch, 5, 0},
	{5, 0b10010, kindMatch, 7, 32},
	{5, 0b10011, kindMatch, 9, 160},
	{5, 0b10100, kindMatch, 10, 672},
	{5, 0b10101, kindMatch, 12, 1696},
	{6, 0b101100, kindMatch, 14, 5792},
	{6, 0b101101, kindMatch, 15, 22176},
	{7, 0b1011100, kindMatch, 18, 54944},
	{7, 0b1011101, kindMatch, 20, 317088},
	{8, 0b10111100, kindMatch, 20, 1365664},
	{8, 0b10111101, kindMatch, 21, 2414240},

	{5, 0b11000, kindLiteral, 0, 0x00},
	{5, 0b11001, kindLiteral, 0, 0x01},
	{6, 0b110100, kindLiteral, 0, 0x02},
	{6, 0b110101, kindLiteral, 0, 0x03},
	{6, 0b110110, kindLiteral, 0, 0xFF},
	{7, 0b1101110, kindLiteral, 0, 0x04},
	{7, 0b1101111, kindLiteral, 0, 0x05},
	{7, 0b1110000, kindLiteral, 0, 0x06},
	{7, 0b1110001, kindLiteral, 0, 0x07},
	{7, 0b1110010, kindLiteral, 0, 0x08},
	{7, 0b1110011, kindLiteral, 0, 0x09},
	{7, 0b1110100, kindLiteral, 0, 0x0A},
	{7, 0b1110101, kindLiteral, 0, 0x0B},
	{7, 0b1110110, kindLiteral, 0, 0x3A},
	{7, 0b1110111, kindLiteral, 0, 0x3B},
	{7, 0b1111000, kindLiteral, 0, 0x3C},
	{7, 0b1111001, kindLiteral, 0, 0x3D},
	{7, 0b1111010, kindLiteral, 0, 0x3E},
	{7, 0b1111011, kindLiteral, 0, 0x3F},
	{7, 0b1111100, kindLiteral, 0, 0x40},
	{7, 0b1111101, kindLiteral, 0, 0x80},
	{8, 0b11111100, kindLiteral, 0, 0x0C},
	{8, 0b11111101, kindLiteral, 0, 0x38},
	{8, 0b11111110, kindLiteral, 0, 0x39},
	{8, 0b11111111, kindLiteral, 0, 0x66},
}

// lutBits is the number of leading bits used to index lut. It covers the
// longest prefix (8 bits) plus the 8 value bits of a "0" literal, so plain
// literals decode in one lookup.
const lutBits = 9

// lutEntry is a decoded prefix. For a plain literal, length is 9 and base is
// the byte; valueBits is then 0.
type lutEntry struct {
	kind      uint8
	length    uint8 // bits consumed by the lookup
	valueBits uint8 // value bits still to read
	base      uint32
}

// lut is computed once at package initialisation and never modified.
var lut = buildLUT()

func buildLUT() (t [1 << lutBits]lutEntry) {
	for _, tk := range specTokens {
		shift := lutBits - tk.prefixLen
		lo := tk.prefix << shift
		for i := uint32(0); i < 1<<shift; i++ {
			idx := lo | i
			e := lutEntry{kind: tk.kind, length: uint8(tk.prefixLen), valueBits: uint8(tk.valueBits), base: tk.base}
			if tk.kind == kindLiteral && tk.valueBits == 8 {
				e = lutEntry{kind: kindLiteral, length: lutBits, base: idx & 0xFF}
			}
			t[idx] = e
		}
	}
	return t
}
