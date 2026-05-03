package bert

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// setupTinyProviderFiles writes config.json and tokenizer.json to
// the directory containing the safetensors at path, then renames
// the safetensors to model.safetensors. Returns the directory.
func setupTinyProviderFiles(t *testing.T, path string, _ ModelConfig) string {
	t.Helper()
	dir := filepath.Dir(path)
	cfgJSON := `{
		"hidden_size": 2,
		"num_attention_heads": 1,
		"intermediate_size": 4,
		"num_hidden_layers": 1,
		"max_position_embeddings": 8,
		"vocab_size": 5,
		"layer_norm_eps": 1e-12
	}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0600); err != nil {
		t.Fatal(err)
	}
	tokJSON := `{
		"model": {"type": "WordPiece", "vocab": {
			"[PAD]": 0, "[UNK]": 1, "[CLS]": 2, "[SEP]": 3, "hello": 4
		}},
		"added_tokens": [],
		"normalizer": {"lowercase": true, "strip_accents": true}
	}`
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), []byte(tokJSON), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(dir, "model.safetensors")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// openTinyProvider opens the safetensors + loads the model + parses
// the tokenizer for a previously-set-up tiny provider directory.
func openTinyProvider(t *testing.T, dir string, cfg ModelConfig) (*SafeTensors, *Model, *Tokenizer) {
	t.Helper()
	st, err := OpenSafeTensors(filepath.Join(dir, "model.safetensors"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := LoadModel(st, cfg)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	tokData, err := os.ReadFile(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	tok, err := NewTokenizerFromJSON(tokData)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, m, tok
}

// newWithPool is a test-only Provider constructor that bypasses the
// download/load path. Same shape as the production New, but takes
// pre-loaded model/tokenizer/safetensors. Used by tests that build
// tiny synthetic models. Layer B introduced this helper so tests
// don't repeat the pool-initialization boilerplate.
func newWithPool(m *Model, tok *Tokenizer, st *SafeTensors, modelID string, cfg ModelConfig) *Provider {
	return &Provider{
		model:     m,
		tokenizer: tok,
		st:        st,
		modelID:   modelID,
		ctxWindow: cfg.MaxPositionEmbeds,
		modelCfg:  cfg,
		scratchPool: &sync.Pool{
			New: func() any {
				return NewScratch(cfg.MaxPositionEmbeds, cfg)
			},
		},
	}
}

// instrumentedPool wraps a sync.Pool to track Get/Put events for
// concurrency tests. Implements scratchPoolHook.
type instrumentedPool struct {
	mu      sync.Mutex
	gets    atomic.Int64
	puts    atomic.Int64
	live    map[*Scratch]int // pointer -> goroutine count holding it
	maxLive atomic.Int64
}

func newInstrumentedPool() *instrumentedPool {
	return &instrumentedPool{live: map[*Scratch]int{}}
}

func (p *instrumentedPool) OnGet(s *Scratch) {
	p.gets.Add(1)
	p.mu.Lock()
	p.live[s]++
	if int64(len(p.live)) > p.maxLive.Load() {
		p.maxLive.Store(int64(len(p.live)))
	}
	p.mu.Unlock()
}

func (p *instrumentedPool) OnPut(s *Scratch) {
	p.puts.Add(1)
	p.mu.Lock()
	p.live[s]--
	if p.live[s] == 0 {
		delete(p.live, s)
	}
	p.mu.Unlock()
}

// TestEmbedScratchReuse verifies the pool reuses Scratch instances
// across sequential Embed calls. After N calls, at most a few unique
// Scratch pointers should have been allocated (sync.Pool keeps
// returning the same pointer when contention is zero).
func TestEmbedScratchReuse(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)

	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-reuse", cfg)
	defer p.Close()

	hook := newInstrumentedPool()
	p.poolHook = hook

	// Run many sequential embeds; pool should reuse the same scratch.
	for i := 0; i < 50; i++ {
		_, err := p.Embed(context.Background(), []string{"hello"})
		if err != nil {
			t.Fatalf("embed %d: %v", i, err)
		}
	}

	if hook.gets.Load() != 50 {
		t.Errorf("got %d Gets, want 50", hook.gets.Load())
	}
	if hook.puts.Load() != 50 {
		t.Errorf("got %d Puts, want 50", hook.puts.Load())
	}
	// Sequential reuse means the pool's "live" map should have been
	// at most 1 at any moment. Layer B still serializes via outer
	// mutex; Layer C will increase this.
	if hook.maxLive.Load() > 1 {
		t.Errorf("maxLive=%d, want <=1 in sequential mode", hook.maxLive.Load())
	}
}

// TestEmbedConcurrentScratchDistinct exercises the case where
// multiple goroutines call Embed in parallel. Layer B still
// serializes via the outer mutex, so concurrent goroutines actually
// run sequentially through Embed; maxLive=1 holds. Layer C lifts
// this; until then, the test confirms the instrumentation works
// and the pool is structurally ready for concurrent access.
func TestEmbedConcurrentScratchDistinct(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)

	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "test-concurrent", cfg)
	defer p.Close()

	hook := newInstrumentedPool()
	p.poolHook = hook

	const goroutines = 8
	release := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			for i := 0; i < 10; i++ {
				_, err := p.Embed(context.Background(), []string{"hello"})
				if err != nil {
					t.Errorf("embed: %v", err)
					return
				}
			}
		}()
	}
	close(release)
	wg.Wait()

	if hook.gets.Load() != goroutines*10 {
		t.Errorf("got %d Gets, want %d", hook.gets.Load(), goroutines*10)
	}
	if hook.gets.Load() != hook.puts.Load() {
		t.Errorf("Get/Put imbalance: gets=%d puts=%d",
			hook.gets.Load(), hook.puts.Load())
	}
	// In Layer B, outer mutex still serializes -> maxLive==1.
	// This is documented and expected; Layer C will change.
	if hook.maxLive.Load() != 1 {
		t.Errorf("maxLive=%d (Layer B): expected exactly 1 since outer mutex serializes Embed",
			hook.maxLive.Load())
	}
}
