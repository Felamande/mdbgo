//go:build goexperiment.simd && amd64

package gomdb

import (
	"math/bits"
	"unicode/utf8"
	"unsafe"

	"simd/archsimd"
)

// AVX2 decode kernels.
//
// Every archsimd operation used below compiles to SSE2/SSSE3/AVX/AVX2
// instructions (VPSHUFB, VPTEST, VPMOVMSKB, VPMOVZXBW, VPSRLW/VPSLLW,
// VPALIGNR, VPAND/VPOR/VPXOR, VPSUBB, VMOVDQU), so the kernels are safe on
// any AVX2-capable CPU. Operations like Uint8x32.Permute (VPERMB) and
// Uint16x16.TruncateToUint8 (VPMOVWB) require AVX-512 and are deliberately
// avoided; building with GOEXPERIMENT=simd does not require AVX-512.
var simdASCIIEnabled = archsimd.X86.AVX2()

var (
	simdMSB8   = archsimd.BroadcastUint8x32(0x80)
	simdOnes8  = archsimd.BroadcastUint8x32(0x01)
	simdZero8  = archsimd.Uint8x32{}
	simdZero16 = archsimd.Uint8x16{}

	// simdUTF16Bad marks, per UTF-16LE unit, the low byte's high bit (0x80)
	// and the whole high byte (0xFF). A unit is ASCII iff neither fires.
	simdUTF16Bad = func() archsimd.Uint8x32 {
		var p [32]byte
		for i := range p {
			if i&1 == 0 {
				p[i] = 0x80
			} else {
				p[i] = 0xFF
			}
		}
		return archsimd.LoadUint8x32(&p)
	}()

	// simdUTF16PackIdx packs the low bytes of 16 UTF-16LE units into the
	// first 16 bytes of the result: lane 0 contributes units 0-7, lane 1
	// units 8-15 (in-lane VPSHUFB indices).
	simdUTF16PackIdx = func() archsimd.Int8x32 {
		var p [32]byte
		for i := 0; i < 8; i++ {
			p[i] = byte(2 * i)
			p[16+i] = byte(2 * i)
			p[8+i] = 0xFF
			p[24+i] = 0xFF
		}
		return archsimd.LoadUint8x32(&p).AsInt8x32()
	}()

	simdC1Hi8   = archsimd.BroadcastUint16x8(0x00C0)
	simdC1Mask8 = archsimd.BroadcastUint16x8(0x003F)
	simdC1Or8   = archsimd.BroadcastUint16x8(0x0080)

	simdC1Hi   = archsimd.BroadcastUint16x16(0x00C0)
	simdC1Mask = archsimd.BroadcastUint16x16(0x003F)
	simdC1Or   = archsimd.BroadcastUint16x16(0x0080)

	simdZeroBytes32 [32]byte
	simdZeroBytes16 [16]byte
)

