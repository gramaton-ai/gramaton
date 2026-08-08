// Package chunking splits large content into structural sections or
// overlapping dumb chunks, embeds each piece, and applies the result
// to a graph. The split into PreChunk (I/O-bound, runs off-lock) and
// Apply (fast, runs under the engine write lock inside a batched
// index transaction) minimises the time the engine is locked during
// large captures.
//
// PreChunk first tries structural splitting via graph.SplitSections;
// when no structure is detected it falls back to overlapping dumb
// chunks. Either path adapts to the embedding model's context window
// by shrinking chunk size on context-length errors.
package chunking

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/graph"
)

// maxRetries caps how many times PreChunk will reduce chunk size
// before giving up on embedding and returning chunks unembedded.
const maxRetries = 4

// conservativeCharsPerToken underestimates chars-per-token so the
// window-derived floor triggers splitting safely before the real
// token-based context window is hit.
const conservativeCharsPerToken = 3

// parentFallbackLen bounds the content prefix used as the parent
// embedding source when content_short is not available.
const parentFallbackLen = 2000

// shortContentThreshold is the content_short length boundary; when
// the section lacks a heading and the section body exceeds this
// length, the first shortContentThreshold chars become content_short.
const shortContentThreshold = 200

// inheritedMetadataKeys lists the property keys a section or chunk
// child inherits from its parent. These are metadata classifications
// and source pointers; body fields (content_full, content_short,
// embeddings) are set per-child. author is inherited (not re-composed
// from config) so sub-nodes carry the attribution of the record they
// were split from.
var inheritedMetadataKeys = []string{
	"temporality", "confidence", "knowledge_type", "epistemic_status",
	"content_keywords", "source_ref", "processing_status", "author",
}

// Result holds section/chunk data and their pre-computed embeddings,
// ready to be applied under the engine write lock without further
// I/O. The Sections vs Texts split reflects whether the content was
// structurally splittable.
type Result struct {
	// Sections holds structurally-split sections when graph.SplitSections
	// succeeded. Preferred over Texts for readability and retrieval.
	Sections []graph.Section
	// Texts holds overlapping dumb chunks used when structural splitting
	// failed. One of Sections or Texts is populated, not both.
	Texts []string
	// Vectors holds one embedding per section/chunk. May be nil when
	// embedding failed or no embedder was configured.
	Vectors [][]float32
	// Model records the embedder's ModelID so the section/chunk nodes
	// can be stamped with the provenance of their embeddings.
	Model string
	// ParentVec is a replacement embedding for the parent node,
	// computed from summary/truncated-content. Used because the
	// original content exceeded the model's context window and any
	// embedding computed from the full content is degraded.
	ParentVec []float32
}

// Embedded reports whether the result carries a vector for every
// piece. Callers with a fail-closed vector contract (the update path)
// use this to reject a degraded result before mutating anything.
func (r *Result) Embedded() bool {
	if r == nil {
		return false
	}
	n := len(r.Sections)
	if n == 0 {
		n = len(r.Texts)
	}
	return n > 0 && len(r.Vectors) == n
}

// Applier gives Apply access to a batched write session without
// forcing chunking to import core. core.WriteSession satisfies this;
// Apply's writes ride the session's shared bbolt transaction so a
// many-child document costs one index commit, not one fsync per
// child.
type Applier interface {
	AddNode(props graph.Properties) *graph.Node
	AddEdge(sourceID, targetID, edgeType string, weight float64, props graph.Properties) (*graph.Edge, error)
	IndexNode(nodeID, content string, vec []float32)
	SetProp(nodeID, key string, val graph.Property)
	AddVector(nodeID string, vec []float32)
}

// Trigger reports whether content of the given length is eligible for
// chunking under cfg: longer than the configured character threshold,
// floored at the window-derived minimum (chunking content that fits a
// single embedding is pure overhead). Exposed so callers can gate
// cheaply before paying for PreChunk's embedding calls.
func Trigger(contentLen int, embedder embed.Provider, cfg config.ChunkingConfig, embedCfg config.EmbeddingConfig) bool {
	gate := cfg.Threshold
	if floor := contextWindow(embedder, embedCfg) * conservativeCharsPerToken; gate < floor {
		gate = floor
	}
	return contentLen > gate
}

// PreChunk determines whether content needs splitting and pre-embeds
// the pieces. Returns nil when content is below the chunking
// threshold (cfg.Threshold in characters, floored at the embedding
// window's capacity).
//
// Call BEFORE acquiring the engine write lock. The embedding calls
// are network-bound (Ollama/OpenAI/etc.) and must not run under the
// lock.
//
// summary is content_short, the preferred parent embedding source; a
// truncated content prefix is the fallback.
func PreChunk(
	ctx context.Context,
	embedder embed.Provider,
	cfg config.ChunkingConfig,
	embedCfg config.EmbeddingConfig,
	content, summary string,
) *Result {
	if !Trigger(len(content), embedder, cfg, embedCfg) {
		return nil
	}

	// Structural split first; fall back to dumb chunks when no
	// structure is detected.
	if sections := graph.SplitSections(content, cfg.SectionMin, cfg.SectionMax); sections != nil {
		return preChunkSections(ctx, embedder, cfg, embedCfg, content, summary, sections)
	}
	return preChunkAdaptive(ctx, embedder, cfg, embedCfg, content, summary)
}

