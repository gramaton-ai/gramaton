package bert

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// buildTinyModel creates a minimal 2-dim, 1-head, 1-layer BERT model
// as a safetensors file for structural testing.
func buildTinyModel(t *testing.T) (string, ModelConfig) {
	t.Helper()

	cfg := ModelConfig{
		HiddenSize:        2,
		NumAttentionHeads: 1,
		IntermediateSize:  4,
		NumHiddenLayers:   1,
		MaxPositionEmbeds: 8,
		VocabSize:         5,
		LayerNormEps:      1e-12,
	}

	// Build tensors. All values are small deterministic numbers.
	tensors := map[string]struct {
		shape []int
		data  []float32
	}{
		// Embeddings.
		"bert.embeddings.word_embeddings.weight":       {[]int{5, 2}, []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}},
		"bert.embeddings.position_embeddings.weight":   {[]int{8, 2}, make([]float32, 16)},
		"bert.embeddings.token_type_embeddings.weight":  {[]int{2, 2}, make([]float32, 4)},
		"bert.embeddings.LayerNorm.weight":             {[]int{2}, []float32{1, 1}},
		"bert.embeddings.LayerNorm.bias":               {[]int{2}, []float32{0, 0}},

		// Attention Q/K/V/O.
		"bert.encoder.layer.0.attention.self.query.weight":  {[]int{2, 2}, []float32{1, 0, 0, 1}},
		"bert.encoder.layer.0.attention.self.query.bias":    {[]int{2}, []float32{0, 0}},
		"bert.encoder.layer.0.attention.self.key.weight":    {[]int{2, 2}, []float32{1, 0, 0, 1}},
		"bert.encoder.layer.0.attention.self.key.bias":      {[]int{2}, []float32{0, 0}},
		"bert.encoder.layer.0.attention.self.value.weight":  {[]int{2, 2}, []float32{1, 0, 0, 1}},
		"bert.encoder.layer.0.attention.self.value.bias":    {[]int{2}, []float32{0, 0}},
		"bert.encoder.layer.0.attention.output.dense.weight": {[]int{2, 2}, []float32{1, 0, 0, 1}},
		"bert.encoder.layer.0.attention.output.dense.bias":   {[]int{2}, []float32{0, 0}},

		// Attention LayerNorm.
		"bert.encoder.layer.0.attention.output.LayerNorm.weight": {[]int{2}, []float32{1, 1}},
		"bert.encoder.layer.0.attention.output.LayerNorm.bias":   {[]int{2}, []float32{0, 0}},

		// FFN.
		"bert.encoder.layer.0.intermediate.dense.weight": {[]int{4, 2}, []float32{1, 0, 0, 1, 1, 0, 0, 1}},
		"bert.encoder.layer.0.intermediate.dense.bias":   {[]int{4}, []float32{0, 0, 0, 0}},
		"bert.encoder.layer.0.output.dense.weight":       {[]int{2, 4}, []float32{1, 0, 1, 0, 0, 1, 0, 1}},
		"bert.encoder.layer.0.output.dense.bias":         {[]int{2}, []float32{0, 0}},

		// FFN LayerNorm.
		"bert.encoder.layer.0.output.LayerNorm.weight": {[]int{2}, []float32{1, 1}},
		"bert.encoder.layer.0.output.LayerNorm.bias":   {[]int{2}, []float32{0, 0}},
	}

	// Serialize to safetensors format.
	type metaEntry struct {
		DType   string `json:"dtype"`
		Shape   []int  `json:"shape"`
		Offsets [2]int `json:"data_offsets"`
	}

	meta := make(map[string]metaEntry)
	var dataBytes []byte
	offset := 0

	for name, info := range tensors {
		byteLen := len(info.data) * 4
		meta[name] = metaEntry{
			DType:   "F32",
			Shape:   info.shape,
			Offsets: [2]int{offset, offset + byteLen},
		}
		for _, v := range info.data {
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], math.Float32bits(v))
			dataBytes = append(dataBytes, buf[:]...)
		}
		offset += byteLen
	}

	headerJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "tiny.safetensors")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var headerLen [8]byte
	binary.LittleEndian.PutUint64(headerLen[:], uint64(len(headerJSON)))
	f.Write(headerLen[:])
	f.Write(headerJSON)
	f.Write(dataBytes)

	return path, cfg
}

