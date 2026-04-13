//go:build arm64

package bert

// matMulNEON is the NEON assembly kernel declared in matmul_arm64.s.
// It computes the (M & ~3) x (N & ~3) 4x4-tiled body of C = A * B^T.
// Remainder rows/columns are not computed and must be handled by the caller.
//
//go:noescape
func matMulNEON(a, bT, out []float32, M, K, N int)

// matMulSIMDMinSize is the minimum dimension product below which the overhead
// of SIMD dispatch exceeds the benefit. For very small matrices, the pure Go
// tiled implementation is faster due to no function call overhead.
const matMulSIMDMinSize = 16

// MatMul computes C = A * B^T where A is [M, K] and bT is [N, K] (transposed).
// Output is written to out [M, N] which must be pre-allocated and zeroed.
//
// On arm64, dispatches the 4x4 tile body to a NEON FMLA kernel for ~3-5x
// speedup over pure Go on BERT-sized matrices. Falls back to pure Go for
// matrices too small to benefit or for remainder rows/columns.
func MatMul(a, bT []float32, M, K, N int, out []float32) {
	// Bounds validation before entering assembly.
	if M <= 0 || K <= 0 || N <= 0 {
		return
	}
	if len(a) < M*K || len(bT) < N*K || len(out) < M*N {
		panic("bert.MatMul: slice too short")
	}

	mBody := M &^ 3 // M rounded down to multiple of 4
	nBody := N &^ 3

	// Fall back to generic for matrices too small for SIMD benefit.
	if mBody < 4 || nBody < 4 || M*N < matMulSIMDMinSize {
		matMulGeneric(a, bT, M, K, N, out)
		return
	}

	// NEON kernel computes the [mBody x nBody] tile body.
	matMulNEON(a, bT, out, M, K, N)

	// Handle remainder columns (N % 4 != 0) for the tiled rows.
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

	// Handle remainder rows (M % 4 != 0).
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
