package bert

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
)

// goroutineCount returns the current number of goroutines.
// Used as a leak guard in Layer D ctx-cancel tests.
func goroutineCount() int { return runtime.NumGoroutine() }

// hasCachedModel returns true if the real bge-small-en-v1.5 weights
// are present in ~/.gramaton/models. Layer F's speedup-gate tests
// skip when absent (download is out of scope for go test).
func hasCachedModel() bool {
	dir := ModelDir(DefaultModel)
	_, err := os.Stat(filepath.Join(dir, "model.safetensors"))
	return err == nil
}

// TestEmbedConcurrentDeterminism — fixed-length inputs.
//
// 8 goroutines each call Embed with the SAME input set; output must
// be byte-identical to a sequential reference run. With per-call
// scratch and read-only model, concurrent Forward must produce the
// same bits.
func TestEmbedConcurrentDeterminism(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)
	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-determinism", cfg)
	defer p.Close()

	inputs := []string{"hello", "[CLS]", "[SEP] hello [SEP]"}

	// Sequential reference.
	ref, err := p.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([][][]float32, goroutines)
	errs := make([]error, goroutines)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-release
			results[g], errs[g] = p.Embed(context.Background(), inputs)
		}(g)
	}
	close(release)
	wg.Wait()

	for g, res := range results {
		if errs[g] != nil {
			t.Errorf("goroutine %d: %v", g, errs[g])
			continue
		}
		if len(res) != len(ref) {
			t.Errorf("goroutine %d: result count %d, want %d", g, len(res), len(ref))
			continue
		}
		for i := range ref {
			for j := range ref[i] {
				if res[i][j] != ref[i][j] {
					t.Errorf("goroutine %d: result[%d][%d]: got %v, want %v",
						g, i, j, res[i][j], ref[i][j])
				}
			}
		}
	}
}

// TestEmbedConcurrentVariableLengths — different inputs per goroutine.
//
// Variable-length inputs stress the buffer slicing in Forward.
// Each goroutine has different sequence lengths, exercising the
// `[:seqLen*h]` slicing patterns under concurrent execution.
func TestEmbedConcurrentVariableLengths(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)
	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-varlen", cfg)
	defer p.Close()

	// Different inputs of varying token-after-tokenization length.
	inputSets := [][]string{
		{"hello"},
		{"hello", "hello"},
		{"hello hello hello"},
		{"[CLS]", "[SEP]"},
	}

	// Sequential reference per goroutine's input set.
	refs := make([][][]float32, len(inputSets))
	for i, inputs := range inputSets {
		r, err := p.Embed(context.Background(), inputs)
		if err != nil {
			t.Fatal(err)
		}
		refs[i] = r
	}

	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([][][]float32, len(inputSets))
	for g, inputs := range inputSets {
		wg.Add(1)
		go func(g int, inputs []string) {
			defer wg.Done()
			<-release
			r, err := p.Embed(context.Background(), inputs)
			if err != nil {
				t.Errorf("goroutine %d: %v", g, err)
				return
			}
			results[g] = r
		}(g, inputs)
	}
	close(release)
	wg.Wait()

	for g, ref := range refs {
		res := results[g]
		if len(res) != len(ref) {
			t.Errorf("goroutine %d: count %d, want %d", g, len(res), len(ref))
			continue
		}
		for i := range ref {
			for j := range ref[i] {
				if res[i][j] != ref[i][j] {
					t.Errorf("goroutine %d: result[%d][%d]: got %v, want %v",
						g, i, j, res[i][j], ref[i][j])
				}
			}
		}
	}
}

