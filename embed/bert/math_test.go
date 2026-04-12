package bert

import (
	"math"
	"testing"
)

const tolerance = 1e-5

func approxEqual(a, b float32) bool {
	return math.Abs(float64(a-b)) < tolerance
}

func requireApprox(t *testing.T, name string, got, want []float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: length mismatch: got %d, want %d", name, len(got), len(want))
	}
	for i := range got {
		if !approxEqual(got[i], want[i]) {
			t.Errorf("%s[%d]: got %v, want %v", name, i, got[i], want[i])
		}
	}
}

// Golden values computed via numpy:
//
//   import numpy as np
//   a = np.array([[1,2,3],[4,5,6]], dtype=np.float32)
//   b = np.array([[7,8],[9,10],[11,12]], dtype=np.float32)
//   # b^T = [[7,9,11],[8,10,12]]
//   print(a @ b)  # [[58,64],[139,154]]
func TestMatMul(t *testing.T) {
	a := []float32{1, 2, 3, 4, 5, 6}       // [2, 3]
	bT := []float32{7, 9, 11, 8, 10, 12}    // [2, 3] (rows of B transposed)
	out := make([]float32, 4)                // [2, 2]
	MatMul(a, bT, 2, 3, 2, out)
	requireApprox(t, "matmul", out, []float32{58, 64, 139, 154})
}

func TestMatMulIdentity(t *testing.T) {
	// A * I^T = A (identity is its own transpose)
	a := []float32{1, 2, 3, 4, 5, 6, 7, 8, 9}
	iT := []float32{1, 0, 0, 0, 1, 0, 0, 0, 1} // [3, 3] identity
	out := make([]float32, 9)
	MatMul(a, iT, 3, 3, 3, out)
	requireApprox(t, "identity", out, a)
}

func TestMatMulLarger(t *testing.T) {
	// Test with dimensions not divisible by tile size (4).
	// A [5, 7] * B^T [3, 7] = C [5, 3]
	M, K, N := 5, 7, 3
	a := make([]float32, M*K)
	bT := make([]float32, N*K)
	for i := range a {
		a[i] = float32(i + 1)
	}
	for i := range bT {
		bT[i] = float32(i + 1)
	}

	out := make([]float32, M*N)
	MatMul(a, bT, M, K, N, out)

	// Verify against naive implementation.
	for i := 0; i < M; i++ {
		for j := 0; j < N; j++ {
			var want float32
			for k := 0; k < K; k++ {
				want += a[i*K+k] * bT[j*K+k]
			}
			if !approxEqual(out[i*N+j], want) {
				t.Errorf("[%d,%d]: got %v, want %v", i, j, out[i*N+j], want)
			}
		}
	}
}

func TestMatMulBERTSized(t *testing.T) {
	// Verify BERT-sized matmul produces correct results.
	// [128, 384] * [384, 384]^T = [128, 384]
	M, K, N := 128, 384, 384
	a := make([]float32, M*K)
	bT := make([]float32, N*K)
	for i := range a {
		a[i] = float32(i%7) * 0.1
	}
	for i := range bT {
		bT[i] = float32(i%11) * 0.01
	}

	out := make([]float32, M*N)
	MatMul(a, bT, M, K, N, out)

	// Spot-check a few values against naive computation.
	for _, pos := range [][2]int{{0, 0}, {63, 127}, {127, 383}} {
		i, j := pos[0], pos[1]
		var want float32
		for k := 0; k < K; k++ {
			want += a[i*K+k] * bT[j*K+k]
		}
		if !approxEqual(out[i*N+j], want) {
			t.Errorf("[%d,%d]: got %v, want %v", i, j, out[i*N+j], want)
		}
	}
}

func TestAddBias(t *testing.T) {
	out := []float32{1, 2, 3, 4, 5, 6}
	bias := []float32{10, 20, 30}
	AddBias(out, bias, 2, 3)
	requireApprox(t, "add_bias", out, []float32{11, 22, 33, 14, 25, 36})
}

func TestMatMulAdd(t *testing.T) {
	a := []float32{1, 2, 3, 4, 5, 6}
	bT := []float32{7, 9, 11, 8, 10, 12}
	bias := []float32{100, 200}
	out := make([]float32, 4)
	MatMulAdd(a, bT, 2, 3, 2, bias, out)
	requireApprox(t, "matmuladd", out, []float32{158, 264, 239, 354})
}

