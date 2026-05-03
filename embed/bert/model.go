package bert

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
)

// Model holds the weights and configuration for BERT inference.
// Weights are backed by mmap'd safetensors data (zero-copy).
//
// Read-only after LoadModel. Concurrent Forward calls are safe
// when each caller supplies its own Scratch.
type Model struct {
	Config    ModelConfig
	Embedding EmbeddingWeights
	Layers    []EncoderLayer
}

// ModelConfig holds the BERT model hyperparameters, typically
// loaded from config.json in the model directory.
type ModelConfig struct {
	HiddenSize        int     `json:"hidden_size"`
	NumAttentionHeads int     `json:"num_attention_heads"`
	IntermediateSize  int     `json:"intermediate_size"`
	NumHiddenLayers   int     `json:"num_hidden_layers"`
	MaxPositionEmbeds int     `json:"max_position_embeddings"`
	VocabSize         int     `json:"vocab_size"`
	LayerNormEps      float64 `json:"layer_norm_eps"`
}

// EmbeddingWeights holds the token, position, and segment embedding
// tables plus the post-embedding layer norm.
type EmbeddingWeights struct {
	Word     []float32 // [VocabSize, HiddenSize]
	Position []float32 // [MaxPositionEmbeds, HiddenSize]
	TokenType []float32 // [2, HiddenSize]
	LNWeight []float32 // [HiddenSize]
	LNBias   []float32 // [HiddenSize]
}

// EncoderLayer holds weights for one transformer encoder layer.
type EncoderLayer struct {
	Attn      AttentionWeights
	AttnLN    LayerNormWeights
	FFNUp     LinearWeights // HiddenSize -> IntermediateSize
	FFNDown   LinearWeights // IntermediateSize -> HiddenSize
	FFNLN     LayerNormWeights
}

// AttentionWeights holds Q, K, V projection weights and the output
// projection for multi-head self-attention.
type AttentionWeights struct {
	Q LinearWeights // [HiddenSize, HiddenSize]
	K LinearWeights
	V LinearWeights
	O LinearWeights // output projection
}

// LinearWeights holds weight and bias for a linear layer.
// Weight is stored as [OutFeatures, InFeatures] (transposed for MatMul).
type LinearWeights struct {
	Weight []float32 // [Out, In] -- already transposed for MatMul
	Bias   []float32 // [Out]
}

// LayerNormWeights holds weight and bias for layer normalization.
type LayerNormWeights struct {
	Weight []float32 // [HiddenSize]
	Bias   []float32 // [HiddenSize]
}

// Scratch holds pre-allocated buffers used by Forward. Each Forward
// call writes every buffer location before reading (verified in
// Layer A audit), so a recycled Scratch is safe to reuse across
// calls without zeroing.
//
// Each goroutine running Forward must supply its own Scratch.
// Concurrent reuse of the same Scratch instance corrupts output.
type Scratch struct {
	hidden  []float32 // [maxSeq, hiddenSize]
	qkv     []float32 // [maxSeq, hiddenSize*3] for fused Q,K,V
	attn    []float32 // [numHeads, maxSeq, maxSeq]
	context []float32 // [maxSeq, hiddenSize]
	ffn     []float32 // [maxSeq, intermediateSize]
	proj    []float32 // [maxSeq, hiddenSize]
}

// NewScratch allocates a Scratch sized for the given configuration.
// Sized for max sequence length so the same Scratch handles any
// input up to MaxPositionEmbeds tokens.
func NewScratch(maxSeq int, cfg ModelConfig) *Scratch {
	h := cfg.HiddenSize
	return &Scratch{
		hidden:  make([]float32, maxSeq*h),
		qkv:     make([]float32, maxSeq*h*3),
		attn:    make([]float32, cfg.NumAttentionHeads*maxSeq*maxSeq),
		context: make([]float32, maxSeq*h),
		ffn:     make([]float32, maxSeq*cfg.IntermediateSize),
		proj:    make([]float32, maxSeq*h),
	}
}

// ParseModelConfig reads a HuggingFace config.json file.
func ParseModelConfig(data []byte) (ModelConfig, error) {
	var cfg ModelConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("model config: %w", err)
	}
	if cfg.HiddenSize == 0 || cfg.NumAttentionHeads == 0 || cfg.NumHiddenLayers == 0 {
		return cfg, fmt.Errorf("model config: missing required fields (hidden_size=%d, heads=%d, layers=%d)",
			cfg.HiddenSize, cfg.NumAttentionHeads, cfg.NumHiddenLayers)
	}
	if cfg.LayerNormEps == 0 {
		cfg.LayerNormEps = 1e-12 // BERT default
	}
	return cfg, nil
}