// TestEmbedConcurrentClose — Close blocks until in-flight Embed
// goroutines finish, then subsequent Embeds return closed-error.
//
// 4 goroutines run Embed in tight loops. Main goroutine calls Close
// after a short delay. Close must wait for in-flight RLock holders
// to release. After Close: Embed returns "bert: provider closed";
// no panic, no segfault.
func TestEmbedConcurrentClose(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)
	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-close", cfg)
	// Note: NO defer p.Close() — we Close() explicitly mid-test.

	var preCloseCount, postCloseCount atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Loop until we see the provider-closed error. Don't bound
			// iterations — bounded loops race the Close timing under
			// -count=N runs.
			for {
				_, err := p.Embed(context.Background(), []string{"hello"})
				if err != nil {
					if err.Error() == "bert: provider closed" {
						postCloseCount.Add(1)
						return
					}
					t.Errorf("unexpected error: %v", err)
					return
				}
				preCloseCount.Add(1)
			}
		}()
	}

	// Let some Embeds happen, then Close.
	time.Sleep(10 * time.Millisecond)
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	wg.Wait()

	if preCloseCount.Load() == 0 {
		t.Error("no Embeds completed before Close")
	}
	if postCloseCount.Load() == 0 {
		t.Error("no goroutines saw provider-closed after Close")
	}

	// Subsequent Embed must return closed-error.
	_, err := p.Embed(context.Background(), []string{"hello"})
	if err == nil || err.Error() != "bert: provider closed" {
		t.Errorf("post-Close Embed: got %v, want provider closed", err)
	}
}

// TestEmbedSingleCallDeterminism — one Embed call with N texts;
// inner-loop fanout. Output must match a per-text sequential
// reference. Layer D.
func TestEmbedSingleCallDeterminism(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)
	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-d-determinism", cfg)
	defer p.Close()

	inputs := make([]string, 20)
	for i := range inputs {
		inputs[i] = "hello"
	}

	// Sequential per-text reference.
	refs := make([][]float32, len(inputs))
	for i, text := range inputs {
		r, err := p.Embed(context.Background(), []string{text})
		if err != nil {
			t.Fatal(err)
		}
		refs[i] = r[0]
	}

	// One Embed call with N texts (uses concurrent inner-loop).
	got, err := p.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(refs) {
		t.Fatalf("count: got %d, want %d", len(got), len(refs))
	}
	for i := range refs {
		for j := range refs[i] {
			if got[i][j] != refs[i][j] {
				t.Errorf("got[%d][%d]=%v, want %v", i, j, got[i][j], refs[i][j])
			}
		}
	}
}

// TestEmbedSingleCallCtxCancel — single Embed call with N texts
// gets a cancelled ctx mid-flight. Returns ctx.Err(); no goroutine
// leak. Layer D.
func TestEmbedSingleCallCtxCancel(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)
	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-d-cancel", cfg)
	defer p.Close()

	// Already-cancelled ctx -> immediate return.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inputs := make([]string, 50)
	for i := range inputs {
		inputs[i] = "hello"
	}

	before := goroutineCount()
	_, err := p.Embed(ctx, inputs)
	if err == nil {
		t.Error("expected error on cancelled ctx")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}

	// Allow goroutines to wind down and assert no leak.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	after := goroutineCount()
	if after > before+2 {
		t.Errorf("goroutine leak: before=%d after=%d (delta=%d)",
			before, after, after-before)
	}
}

// TestEmbedMaxWorkersBound — MaxWorkers=2 caps simultaneous live
// Scratches at 2 even with N=8 texts. Layer D.
func TestEmbedMaxWorkersBound(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)
	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-d-bound", cfg)
	p.maxWorkers = 2
	defer p.Close()

	hook := newInstrumentedPool()
	p.poolHook = hook

	inputs := make([]string, 8)
	for i := range inputs {
		inputs[i] = "hello"
	}

	if _, err := p.Embed(context.Background(), inputs); err != nil {
		t.Fatal(err)
	}

	if hook.maxLive.Load() > 2 {
		t.Errorf("maxLive=%d exceeds bound=2", hook.maxLive.Load())
	}
	if hook.gets.Load() != 8 {
		t.Errorf("gets=%d, want 8", hook.gets.Load())
	}
}

// TestEmbedSingleCallSingleItem — N=1 takes the fast path; no
// errgroup, no semaphore. Verifies the fast path produces correct
// output. Layer D.
func TestEmbedSingleCallSingleItem(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)
	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-d-single", cfg)
	defer p.Close()

	res, err := p.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("count: got %d, want 1", len(res))
	}
	// Sanity: L2-normalized, no NaN/Inf.
	var norm float64
	for _, v := range res[0] {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Errorf("NaN/Inf in output")
		}
		norm += float64(v) * float64(v)
	}
	if math.Abs(norm-1.0) > 1e-3 {
		t.Errorf("|v|^2=%v, want ~1.0", norm)
	}
}