// Golden values computed via numpy:
//
//   x = np.array([[1.0, 2.0, 3.0]], dtype=np.float32)
//   mean = x.mean(-1, keepdims=True)  # 2.0
//   var = ((x - mean)**2).mean(-1, keepdims=True)  # 0.6667
//   norm = (x - mean) / np.sqrt(var + 1e-12)  # [-1.2247, 0, 1.2247]
//   w = np.array([1.0, 1.0, 1.0], dtype=np.float32)
//   b = np.array([0.0, 0.0, 0.0], dtype=np.float32)
//   out = norm * w + b  # [-1.2247, 0, 1.2247]
func TestLayerNorm(t *testing.T) {
	x := []float32{1, 2, 3}
	w := []float32{1, 1, 1}
	b := []float32{0, 0, 0}
	LayerNorm(x, w, b, 1, 3, 1e-12)
	requireApprox(t, "layernorm", x, []float32{-1.2247449, 0, 1.2247449})
}

func TestLayerNormWithAffine(t *testing.T) {
	x := []float32{1, 2, 3, 4}
	w := []float32{2, 0.5}
	b := []float32{1, -1}
	LayerNorm(x, w, b, 2, 2, 1e-12)
	// Row 1: [1,2] -> norm=[-1,1] -> affine=[-2+1, 0.5-1]=[-1, -0.5]
	// Row 2: [3,4] -> norm=[-1,1] -> affine=[-2+1, 0.5-1]=[-1, -0.5]
	requireApprox(t, "layernorm_affine", x, []float32{-1, -0.5, -1, -0.5})
}

// Golden values computed via Python:
//
//   import torch
//   x = torch.tensor([-1.0, 0.0, 1.0, 2.0])
//   torch.nn.functional.gelu(x, approximate='tanh')
//   # tensor([-0.1588, 0.0000, 0.8412, 1.9545])
func TestGELU(t *testing.T) {
	x := []float32{-1.0, 0.0, 1.0, 2.0}
	GELU(x)
	want := []float32{-0.15880796, 0.0, 0.84119204, 1.9545977}
	requireApprox(t, "gelu", x, want)
}

func TestGELUSmallValues(t *testing.T) {
	x := []float32{-3.0, -0.5, 0.5, 3.0}
	GELU(x)
	// Tanh approximation values (differ slightly from exact GELU):
	// GELU(-3) ~ -0.00364, GELU(-0.5) ~ -0.15429, GELU(0.5) ~ 0.34571, GELU(3) ~ 2.99636
	want := []float32{-0.0036374, -0.15429, 0.34571, 2.99636}
	requireApprox(t, "gelu_small", x, want)
}

// Golden values: softmax([1,2,3]) = [0.0900, 0.2447, 0.6652]
func TestSoftmax(t *testing.T) {
	x := []float32{1, 2, 3}
	Softmax(x, 1, 3)
	want := []float32{0.09003057, 0.24472848, 0.66524094}
	requireApprox(t, "softmax", x, want)

	// Sum should be 1.
	var sum float32
	for _, v := range x {
		sum += v
	}
	if !approxEqual(sum, 1.0) {
		t.Errorf("softmax sum: got %v, want 1.0", sum)
	}
}

func TestSoftmaxMultiRow(t *testing.T) {
	x := []float32{1, 2, 3, 100, 100, 100}
	Softmax(x, 2, 3)

	// Row 2: equal values -> uniform distribution.
	want2 := []float32{1.0 / 3, 1.0 / 3, 1.0 / 3}
	requireApprox(t, "softmax_row2", x[3:6], want2)
}

func TestSoftmaxNumericalStability(t *testing.T) {
	// Large values that would overflow without max subtraction.
	x := []float32{1000, 1001, 1002}
	Softmax(x, 1, 3)
	want := []float32{0.09003057, 0.24472848, 0.66524094}
	requireApprox(t, "softmax_stable", x, want)
}

func TestSoftmaxMasked(t *testing.T) {
	x := []float32{1, 2, 3, 4}
	mask := []int32{1, 1, 0, 0} // only first 2 positions unmasked
	SoftmaxMasked(x, 1, 4, mask)

	// Masked positions should be ~0.
	if x[2] > 1e-6 || x[3] > 1e-6 {
		t.Errorf("masked positions should be ~0: got %v, %v", x[2], x[3])
	}
	// Unmasked should sum to ~1.
	if !approxEqual(x[0]+x[1], 1.0) {
		t.Errorf("unmasked sum: got %v, want 1.0", x[0]+x[1])
	}
}

func TestL2Normalize(t *testing.T) {
	x := []float32{3, 4}
	L2Normalize(x)
	requireApprox(t, "l2norm", x, []float32{0.6, 0.8})
}

