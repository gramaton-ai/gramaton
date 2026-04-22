//go:build !arm64 && !amd64

package bert

// MatMul computes C = A * B^T where A is [M, K] and bT is [N, K] (transposed).
// Output is written to out [M, N] which must be pre-allocated and zeroed.
// This layout matches safetensors weight storage ([out_features, in_features])
// and provides cache-friendly row access on both operands.
//
// Uses 4x4 register tiling to amortize loads across tile elements.
// Each A value participates in 4 output columns; each B^T value in 4 rows.
func MatMul(a, bT []float32, M, K, N int, out []float32) {
	if M <= 0 || K <= 0 || N <= 0 {
		return
	}
	if len(a) < M*K || len(bT) < N*K || len(out) < M*N {
		panic("bert.MatMul: slice too short")
	}
	matMulGeneric(a, bT, M, K, N, out)
}