// TestEmbedConcurrentMixed — Embeds + Close interleaved randomly.
//
// 4 goroutines Embed in a tight loop; a 5th calls Close after a
// random delay. Either: (a) all Embeds before Close return success,
// (b) some Embeds after Close return provider-closed-error, never
// (c) panic or garbage output.
func TestEmbedConcurrentMixed(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)
	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-mixed", cfg)

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				res, err := p.Embed(context.Background(), []string{"hello"})
				if err != nil {
					if !errors.Is(err, context.Canceled) &&
						err.Error() != "bert: provider closed" {
						t.Errorf("unexpected error: %v", err)
					}
					return
				}
				// Sanity-check output isn't garbage: must be 1 vector,
				// L2-normalized, no NaN/Inf.
				if len(res) != 1 {
					t.Errorf("expected 1 result, got %d", len(res))
					return
				}
				var norm float64
				for _, v := range res[0] {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						t.Errorf("got NaN/Inf in output")
						return
					}
					norm += float64(v) * float64(v)
				}
				if math.Abs(norm-1.0) > 1e-3 {
					t.Errorf("not L2-normalized: |v|^2 = %v", norm)
					return
				}
			}
		}()
	}

	// Random-ish delay before Close.
	time.Sleep(time.Duration(5+runtime.NumCPU()) * time.Millisecond)
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	wg.Wait()
}