func TestL2NormalizeUnit(t *testing.T) {
	x := []float32{1, 2, 3, 4, 5}
	L2Normalize(x)
	var sum float64
	for _, v := range x {
		sum += float64(v) * float64(v)
	}
	if !approxEqual(float32(sum), 1.0) {
		t.Errorf("l2norm magnitude: got %v, want 1.0", sum)
	}
}

func TestL2NormalizeZero(t *testing.T) {
	x := []float32{0, 0, 0}
	L2Normalize(x) // should not panic or produce NaN
	for i, v := range x {
		if math.IsNaN(float64(v)) {
			t.Errorf("l2norm zero[%d]: got NaN", i)
		}
	}
}

func TestZeroSlice(t *testing.T) {
	x := []float32{1, 2, 3, 4}
	ZeroSlice(x)
	for i, v := range x {
		if v != 0 {
			t.Errorf("zero[%d]: got %v", i, v)
		}
	}
}

// --- Benchmarks (PoC gate) ---

// BenchmarkMatMulAttnProj benchmarks the attention projection matmul:
// [128, 384] * [384, 384] (the most common matmul in BERT).
// Target: < 500us.
func BenchmarkMatMulAttnProj(b *testing.B) {
	M, K, N := 128, 384, 384
	a := make([]float32, M*K)
	bT := make([]float32, N*K)
	out := make([]float32, M*N)
	for i := range a {
		a[i] = float32(i%7) * 0.1
	}
	for i := range bT {
		bT[i] = float32(i%11) * 0.01
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ZeroSlice(out)
		MatMul(a, bT, M, K, N, out)
	}
}

// BenchmarkMatMulFFNUp benchmarks the FFN intermediate matmul:
// [128, 384] * [1536, 384] = [128, 1536].
// Target: < 1ms.
func BenchmarkMatMulFFNUp(b *testing.B) {
	M, K, N := 128, 384, 1536
	a := make([]float32, M*K)
	bT := make([]float32, N*K)
	out := make([]float32, M*N)
	for i := range a {
		a[i] = float32(i%7) * 0.1
	}
	for i := range bT {
		bT[i] = float32(i%11) * 0.01
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ZeroSlice(out)
		MatMul(a, bT, M, K, N, out)
	}
}

// BenchmarkMatMulFFNDown benchmarks the FFN output matmul:
// [128, 1536] * [384, 1536] = [128, 384].
// Target: < 1ms.
func BenchmarkMatMulFFNDown(b *testing.B) {
	M, K, N := 128, 1536, 384
	a := make([]float32, M*K)
	bT := make([]float32, N*K)
	out := make([]float32, M*N)
	for i := range a {
		a[i] = float32(i%7) * 0.1
	}
	for i := range bT {
		bT[i] = float32(i%11) * 0.01
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ZeroSlice(out)
		MatMul(a, bT, M, K, N, out)
	}
}

// BenchmarkMatMulAttnScores benchmarks the attention score matmul:
// [128, 32] * [128, 32] (per-head, seqlen x seqlen).
// One of 12 heads. Target: < 50us.
func BenchmarkMatMulAttnScores(b *testing.B) {
	M, K, N := 128, 32, 128
	a := make([]float32, M*K)
	bT := make([]float32, N*K)
	out := make([]float32, M*N)
	for i := range a {
		a[i] = float32(i%7) * 0.1
	}
	for i := range bT {
		bT[i] = float32(i%11) * 0.01
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ZeroSlice(out)
		MatMul(a, bT, M, K, N, out)
	}
}

// BenchmarkGELU benchmarks GELU on FFN-sized intermediate: 128*1536 elements.
func BenchmarkGELU(b *testing.B) {
	x := make([]float32, 128*1536)
	for i := range x {
		x[i] = float32(i%100) * 0.01
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GELU(x)
	}
}

// BenchmarkLayerNorm benchmarks LayerNorm on [128, 384].
func BenchmarkLayerNorm(b *testing.B) {
	x := make([]float32, 128*384)
	w := make([]float32, 384)
	bias := make([]float32, 384)
	for i := range x {
		x[i] = float32(i%100) * 0.01
	}
	for i := range w {
		w[i] = 1.0
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LayerNorm(x, w, bias, 128, 384, 1e-12)
	}
}

// BenchmarkSoftmax benchmarks row-wise softmax on [128, 128] (attention scores).
func BenchmarkSoftmax(b *testing.B) {
	x := make([]float32, 128*128)
	for i := range x {
		x[i] = float32(i%100) * 0.01
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Softmax(x, 128, 128)
	}
}
