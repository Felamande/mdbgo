package gomdb

import (
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
	for _, b := range body {
		if b == 0 || b >= utf8.RuneSelf {
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
	default:
		dst = utf8.AppendRune(dst, rune(unit))
	}
	return dst
}

func appendUTF16LE(dst, src []byte) []byte {
	var pendingHigh uint16
	i := 0
	for i+1 < len(src) {
		// Bulk-copy runs of ASCII units (high byte 0x00, low byte below
		// RuneSelf); those cannot interact with a pending surrogate either.
		if src[i+1] == 0 && src[i] < utf8.RuneSelf && pendingHigh == 0 {
			j := i + 2
			for j+1 < len(src) && src[j+1] == 0 && src[j] < utf8.RuneSelf {
				j += 2
			}
			for ; i < j; i += 2 {
				dst = append(dst, src[i])
			}
			continue
		}
		unit := uint16(src[i]) | uint16(src[i+1])<<8
		dst = appendUTF16Unit(dst, &pendingHigh, unit)
		i += 2
	}
	if pendingHigh != 0 {
		dst = utf8.AppendRune(dst, utf8.RuneError)
	}
	return dst
}

func appendCompressedUnicode(dst, src []byte) []byte {
	compressed := true
	var pendingHigh uint16
	i := 0
	for i < len(src) {
		if src[i] == 0 {
			compressed = !compressed
			i++
			continue
		}
		if compressed {
			// Process the run of bytes up to the next mode toggle at once.
			j := i
			for j < len(src) && src[j] != 0 {
				j++
			}
			// Compressed bytes decode to U+00xx, which can never complete a
			// pending surrogate pair, so bulk-copy the ASCII prefix of the
			// run (each byte is its own UTF-8 character).
			if pendingHigh == 0 {
				k := i
				for k < j && src[k] < utf8.RuneSelf {
					k++
				}
				dst = append(dst, src[i:k]...)
				i = k
			}
			for ; i < j; i++ {
				dst = appendUTF16Unit(dst, &pendingHigh, uint16(src[i]))
			}
			continue
		}
		if i+1 >= len(src) {
			break
		}
		unit := uint16(src[i]) | uint16(src[i+1])<<8
		dst = appendUTF16Unit(dst, &pendingHigh, unit)
		i += 2
	}
	if pendingHigh != 0 {
		dst = utf8.AppendRune(dst, utf8.RuneError)
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

func unicodeUTF8Capacity(src []byte, isJet4 bool) int {
	if !isJet4 {
		return len(src) * 2
	}
	if len(src) >= 2 && src[0] == 0xff && src[1] == 0xfe {
		return (len(src) - 2) * 2
	}
	return (len(src) / 2) * 3
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
