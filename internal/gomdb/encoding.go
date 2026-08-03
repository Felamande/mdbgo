package gomdb

import (
	"encoding/binary"
	"unicode/utf16"
	"unicode/utf8"
)

// unicodeFastPath reports whether src is a Jet4 fully-compressed ASCII string,
// i.e. a 0xFF 0xFE prefix followed by bytes that are all non-zero and below
// RuneSelf. Such text decodes to the body itself, so callers can copy it
// directly (or string() it with a single allocation) instead of running the
// per-byte decode loop.
func unicodeFastPath(src []byte, isJet4 bool) ([]byte, bool) {
	if !isJet4 || len(src) < 2 || src[0] != 0xff || src[1] != 0xfe {
		return nil, false
	}
	body := src[2:]
	// Check eight bytes at a time: no byte may have the high bit set (>= 0x80)
	// and no byte may be zero. The second test is the classic haszero word
	// trick; it borrows from the top bit, which is already excluded above.
	const msb = 0x8080808080808080
	const ones = 0x0101010101010101
	i := 0
	for ; i+8 <= len(body); i += 8 {
		w := binary.LittleEndian.Uint64(body[i:])
		if w&msb != 0 || (w-ones)&^w&msb != 0 {
			return nil, false
		}
	}
	for ; i < len(body); i++ {
		if body[i] == 0 || body[i] >= utf8.RuneSelf {
			return nil, false
		}
	}
	return body, true
}

// decompressUnicode decompresses an Access "Unicode Compressed" string.
// Access uses a run-length-like compression where:
// - A 0x00 byte toggles between compressed and uncompressed mode
// - In compressed mode: one byte at a time, with implicit 0x00 high byte
// - In uncompressed mode: two bytes at a time (full UCS-2)
// The input src does NOT include the 0xFF 0xFE prefix — those are stripped by the caller.
//
// Returns the decompressed UCS-2LE bytes.
func decompressUnicode(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}

	// Worst case: every byte expands to 2 (all compressed single bytes)
	dst := make([]byte, 0, len(src)*2)

	compress := true // start in compressed mode
	i := 0
	for i < len(src) {
		if src[i] == 0x00 {
			// Toggle compression mode
			compress = !compress
			i++
		} else if compress {
			// One byte → two bytes (low byte = data, high byte = 0x00)
			dst = append(dst, src[i], 0x00)
			i++
		} else if i+1 < len(src) {
			// Two bytes → copied as-is
			dst = append(dst, src[i], src[i+1])
			i += 2
		} else {
			// Odd number of bytes — shouldn't happen, but handle gracefully
			break
		}
	}

	return dst
}

func appendUTF16Unit(dst []byte, pendingHigh *uint16, unit uint16) []byte {
	if *pendingHigh != 0 {
		if unit >= 0xdc00 && unit <= 0xdfff {
			dst = utf8.AppendRune(dst, utf16.DecodeRune(rune(*pendingHigh), rune(unit)))
			*pendingHigh = 0
			return dst
		}
		dst = utf8.AppendRune(dst, utf8.RuneError)
		*pendingHigh = 0
	}

	switch {
	case unit >= 0xd800 && unit <= 0xdbff:
		*pendingHigh = unit
	case unit >= 0xdc00 && unit <= 0xdfff:
		dst = utf8.AppendRune(dst, utf8.RuneError)
	case unit < 0x80:
		dst = append(dst, byte(unit))
	case unit < 0x800:
		dst = append(dst, 0xc0|byte(unit>>6), 0x80|byte(unit&0x3f))
	default:
		dst = append(dst,
			0xe0|byte(unit>>12),
			0x80|byte((unit>>6)&0x3f),
			0x80|byte(unit&0x3f))
	}
	return dst
}

// utf8C1 maps compressed bytes 0x80..0xFF to their fixed 2-byte UTF-8
// encoding (U+0080..U+00FF), stored big-endian in a uint16.
var utf8C1 = func() (t [128]uint16) {
	for b := 0; b < 128; b++ {
		u := 0x80 + b
		t[b] = uint16(0xc0|(u>>6))<<8 | uint16(0x80|(u&0x3f))
	}
	return t
}()

func appendUTF16LE(dst, src []byte) []byte {
	var pendingHigh uint16
	return appendUTF16LEState(dst, src, &pendingHigh)
}

// appendUTF16LEState decodes UTF-16LE units, carrying a pending high
// surrogate across chunk boundaries (multi-page memo streams).
func appendUTF16LEState(dst, src []byte, pendingHigh *uint16) []byte {
	i := 0
	for i+1 < len(src) {
		// Bulk-copy runs of ASCII units (high byte 0x00, low byte below
		// RuneSelf); those cannot interact with a pending surrogate either.
		if src[i+1] == 0 && src[i] < utf8.RuneSelf && *pendingHigh == 0 {
			j := asciiUTF16RunLen(src, i)
			// Copy the low bytes of the ASCII units.
			low := src[i:j:j]
			dst = append(dst, low[0])
			for k := 2; k < len(low); k += 2 {
				dst = append(dst, low[k])
			}
			i = j
			continue
		}
		unit := uint16(src[i]) | uint16(src[i+1])<<8
		dst = appendUTF16Unit(dst, pendingHigh, unit)
		i += 2
	}
	if *pendingHigh != 0 {
		dst = utf8.AppendRune(dst, utf8.RuneError)
		*pendingHigh = 0
	}
	return dst
}

