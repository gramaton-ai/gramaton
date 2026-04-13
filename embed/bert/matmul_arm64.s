#include "textflag.h"

// FADDP (float pairwise add) for 4xfloat32 vectors.
// Not yet supported as a mnemonic in Go's arm64 assembler; encode directly.
// ARM encoding: FADDP Vd.4S, Vn.4S, Vm.4S = 0x6e20d400 | Vm<<16 | Vn<<5 | Vd
#define FADDP_S4(Vm, Vn, Vd) \
    WORD $(0x6e20d400 | (Vm << 16) | (Vn << 5) | Vd)

// matMulNEON computes the 4x4-tiled body of C = A * B^T using NEON FMLA.
//
// A is [M, K] row-major, bT is [N, K] row-major (transposed B), out is [M, N].
// Computes the (M & ~3) x (N & ~3) tile body. The caller handles remainder
// rows (M%4) and columns (N%4) in Go.
//
// The inner loop processes K in chunks of 4 using 128-bit NEON loads and
// VFMLA (fused multiply-add). Remaining K elements (K%4) are handled with
// scalar loads into vector lane 0 (upper lanes zero) and the same VFMLA path.
// Sixteen NEON accumulators (V16-V31) hold the 4x4 output tile. After the
// K loop, each accumulator is horizontally reduced via two FADDP operations.
//
// Register allocation:
//   R0  = A row base (advances per 4-row block)
//   R2  = out row base (advances per 4-row block)
//   R4  = K
//   R5  = N
//   R6  = K*4 (row stride in bytes for A and bT)
//   R7  = N*4 (row stride in bytes for out)
//   R8  = M & ~3 (i loop limit)
//   R9  = N & ~3 (j loop limit)
//   R10 = K >> 2 (number of 4-element K blocks)
//   R11 = K & 3 (K remainder count)
//   R12 = i counter
//   R13 = j counter
//   R14 = &bT[j*K] (bT column pointer, reset each i)
//   R15 = &out[i*N + j] (out column pointer)
//   R16 = &A[(i+1)*K]
//   R17 = &A[(i+2)*K]
//   R19 = &A[(i+3)*K]
//   R20 = temp address
//   R21 = k-loop counter
//   R22 = k_offset (bytes)
//   R23 = &bT[(j+1)*K]
//   R24 = &bT[(j+2)*K]
//   R25 = &bT[(j+3)*K]
//   R27 = bT base pointer (saved across loops)
//
//   V0-V3:   A row data (current k chunk)
//   V4-V7:   bT row data (current k chunk)
//   V8-V11:  temporaries for horizontal reduction
//   V16-V31: 4x4 tile accumulators (16 dot products)
//
// func matMulNEON(a, bT, out []float32, M, K, N int)
TEXT ·matMulNEON(SB), NOSPLIT, $0-96
    MOVD  a_base+0(FP), R0
    MOVD  bT_base+24(FP), R27
    MOVD  out_base+48(FP), R2
    MOVD  M+72(FP), R3
    MOVD  K+80(FP), R4
    MOVD  N+88(FP), R5

    // Compute byte strides.
    LSL   $2, R4, R6            // R6 = K * 4
    LSL   $2, R5, R7            // R7 = N * 4

    // Tile loop limits.
    AND   $~3, R3, R8           // M & ~3
    AND   $~3, R5, R9           // N & ~3

    // K-dimension chunking.
    LSR   $2, R4, R10           // K / 4 (vector blocks)
    AND   $3, R4, R11           // K % 4 (scalar tail)

    CBZ   R8, done              // M < 4: nothing for SIMD
    CBZ   R9, done              // N < 4: nothing for SIMD

    MOVD  ZR, R12               // i = 0

i_loop:
    CMP   R8, R12
    BGE   done

    // A row pointers for rows i through i+3.
    ADD   R6, R0, R16           // &A[(i+1)*K]
    ADD   R6, R16, R17          // &A[(i+2)*K]
    ADD   R6, R17, R19          // &A[(i+3)*K]

    MOVD  R27, R14              // bT column pointer = &bT[0]
    MOVD  R2, R15               // out column pointer = &out[i*N]
    MOVD  ZR, R13               // j = 0

