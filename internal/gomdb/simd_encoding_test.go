package gomdb

import (
	"bytes"
	"math/rand"
	"testing"
	"unicode/utf8"
)

func scalarASCIIValid(body []byte) bool {
	for _, b := range body {
		if b == 0 || b >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func scalarASCIIRunLen(src []byte, start int) int {
	i := start
	for i < len(src) && src[i] != 0 && src[i] < utf8.RuneSelf {
		i++
	}
	return i
}

func scalarLatin1RunLen(src []byte, start int) int {
	i := start
	for i < len(src) && src[i] < utf8.RuneSelf {
		i++
	}
	return i
}

func scalarUTF16RunLen(src []byte, start int) int {
	i := start
	for i+1 < len(src) && src[i+1] == 0 && src[i] < utf8.RuneSelf {
		i += 2
	}
	return i
}

func scalarPackUTF16Low(dst, src []byte) []byte {
	if len(src) == 0 {
		return dst
	}
	dst = append(dst, src[0])
	for k := 2; k < len(src); k += 2 {
		dst = append(dst, src[k])
	}
	return dst
}

func scalarExpandC1(dst, src []byte) []byte {
	for _, b := range src {
		enc := utf8C1[b-0x80]
		dst = append(dst, byte(enc>>8), byte(enc))
	}
	return dst
}

// TestSimdKernelsParity checks every SIMD kernel against an independent
// scalar reference over randomized inputs, including all vector-chunk
// boundaries (0..159 bytes) and edge byte values.
func TestSimdKernelsParity(t *testing.T) {
	if !simdASCIIEnabled {
		t.Skip("SIMD kernels not compiled in or AVX2 unavailable (GOEXPERIMENT=simd + amd64 required)")
	}
	rng := rand.New(rand.NewSource(20260804))

	for iter := 0; iter < 400; iter++ {
		n := rng.Intn(160)
		buf := make([]byte, n)
		for i := range buf {
			switch r := rng.Intn(10); {
			case r < 7:
				buf[i] = byte(rng.Intn(0x7F))
			case r < 8:
				buf[i] = 0
			default:
				buf[i] = byte(0x80 + rng.Intn(0x80))
			}
		}

		if got, want := simdASCIIValid(buf), scalarASCIIValid(buf); got != want {
			t.Fatalf("simdASCIIValid(%x) = %v, want %v", buf, got, want)
		}
		for _, start := range []int{0, 1, 7, n / 3, n - 1} {
			if start < 0 || start >= n {
				continue
			}
			if got, want := simdASCIIRunLen(buf, start), scalarASCIIRunLen(buf, start); got != want {
				t.Fatalf("simdASCIIRunLen(%x, %d) = %d, want %d", buf, start, got, want)
			}
			if got, want := simdLatin1RunLen(buf, start), scalarLatin1RunLen(buf, start); got != want {
				t.Fatalf("simdLatin1RunLen(%x, %d) = %d, want %d", buf, start, got, want)
			}
		}
	}

	for iter := 0; iter < 400; iter++ {
		n := rng.Intn(80) * 2
		buf := make([]byte, n)
		for i := 0; i+1 < len(buf); i += 2 {
			if rng.Intn(10) < 7 {
				buf[i] = byte(rng.Intn(0x80))
				buf[i+1] = 0
			} else {
				buf[i] = byte(rng.Intn(256))
				buf[i+1] = byte(rng.Intn(256))
			}
		}
		// The kernel contract (like the original scalar helper) assumes an
		// even start: callers always resume from a unit boundary.
		for _, start := range []int{0, 2, 14, (n / 4) &^ 1, n &^ 1} {
			if start < 0 || start > len(buf) {
				continue
			}
			if got, want := simdUTF16RunLen(buf, start), scalarUTF16RunLen(buf, start); got != want {
				t.Fatalf("simdUTF16RunLen(%x, %d) = %d, want %d", buf, start, got, want)
			}
		}

		ascii := make([]byte, n)
		for i := 0; i+1 < len(ascii); i += 2 {
			ascii[i] = byte(rng.Intn(0x80))
			ascii[i+1] = 0
		}
		if got, want := simdPackUTF16Low(nil, ascii), scalarPackUTF16Low(nil, ascii); !bytes.Equal(got, want) {
			t.Fatalf("simdPackUTF16Low(%x) = %x, want %x", ascii, got, want)
		}
	}

	for iter := 0; iter < 400; iter++ {
		n := rng.Intn(80)
		buf := make([]byte, n)
		for i := range buf {
			buf[i] = byte(0x80 + rng.Intn(0x80))
		}
		if got, want := simdExpandC1(nil, buf), scalarExpandC1(nil, buf); !bytes.Equal(got, want) {
			t.Fatalf("simdExpandC1(%x) = %x, want %x", buf, got, want)
		}
	}

	for iter := 0; iter < 200; iter++ {
		n := rng.Intn(100)
		s := make([]byte, n)
		for i := range s {
			s[i] = byte(rng.Intn(256))
		}
		str := string(s)
		dst := make([]byte, len(str)*2)
		simdWidenASCII(str, dst)
		want := make([]byte, len(str)*2)
		for i := 0; i < len(str); i++ {
			want[i*2] = str[i]
		}
		if !bytes.Equal(dst, want) {
			t.Fatalf("simdWidenASCII(%q) = %x, want %x", str, dst, want)
		}
	}
}

// TestSimdEdgeCases pins the vector-chunk boundary values directly.
func TestSimdEdgeCases(t *testing.T) {
	if !simdASCIIEnabled {
		t.Skip("SIMD kernels not compiled in or AVX2 unavailable")
	}
	ascii := bytes.Repeat([]byte{'a'}, 128)
	if !simdASCIIValid(ascii) {
		t.Fatal("all-ASCII body rejected")
	}
	if got := simdASCIIRunLen(ascii, 0); got != len(ascii) {
		t.Fatalf("all-ASCII run length = %d, want %d", got, len(ascii))
	}

	for i := 0; i < 128; i++ {
		body := bytes.Repeat([]byte{'a'}, 128)
		body[i] = 0
		if simdASCIIValid(body) {
			t.Fatalf("zero at %d accepted", i)
		}
		if got := simdASCIIRunLen(body, 0); got != i {
			t.Fatalf("run length with zero at %d = %d, want %d", i, got, i)
		}
		body[i] = 0x80
		if simdASCIIValid(body) {
			t.Fatalf("high bit at %d accepted", i)
		}
		if got := simdASCIIRunLen(body, 0); got != i {
			t.Fatalf("run length with high byte at %d = %d, want %d", i, got, i)
		}
	}

	mkUnits := func() []byte {
		u := make([]byte, 128)
		for i := 0; i < 128; i += 2 {
			u[i] = 'x'
		}
		return u
	}
	if got := simdUTF16RunLen(mkUnits(), 0); got != 128 {
		t.Fatalf("all-ASCII UTF-16 run = %d, want 128", got)
	}
	for i := 0; i < 128; i += 2 {
		u := mkUnits()
		u[i] = 0x80
		if got := simdUTF16RunLen(u, 0); got != i {
			t.Fatalf("UTF-16 run with bad low byte at %d = %d, want %d", i, got, i)
		}
		u = mkUnits()
		u[i+1] = 1
		if got := simdUTF16RunLen(u, 0); got != i {
			t.Fatalf("UTF-16 run with bad high byte at %d = %d, want %d", i+1, got, i)
		}
		if got := simdPackUTF16Low(nil, u[:i]); len(got) != i/2 {
			t.Fatalf("packed prefix length at %d = %d, want %d", i, len(got), i/2)
		}
	}

	for _, n := range []int{8, 15, 16, 17, 31, 32, 33, 64} {
		c1 := make([]byte, n)
		for i := range c1 {
			c1[i] = byte(0x80 + i)
		}
		if got, want := simdExpandC1(nil, c1), scalarExpandC1(nil, c1); !bytes.Equal(got, want) {
			t.Fatalf("C1 expansion mismatch for n=%d", n)
		}
	}
}

func benchASCIIData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

func BenchmarkSimdASCIIValid(b *testing.B) {
	if !simdASCIIEnabled {
		b.Skip("SIMD kernels not compiled in or AVX2 unavailable")
	}
	data := benchASCIIData(4096)
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		if !simdASCIIValid(data) {
			b.Fatal("unexpected result")
		}
	}
}

func BenchmarkScalarASCIIValid(b *testing.B) {
	data := benchASCIIData(4096)
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		if !scalarASCIIValid(data) {
			b.Fatal("unexpected result")
		}
	}
}

