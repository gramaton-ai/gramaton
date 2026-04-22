#include "textflag.h"

// matMulAVX2 computes the 4x4-tiled body of C = A * B^T using AVX2 + FMA3.
//
// A is [M, K] row-major, bT is [N, K] row-major (transposed B), out is [M, N].
// Computes the (M & ~3) x (N & ~3) tile body; remainder rows/cols are handled
// by the caller.
//
// Inner loop processes K in 8-float chunks via 256-bit YMM loads and
// VFMADD231PS. K%8 tail uses scalar XMM loads (zero-extended into YMM so
// upper lanes contribute nothing to the accumulator).
//
// amd64 has 16 YMM registers (arm64 has 32). To fit the 4x4 output tile
// within that, we split each tile into two K-passes: pass 1 computes
// output rows i and i+1 (8 accumulators); pass 2 computes rows i+2 and
// i+3, reusing the same 8 accumulator registers after storing pass 1's
// results. The bT loads repeat across the two passes (2x memory bandwidth
// on bT vs arm64's single pass) but accumulator register pressure stays
// within 16 YMM. FMA throughput is the bottleneck for BERT matmul sizes,
// not bT bandwidth, so the 2-pass split is close to arm64 performance in
// practice.
//
// Register allocation:
//   AX  = &A[i, 0]           (advances per i)
//   BX  = bT base             (invariant)
//   CX  = &out[i, 0]          (advances per i)
//   DX  = K*4 bytes (row stride for A and bT)
//   DI  = N*4 bytes (row stride for out)
//   SI  = k_offset in bytes inside the k loop
//   BP  = k-pass iteration counter
//   R8  = M & ~3              (i limit)
//   R9  = N & ~3              (j limit)
//   R10 = K / 8               (k-vec block count, re-read per pass)
//   R11 = K % 8               (k-scalar tail, re-read per pass)
//   R12 = scratch pointer (LEAQ target, clobbered freely in tile body)
//   R13 = j counter           (invariant across tile body)
//   R14 = &bT[j, 0]           (advances per j, reset per i)
//   R15 = &out[i, j]          (advances per j, reset per i)
//   0(SP) = i counter (spilled; R12 is scratch so we can't keep i there)
//
// YMM registers:
//   Y0, Y1  = current A rows (i/i+1 on pass 1, i+2/i+3 on pass 2)
//   Y2-Y5   = current bT row block (cols j..j+3)
//   Y8-Y15  = 8 accumulators (two rows x four cols)
//   Y6, Y7  = horizontal-reduction scratch (X6, X7 aliases for the low 128)
//
// func matMulAVX2(a, bT, out []float32, M, K, N int)
TEXT ·matMulAVX2(SB), NOSPLIT, $8-96
	MOVQ a_base+0(FP), AX
	MOVQ bT_base+24(FP), BX
	MOVQ out_base+48(FP), CX

	MOVQ K+80(FP), DX
	SHLQ $2, DX                 // DX = K*4
	MOVQ N+88(FP), DI
	SHLQ $2, DI                 // DI = N*4

	MOVQ M+72(FP), R8
	ANDQ $-4, R8                // R8 = M & ~3
	MOVQ N+88(FP), R9
	ANDQ $-4, R9                // R9 = N & ~3

	MOVQ K+80(FP), R10
	SHRQ $3, R10                // R10 = K / 8
	MOVQ K+80(FP), R11
	ANDQ $7, R11                // R11 = K % 8

	// Early exit if tile body is empty.
	TESTQ R8, R8
	JZ    done
	TESTQ R9, R9
	JZ    done

	// i = 0, spilled to stack because R12 is scratch.
	MOVQ $0, 0(SP)

i_loop:
	MOVQ 0(SP), R12
	CMPQ R12, R8
	JGE  done

	MOVQ BX, R14                // bT col ptr = bT base (j = 0)
	MOVQ CX, R15                // out col ptr = &out[i, 0]
	XORQ R13, R13               // j = 0