j_loop:
    CMP   R9, R13
    BGE   j_done

    // Zero 16 accumulators for this 4x4 output tile.
    VEOR  V16.B16, V16.B16, V16.B16
    VEOR  V17.B16, V17.B16, V17.B16
    VEOR  V18.B16, V18.B16, V18.B16
    VEOR  V19.B16, V19.B16, V19.B16
    VEOR  V20.B16, V20.B16, V20.B16
    VEOR  V21.B16, V21.B16, V21.B16
    VEOR  V22.B16, V22.B16, V22.B16
    VEOR  V23.B16, V23.B16, V23.B16
    VEOR  V24.B16, V24.B16, V24.B16
    VEOR  V25.B16, V25.B16, V25.B16
    VEOR  V26.B16, V26.B16, V26.B16
    VEOR  V27.B16, V27.B16, V27.B16
    VEOR  V28.B16, V28.B16, V28.B16
    VEOR  V29.B16, V29.B16, V29.B16
    VEOR  V30.B16, V30.B16, V30.B16
    VEOR  V31.B16, V31.B16, V31.B16

    // bT row pointers for columns j through j+3.
    ADD   R6, R14, R23          // &bT[(j+1)*K]
    ADD   R6, R23, R24          // &bT[(j+2)*K]
    ADD   R6, R24, R25          // &bT[(j+3)*K]

    MOVD  ZR, R22               // k_offset = 0 (bytes)
    MOVD  R10, R21              // k block counter = K/4
    CBZ   R21, k_scalar

    // ---- Vectorized K loop: process 4 floats per iteration ----
k_vec:
    // Load 4 floats from each A row.
    ADD   R22, R0, R20
    VLD1  (R20), [V0.S4]
    ADD   R22, R16, R20
    VLD1  (R20), [V1.S4]
    ADD   R22, R17, R20
    VLD1  (R20), [V2.S4]
    ADD   R22, R19, R20
    VLD1  (R20), [V3.S4]

    // Load 4 floats from each bT row.
    ADD   R22, R14, R20
    VLD1  (R20), [V4.S4]
    ADD   R22, R23, R20
    VLD1  (R20), [V5.S4]
    ADD   R22, R24, R20
    VLD1  (R20), [V6.S4]
    ADD   R22, R25, R20
    VLD1  (R20), [V7.S4]

    // 16 fused multiply-adds: Vd += Vn * Vm (element-wise).
    // Row i+0: C[i,j..j+3]
    VFMLA V0.S4, V4.S4, V16.S4
    VFMLA V0.S4, V5.S4, V17.S4
    VFMLA V0.S4, V6.S4, V18.S4
    VFMLA V0.S4, V7.S4, V19.S4
    // Row i+1
    VFMLA V1.S4, V4.S4, V20.S4
    VFMLA V1.S4, V5.S4, V21.S4
    VFMLA V1.S4, V6.S4, V22.S4
    VFMLA V1.S4, V7.S4, V23.S4
    // Row i+2
    VFMLA V2.S4, V4.S4, V24.S4
    VFMLA V2.S4, V5.S4, V25.S4
    VFMLA V2.S4, V6.S4, V26.S4
    VFMLA V2.S4, V7.S4, V27.S4
    // Row i+3
    VFMLA V3.S4, V4.S4, V28.S4
    VFMLA V3.S4, V5.S4, V29.S4
    VFMLA V3.S4, V6.S4, V30.S4
    VFMLA V3.S4, V7.S4, V31.S4

    ADD   $16, R22              // k_offset += 4 floats * 4 bytes
    SUB   $1, R21
    CBNZ  R21, k_vec

    // ---- Scalar K tail: K%4 remaining elements ----
    // FMOVS loads a scalar float into Fn (lane 0), zeroing lanes 1-3.
    // VFMLA then accumulates only in lane 0 of each accumulator (other
    // lanes get += 0), which is included in the horizontal reduction.
k_scalar:
    CBZ   R11, reduce
    MOVD  R11, R21              // scalar iteration count

