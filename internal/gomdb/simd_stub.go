//go:build !goexperiment.simd || !amd64

package gomdb

// simdASCIIEnabled is false when the binary was built without
// GOEXPERIMENT=simd or for a non-amd64 target. Callers keep the scalar
// paths, so the kernel stubs below are never executed.
const simdASCIIEnabled = false

func simdASCIIValid(body []byte) bool { return false }

func simdASCIIRunLen(src []byte, start int) int { return start }

func simdLatin1RunLen(src []byte, start int) int { return start }

func simdUTF16RunLen(src []byte, start int) int { return start }

func simdPackUTF16Low(dst, src []byte) []byte { return dst }

func simdExpandC1(dst, src []byte) []byte { return dst }

func simdWidenASCII(src string, dst []byte) {}