// loadRealProvider opens the cached BERT model for Layer F's
// speedup-gate tests. Skips when the model isn't cached; download
// is out of scope for go test.
func loadRealProvider(t *testing.T) *Provider {
	t.Helper()
	if !hasCachedModel() {
		t.Skip("requires cached BERT model at " + ModelDir(DefaultModel))
	}
	p, err := New(config.EmbeddingConfig{Provider: "bert", Model: DefaultModel})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// timeEmbed returns the wall-clock duration for a single Embed
// call. Used by the speedup gates to compute ratios from a
// per-run baseline so the assertions tolerate slow CI runners.
func timeEmbed(t *testing.T, p *Provider, texts []string) time.Duration {
	t.Helper()
	start := time.Now()
	if _, err := p.Embed(context.Background(), texts); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}

// TestEmbedSpeedupGateSingleCall4 — N=8 single-call wall-clock vs
// 8 sequential single-text calls. Asserts >=1.8x speedup at
// effective 4-worker concurrency. Conservative for memory-
// bandwidth-bound matmul. Skips when NumCPU < 4.
func TestEmbedSpeedupGateSingleCall4(t *testing.T) {
	if runtime.NumCPU() < 4 {
		t.Skipf("requires NumCPU >= 4, got %d", runtime.NumCPU())
	}
	p := loadRealProvider(t)
	defer p.Close()
	p.maxWorkers = 4

	texts := make([]string, 8)
	for i := range texts {
		texts[i] = "the quick brown fox jumps over the lazy dog"
	}

	// Warm up (first call may pay one-time costs).
	_, _ = p.Embed(context.Background(), []string{texts[0]})

	// Sequential baseline: 8 separate Embed(N=1) calls.
	seqStart := time.Now()
	for _, text := range texts {
		if _, err := p.Embed(context.Background(), []string{text}); err != nil {
			t.Fatal(err)
		}
	}
	seqDur := time.Since(seqStart)

	// Concurrent: one Embed(N=8) with maxWorkers=4.
	parDur := timeEmbed(t, p, texts)

	speedup := float64(seqDur) / float64(parDur)
	t.Logf("seq=%v par=%v speedup=%.2fx", seqDur, parDur, speedup)
	if speedup < 1.8 {
		t.Errorf("speedup %.2fx below 1.8x gate (seq=%v, par=%v)",
			speedup, seqDur, parDur)
	}
}

// TestEmbedSpeedupGateSingleCall8 — N=64 single-call vs 64
// sequential. Asserts >=2.5x at maxWorkers=8. Skips when
// NumCPU < 8.
func TestEmbedSpeedupGateSingleCall8(t *testing.T) {
	if runtime.NumCPU() < 8 {
		t.Skipf("requires NumCPU >= 8, got %d", runtime.NumCPU())
	}
	p := loadRealProvider(t)
	defer p.Close()
	p.maxWorkers = 8

	texts := make([]string, 64)
	for i := range texts {
		texts[i] = "the quick brown fox jumps over the lazy dog"
	}

	_, _ = p.Embed(context.Background(), []string{texts[0]})

	seqStart := time.Now()
	for _, text := range texts {
		if _, err := p.Embed(context.Background(), []string{text}); err != nil {
			t.Fatal(err)
		}
	}
	seqDur := time.Since(seqStart)

	parDur := timeEmbed(t, p, texts)

	speedup := float64(seqDur) / float64(parDur)
	t.Logf("seq=%v par=%v speedup=%.2fx", seqDur, parDur, speedup)
	if speedup < 2.5 {
		t.Errorf("speedup %.2fx below 2.5x gate (seq=%v, par=%v)",
			speedup, seqDur, parDur)
	}
}

// TestEmbedSpeedupGateMultiCaller4 — 4 concurrent goroutines
// each calling Embed(N=1). Aggregate wall-clock vs sequential 4
// separate calls. Asserts >=1.8x. Skips when NumCPU < 4.
func TestEmbedSpeedupGateMultiCaller4(t *testing.T) {
	if runtime.NumCPU() < 4 {
		t.Skipf("requires NumCPU >= 4, got %d", runtime.NumCPU())
	}
	p := loadRealProvider(t)
	defer p.Close()

	const k = 4
	const iters = 8 // each goroutine

	_, _ = p.Embed(context.Background(), []string{"warmup"})

	// Sequential: k*iters separate Embeds.
	seqStart := time.Now()
	for i := 0; i < k*iters; i++ {
		if _, err := p.Embed(context.Background(), []string{"hello"}); err != nil {
			t.Fatal(err)
		}
	}
	seqDur := time.Since(seqStart)

	// Concurrent: k goroutines, each doing iters Embeds.
	parStart := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < k; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if _, err := p.Embed(context.Background(), []string{"hello"}); err != nil {
					t.Errorf("embed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	parDur := time.Since(parStart)

	speedup := float64(seqDur) / float64(parDur)
	t.Logf("seq=%v par=%v speedup=%.2fx", seqDur, parDur, speedup)
	if speedup < 1.8 {
		t.Errorf("speedup %.2fx below 1.8x gate (seq=%v, par=%v)",
			speedup, seqDur, parDur)
	}
}

// TestEmbedSpeedupGateMultiCaller8 — 8 concurrent goroutines.
// Asserts >=2.5x. Skips when NumCPU < 8.
func TestEmbedSpeedupGateMultiCaller8(t *testing.T) {
	if runtime.NumCPU() < 8 {
		t.Skipf("requires NumCPU >= 8, got %d", runtime.NumCPU())
	}
	p := loadRealProvider(t)
	defer p.Close()

	const k = 8
	const iters = 8

	_, _ = p.Embed(context.Background(), []string{"warmup"})

	seqStart := time.Now()
	for i := 0; i < k*iters; i++ {
		if _, err := p.Embed(context.Background(), []string{"hello"}); err != nil {
			t.Fatal(err)
		}
	}
	seqDur := time.Since(seqStart)

	parStart := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < k; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if _, err := p.Embed(context.Background(), []string{"hello"}); err != nil {
					t.Errorf("embed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	parDur := time.Since(parStart)

	speedup := float64(seqDur) / float64(parDur)
	t.Logf("seq=%v par=%v speedup=%.2fx", seqDur, parDur, speedup)
	if speedup < 2.5 {
		t.Errorf("speedup %.2fx below 2.5x gate (seq=%v, par=%v)",
			speedup, seqDur, parDur)
	}
}
