package bert

import (
	"context"
	"errors"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