// preChunkSections embeds structurally-split sections. Falls back to
// adaptive dumb chunking when the sections themselves exceed the
// model's context window.
func preChunkSections(
	ctx context.Context,
	embedder embed.Provider,
	cfg config.ChunkingConfig,
	embedCfg config.EmbeddingConfig,
	content, summary string,
	sections []graph.Section,
) *Result {
	result := &Result{Sections: sections}
	if embedder == nil {
		return result
	}

	texts := make([]string, len(sections))
	for i, s := range sections {
		texts[i] = s.Text
	}

	vecs, err := embedder.Embed(ctx, texts)
	if err == nil {
		result.Vectors = vecs
		result.Model = embedder.ModelID()
		embedParent(ctx, embedder, result, content, summary)
		return result
	}

	if IsContextLengthError(err) {
		// Sections themselves are too large for the model; fall back
		// to adaptive dumb chunking which shrinks until they fit.
		return preChunkAdaptive(ctx, embedder, cfg, embedCfg, content, summary)
	}

	// Non-context error (network, etc.) -- return sections without
	// embeddings. Apply will still create the section nodes; they just
	// won't be vector-searchable.
	return result
}

// preChunkAdaptive splits content into overlapping dumb chunks and
// embeds them, shrinking chunk size on context-length errors until
// the chunks fit or retries are exhausted.
func preChunkAdaptive(
	ctx context.Context,
	embedder embed.Provider,
	cfg config.ChunkingConfig,
	embedCfg config.EmbeddingConfig,
	content, summary string,
) *Result {
	// Start at cfg.ChunkSize but cap to the model's context window so
	// the first attempt is likely to fit.
	chunkSize := cfg.ChunkSize
	ctxTokens := contextWindow(embedder, embedCfg)
	if chunkSize > ctxTokens {
		chunkSize = ctxTokens
	}
	overlap := cfg.Overlap

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Threshold 0: eligibility was already decided by the
		// caller-facing gate in PreChunk; ChunkText's own floor would
		// re-interpret cfg.Threshold in token units.
		chunks := graph.ChunkText(content, 0, chunkSize, overlap)
		if len(chunks) == 0 {
			return nil
		}
		result := &Result{Texts: chunks}
		if embedder == nil {
			return result
		}

		// Canary: embed the longest chunk first. If it errors with
		// context-length we shrink and retry without doing the full
		// batch embedding.
		longest := chunks[0]
		for _, c := range chunks[1:] {
			if len(c) > len(longest) {
				longest = c
			}
		}
		_, err := embedder.Embed(ctx, []string{longest})
		if err != nil && IsContextLengthError(err) {
			chunkSize = chunkSize * 3 / 4
			if chunkSize < 64 {
				chunkSize = 64
			}
			overlap = overlap * 3 / 4
			continue
		}

		// Canary passed (or non-context error). Embed the full batch;
		// ignore non-context errors and return chunks without vectors.
		vecs, err := embedder.Embed(ctx, chunks)
		if err == nil {
			result.Vectors = vecs
			result.Model = embedder.ModelID()
		}
		embedParent(ctx, embedder, result, content, summary)
		return result
	}

	// Retries exhausted -- return chunks (at the last attempted size)
	// without embeddings.
	chunks := graph.ChunkText(content, 0, chunkSize, overlap)
	return &Result{Texts: chunks}
}

// embedParent computes the replacement parent embedding. Preference:
// content_short (the record's semantic anchor), then a truncated
// prefix of content_full. Shrinks by half on context-length errors.
func embedParent(ctx context.Context, embedder embed.Provider, result *Result, content, summary string) {
	if embedder == nil {
		return
	}
	parentText := summary
	if parentText == "" {
		parentText = content
		if len(parentText) > parentFallbackLen {
			parentText = parentText[:parentFallbackLen]
		}
	}
	if parentText == "" {
		return
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		pvecs, err := embedder.Embed(ctx, []string{parentText})
		if err == nil && len(pvecs) > 0 {
			result.ParentVec = pvecs[0]
			return
		}
		if !IsContextLengthError(err) {
			return
		}
		parentText = parentText[:len(parentText)/2]
		if len(parentText) == 0 {
			return
		}
	}
}