// asciiUTF16RunLen returns the end of a run of UTF-16LE units whose high byte
// is 0 and whose low byte is below RuneSelf, checked eight bytes at a time.
func asciiUTF16RunLen(src []byte, start int) int {
	i := start
	for i+8 <= len(src) {
		w := binary.LittleEndian.Uint64(src[i:])
		// Odd bytes (high bytes) must be zero; even bytes (low bytes) must
		// not have the high bit set.
		if w&0xFF80FF80FF80FF80 != 0 {
			break
		}
		i += 8
	}
	for i+1 < len(src) && src[i+1] == 0 && src[i] < utf8.RuneSelf {
		i += 2
	}
	return i
}

// asciiRunLen returns the end of a run of bytes that are neither zero nor
// >= RuneSelf, checked eight bytes at a time.
func asciiRunLen(src []byte, start int) int {
	i := start
	for i+8 <= len(src) {
		w := binary.LittleEndian.Uint64(src[i:])
		const msb = 0x8080808080808080
		const ones = 0x0101010101010101
		if w&msb != 0 || (w-ones)&^w&msb != 0 {
			break
		}
		i += 8
	}
	for i < len(src) && src[i] != 0 && src[i] < utf8.RuneSelf {
		i++
	}
	return i
}

func appendCompressedUnicode(dst, src []byte) []byte {
	st := unicodeChunkState{compressed: true}
	return appendCompressedUnicodeState(dst, src, &st)
}

// unicodeChunkState carries compression mode and a pending surrogate across
// chunks of a single multi-page memo stream.
type unicodeChunkState struct {
	compressed  bool
	pendingHigh uint16
}

func appendCompressedUnicodeState(dst, src []byte, st *unicodeChunkState) []byte {
	i := 0
	for i < len(src) {
		if src[i] == 0 {
			st.compressed = !st.compressed
			i++
			continue
		}
		if st.compressed {
			// An unpaired high surrogate is resolved before consuming the
			// next compressed unit, mirroring the per-unit decoder.
			if st.pendingHigh != 0 {
				dst = utf8.AppendRune(dst, utf8.RuneError)
				st.pendingHigh = 0
				continue
			}
			// Compressed bytes decode to U+00xx, which can never complete a
			// pending surrogate pair, so copy the ASCII prefix of the run in
			// bulk (each byte is its own UTF-8 character), then encode the
			// non-ASCII bytes directly.
			k := asciiRunLen(src, i)
			dst = append(dst, src[i:k]...)
			i = k
			if i >= len(src) {
				continue
			}
			if src[i] == 0 {
				st.compressed = !st.compressed
				i++
				continue
			}
			// Process the whole run of non-ASCII compressed bytes at once:
			// each encodes to a fixed 2-byte UTF-8 sequence.
			j := i
			for j < len(src) && src[j] >= 0x80 {
				j++
			}
			for ; i < j; i++ {
				enc := utf8C1[src[i]-0x80]
				dst = append(dst, byte(enc>>8), byte(enc))
			}
			continue
		}
		if i+1 >= len(src) {
			break
		}
		unit := uint16(src[i]) | uint16(src[i+1])<<8
		dst = appendUTF16Unit(dst, &st.pendingHigh, unit)
		i += 2
	}
	if st.pendingHigh != 0 {
		dst = utf8.AppendRune(dst, utf8.RuneError)
		st.pendingHigh = 0
	}
	return dst
}

func appendLatin1UTF8(dst, src []byte) []byte {
	for _, b := range src {
		if b < utf8.RuneSelf {
			dst = append(dst, b)
		} else {
			dst = utf8.AppendRune(dst, rune(b))
		}
	}
	return dst
}

func appendUnicodeUTF8(dst, src []byte, isJet4 bool) []byte {
	if !isJet4 {
		return appendLatin1UTF8(dst, src)
	}
	if body, ok := unicodeFastPath(src, isJet4); ok {
		return append(dst, body...)
	}
	if len(src) >= 2 && src[0] == 0xff && src[1] == 0xfe {
		return appendCompressedUnicode(dst, src[2:])
	}
	return appendUTF16LE(dst, src)
}

// appendUnicodeChunk appends the UTF-8 form of one chunk of a multi-page
// memo stream. The first chunk may carry the 0xFF 0xFE compression prefix;
// compression mode and surrogate state continue across chunks.
func appendUnicodeChunk(dst, src []byte, first bool, isJet4 bool, st *unicodeChunkState) []byte {
	if !isJet4 {
		return appendLatin1UTF8(dst, src)
	}
	if first && len(src) >= 2 && src[0] == 0xff && src[1] == 0xfe {
		src = src[2:]
		st.compressed = true
	} else if first {
		return appendUTF16LEState(dst, src, &st.pendingHigh)
	}
	if st.compressed {
		return appendCompressedUnicodeState(dst, src, st)
	}
	return appendUTF16LEState(dst, src, &st.pendingHigh)
}