j_loop:
	CMPQ R13, R9
	JGE  j_done

	// ================== PASS 1: rows i and i+1 ==================
	VXORPS Y8, Y8, Y8
	VXORPS Y9, Y9, Y9
	VXORPS Y10, Y10, Y10
	VXORPS Y11, Y11, Y11
	VXORPS Y12, Y12, Y12
	VXORPS Y13, Y13, Y13
	VXORPS Y14, Y14, Y14
	VXORPS Y15, Y15, Y15

	XORQ SI, SI                 // k_offset = 0
	MOVQ R10, BP
	TESTQ BP, BP
	JZ    pass1_scalar

pass1_vec:
	// A[i, k..k+7]
	VMOVUPS (AX)(SI*1), Y0
	// A[i+1, k..k+7] = *(AX + DX + SI)
	LEAQ (AX)(DX*1), R12
	VMOVUPS (R12)(SI*1), Y1

	// bT[j, k..k+7]
	VMOVUPS (R14)(SI*1), Y2
	LEAQ (R14)(DX*1), R12
	VMOVUPS (R12)(SI*1), Y3
	LEAQ (R12)(DX*1), R12
	VMOVUPS (R12)(SI*1), Y4
	LEAQ (R12)(DX*1), R12
	VMOVUPS (R12)(SI*1), Y5

	VFMADD231PS Y2, Y0, Y8
	VFMADD231PS Y3, Y0, Y9
	VFMADD231PS Y4, Y0, Y10
	VFMADD231PS Y5, Y0, Y11
	VFMADD231PS Y2, Y1, Y12
	VFMADD231PS Y3, Y1, Y13
	VFMADD231PS Y4, Y1, Y14
	VFMADD231PS Y5, Y1, Y15

	ADDQ $32, SI                // k_offset += 8*4 bytes
	DECQ BP
	JNZ  pass1_vec

pass1_scalar:
	TESTQ R11, R11
	JZ    pass1_reduce
	MOVQ R11, BP

pass1_scalar_loop:
	VMOVSS (AX)(SI*1), X0
	LEAQ (AX)(DX*1), R12
	VMOVSS (R12)(SI*1), X1

	VMOVSS (R14)(SI*1), X2
	LEAQ (R14)(DX*1), R12
	VMOVSS (R12)(SI*1), X3
	LEAQ (R12)(DX*1), R12
	VMOVSS (R12)(SI*1), X4
	LEAQ (R12)(DX*1), R12
	VMOVSS (R12)(SI*1), X5

	VFMADD231PS Y2, Y0, Y8
	VFMADD231PS Y3, Y0, Y9
	VFMADD231PS Y4, Y0, Y10
	VFMADD231PS Y5, Y0, Y11
	VFMADD231PS Y2, Y1, Y12
	VFMADD231PS Y3, Y1, Y13
	VFMADD231PS Y4, Y1, Y14
	VFMADD231PS Y5, Y1, Y15

	ADDQ $4, SI
	DECQ BP
	JNZ  pass1_scalar_loop

pass1_reduce:
	// Horizontal-reduce Y8..Y11 into scalars at out[i, j..j+3].
	VEXTRACTF128 $1, Y8, X6
	VADDPS X8, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, (R15)

	VEXTRACTF128 $1, Y9, X6
	VADDPS X9, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 4(R15)

	VEXTRACTF128 $1, Y10, X6
	VADDPS X10, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 8(R15)

	VEXTRACTF128 $1, Y11, X6
	VADDPS X11, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 12(R15)

	// Row i+1 base = R15 + DI. Use R12 as scratch (i is on stack).
	LEAQ (R15)(DI*1), R12

	VEXTRACTF128 $1, Y12, X6
	VADDPS X12, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, (R12)

	VEXTRACTF128 $1, Y13, X6
	VADDPS X13, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 4(R12)

	VEXTRACTF128 $1, Y14, X6
	VADDPS X14, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 8(R12)

	VEXTRACTF128 $1, Y15, X6
	VADDPS X15, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 12(R12)

	// ================== PASS 2: rows i+2 and i+3 ==================
	VXORPS Y8, Y8, Y8
	VXORPS Y9, Y9, Y9
	VXORPS Y10, Y10, Y10
	VXORPS Y11, Y11, Y11
	VXORPS Y12, Y12, Y12
	VXORPS Y13, Y13, Y13
	VXORPS Y14, Y14, Y14
	VXORPS Y15, Y15, Y15

	XORQ SI, SI
	MOVQ R10, BP
	TESTQ BP, BP
	JZ    pass2_scalar