k_scalar_loop:
    // Load one float from each A row (upper lanes zeroed by FMOVS).
    ADD   R22, R0, R20
    FMOVS (R20), F0
    ADD   R22, R16, R20
    FMOVS (R20), F1
    ADD   R22, R17, R20
    FMOVS (R20), F2
    ADD   R22, R19, R20
    FMOVS (R20), F3

    // Load one float from each bT row.
    ADD   R22, R14, R20
    FMOVS (R20), F4
    ADD   R22, R23, R20
    FMOVS (R20), F5
    ADD   R22, R24, R20
    FMOVS (R20), F6
    ADD   R22, R25, R20
    FMOVS (R20), F7

    // Same 16 FMLAs; only lane 0 contributes non-zero products.
    VFMLA V0.S4, V4.S4, V16.S4
    VFMLA V0.S4, V5.S4, V17.S4
    VFMLA V0.S4, V6.S4, V18.S4
    VFMLA V0.S4, V7.S4, V19.S4
    VFMLA V1.S4, V4.S4, V20.S4
    VFMLA V1.S4, V5.S4, V21.S4
    VFMLA V1.S4, V6.S4, V22.S4
    VFMLA V1.S4, V7.S4, V23.S4
    VFMLA V2.S4, V4.S4, V24.S4
    VFMLA V2.S4, V5.S4, V25.S4
    VFMLA V2.S4, V6.S4, V26.S4
    VFMLA V2.S4, V7.S4, V27.S4
    VFMLA V3.S4, V4.S4, V28.S4
    VFMLA V3.S4, V5.S4, V29.S4
    VFMLA V3.S4, V6.S4, V30.S4
    VFMLA V3.S4, V7.S4, V31.S4

    ADD   $4, R22               // k_offset += 1 float * 4 bytes
    SUB   $1, R21
    CBNZ  R21, k_scalar_loop

    // ---- Horizontal reduction ----
    // Each accumulator V has 4 partial sums. Two FADDP passes reduce to
    // a scalar in lane 0: [a,b,c,d] -> [a+b,c+d,...] -> [(a+b)+(c+d),...].
reduce:
    // Row i+0: accumulators V16-V19 -> out[i*N + j..j+3]
    FADDP_S4(16, 16, 8)
    FADDP_S4(8, 8, 8)
    FADDP_S4(17, 17, 9)
    FADDP_S4(9, 9, 9)
    FADDP_S4(18, 18, 10)
    FADDP_S4(10, 10, 10)
    FADDP_S4(19, 19, 11)
    FADDP_S4(11, 11, 11)
    FMOVS F8, (R15)
    FMOVS F9, 4(R15)
    FMOVS F10, 8(R15)
    FMOVS F11, 12(R15)

    // Row i+1: V20-V23
    FADDP_S4(20, 20, 8)
    FADDP_S4(8, 8, 8)
    FADDP_S4(21, 21, 9)
    FADDP_S4(9, 9, 9)
    FADDP_S4(22, 22, 10)
    FADDP_S4(10, 10, 10)
    FADDP_S4(23, 23, 11)
    FADDP_S4(11, 11, 11)
    ADD   R7, R15, R20          // &out[(i+1)*N + j]
    FMOVS F8, (R20)
    FMOVS F9, 4(R20)
    FMOVS F10, 8(R20)
    FMOVS F11, 12(R20)

    // Row i+2: V24-V27
    FADDP_S4(24, 24, 8)
    FADDP_S4(8, 8, 8)
    FADDP_S4(25, 25, 9)
    FADDP_S4(9, 9, 9)
    FADDP_S4(26, 26, 10)
    FADDP_S4(10, 10, 10)
    FADDP_S4(27, 27, 11)
    FADDP_S4(11, 11, 11)
    ADD   R7, R20, R20          // &out[(i+2)*N + j]
    FMOVS F8, (R20)
    FMOVS F9, 4(R20)
    FMOVS F10, 8(R20)
    FMOVS F11, 12(R20)

    // Row i+3: V28-V31
    FADDP_S4(28, 28, 8)
    FADDP_S4(8, 8, 8)
    FADDP_S4(29, 29, 9)
    FADDP_S4(9, 9, 9)
    FADDP_S4(30, 30, 10)
    FADDP_S4(10, 10, 10)
    FADDP_S4(31, 31, 11)
    FADDP_S4(11, 11, 11)
    ADD   R7, R20, R20          // &out[(i+3)*N + j]
    FMOVS F8, (R20)
    FMOVS F9, 4(R20)
    FMOVS F10, 8(R20)
    FMOVS F11, 12(R20)

    // Advance to next 4-column block.
    ADD   $4, R13               // j += 4
    LSL   $2, R6, R20           // 4 * K * 4 bytes
    ADD   R20, R14, R14         // bT pointer += 4 rows
    ADD   $16, R15              // out pointer += 4 floats
    B     j_loop

j_done:
    // Advance to next 4-row block.
    ADD   $4, R12               // i += 4
    LSL   $2, R6, R20           // 4 * K * 4 bytes
    ADD   R20, R0, R0           // A pointer += 4 rows
    LSL   $2, R7, R20           // 4 * N * 4 bytes
    ADD   R20, R2, R2           // out pointer += 4 rows
    B     i_loop

done:
    RET
