//go:build amd64

package bert

import "golang.org/x/sys/cpu"

// matMulAVX2 is the AVX2+FMA3 assembly kernel declared in matmul_amd64.s.
// It computes the (M & ~3) x (N & ~3) 4x4-tiled body of C = A * B^T using
// 256-bit YMM loads and VFMADD231PS. Remainder rows/columns are not computed
// and must be handled by the caller.
//
// Baseline: AVX2 + FMA3 (Haswell 2013+, AMD Excavator 2015+ / Zen 2017+).
// Any amd64 hardware deployed today supports this; the runtime CPU-feature
// gate in MatMul below falls through to the pure-Go path on pre-Haswell
// silicon (e.g., Ivy Bridge laptops from 2012) or under Apple Rosetta 2,
// which translates x86 but doesn't expose AVX2.
//
//go:noescape
func matMulAVX2(a, bT, out []float32, M, K, N int)

// useAVX2 is set at init from runtime CPU feature detection.
var useAVX2 = cpu.X86.HasAVX2 && cpu.X86.HasFMA

// matMulSIMDMinSize is the minimum M*N below which SIMD dispatch overhead
// exceeds the benefit. Matches the arm64 constant.
const matMulSIMDMinSize = 16

// MatMul computes C = A * B^T where A is [M, K] and bT is [N, K] (transposed).
// Output is written to out [M, N] which must be pre-allocated and zeroed.
//
// On amd64 with AVX2 + FMA3, dispatches the 4x4 tile body to an assembly
// kernel for ~4-6x speedup over pure Go on BERT-sized matrices. Falls back
// to the pure-Go tiled implementation for matrices too small to benefit,
// for remainder rows/columns, and for pre-Haswell/Rosetta hosts without
// AVX2.
func MatMul(a, bT []float32, M, K, N int, out []float32) {
	if M <= 0 || K <= 0 || N <= 0 {
		return
	}
	if len(a) < M*K || len(bT) < N*K || len(out) < M*N {
		panic("bert.MatMul: slice too short")
	}

	mBody := M &^ 3
	nBody := N &^ 3

	// Size too small or CPU lacks AVX2/FMA3: pure-Go path.
	if !useAVX2 || mBody < 4 || nBody < 4 || M*N < matMulSIMDMinSize {
		matMulGeneric(a, bT, M, K, N, out)
		return
	}

	// Assembly kernel computes the [mBody x nBody] tile body.
	matMulAVX2(a, bT, out, M, K, N)

	// Remainder columns (N % 4) for tiled rows.
	if nTail := N & 3; nTail > 0 {
		for i := 0; i < mBody; i++ {
			ai := a[i*K : (i+1)*K]
			for j := nBody; j < N; j++ {
				bj := bT[j*K : (j+1)*K]
				var s float32
				for k := 0; k < K; k++ {
					s += ai[k] * bj[k]
				}
				out[i*N+j] = s
			}
		}
	}

	// Remainder rows (M % 4).
	if mTail := M & 3; mTail > 0 {
		for i := mBody; i < M; i++ {
			ai := a[i*K : (i+1)*K]
			for j := 0; j < N; j++ {
				bj := bT[j*K : (j+1)*K]
				var s float32
				for k := 0; k < K; k++ {
					s += ai[k] * bj[k]
				}
				out[i*N+j] = s
			}
		}
	}
}