// LoadModel loads a BERT model from a safetensors file using the given config.
func LoadModel(st *SafeTensors, cfg ModelConfig) (*Model, error) {
	m := &Model{
		Config: cfg,
		Layers: make([]EncoderLayer, cfg.NumHiddenLayers),
	}

	var err error

	// Embedding weights.
	m.Embedding.Word, err = getWeight(st, "embeddings.word_embeddings.weight")
	if err != nil {
		return nil, err
	}
	m.Embedding.Position, err = getWeight(st, "embeddings.position_embeddings.weight")
	if err != nil {
		return nil, err
	}
	m.Embedding.TokenType, err = getWeight(st, "embeddings.token_type_embeddings.weight")
	if err != nil {
		return nil, err
	}
	m.Embedding.LNWeight, err = getWeight(st, "embeddings.LayerNorm.weight")
	if err != nil {
		return nil, err
	}
	m.Embedding.LNBias, err = getWeight(st, "embeddings.LayerNorm.bias")
	if err != nil {
		return nil, err
	}

	// Encoder layers.
	for i := 0; i < cfg.NumHiddenLayers; i++ {
		p := fmt.Sprintf("encoder.layer.%d.", i)
		layer := &m.Layers[i]

		// Self-attention.
		if layer.Attn.Q, err = loadLinear(st, p+"attention.self.query"); err != nil {
			return nil, err
		}
		if layer.Attn.K, err = loadLinear(st, p+"attention.self.key"); err != nil {
			return nil, err
		}
		if layer.Attn.V, err = loadLinear(st, p+"attention.self.value"); err != nil {
			return nil, err
		}
		if layer.Attn.O, err = loadLinear(st, p+"attention.output.dense"); err != nil {
			return nil, err
		}

		// Attention layer norm.
		if layer.AttnLN.Weight, err = getWeight(st, p+"attention.output.LayerNorm.weight"); err != nil {
			return nil, err
		}
		if layer.AttnLN.Bias, err = getWeight(st, p+"attention.output.LayerNorm.bias"); err != nil {
			return nil, err
		}

		// FFN.
		if layer.FFNUp, err = loadLinear(st, p+"intermediate.dense"); err != nil {
			return nil, err
		}
		if layer.FFNDown, err = loadLinear(st, p+"output.dense"); err != nil {
			return nil, err
		}

		// FFN layer norm.
		if layer.FFNLN.Weight, err = getWeight(st, p+"output.LayerNorm.weight"); err != nil {
			return nil, err
		}
		if layer.FFNLN.Bias, err = getWeight(st, p+"output.LayerNorm.bias"); err != nil {
			return nil, err
		}
	}

	return m, nil
}

