package bert

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestProviderTiny tests the Provider with the tiny synthetic model.
// Does not require downloading the real model.
func TestProviderTiny(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := setupTinyProviderFiles(t, path, cfg)
	st, m, tok := openTinyProvider(t, dir, cfg)
	p := newWithPool(m, tok, st, "tiny-test", cfg)
	defer p.Close()

	// Test Embed with empty input.
	result, err := p.Embed(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}

	// Test Embed with single text.
	result, err = p.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if len(result[0]) != cfg.HiddenSize {
		t.Errorf("embedding dim: got %d, want %d", len(result[0]), cfg.HiddenSize)
	}

	// Verify L2 normalized.
	var norm float64
	for _, v := range result[0] {
		norm += float64(v) * float64(v)
	}
	if math.Abs(norm-1.0) > 1e-4 {
		t.Errorf("not L2 normalized: |v|^2 = %v", norm)
	}

	// Test Embed with multiple texts.
	result, err = p.Embed(context.Background(), []string{"hello", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}

	// Same text should produce same embedding.
	for i := range result[0] {
		if result[0][i] != result[1][i] {
			t.Errorf("same text should produce same embedding: [0][%d]=%v vs [1][%d]=%v",
				i, result[0][i], i, result[1][i])
		}
	}

	// Test ModelID.
	if p.ModelID() != "tiny-test" {
		t.Errorf("ModelID: got %q, want %q", p.ModelID(), "tiny-test")
	}

	// Test ContextWindow.
	if p.ContextWindow() != 8 {
		t.Errorf("ContextWindow: got %d, want 8", p.ContextWindow())
	}
}

func TestProviderContextCancellation(t *testing.T) {
	path, cfg := buildTinyModel(t)
	dir := filepath.Dir(path)

	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{
		"hidden_size": 2, "num_attention_heads": 1,
		"intermediate_size": 4, "num_hidden_layers": 1,
		"max_position_embeddings": 8, "vocab_size": 5
	}`), 0600)
	os.WriteFile(filepath.Join(dir, "tokenizer.json"), []byte(`{
		"model": {"type": "WordPiece", "vocab": {"[PAD]":0,"[UNK]":1,"[CLS]":2,"[SEP]":3,"a":4}},
		"added_tokens": []
	}`), 0600)
	os.Rename(path, filepath.Join(dir, "model.safetensors"))

	st, _ := OpenSafeTensors(filepath.Join(dir, "model.safetensors"))
	m, _ := LoadModel(st, cfg)
	tokData, _ := os.ReadFile(filepath.Join(dir, "tokenizer.json"))
	tok, _ := NewTokenizerFromJSON(tokData)

	p := newWithPool(m, tok, st, "test", cfg)
	defer p.Close()

	// Cancel context before calling Embed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Embed(ctx, []string{"a", "a", "a"})
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

// TestGoldenBGESmall validates against the real bge-small-en-v1.5 model.
// Requires GRAMATON_TEST_BERT=1 and model downloaded to ~/.gramaton/models/.
func TestGoldenBGESmall(t *testing.T) {
	if os.Getenv("GRAMATON_TEST_BERT") != "1" {
		t.Skip("set GRAMATON_TEST_BERT=1 to run golden tests (requires model download)")
	}

	dir := ModelDir(DefaultModel)

	// Check if model exists.
	if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err != nil {
		// Try to download.
		t.Log("Downloading bge-small-en-v1.5 for golden tests...")
		if err := EnsureModel(context.Background(), DefaultModelRepo, DefaultModel, func(msg string) {
			t.Log(msg)
		}); err != nil {
			t.Fatalf("download model: %v", err)
		}
	}

	// Load config.
	cfgData, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseModelConfig(cfgData)
	if err != nil {
		t.Fatal(err)
	}

	// Verify expected config.
	if cfg.HiddenSize != 384 {
		t.Fatalf("unexpected hidden_size: %d", cfg.HiddenSize)
	}
	if cfg.NumHiddenLayers != 6 && cfg.NumHiddenLayers != 12 {
		t.Fatalf("unexpected num_hidden_layers: %d", cfg.NumHiddenLayers)
	}

	// Load model.
	st, err := OpenSafeTensors(filepath.Join(dir, "model.safetensors"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m, err := LoadModel(st, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Load tokenizer.
	tokData, err := os.ReadFile(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := NewTokenizerFromJSON(tokData)
	if err != nil {
		t.Fatal(err)
	}

	// Run forward pass on a simple input.
	ids, mask, _ := tok.Encode("hello world")
	t.Logf("tokens: %v (len=%d)", ids, len(ids))

	scratch := NewScratch(cfg.MaxPositionEmbeds, cfg)
	embedding := m.Forward(scratch, ids, mask)

	// Verify dimension.
	if len(embedding) != 384 {
		t.Fatalf("embedding dim: got %d, want 384", len(embedding))
	}

	// Verify L2 normalized.
	var norm float64
	for _, v := range embedding {
		norm += float64(v) * float64(v)
	}
	if math.Abs(norm-1.0) > 1e-4 {
		t.Errorf("not L2 normalized: |v|^2 = %v", norm)
	}

	// Verify not all zeros or all same value.
	allSame := true
	for _, v := range embedding[1:] {
		if v != embedding[0] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Error("embedding is degenerate (all same value)")
	}

	// Log first few dims for manual comparison with Python.
	t.Logf("embedding[:5] = %v", embedding[:5])

	// Test that different texts produce different embeddings.
	// Reuse the scratch to also exercise the reuse-safety property.
	ids2, mask2, _ := tok.Encode("the quick brown fox")
	embedding2 := m.Forward(scratch, ids2, mask2)

	// Compute cosine similarity.
	var dot float64
	for i := range embedding {
		dot += float64(embedding[i]) * float64(embedding2[i])
	}
	t.Logf("cosine similarity('hello world', 'the quick brown fox') = %v", dot)

	// They should NOT be identical.
	if dot > 0.999 {
		t.Error("different texts produced nearly identical embeddings")
	}
}
