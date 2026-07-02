// Package chunking splits large content into structural sections or
// overlapping dumb chunks, embeds each piece, and applies the result
// to a graph. The split into PreChunk (I/O-bound, runs off-lock) and
// Apply (fast, runs under the engine write lock) minimises the time
// the engine is locked during large captures.
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

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// maxRetries caps how many times PreChunk will reduce chunk size
// before giving up on embedding and returning chunks unembedded.
const maxRetries = 4

// conservativeCharsPerToken underestimates chars-per-token so the
// chars-based threshold triggers splitting safely before the real
// token-based context window is hit.
const conservativeCharsPerToken = 3

// parentFallbackLen bounds the content prefix used as the parent
// embedding source when neither content_medium nor content_short is
// available.
const parentFallbackLen = 2000

// shortContentThreshold is the content_short length boundary; when
// the section lacks a heading and the section body exceeds this
// length, the first shortContentThreshold chars become content_short.
const shortContentThreshold = 200

// inheritedMetadataKeys lists the property keys a section inherits
// from its parent. These are metadata classifications and source
// pointers; body fields (content_full, content_short, embeddings)
// are set per-section. author is inherited (not re-composed from
// config) so sub-nodes carry the attribution of the record they were
// split from.
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
	// computed from medium/summary/truncated-content. Used because
	// the original content exceeded the model's context window and
	// any embedding computed from the full content is degraded.
	ParentVec []float32
}

// Applier gives Apply access to the engine's graph and index layer
// without forcing chunking to import core. core.Engine satisfies this
// by virtue of its existing public accessors.
type Applier interface {
	Graph() *graph.Graph
	PropIdx() index.PropertyIndex
	VecIdx() index.VectorIndex
	IndexNode(nodeID, content string, vec []float32)
	SetProp(nodeID, key string, val graph.Property)
}

// PreChunk determines whether content needs splitting and pre-embeds
// the pieces. Returns nil when content fits in a single embedding.
//
// Call BEFORE acquiring the engine write lock. The embedding calls
// are network-bound (Ollama/OpenAI/etc.) and must not run under the
// lock.
//
// medium is content_medium (~1500 chars) used as the preferred parent
// embedding source; summary is content_short (~200 chars), the
// fallback.
func PreChunk(
	ctx context.Context,
	embedder embed.Provider,
	cfg config.ChunkingConfig,
	embedCfg config.EmbeddingConfig,
	content, medium, summary string,
) *Result {
	ctxTokens := contextWindow(embedder, embedCfg)
	thresholdChars := ctxTokens * conservativeCharsPerToken
	if len(content) <= thresholdChars {
		return nil
	}

	// Structural split first; fall back to dumb chunks when no
	// structure is detected.
	if sections := graph.SplitSections(content, cfg.SectionMin, cfg.SectionMax); sections != nil {
		return preChunkSections(ctx, embedder, cfg, embedCfg, content, medium, summary, sections)
	}
	return preChunkAdaptive(ctx, embedder, cfg, embedCfg, content, medium, summary)
}

// preChunkSections embeds structurally-split sections. Falls back to
// adaptive dumb chunking when the sections themselves exceed the
// model's context window.
func preChunkSections(
	ctx context.Context,
	embedder embed.Provider,
	cfg config.ChunkingConfig,
	embedCfg config.EmbeddingConfig,
	content, medium, summary string,
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
		embedParent(ctx, embedder, result, content, medium, summary)
		return result
	}

	if IsContextLengthError(err) {
		// Sections themselves are too large for the model; fall back
		// to adaptive dumb chunking which shrinks until they fit.
		return preChunkAdaptive(ctx, embedder, cfg, embedCfg, content, medium, summary)
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
	content, medium, summary string,
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
		chunks := graph.ChunkText(content, cfg.Threshold, chunkSize, overlap)
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
		embedParent(ctx, embedder, result, content, medium, summary)
		return result
	}

	// Retries exhausted -- return chunks (at the last attempted size)
	// without embeddings.
	chunks := graph.ChunkText(content, cfg.Threshold, chunkSize, overlap)
	return &Result{Texts: chunks}
}

// embedParent computes the replacement parent embedding. Preference:
// content_medium (purpose-built for the model's window), then
// content_short, then a truncated prefix of content_full. Shrinks by
// half on context-length errors.
func embedParent(ctx context.Context, embedder embed.Provider, result *Result, content, medium, summary string) {
	if embedder == nil {
		return
	}
	parentText := medium
	if parentText == "" {
		parentText = summary
	}
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
// engine write lock. parentProps supplies the metadata that sections
// inherit (temporality, confidence, etc.) and is also consulted for
// the existing embedding_full (for replacement by pre.ParentVec).
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
	if pre.ParentVec != nil {
		g := a.Graph()
		propIdx := a.PropIdx()
		vecIdx := a.VecIdx()
		if old, hasEmbed := parentProps.GetVector("embedding_full"); hasEmbed {
			propIdx.Remove(parentID, "embedding_full", graph.VectorProperty(old))
			vecIdx.Remove(parentID)
		}
		prop := graph.VectorProperty(pre.ParentVec)
		g.SetNodeProperty(parentID, "embedding_full", prop)
		propIdx.Add(parentID, "embedding_full", prop)
		vecIdx.Add(parentID, pre.ParentVec)
		if pre.Model != "" {
			modelProp := graph.StringProperty(pre.Model)
			g.SetNodeProperty(parentID, "embedding_model", modelProp)
			propIdx.Add(parentID, "embedding_model", modelProp)
		}
	}

	if len(pre.Sections) > 0 {
		return applySections(a, parentID, pre, parentProps)
	}
	return applyLegacyChunks(a, parentID, pre)
}

// applySections creates section_of nodes with inherited metadata.
func applySections(a Applier, parentID string, pre *Result, parentProps graph.Properties) int {
	g := a.Graph()
	for i, sec := range pre.Sections {
		props := graph.Properties{
			"content_full": graph.StringProperty(sec.Text),
		}
		if sec.Heading != "" {
			props["content_short"] = graph.StringProperty(sec.Heading)
		} else if len(sec.Text) > shortContentThreshold {
			props["content_short"] = graph.StringProperty(sec.Text[:shortContentThreshold])
		}
		for _, key := range inheritedMetadataKeys {
			if v, ok := parentProps[key]; ok {
				props[key] = v
			}
		}

		var vec []float32
		if i < len(pre.Vectors) {
			vec = pre.Vectors[i]
		}

		node := g.AddNode(props)
		if _, err := g.AddEdge(node.ID, parentID, "section_of", 1.0, nil); err != nil {
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

// applyLegacyChunks creates chunk_of nodes (backward-compatible dumb
// chunks).
func applyLegacyChunks(a Applier, parentID string, pre *Result) int {
	g := a.Graph()
	for i, chunkText := range pre.Texts {
		var vec []float32
		if i < len(pre.Vectors) {
			vec = pre.Vectors[i]
		}
		chunkNode := g.AddNode(graph.Properties{
			"content_full": graph.StringProperty(chunkText),
		})
		if _, err := g.AddEdge(chunkNode.ID, parentID, "chunk_of", 1.0, nil); err != nil {
			slog.Error("failed to add chunk_of edge",
				"component", "chunking", "child", chunkNode.ID, "parent", parentID, "err", err)
		}
		a.IndexNode(chunkNode.ID, chunkText, vec)
		if vec != nil && pre.Model != "" {
			a.SetProp(chunkNode.ID, "embedding_model", graph.StringProperty(pre.Model))
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