// Forward runs the BERT encoder and returns the CLS embedding (L2-normalized).
// tokenIDs and attentionMask must be the same length (<= MaxPositionEmbeds).
//
// The caller supplies a Scratch sized for the model's MaxPositionEmbeds.
// Each goroutine calling Forward concurrently must use its own Scratch.
func (m *Model) Forward(s *Scratch, tokenIDs, attentionMask []int32) []float32 {
	seqLen := len(tokenIDs)
	h := m.Config.HiddenSize
	eps := m.Config.LayerNormEps

	// --- Embedding lookup ---
	hidden := s.hidden[:seqLen*h]
	for i, tid := range tokenIDs {
		row := hidden[i*h : (i+1)*h]
		wOff := int(tid) * h
		pOff := i * h
		word := m.Embedding.Word[wOff : wOff+h]
		pos := m.Embedding.Position[pOff : pOff+h]
		// Token type is always 0 for single-segment.
		tt := m.Embedding.TokenType[0:h]
		for j := 0; j < h; j++ {
			row[j] = word[j] + pos[j] + tt[j]
		}
	}
	LayerNorm(hidden, m.Embedding.LNWeight, m.Embedding.LNBias, seqLen, h, eps)

	// --- Encoder layers ---
	numHeads := m.Config.NumAttentionHeads
	headDim := h / numHeads
	attnScale := float32(1.0 / math.Sqrt(float64(headDim)))

	for li := range m.Layers {
		layer := &m.Layers[li]

		// Self-attention: compute Q, K, V projections.
		q := s.qkv[:seqLen*h]
		k := s.qkv[seqLen*h : 2*seqLen*h]
		v := s.qkv[2*seqLen*h : 3*seqLen*h]

		ZeroSlice(q)
		ZeroSlice(k)
		ZeroSlice(v)
		MatMulAdd(hidden, layer.Attn.Q.Weight, seqLen, h, h, layer.Attn.Q.Bias, q)
		MatMulAdd(hidden, layer.Attn.K.Weight, seqLen, h, h, layer.Attn.K.Bias, k)
		MatMulAdd(hidden, layer.Attn.V.Weight, seqLen, h, h, layer.Attn.V.Bias, v)

		// Multi-head attention.
		context := s.context[:seqLen*h]
		attnBuf := s.attn[:numHeads*seqLen*seqLen]

		for head := 0; head < numHeads; head++ {
			// Extract per-head Q, K slices (interleaved in the full projection).
			// Q[seq, head, headDim] is stored as Q[seq, numHeads*headDim]
			// We need to compute: scores[seq, seq] = Q_h @ K_h^T / sqrt(d)

			// Compute attention scores for this head.
			scores := attnBuf[head*seqLen*seqLen : (head+1)*seqLen*seqLen]
			for i := 0; i < seqLen; i++ {
				qi := q[i*h+head*headDim : i*h+head*headDim+headDim]
				for j := 0; j < seqLen; j++ {
					kj := k[j*h+head*headDim : j*h+head*headDim+headDim]
					var dot float32
					for d := 0; d < headDim; d++ {
						dot += qi[d] * kj[d]
					}
					scores[i*seqLen+j] = dot * attnScale
				}
			}

			// Apply attention mask and softmax.
			SoftmaxMasked(scores, seqLen, seqLen, attentionMask)

			// Weighted sum of values.
			for i := 0; i < seqLen; i++ {
				ci := context[i*h+head*headDim : i*h+head*headDim+headDim]
				for d := 0; d < headDim; d++ {
					ci[d] = 0
				}
				for j := 0; j < seqLen; j++ {
					s := scores[i*seqLen+j]
					vj := v[j*h+head*headDim : j*h+head*headDim+headDim]
					for d := 0; d < headDim; d++ {
						ci[d] += s * vj[d]
					}
				}
			}
		}

		// Output projection + residual + layer norm.
		proj := s.proj[:seqLen*h]
		ZeroSlice(proj)
		MatMulAdd(context, layer.Attn.O.Weight, seqLen, h, h, layer.Attn.O.Bias, proj)

		// Residual connection.
		for i := range proj[:seqLen*h] {
			proj[i] += hidden[i]
		}
		// Copy to hidden for next residual.
		copy(hidden, proj[:seqLen*h])
		LayerNorm(hidden, layer.AttnLN.Weight, layer.AttnLN.Bias, seqLen, h, eps)

		// Feed-forward network.
		ffn := s.ffn[:seqLen*m.Config.IntermediateSize]
		ZeroSlice(ffn)
		MatMulAdd(hidden, layer.FFNUp.Weight, seqLen, h, m.Config.IntermediateSize, layer.FFNUp.Bias, ffn)
		GELU(ffn)

		ZeroSlice(proj)
		MatMulAdd(ffn, layer.FFNDown.Weight, seqLen, m.Config.IntermediateSize, h, layer.FFNDown.Bias, proj)

		// Residual + layer norm.
		for i := range proj[:seqLen*h] {
			proj[i] += hidden[i]
		}
		copy(hidden, proj[:seqLen*h])
		LayerNorm(hidden, layer.FFNLN.Weight, layer.FFNLN.Bias, seqLen, h, eps)
	}

	// --- CLS token extraction + L2 normalize ---
	cls := make([]float32, h)
	copy(cls, hidden[:h])
	L2Normalize(cls)
	return cls
}

// getWeight looks up a tensor by trying common BERT weight naming
// prefixes in priority order. If multiple prefixes match the same
// suffix (e.g. an HF re-export carrying both "bert.X" and "model.X"
// for the same parameter), the higher-priority prefix wins -- but
// the ambiguity is logged once at Warn so silent inference variance
// across multi-format files is observable.
func getWeight(st *SafeTensors, suffix string) ([]float32, error) {
	prefixes := []string{"bert.", "model.", ""}
	chosen := -1
	for i, prefix := range prefixes {
		if st.Has(prefix + suffix) {
			if chosen < 0 {
				chosen = i
				continue
			}
			// Already found a match with a higher-priority prefix;
			// flag the ambiguity but stick with the chosen one.
			slog.Warn("BERT weight has multiple matching prefixes; using highest-priority",
				"component", "bert",
				"suffix", suffix,
				"chosen_prefix", prefixes[chosen],
				"also_found_prefix", prefix)
		}
	}
	if chosen < 0 {
		return nil, fmt.Errorf("weight not found: tried prefixes %v with suffix %q", prefixes, suffix)
	}
	name := prefixes[chosen] + suffix
	data, _, err := st.GetFloat32(name)
	if err != nil {
		return nil, fmt.Errorf("load weight %q: %w", name, err)
	}
	return data, nil
}

func loadLinear(st *SafeTensors, prefix string) (LinearWeights, error) {
	w, err := getWeight(st, prefix+".weight")
	if err != nil {
		return LinearWeights{}, err
	}
	b, err := getWeight(st, prefix+".bias")
	if err != nil {
		return LinearWeights{}, err
	}
	return LinearWeights{Weight: w, Bias: b}, nil
}
