package bert

// Layer E benchmarks for BERT embedding throughput. These exercise
// the real bge-small-en-v1.5 model when present in
// ~/.gramaton/models/. Skipped automatically when the model isn't
// cached (no download in benchmarks).
//
// Run with:
//   go test -bench=. -benchtime=10x ./embed/bert/
//
// Measured on Apple M3 (8-core, default max_workers=8):
//   BenchmarkEmbedSequential:           62 ms/op
//   BenchmarkEmbedSingleCallSize/N=1:   62 ms/op (62 ms/text)
//   BenchmarkEmbedSingleCallSize/N=8:  110 ms/op (~14 ms/text, 4.4x)
//   BenchmarkEmbedSingleCallSize/N=64: 716 ms/op (~11 ms/text, 5.6x)
//   BenchmarkEmbedSingleCallSize/N=500: 5.5 s/op (~11 ms/text, 5.6x)
//   BenchmarkEmbedMultiCaller/k=1-8:    22-23 ms/op effective
//
// N=500 in 5.5s is well under the F1 wall-clock target of 15s.
// Memory-bandwidth-bound matmul tops out around 5-6x speedup on
// 8 cores in practice; Layer F's gates use conservative thresholds
// (>=1.8x at k=4, >=2.5x at k=8) so they don't false-fail on
// slower CPUs.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

// loadBenchProvider opens a cached BERT provider for benchmarks.
// Returns nil if the model isn't cached locally — caller skips.
func loadBenchProvider(b *testing.B) *Provider {
	b.Helper()
	dir := ModelDir(DefaultModel)
	if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err != nil {
		b.Skip("benchmark requires cached BERT model at " + dir)
	}
	p, err := New(config.EmbeddingConfig{Provider: "bert", Model: DefaultModel})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	return p
}

// BenchmarkEmbedSequential measures the cost of a single embed call
// with one text. Baseline for ms-per-text on the test machine.
func BenchmarkEmbedSequential(b *testing.B) {
	p := loadBenchProvider(b)
	if p == nil {
		return
	}
	defer p.Close()

	ctx := context.Background()
	text := []string{"the quick brown fox jumps over the lazy dog"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Embed(ctx, text); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEmbedSingleCallSize measures one Embed call with N
// texts at varying batch sizes. Layer D's inner-loop fanout
// should produce sub-linear scaling as N grows past 1.
func BenchmarkEmbedSingleCallSize(b *testing.B) {
	p := loadBenchProvider(b)
	if p == nil {
		return
	}
	defer p.Close()

	ctx := context.Background()
	for _, n := range []int{1, 8, 64, 500} {
		texts := make([]string, n)
		for i := range texts {
			texts[i] = "the quick brown fox jumps over the lazy dog"
		}
		b.Run("N="+itoa(n), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := p.Embed(ctx, texts); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkEmbedMultiCaller measures k concurrent goroutines each
// calling Embed with a single text. Layer C's RWMutex pattern
// should give near-linear scaling up to memory-bandwidth limits.
func BenchmarkEmbedMultiCaller(b *testing.B) {
	p := loadBenchProvider(b)
	if p == nil {
		return
	}
	defer p.Close()

	ctx := context.Background()
	text := []string{"the quick brown fox jumps over the lazy dog"}

	for _, k := range []int{1, 2, 4, 8} {
		b.Run("k="+itoa(k), func(b *testing.B) {
			b.SetParallelism(k)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := p.Embed(ctx, text); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkEmbedScratchPoolPressure is a stress test for pool
// allocation under burst concurrency. 32 goroutines each issuing
// a steady stream of Embeds; the pool should grow then stabilize.
func BenchmarkEmbedScratchPoolPressure(b *testing.B) {
	p := loadBenchProvider(b)
	if p == nil {
		return
	}
	defer p.Close()

	ctx := context.Background()
	text := []string{"hello"}

	const k = 32
	b.SetParallelism(k)
	b.ResetTimer()
	var wg sync.WaitGroup
	for g := 0; g < k; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < b.N/k+1; i++ {
				if _, err := p.Embed(ctx, text); err != nil {
					b.Errorf("embed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// itoa avoids the strconv import; small integers only.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
