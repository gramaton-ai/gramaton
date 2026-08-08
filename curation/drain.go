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

// DrainResult describes what a DrainContradictionsNoLLM call did.
type DrainResult struct {
	// PairsDrained is the number of no_contradiction edges added.
	PairsDrained int `json:"pairs_drained"`
	// PairsConsidered is the total number of unique candidate pairs
	// walked; equals PairsDrained unless some pair's endpoints were
	// missing or racing writes blocked the edge creation.
	PairsConsidered int `json:"pairs_considered"`
	// DurationMS is how long the drain took end-to-end.
	DurationMS int64 `json:"duration_ms"`
}

// DrainContradictionsNoLLM writes an artificial `no_contradiction` edge
// between every processed-record pair that currently sits in the
// contradiction similarity window without an existing edge between them.
// No LLM is consulted -- each edge carries `artificial: true` so a
// future recheck-pass can distinguish artificially-drained marks from
// genuinely-LLM-checked ones.
//
// Use case: on stores where the operator does not want to pay the
// ambient Sonnet cost of organically draining the pre-fix pool via the
// autonomous curation pass (see design-decisions.md D38). The tradeoff
// is that any real contradictions in the drained set will not be
// flagged -- operators opt into the tradeoff knowingly.
//
// Safety: writes only edges. No records are modified or deleted.
// Already-connected pairs are skipped, so pre-existing contradicts /
// no_contradiction edges -- and the supersedes edges legacy stores
// still carry -- survive unchanged.
func DrainContradictionsNoLLM(ctx context.Context, e *core.Engine, cfg config.Config, logger *slog.Logger) (*DrainResult, error) {
	logger = ensureLogger(logger)
	start := time.Now()

	minSim := cfg.LLM.Curation.Contradiction.MinSimilarity
	if minSim <= 0 {
		minSim = 0.5
	}
	maxSim := cfg.LLM.Curation.Contradiction.MaxSimilarity
	if maxSim <= 0 {
		maxSim = 0.85
	}

	// Collect pairs under RLock (same shape as detectContradictions'
	// read phase, minus the MaxContradictionChecks cap -- we want to
	// drain everything in one pass).
	type pair struct{ idA, idB string }
	var pairs []pair

	e.RLock()
	processedIDs := e.PropIdx().Lookup("processing_status", graph.StringProperty("processed"))
	seen := make(map[string]bool)

	for _, idA := range processedIDs {
		select {
		case <-ctx.Done():
			e.RUnlock()
			return nil, ctx.Err()
		default:
		}
		nA, ok := e.Graph().GetNode(idA)
		if !ok {
			continue
		}
		// Derived nodes are machine-owned; no contradiction edges.
		if nt, _ := nA.Properties.GetString("node_type"); nt == "concept" || nt == "observation" {
			continue
		}
		if isChunkNode(e.Graph(), idA) {
			continue
		}
		emb, ok := nA.Properties.GetVector("embedding_full")
		if !ok {
			continue
		}
		// Search wide: we want every in-window neighbor, not just a
		// per-cycle cap. k=50 is a pragmatic upper bound per record;
		// the in-window filter below will throw out most hits.
		results := e.VecIdx().Search(emb, 50, nil)
		for _, sr := range results {
			if sr.NodeID == idA {
				continue
			}
			if nB, ok := e.Graph().GetNode(sr.NodeID); ok {
				if nt, _ := nB.Properties.GetString("node_type"); nt == "concept" || nt == "observation" {
					continue
				}
			}
			sim := float64(sr.Similarity)
			if sim < minSim || sim >= maxSim {
				continue
			}
			// Deduplicate pairs irrespective of direction.
			pk := idA + ":" + sr.NodeID
			pkRev := sr.NodeID + ":" + idA
			if seen[pk] || seen[pkRev] {
				continue
			}
			seen[pk] = true

			// Skip pairs that already have an edge in either direction.
			// Mirrors the read-phase hasEdge guard in detectContradictions
			// so we neither create duplicate edges nor overwrite real
			// contradicts relationships (or legacy supersedes edges).
			hasEdge := false
			for _, edge := range e.Graph().EdgesFrom(idA) {
				if edge.TargetID == sr.NodeID {
					hasEdge = true
					break
				}
			}
			if !hasEdge {
				for _, edge := range e.Graph().EdgesFrom(sr.NodeID) {
					if edge.TargetID == idA {
						hasEdge = true
						break
					}
				}
			}
			if hasEdge {
				continue
			}

			pairs = append(pairs, pair{idA: idA, idB: sr.NodeID})
		}
	}
	e.RUnlock()

	if len(pairs) == 0 {
		return &DrainResult{
			PairsDrained:    0,
			PairsConsidered: 0,
			DurationMS:      time.Since(start).Milliseconds(),
		}, nil
	}

	// Write phase: lock once, emit all edges.
	checkedAt := time.Now().UTC()
	drained := 0
	e.Lock()
	var drainActions []graph.CommitAction
	for _, p := range pairs {
		if _, ok := e.Graph().GetNode(p.idA); !ok {
			continue
		}
		if _, ok := e.Graph().GetNode(p.idB); !ok {
			continue
		}
		props := graph.Properties{
			"checked_at": graph.TimestampProperty(checkedAt),
			"artificial": graph.BoolProperty(true),
		}
		if _, err := e.Graph().AddEdge(p.idA, p.idB, "no_contradiction", 1.0, props); err != nil {
			logger.Warn("drain: add no_contradiction edge failed",
				"component", "curation", "from", p.idA, "to", p.idB, "err", err)
			continue
		}
		drained++
		drainActions = append(drainActions,
			graph.CommitAction{Kind: graph.ActionCurationContradictionCheck, RecordID: p.idA},
			graph.CommitAction{Kind: graph.ActionCurationContradictionCheck, RecordID: p.idB},
		)
	}
	if drained > 0 {
		e.SaveOrLog("curation: drain_contradictions", drainActions...)
	}
	e.Unlock()

	result := &DrainResult{
		PairsDrained:    drained,
		PairsConsidered: len(pairs),
		DurationMS:      time.Since(start).Milliseconds(),
	}
	logger.Info("contradiction pool drain complete",
		"component", "curation",
		"pairs_drained", result.PairsDrained,
		"pairs_considered", result.PairsConsidered,
		"duration_ms", result.DurationMS)
	if drained == 0 && len(pairs) > 0 {
		return result, fmt.Errorf("drain wrote 0 edges but found %d candidate pairs", len(pairs))
	}
	return result, nil
}