// simdASCIIValid reports whether every byte is non-zero and below RuneSelf.
// body must be at least 32 bytes; the tail is checked with the scalar loop.
func simdASCIIValid(body []byte) bool {
	i := 0
	for ; i+32 <= len(body); i += 32 {
		v := archsimd.LoadUint8x32Slice(body[i:])
		hi := v.And(simdMSB8)
		zero := v.Sub(simdOnes8).And(v.Not()).And(simdMSB8)
		if !hi.Or(zero).IsZero() {
			return false
		}
	}
	for ; i < len(body); i++ {
		if body[i] == 0 || body[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// simdASCIIRunLen returns the length of the run of non-zero bytes below
// RuneSelf starting at src[start:].
func simdASCIIRunLen(src []byte, start int) int {
	i := start
	for ; i+32 <= len(src); i += 32 {
		v := archsimd.LoadUint8x32Slice(src[i:])
		t := v.And(simdMSB8).Or(v.Sub(simdOnes8).And(v.Not()).And(simdMSB8))
		if !t.IsZero() {
			bad := t.NotEqual(simdZero8).ToBits()
			return i + bits.TrailingZeros32(bad)
		}
	}
	for ; i < len(src) && src[i] != 0 && src[i] < utf8.RuneSelf; i++ {
	}
	return i
}

// simdLatin1RunLen returns the length of the run of bytes below RuneSelf
// (zeros are allowed) starting at src[start:].
func simdLatin1RunLen(src []byte, start int) int {
	i := start
	for ; i+32 <= len(src); i += 32 {
		v := archsimd.LoadUint8x32Slice(src[i:])
		t := v.And(simdMSB8)
		if !t.IsZero() {
			bad := t.NotEqual(simdZero8).ToBits()
			return i + bits.TrailingZeros32(bad)
		}
	}
	for ; i < len(src) && src[i] < utf8.RuneSelf; i++ {
	}
	return i
}

// simdUTF16RunLen returns the even byte offset at which the run of ASCII
// UTF-16LE units (high byte 0, low byte below RuneSelf) ends.
func simdUTF16RunLen(src []byte, start int) int {
	i := start
	for ; i+32 <= len(src); i += 32 {
		v := archsimd.LoadUint8x32Slice(src[i:])
		t := v.And(simdUTF16Bad)
		if !t.IsZero() {
			bad := t.NotEqual(simdZero8).ToBits()
			return (i + bits.TrailingZeros32(bad)) &^ 1
		}
	}
	for i+1 < len(src) && src[i+1] == 0 && src[i] < utf8.RuneSelf {
		i += 2
	}
	return i
}

// simdPackUTF16Low appends the low bytes of an even-length UTF-16LE ASCII
// run (one byte per unit) to dst.
func simdPackUTF16Low(dst, src []byte) []byte {
	i := 0
	for ; i+32 <= len(src); i += 32 {
		p := archsimd.LoadUint8x32Slice(src[i:]).PermuteOrZeroGrouped(simdUTF16PackIdx)
		packed := p.GetLo().Or(p.GetHi().ConcatShiftBytesRight(8, simdZero16))
		dst = append(dst, simdZeroBytes16[:]...)
		packed.StoreSlice(dst[len(dst)-16:])
	}
	for ; i+1 < len(src); i += 2 {
		dst = append(dst, src[i])
	}
	return dst
}

// simdExpandC1 appends the UTF-8 form of a run of compressed bytes
// 0x80..0xFF (each becomes a fixed 2-byte U+0080..U+00FF sequence).
func simdExpandC1(dst, src []byte) []byte {
	i := 0
	for ; i+16 <= len(src); i += 16 {
		u := archsimd.LoadUint8x16Slice(src[i:]).ExtendToUint16()
		dst = append(dst, simdZeroBytes32[:]...)
		simdEncodeC1(u).StoreSlice(unsafe.Slice((*uint16)(unsafe.Pointer(&dst[len(dst)-32])), 16))
	}
	for ; i+8 <= len(src); i += 8 {
		u := archsimd.LoadUint8x16SlicePart(src[i : i+8]).ExtendLo8ToUint16()
		dst = append(dst, simdZeroBytes16[:]...)
		simdEncodeC18(u).StoreSlice(unsafe.Slice((*uint16)(unsafe.Pointer(&dst[len(dst)-16])), 8))
	}
	for ; i < len(src); i++ {
		enc := utf8C1[src[i]-0x80]
		dst = append(dst, byte(enc>>8), byte(enc))
	}
	return dst
}

func simdEncodeC1(u archsimd.Uint16x16) archsimd.Uint16x16 {
	hi := u.ShiftAllRight(6).Or(simdC1Hi)
	lo := u.And(simdC1Mask).Or(simdC1Or).ShiftAllLeft(8)
	return hi.Or(lo)
}

func simdEncodeC18(u archsimd.Uint16x8) archsimd.Uint16x8 {
	hi := u.ShiftAllRight(6).Or(simdC1Hi8)
	lo := u.And(simdC1Mask8).Or(simdC1Or8).ShiftAllLeft(8)
	return hi.Or(lo)
}

// simdWidenASCII writes src's bytes as zero-extended UCS-2LE uint16s into
// dst, which must have len(src)*2 bytes.
func simdWidenASCII(src string, dst []byte) {
	i := 0
	for ; i+8 <= len(src); i += 8 {
		u := archsimd.LoadUint8x16SlicePart(stringBytes(src[i : i+8])).ExtendLo8ToUint16()
		u.StoreSlice(unsafe.Slice((*uint16)(unsafe.Pointer(&dst[i*2])), 8))
	}
	for ; i < len(src); i++ {
		dst[i*2] = src[i]
	}
}

func stringBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
