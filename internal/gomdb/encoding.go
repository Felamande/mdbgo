package gomdb

import (
	"unicode/utf16"
)

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

// ucs2ToUTF8 converts a UCS-2LE encoded byte slice to a UTF-8 string.
func ucs2ToUTF8(src []byte) string {
	if len(src) == 0 {
		return ""
	}

	// Convert bytes to []uint16 (UCS-2 code units)
	n := len(src) / 2
	u16 := make([]uint16, n)
	for i := 0; i < n; i++ {
		u16[i] = uint16(src[i*2]) | uint16(src[i*2+1])<<8
	}

	// Decode UTF-16 (UCS-2 is a subset of UTF-16, but handle surrogates too)
	runes := utf16.Decode(u16)
	return string(runes)
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

	var decompressed []byte

	if isJet4 && len(src) >= 2 && src[0] == 0xFF && src[1] == 0xFE {
		// Unicode Compressed format (Jet4)
		decompressed = decompressUnicode(src[2:])
	} else if isJet4 {
		// Uncompressed UCS-2LE (Jet4)
		decompressed = src
	} else {
		// Jet3: single-byte encoding (typically Windows-1252 / Latin-1)
		// For now, treat as Latin-1 → UTF-8
		return latin1ToUTF8(src)
	}

	return ucs2ToUTF8(decompressed)
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
	runes := make([]rune, len(src))
	for i, b := range src {
		runes[i] = rune(b)
	}
	return string(runes)
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

// ASCIItoUCS2 converts an ASCII/UTF-8 string to UCS-2LE bytes, used for temp table
// field packing in LIST TABLES and DESCRIBE TABLE commands.
func ASCIItoUCS2(src string) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src)*2)
	for i, r := range src {
		// Only handle ASCII range for temp table usage
		if r < 256 {
			dst[i*2] = byte(r)
			dst[i*2+1] = 0
		}
	}
	return dst
}

// ASCIItoUCS2Len is like ASCIItoUCS2 but takes a byte slice and returns
// UCS-2LE bytes along with their length.
func ASCIItoUCS2Len(src []byte) ([]byte, int) {
	result := ASCIItoUCS2(string(src))
	return result, len(result)
}
