package core

import (
	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/llm"
	"github.com/gramaton-ai/gramaton/search"
)

// searcherSubsystem owns the *search.Tool and centralises its
// construction. Without this, both LoadEngine and RebuildAllIndexes
// duplicated the same five-argument search.New() ceremony plus
// option-assembly logic; subtle drift between the two paths was a
// realistic bug. The subsystem keeps assembly in one place.
type searcherSubsystem struct {
	tool *search.Tool
}

// rebuild reconstructs the underlying search tool against the current
// graph + indexes + providers. Call this after any structural change
// (initial construction, RebuildAllIndexes, branch checkout/merge that
// swapped the graph). The caller must hold the engine write lock.
func (s *searcherSubsystem) rebuild(
	g *graph.Graph,
	propIdx index.PropertyIndex,
	vecIdx index.VectorIndex,
	bm25Full index.BM25Index,
	secIdx *index.BboltSecondaryIndex,
	embedder embed.Provider,
	llmProv llm.Provider,
	cfg config.Config,
) {
	var opts []search.ToolOption
	if secIdx != nil {
		opts = append(opts, search.WithSecondaryIndex(secIdx))
	}
	if llmProv != nil && cfg.Search.RerankEnabled {
		opts = append(opts, search.WithReranker(llmProv))
	}
	s.tool = search.New(g, propIdx, vecIdx, bm25Full, embedder, cfg, opts...)
}
