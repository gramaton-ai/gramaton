package bert

// TODO(simd): Add AVX2 matmul kernel for amd64.
//
// Same 4x4 tiling approach as matmul_arm64.s but using YMM registers
// (8 float32s per VFMADD231PS). Expected ~5-8x matmul speedup over
// pure Go on Intel/AMD hardware.
//
// When implemented:
//   1. Create matmul_amd64.s with the AVX2 kernel
//   2. Move this file's MatMul to use SIMD dispatch (like matmul_simd.go)
//   3. Update matmul_generic.go build tag from !arm64 to !(arm64 || amd64)
//
// Until then, amd64 uses the pure Go fallback via matmul_generic.go.