pass2_vec:
	// A[i+2] = AX + 2*DX, A[i+3] = AX + 3*DX
	LEAQ (AX)(DX*2), R12
	VMOVUPS (R12)(SI*1), Y0
	LEAQ (R12)(DX*1), R12
	VMOVUPS (R12)(SI*1), Y1

	VMOVUPS (R14)(SI*1), Y2
	LEAQ (R14)(DX*1), R12
	VMOVUPS (R12)(SI*1), Y3
	LEAQ (R12)(DX*1), R12
	VMOVUPS (R12)(SI*1), Y4
	LEAQ (R12)(DX*1), R12
	VMOVUPS (R12)(SI*1), Y5

	VFMADD231PS Y2, Y0, Y8
	VFMADD231PS Y3, Y0, Y9
	VFMADD231PS Y4, Y0, Y10
	VFMADD231PS Y5, Y0, Y11
	VFMADD231PS Y2, Y1, Y12
	VFMADD231PS Y3, Y1, Y13
	VFMADD231PS Y4, Y1, Y14
	VFMADD231PS Y5, Y1, Y15

	ADDQ $32, SI
	DECQ BP
	JNZ  pass2_vec

pass2_scalar:
	TESTQ R11, R11
	JZ    pass2_reduce
	MOVQ R11, BP

pass2_scalar_loop:
	LEAQ (AX)(DX*2), R12
	VMOVSS (R12)(SI*1), X0
	LEAQ (R12)(DX*1), R12
	VMOVSS (R12)(SI*1), X1

	VMOVSS (R14)(SI*1), X2
	LEAQ (R14)(DX*1), R12
	VMOVSS (R12)(SI*1), X3
	LEAQ (R12)(DX*1), R12
	VMOVSS (R12)(SI*1), X4
	LEAQ (R12)(DX*1), R12
	VMOVSS (R12)(SI*1), X5

	VFMADD231PS Y2, Y0, Y8
	VFMADD231PS Y3, Y0, Y9
	VFMADD231PS Y4, Y0, Y10
	VFMADD231PS Y5, Y0, Y11
	VFMADD231PS Y2, Y1, Y12
	VFMADD231PS Y3, Y1, Y13
	VFMADD231PS Y4, Y1, Y14
	VFMADD231PS Y5, Y1, Y15

	ADDQ $4, SI
	DECQ BP
	JNZ  pass2_scalar_loop

pass2_reduce:
	// Row i+2 base = R15 + 2*DI
	LEAQ (R15)(DI*2), R12
	VEXTRACTF128 $1, Y8, X6
	VADDPS X8, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, (R12)

	VEXTRACTF128 $1, Y9, X6
	VADDPS X9, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 4(R12)

	VEXTRACTF128 $1, Y10, X6
	VADDPS X10, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 8(R12)

	VEXTRACTF128 $1, Y11, X6
	VADDPS X11, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 12(R12)

	// Row i+3 base = R15 + 3*DI (= prev + DI)
	LEAQ (R12)(DI*1), R12
	VEXTRACTF128 $1, Y12, X6
	VADDPS X12, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, (R12)

	VEXTRACTF128 $1, Y13, X6
	VADDPS X13, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 4(R12)

	VEXTRACTF128 $1, Y14, X6
	VADDPS X14, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 8(R12)

	VEXTRACTF128 $1, Y15, X6
	VADDPS X15, X6, X7
	VHADDPS X7, X7, X7
	VHADDPS X7, X7, X7
	VMOVSS X7, 12(R12)

	// Advance to next 4-column block within this i.
	ADDQ $4, R13                // j += 4
	LEAQ (R14)(DX*4), R14       // bT ptr += 4 rows
	ADDQ $16, R15               // out ptr += 4 floats
	JMP  j_loop

j_done:
	// Advance i by 4, save back to stack, advance AX/CX.
	MOVQ 0(SP), R12
	ADDQ $4, R12
	MOVQ R12, 0(SP)
	LEAQ (AX)(DX*4), AX         // A ptr += 4 rows
	LEAQ (CX)(DI*4), CX         // out ptr += 4 rows
	JMP  i_loop

done:
	VZEROUPPER                  // avoid AVX/SSE transition penalty
	RET