func BenchmarkSimdPackUTF16Low(b *testing.B) {
	if !simdASCIIEnabled {
		b.Skip("SIMD kernels not compiled in or AVX2 unavailable")
	}
	src := make([]byte, 4096)
	for i := 0; i < len(src); i += 2 {
		src[i] = byte('a' + (i/2)%26)
	}
	var dst []byte
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		dst = simdPackUTF16Low(dst[:0], src)
	}
}

func BenchmarkScalarPackUTF16Low(b *testing.B) {
	src := make([]byte, 4096)
	for i := 0; i < len(src); i += 2 {
		src[i] = byte('a' + (i/2)%26)
	}
	var dst []byte
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		dst = scalarPackUTF16Low(dst[:0], src)
	}
}

func BenchmarkSimdExpandC1(b *testing.B) {
	if !simdASCIIEnabled {
		b.Skip("SIMD kernels not compiled in or AVX2 unavailable")
	}
	src := make([]byte, 2048)
	for i := range src {
		src[i] = byte(0x80 + i%0x80)
	}
	var dst []byte
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		dst = simdExpandC1(dst[:0], src)
	}
}

func BenchmarkScalarExpandC1(b *testing.B) {
	src := make([]byte, 2048)
	for i := range src {
		src[i] = byte(0x80 + i%0x80)
	}
	var dst []byte
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		dst = scalarExpandC1(dst[:0], src)
	}
}