// Apply creates section/chunk nodes from pre. Caller must hold the
// engine write lock and supply a batched write session (Applier) so
// the child writes share one index transaction. parentProps supplies
// the metadata that children inherit (temporality, confidence, etc.)
// and must be read AFTER the parent's own embedding was applied, so
// the ParentVec replacement below swaps the value actually stored.
//
// Returns the number of section/chunk nodes created.
func Apply(a Applier, parentID string, pre *Result, parentProps graph.Properties) int {
	if pre == nil {
		return 0
	}

	// Replace the parent's embedding with the chunk-pipeline's
	// ParentVec when available. Chunking triggers mean the original
	// embedding was computed from content the model silently truncated;
	// ParentVec is purpose-sized and is the better representation.
	// SetProp replaces the old property-index posting; AddVector
	// overwrites the vector index entry in place.
	if pre.ParentVec != nil {
		a.SetProp(parentID, "embedding_full", graph.VectorProperty(pre.ParentVec))
		a.AddVector(parentID, pre.ParentVec)
		if pre.Model != "" {
			a.SetProp(parentID, "embedding_model", graph.StringProperty(pre.Model))
		}
	}

	if len(pre.Sections) > 0 {
		return applySections(a, parentID, pre, parentProps)
	}
	return applyChunks(a, parentID, pre, parentProps)
}

// childProps builds the shared property set for a section or chunk
// child: the machine-derived discriminator (node_type), creation
// time, ordinal, and the parent's inherited metadata. node_type is
// load-bearing: the save guard, duplicates, curation guards, and the
// update path's child enumeration all key on it.
func childProps(nodeType string, ordinal int, parentProps graph.Properties) graph.Properties {
	props := graph.Properties{
		"node_type":     graph.StringProperty(nodeType),
		"created_at":    graph.TimestampProperty(time.Now().UTC()),
		"section_index": graph.Int64Property(int64(ordinal)),
	}
	for _, key := range inheritedMetadataKeys {
		if v, ok := parentProps[key]; ok {
			props[key] = v
		}
	}
	return props
}

// applySections creates section_of children with inherited metadata.
func applySections(a Applier, parentID string, pre *Result, parentProps graph.Properties) int {
	for i, sec := range pre.Sections {
		props := childProps("section", i+1, parentProps)
		props["content_full"] = graph.StringProperty(sec.Text)
		if sec.Heading != "" {
			props["content_short"] = graph.StringProperty(sec.Heading)
			// The heading also gets its own property: content_short
			// doubles as a body prefix for headless sections, so it
			// cannot serve as the matched-section label by itself.
			props["section_heading"] = graph.StringProperty(sec.Heading)
		} else if len(sec.Text) > shortContentThreshold {
			props["content_short"] = graph.StringProperty(sec.Text[:shortContentThreshold])
		}

		var vec []float32
		if i < len(pre.Vectors) {
			vec = pre.Vectors[i]
		}

		node := a.AddNode(props)
		if _, err := a.AddEdge(node.ID, parentID, "section_of", 1.0, nil); err != nil {
			slog.Error("failed to add section_of edge",
				"component", "chunking", "child", node.ID, "parent", parentID, "err", err)
		}
		a.IndexNode(node.ID, sec.Text, vec)
		if vec != nil && pre.Model != "" {
			a.SetProp(node.ID, "embedding_model", graph.StringProperty(pre.Model))
		}
	}
	return len(pre.Sections)
}

// applyChunks creates chunk_of children (overlapping dumb chunks used
// when no document structure was detected). Chunks carry the same
// inherited metadata and discriminator as sections: they are the
// retrieval unit for structureless documents and must be foldable,
// excludable, and attributable exactly like sections.
func applyChunks(a Applier, parentID string, pre *Result, parentProps graph.Properties) int {
	for i, chunkText := range pre.Texts {
		props := childProps("chunk", i+1, parentProps)
		props["content_full"] = graph.StringProperty(chunkText)
		if len(chunkText) > shortContentThreshold {
			props["content_short"] = graph.StringProperty(chunkText[:shortContentThreshold])
		}

		var vec []float32
		if i < len(pre.Vectors) {
			vec = pre.Vectors[i]
		}

		node := a.AddNode(props)
		if _, err := a.AddEdge(node.ID, parentID, "chunk_of", 1.0, nil); err != nil {
			slog.Error("failed to add chunk_of edge",
				"component", "chunking", "child", node.ID, "parent", parentID, "err", err)
		}
		a.IndexNode(node.ID, chunkText, vec)
		if vec != nil && pre.Model != "" {
			a.SetProp(node.ID, "embedding_model", graph.StringProperty(pre.Model))
		}
	}
	return len(pre.Texts)
}

// contextWindow returns the effective embedding context window in
// tokens. Priority: config override > auto-detected from provider >
// embed.DefaultContextWindow.
func contextWindow(embedder embed.Provider, cfg config.EmbeddingConfig) int {
	if cfg.MaxTokens > 0 {
		return cfg.MaxTokens
	}
	if embedder != nil {
		if cw := embedder.ContextWindow(); cw > 0 {
			return cw
		}
	}
	return embed.DefaultContextWindow
}

// IsContextLengthError reports whether an embedding error indicates
// that the input exceeded the model's context window. Detection is
// substring-based across known provider error messages; providers
// don't share a sentinel error.
func IsContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "context length") ||
		strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "maximum context") ||
		strings.Contains(msg, "token limit")
}
