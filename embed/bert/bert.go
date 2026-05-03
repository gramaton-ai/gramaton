package bert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gramaton-ai/gramaton/config"
)

// scratchPoolHook lets tests instrument Get/Put without modifying
// production code. nil in production. Set via newWithPool in
// bert_test.go.
type scratchPoolHook interface {
	OnGet(s *Scratch)
	OnPut(s *Scratch)
}

// Provider implements embed.Provider using a pure Go BERT inference engine.
// Default model is bge-small-en-v1.5 (384-dim, 12-layer BERT encoder).
//
// Thread-safety (RWMutex pattern):
// - Embed takes RLock for the duration of each per-text Encode +
//   Forward. Multiple goroutines can hold RLocks concurrently; the
//   model is read-only after LoadModel and each Embed iteration uses
//   its own Scratch from the pool, so concurrent Forward is safe.
// - Close takes the full Lock and blocks until every in-flight
//   RLock holder releases. After Close returns, model/tokenizer/
//   scratchPool/st are all nil; Embed checks under RLock and returns
//   "bert: provider closed" cleanly without segfault.
//
// Critical: the RLock must wrap BOTH the nil-check AND Encode +
// Forward. Releasing RLock between them would let Close Munmap the
// safetensors region while Forward is mid-read of float32 slices
// that point into mmap'd memory.
//
// scratchPool holds Scratch instances reused across Forward calls.
// Each Embed iteration acquires a Scratch from the pool, runs
// Forward, returns the Scratch to the pool. Concurrent Embed
// goroutines each get their own Scratch instance.
//
// Memory bound: each Scratch is ~14MB at maxSeq=512, hidden=384,
// intermediate=1536, heads=12. The pool grows under contention and
// shrinks during idle (sync.Pool semantics; entries are GC-eligible
// when not referenced). Peak live Scratches with the current outer-
// loop in Embed = number of concurrent goroutines calling Embed.
// Layer D will introduce inner-loop fanout with bounded workers
// (default cap 8 = ~112MB).
type Provider struct {
	model       *Model
	tokenizer   *Tokenizer
	st          *SafeTensors
	modelID     string
	ctxWindow   int
	modelCfg    ModelConfig // retained for pool factory
	scratchPool *sync.Pool
	poolHook    scratchPoolHook // test-only; nil in production
	mu          sync.RWMutex
}

// New creates a BERT embedding provider. Downloads the model from
// HuggingFace on first use if not cached locally.
func New(cfg config.EmbeddingConfig) (*Provider, error) {
	model := cfg.Model
	if model == "" {
		model = DefaultModel
	}

	repo := DefaultModelRepo
	// Allow custom models by setting model to the HuggingFace repo path.
	// e.g., "BAAI/bge-small-en-v1.5" or just "bge-small-en-v1.5" for default.
	if model != DefaultModel {
		repo = model
		// Use the last path component as the directory name.
		// filepath.SplitList splits on the OS PATH separator (':' on
		// Unix), not '/' -- so a HF repo path "BAAI/bge-..." used to
		// produce a single-element slice with the slash intact, then
		// ModelDir would create two nested dirs.
		model = filepath.Base(model)
	}

	// Ensure model files are downloaded.
	if err := EnsureModel(context.Background(), repo, model, nil); err != nil {
		return nil, fmt.Errorf("bert: ensure model: %w", err)
	}

	dir := ModelDir(model)

	// Load model config.
	cfgData, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("bert: read config.json: %w", err)
	}
	modelCfg, err := ParseModelConfig(cfgData)
	if err != nil {
		return nil, fmt.Errorf("bert: parse config: %w", err)
	}

	// Open safetensors weights.
	st, err := OpenSafeTensors(filepath.Join(dir, "model.safetensors"))
	if err != nil {
		return nil, fmt.Errorf("bert: open weights: %w", err)
	}

	// Load model.
	m, err := LoadModel(st, modelCfg)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("bert: load model: %w", err)
	}

	// Load tokenizer.
	tokData, err := os.ReadFile(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("bert: read tokenizer.json: %w", err)
	}
	tok, err := NewTokenizerFromJSON(tokData)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("bert: load tokenizer: %w", err)
	}

	// Clamp tokenizer.maxLen to the model's MaxPositionEmbeds. Scratch
	// buffers in model.Forward are sized from MaxPositionEmbeds; if the
	// tokenizer emits a longer sequence the buffer slicing panics.
	// (tokenizer.json's truncation.max_length defaults to 512 but
	// custom models may declare higher.)
	if tok.MaxLen() > modelCfg.MaxPositionEmbeds {
		tok.SetMaxLen(modelCfg.MaxPositionEmbeds)
	}

	return &Provider{
		model:     m,
		tokenizer: tok,
		st:        st,
		modelID:   model,
		ctxWindow: modelCfg.MaxPositionEmbeds,
		modelCfg:  modelCfg,
		scratchPool: &sync.Pool{
			New: func() any {
				return NewScratch(modelCfg.MaxPositionEmbeds, modelCfg)
			},
		},
	}, nil
}

// Embed generates embeddings for the given texts. Returns one 384-dim
// vector per input text in the same order. Returns nil, nil for empty input.
func (p *Provider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(texts))
	for i, text := range texts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		p.mu.RLock()
		// Re-check under the RLock: a concurrent Close holds the
		// write Lock, blocking us if it's mid-cleanup. Once we hold
		// RLock, the fields can't be nil'd until we release.
		// Returning an error is preferable to a nil dereference;
		// the only legitimate caller pattern is "stop submitting
		// before Close" anyway.
		if p.tokenizer == nil || p.model == nil || p.scratchPool == nil {
			p.mu.RUnlock()
			return nil, fmt.Errorf("bert: provider closed")
		}
		ids, mask, _ := p.tokenizer.Encode(text)
		s := p.scratchPool.Get().(*Scratch)
		if p.poolHook != nil {
			p.poolHook.OnGet(s)
		}
		embedding := p.model.Forward(s, ids, mask)
		if p.poolHook != nil {
			p.poolHook.OnPut(s)
		}
		p.scratchPool.Put(s)
		p.mu.RUnlock()

		results[i] = embedding
	}

	return results, nil
}

// ModelID returns the model identifier for embedding provenance tracking.
func (p *Provider) ModelID() string {
	return p.modelID
}

// ContextWindow returns the model's maximum sequence length in tokens.
func (p *Provider) ContextWindow() int {
	return p.ctxWindow
}

// Close releases the mmap'd safetensors file. Takes the full write
// Lock; blocks until every concurrent Embed holding RLock has
// released. Without this guard, a concurrent Forward could read
// float32 slices that point into the mmap'd region after Munmap,
// causing a segfault.
//
// Callers must NOT call Embed after Close returns; the model,
// tokenizer, and scratchPool fields are zeroed to make subsequent
// misuse return "bert: provider closed" rather than silently corrupt.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st == nil {
		return nil
	}
	err := p.st.Close()
	p.st = nil
	p.model = nil
	p.tokenizer = nil
	p.scratchPool = nil
	return err
}
