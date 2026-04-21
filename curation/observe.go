package curation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// extractAndCreateObservations finds processed records >500 chars that
// don't yet have observation children, extracts key sentences via TF-IDF,
// creates observation graph nodes with observation_of edges, embeds them
// in a single batched call, and indexes them.
//
// This is the deterministic extraction pipeline (D18). LLM refinement
// is a future enhancement in the autonomous pipeline.
func extractAndCreateObservations(e *core.Engine, cfg config.Config, logger *slog.Logger) int {
	logger = ensureLogger(logger)

	// Observation cap from config. Default 20 per D23.
	maxCap := cfg.Observe.MaxFactsPerCall
	if maxCap <= 0 {
		maxCap = 20
	}

	// --- Read phase: find candidates ---
	e.RLock()

	// Collect parent IDs that already have observations.
	hasObservations := make(map[string]struct{})
	for _, edge := range e.Graph().EdgesByType("observation_of") {
		hasObservations[edge.TargetID] = struct{}{}
	}

	// Find processed records >500 chars without observations.
	type candidate struct {
		id      string
		content string
		props   graph.Properties
	}
	var candidates []candidate

	it := e.Graph().NodeIterator()
	for it.Next() {
		n := it.Node()
		ps, _ := n.Properties.GetString("processing_status")
		if ps != "processed" {
			continue
		}
		// Skip structural children (sections, chunks).
		if e.Graph().IsStructuralChild(n.ID) {
			continue
		}
		// Skip concept nodes.
		if nt, ok := n.Properties.GetString("node_type"); ok && nt == "concept" {
			continue
		}
		// Skip Session segment nodes (already extracted by session LLM).
		if kt, ok := n.Properties.GetString("knowledge_type"); ok && kt == "segment" {
			continue
		}
		content, ok := n.Properties.GetString("content_full")
		minLen := cfg.Curation.ObservationMinContentLength
		if minLen <= 0 {
			minLen = 1500
		}
		if !ok || len(content) < minLen {
			continue
		}
		if _, done := hasObservations[n.ID]; done {
			continue
		}
		// Copy properties for metadata inheritance.
		candidates = append(candidates, candidate{
			id:      n.ID,
			content: content,
			props:   n.Properties.Clone(),
		})
	}
	it.Close()
	e.RUnlock()

	if len(candidates) == 0 {
		return 0
	}

	// Cap candidates per cycle. The constraint is write lock duration,
	// not embed speed: ~50 parents * ~10 observations = ~500 nodes
	// committed in one transaction, keeping the write lock under ~5s.
	// Embed cost varies by provider but happens outside the lock.
	// Default 0 = auto-detect: 50 for local (bert/ollama), 10 for
	// external (API rate limits and cost).
	maxPerCycle := cfg.Curation.ObservationBatchSize
	if maxPerCycle <= 0 {
		if emb := e.Embedder(); emb != nil {
			switch cfg.Embedding.Provider {
			case "bert", "ollama", "":
				maxPerCycle = 500
			default:
				maxPerCycle = 10
			}
		} else {
			maxPerCycle = 50
		}
	}
	if len(candidates) > maxPerCycle {
		candidates = candidates[:maxPerCycle]
	}

	logger.Info("observation extraction started",
		"component", "curation",
		"parents", len(candidates),
		"max_per_cycle", maxPerCycle)

	// --- Extract and embed phase (outside lock) ---
	type obsNode struct {
		parentID string
		text     string
		vec      []float32
	}
	var allObs []obsNode
	embedStart := time.Now()
	embedErrors := 0

	for pi, c := range candidates {
		obs := ExtractObservations(c.content, maxCap)
		if len(obs) == 0 {
			continue
		}

		// Batch embed all observations for this parent.
		var texts []string
		for _, o := range obs {
			texts = append(texts, o.Text)
		}

		var vecs [][]float32
		embedFailed := false
		haveEmbedder := e.Embedder() != nil
		if haveEmbedder && len(texts) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			var err error
			vecs, err = e.Embedder().Embed(ctx, texts)
			cancel()
			if err != nil {
				logger.Warn("observation embedding failed, skipping parent this cycle",
					"component", "curation",
					"parent", c.id,
					"err", err)
				vecs = nil
				embedFailed = true
				embedErrors++
			}
		}

		// If the embedder is configured but failed, skip this parent's
		// observations for this cycle. Creating vector-less nodes in
		// an embedding-enabled store produces orphans that duplicate
		// detection and vector search silently miss. Observations are
		// re-extracted on the next curation tick when the embedder
		// recovers, so no data is lost. Stores running without an
		// embedder at all (haveEmbedder=false) still create nodes --
		// vector-less is the only option there. (P1-06.)
		if embedFailed {
			continue
		}

		for i, o := range obs {
			on := obsNode{parentID: c.id, text: o.Text}
			if vecs != nil && i < len(vecs) {
				on.vec = vecs[i]
			}
			allObs = append(allObs, on)
		}

		// Progress logging every 50 parents.
		if (pi+1)%50 == 0 {
			elapsed := time.Since(embedStart)
			rate := float64(len(allObs)) / elapsed.Seconds()
			logger.Info("observation embedding progress",
				"component", "curation",
				"parents_done", pi+1,
				"parents_total", len(candidates),
				"observations", len(allObs),
				"embed_rate", fmt.Sprintf("%.1f/sec", rate),
				"elapsed", elapsed.Round(time.Second).String(),
				"errors", embedErrors)
		}
	}

	embedDur := time.Since(embedStart)
	logger.Info("observation embedding complete",
		"component", "curation",
		"parents", len(candidates),
		"observations", len(allObs),
		"embed_ms", embedDur.Milliseconds(),
		"errors", embedErrors)

	if len(allObs) == 0 {
		return 0
	}

	// --- Write phase ---
	// WithWriteBatch takes the engine write lock, batches all bbolt
	// index writes into a single transaction (without batching each
	// IndexNode would fsync separately, making 500-observation runs
	// take minutes), and Saves under the message label when fn
	// reports mutations.
	writeStart := time.Now()
	created := 0

	err := e.WithWriteBatch("curation: observation extraction", func() (bool, error) {
		for _, o := range allObs {
			parent, ok := e.Graph().GetNode(o.parentID)
			if !ok {
				continue // parent deleted between read and write
			}

			// Build observation node properties, inheriting from parent.
			props := graph.Properties{
				"content_full":      graph.StringProperty(o.text),
				"processing_status": graph.StringProperty("processed"),
				"created_at":        graph.TimestampProperty(time.Now().UTC()),
				"access_count":      graph.Int64Property(0),
				"node_type":         graph.StringProperty("observation"),
			}

			// Inherit metadata from parent (D18).
			for _, key := range []string{
				"temporality", "confidence", "knowledge_type",
				"epistemic_status", "content_keywords", "source_ref",
			} {
				if v, ok := parent.Properties[key]; ok {
					props[key] = v
				}
			}

			// Truncate text for content_short.
			short := o.text
			if len(short) > 200 {
				short = short[:200]
			}
			props["content_short"] = graph.StringProperty(short)

			n := e.Graph().AddNode(props)

			// Create observation_of edge (child -> parent).
			if _, err := e.Graph().AddEdge(n.ID, o.parentID, "observation_of", 1.0, nil); err != nil {
				logger.Error("failed to add observation_of edge",
					"component", "curation", "child", n.ID, "parent", o.parentID, "err", err)
			}

			// Index the node (properties + BM25 + vector).
			e.IndexNode(n.ID, o.text, o.vec)

			created++
		}
		return created > 0, nil
	})
	if err != nil {
		logger.Error("observation write batch failed",
			"component", "curation",
			"err", err,
			"attempted", len(allObs))
		return 0
	}

	if created > 0 {
		logger.Info("observations extracted",
			"component", "curation",
			"observations", created,
			"parents", len(candidates),
			"write_ms", time.Since(writeStart).Milliseconds())
	}

	return created
}