func TestLoadTinyModel(t *testing.T) {
	path, cfg := buildTinyModel(t)

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m, err := LoadModel(st, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if len(m.Layers) != 1 {
		t.Errorf("expected 1 layer, got %d", len(m.Layers))
	}
	if len(m.Embedding.Word) != 5*2 {
		t.Errorf("word embedding size: got %d, want 10", len(m.Embedding.Word))
	}
}

func TestForwardTinyModel(t *testing.T) {
	path, cfg := buildTinyModel(t)

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m, err := LoadModel(st, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Token IDs: [CLS]=2, hello=1, [SEP]=3 -> look up embeddings for IDs 2, 1, 3
	tokenIDs := []int32{2, 1, 3}
	mask := []int32{1, 1, 1}

	result := m.Forward(tokenIDs, mask)

	// Verify output dimension.
	if len(result) != cfg.HiddenSize {
		t.Fatalf("output dim: got %d, want %d", len(result), cfg.HiddenSize)
	}

	// Verify L2 normalized.
	var norm float64
	for _, v := range result {
		norm += float64(v) * float64(v)
	}
	if math.Abs(norm-1.0) > 1e-4 {
		t.Errorf("output not L2 normalized: |v|^2 = %v", norm)
	}

	// Verify deterministic: same input -> same output.
	result2 := m.Forward(tokenIDs, mask)
	for i := range result {
		if result[i] != result2[i] {
			t.Errorf("non-deterministic: result[%d] = %v vs %v", i, result[i], result2[i])
		}
	}
}

func TestForwardSingleToken(t *testing.T) {
	path, cfg := buildTinyModel(t)

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m, err := LoadModel(st, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Just [CLS] + [SEP].
	tokenIDs := []int32{2, 3}
	mask := []int32{1, 1}

	result := m.Forward(tokenIDs, mask)
	if len(result) != cfg.HiddenSize {
		t.Fatalf("output dim: got %d, want %d", len(result), cfg.HiddenSize)
	}

	// Verify L2 normalized.
	var norm float64
	for _, v := range result {
		norm += float64(v) * float64(v)
	}
	if math.Abs(norm-1.0) > 1e-4 {
		t.Errorf("output not L2 normalized: |v|^2 = %v", norm)
	}
}

func TestParseModelConfig(t *testing.T) {
	data := []byte(`{
		"hidden_size": 384,
		"num_attention_heads": 12,
		"intermediate_size": 1536,
		"num_hidden_layers": 12,
		"max_position_embeddings": 512,
		"vocab_size": 30522,
		"layer_norm_eps": 1e-12
	}`)

	cfg, err := ParseModelConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HiddenSize != 384 {
		t.Errorf("hidden_size: got %d, want 384", cfg.HiddenSize)
	}
	if cfg.NumAttentionHeads != 12 {
		t.Errorf("num_attention_heads: got %d, want 12", cfg.NumAttentionHeads)
	}
	if cfg.IntermediateSize != 1536 {
		t.Errorf("intermediate_size: got %d, want 1536", cfg.IntermediateSize)
	}
}

func TestParseModelConfigDefaults(t *testing.T) {
	data := []byte(`{
		"hidden_size": 384,
		"num_attention_heads": 12,
		"intermediate_size": 1536,
		"num_hidden_layers": 12,
		"max_position_embeddings": 512,
		"vocab_size": 30522
	}`)

	cfg, err := ParseModelConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LayerNormEps != 1e-12 {
		t.Errorf("layer_norm_eps: got %v, want 1e-12", cfg.LayerNormEps)
	}
}

func TestParseModelConfigMissing(t *testing.T) {
	data := []byte(`{"hidden_size": 384}`)
	_, err := ParseModelConfig(data)
	if err == nil {
		t.Error("expected error for missing fields")
	}
}