func unicodeUTF8Capacity(src []byte, isJet4 bool) int {
	if !isJet4 {
		return len(src) * 2
	}
	if len(src) >= 2 && src[0] == 0xff && src[1] == 0xfe {
		return (len(src) - 2) * 2
	}
	return (len(src) / 2) * 3
}

// unicodeScratch converts src to a UTF-8 string using caller-owned scratch.
// The returned string owns its bytes (string() copies), so the scratch may
// be reused immediately.
func unicodeScratch(src []byte, isJet4 bool, s *decodeScratch) string {
	if len(src) == 0 {
		return ""
	}
	if body, ok := unicodeFastPath(src, isJet4); ok {
		return string(body)
	}
	return unicodeScratchSlow(src, isJet4, s)
}

// unicodeScratchSlow decodes src into the scratch buffer and copies it to a
// string, skipping the fast-path check (callers that already ruled it out).
func unicodeScratchSlow(src []byte, isJet4 bool, s *decodeScratch) string {
	s.unicode = appendUnicodeUTF8(s.unicode[:0], src, isJet4)
	return string(s.unicode)
}

// ucs2ToUTF8 converts a UCS-2LE encoded byte slice to a UTF-8 string.
func ucs2ToUTF8(src []byte) string {
	if len(src) == 0 {
		return ""
	}
	dst := make([]byte, 0, (len(src)/2)*3)
	return string(appendUTF16LE(dst, src))
}

// UnicodeToUTF8 converts an Access string (possibly Unicode Compressed) to a UTF-8 string.
// For Jet4 databases, the text may be prefixed with 0xFF 0xFE to indicate compression.
// For Jet3 databases, text is typically single-byte in the database code page.
//
// Parameters:
//   - src: raw byte data from the database
//   - isJet4: true if Jet4 database, false if Jet3
//
// Returns the UTF-8 string.
func UnicodeToUTF8(src []byte, isJet4 bool) string {
	if len(src) == 0 {
		return ""
	}

	dst := make([]byte, 0, unicodeUTF8Capacity(src, isJet4))
	return string(appendUnicodeUTF8(dst, src, isJet4))
}

func (mdb *MdbHandle) unicodeToUTF8(src []byte) string {
	if len(src) == 0 {
		return ""
	}
	if body, ok := unicodeFastPath(src, mdb.IsJet4()); ok {
		// The UTF-8 form of a fully-compressed ASCII string is the body
		// itself; string() copies it directly, skipping the scratch buffer.
		return string(body)
	}
	mdb.unicodeBuf = appendUnicodeUTF8(mdb.unicodeBuf[:0], src, mdb.IsJet4())
	return string(mdb.unicodeBuf)
}

// UnicodeToUTF8Len converts bytes and returns both the UTF-8 string and its byte length.
func UnicodeToUTF8Len(src []byte, isJet4 bool) (string, int) {
	s := UnicodeToUTF8(src, isJet4)
	return s, len(s)
}

// latin1ToUTF8 converts a Latin-1 (ISO-8859-1) encoded byte slice to a UTF-8 string.
// Characters 0x00-0x7F are single bytes in UTF-8.
// Characters 0x80-0xFF are encoded as 2-byte UTF-8 sequences.
func latin1ToUTF8(src []byte) string {
	dst := make([]byte, 0, len(src)*2)
	return string(appendLatin1UTF8(dst, src))
}

// Jet3CodePageToUTF8 converts a Jet3 string using the specified code page to UTF-8.
// Currently supports common code pages. For unsupported code pages, falls back to Latin-1.
//
// In a full implementation, this would use golang.org/x/text/encoding/charmap.
// For now we handle the most common case: code page 1252 (Windows Latin-1).
func Jet3CodePageToUTF8(src []byte, codePage uint16) string {
	switch codePage {
	case 1252, 0:
		// Windows-1252 — most common for Jet3 English databases
		// For ASCII range it's identical to Latin-1
		return latin1ToUTF8(src)
	default:
		// Fallback to Latin-1 for unknown code pages
		return latin1ToUTF8(src)
	}
}

// ASCIItoUCS2 widens each source byte to UCS-2LE, matching mdbtools' legacy
// temp-table conversion used by LIST TABLES and DESCRIBE TABLE. This is
// intentionally byte-oriented rather than a UTF-8-to-UTF-16 conversion.
func ASCIItoUCS2(src string) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src)*2)
	for i := 0; i < len(src); i++ {
		dst[i*2] = src[i]
	}
	return dst
}

// ASCIItoUCS2Len is like ASCIItoUCS2 but takes a byte slice and returns
// UCS-2LE bytes along with their length.
func ASCIItoUCS2Len(src []byte) ([]byte, int) {
	result := ASCIItoUCS2(string(src))
	return result, len(result)
}
