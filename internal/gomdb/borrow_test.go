package gomdb

import (
	"runtime"
	"strings"
	"testing"
)

// TestBorrowStringParity checks that borrowString/unicodeBorrow produce the
// same values as the copying variants for the same inputs, and that the
// fast-scan decode path yields identical text to the synchronous path.
func TestBorrowStringParity(t *testing.T) {
	cases := []string{
		"",
		"a",
		"hello world",
		"OID-12345-ABCD",
		strings.Repeat("x", 100),
		"nul\x00terminated",
		"caf\u00e9",
		"\u4e2d\u6587",
		"mixed \u00e9\u4e2d ascii",
	}
	for _, want := range cases {
		// Fully-compressed ASCII encoding: 0xFF 0xFE + body.
		if strings.IndexByte(want, 0) < 0 && isASCIIString(want) {
			body := []byte(want)
			src := make([]byte, 2, 2+len(body))
			src[0], src[1] = 0xFF, 0xFE
			src = append(src, body...)
			var s decodeScratch
			if got := unicodeBorrow(src, true, &s); got != want {
				t.Fatalf("unicodeBorrow(%q) = %q, want %q", want, got, want)
			}
			if got := unicodeScratch(src, true, &s); got != want {
				t.Fatalf("unicodeScratch(%q) = %q, want %q", want, got, want)
			}
		}
		// Uncompressed UTF-16LE and compressed non-ASCII go through the
		// scratch-backed branch; both variants must agree.
		u := make([]byte, 0, len(want)*2)
		for _, r := range want {
			u = append(u, byte(r), byte(r>>8))
		}
		var s decodeScratch
		if got := unicodeBorrow(u, true, &s); got != want {
			t.Fatalf("unicodeBorrow(utf16 %q) = %q, want %q", want, got, want)
		}
		if got := unicodeScratch(u, true, &s); got != want {
			t.Fatalf("unicodeScratch(utf16 %q) = %q, want %q", want, got, want)
		}
	}
}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// TestBorrowStringGCKeepAlive verifies that a string borrowed from a dropped
// backing array stays intact across GC cycles and allocation churn. This is
// the property that makes zero-copy strings from the file cache safe after
// cache eviction or connection close.
func TestBorrowStringGCKeepAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory-churn test in -short mode")
	}
	const size = 8 << 20
	var s string
	func() {
		b := make([]byte, size)
		for i := range b {
			b[i] = byte(i)
		}
		s = borrowString(b)
	}()
	for i := 0; i < 8; i++ {
		runtime.GC()
		_ = make([]byte, size)
	}
	for i := 0; i < len(s); i++ {
		if s[i] != byte(i) {
			t.Fatalf("borrowed string corrupted at %d", i)
		}
	}
}
