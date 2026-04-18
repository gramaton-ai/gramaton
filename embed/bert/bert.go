package bert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gramaton-ai/gramaton/config"
)

// Provider implements embed.Provider using a pure Go BERT inference engine.
// Default model is bge-small-en-v1.5 (384-dim, 12-layer BERT encoder).
// Thread-safe: uses a mutex to serialize forward passes (scratch buffers
// are shared). Multiple concurrent Embed calls are serialized but each
// is fast enough (~100-200ms) that this is not a bottleneck.
type Provider struct {
	model     *Model
	tokenizer *Tokenizer
	st        *SafeTensors
	modelID   string
	ctxWindow int
	mu        sync.Mutex
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
		parts := filepath.SplitList(model)
		if len(parts) > 0 {
			model = parts[len(parts)-1]
		}
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
	// (P0-10: tokenizer.json's truncation.max_length defaults to 512
	// but custom models may declare higher.)
	if tok.MaxLen() > modelCfg.MaxPositionEmbeds {
		tok.SetMaxLen(modelCfg.MaxPositionEmbeds)
	}

	return &Provider{
		model:     m,
		tokenizer: tok,
		st:        st,
		modelID:   model,
		ctxWindow: modelCfg.MaxPositionEmbeds,
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

		p.mu.Lock()
		// Re-check under the lock: a concurrent Close may have
		// nil'd these out. Returning an error is preferable to a
		// nil dereference; the only legitimate caller pattern is
		// "stop submitting before Close" anyway.
		if p.tokenizer == nil || p.model == nil {
			p.mu.Unlock()
			return nil, fmt.Errorf("bert: provider closed")
		}
		ids, mask, _ := p.tokenizer.Encode(text)
		embedding := p.model.Forward(ids, mask)
		p.mu.Unlock()

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

// Close releases the mmap'd safetensors file. Takes the same mutex
// as Embed so an in-flight Forward pass cannot read float32 slices
// (which point into the mmap'd region) after Munmap. Without this
// guard, a concurrent Embed during shutdown would segfault.
// (Wave 7 P1-33.)
//
// Callers must NOT call Embed after Close returns; the model and
// tokenizer fields are zeroed to make subsequent misuse panic
// loudly rather than silently corrupt.
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
	return err
}
