package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/strutil"
)

// lastEmbedErrorMaxRunes caps the size of the per-record
// last_embed_error property. Same rationale as the curation
// counterparts: provider errors may embed prompt fragments / URLs;
// the cap bounds what lands on a record visible through
// gramaton_inspect.
const lastEmbedErrorMaxRunes = 200

// ReembedRequest bounds how many records the cycle processes per call.
// Reembed is idempotent and pageable: callers can drive the store to
// completion by looping until reembedded+errors == 0.
type ReembedRequest struct {
	Batch int `json:"batch,omitempty" jsonschema:"max records to process (default 50, max 500)"`
}

// ReembedResponse summarises one batch. ErrorIDs is omitted when no
// records failed.
type ReembedResponse struct {
	Reembedded int      `json:"reembedded"`
	Skipped    int      `json:"skipped"`
	Errors     int      `json:"errors"`
	ErrorIDs   []string `json:"error_ids,omitempty"`
	// SimilarPairs reports deferred save-guard matches: records saved
	// during an embedder outage skipped the save-time similarity scan;
	// when their vectors arrive the scan re-runs, and hold-grade
	// matches are reported here (both records exist by now, so this
	// surfaces rather than holds -- triage via gramaton_update /
	// gramaton_resolve, or find the pair in gramaton_duplicates).
	SimilarPairs []ReembedSimilarPair `json:"similar_pairs,omitempty"`
}

// ReembedSimilarPair is one deferred save-guard match.
type ReembedSimilarPair struct {
	ID         string  `json:"id"`
	SimilarTo  string  `json:"similar_to"`
	Similarity float64 `json:"similarity"`
}

// ReembedDescription is shared by HTTP, MCP, and CLI proxy.
const ReembedDescription = "Regenerate stale embeddings (model changed or missing). Bounded batch processing -- call repeatedly until reembedded+errors hit zero."

// Reembed regenerates embeddings for records whose stored
// embedding_model differs from the current embedder, or whose
// content_short embedding is missing. Three-phase to avoid holding
// the engine lock during embedding I/O:
//  1. RLock: identify candidates and gather embedding source texts.
//  2. (no lock): run the embedder; on context-length errors, retry
//     each text individually with halving truncation.
//  3. Lock: apply vectors and persist.
func (a *API) Reembed(ctx context.Context, req ReembedRequest) (ReembedResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("reembed"); apiErr != nil {
		return ReembedResponse{}, apiErr
	}
	start := time.Now()
	batch := req.Batch
	if batch <= 0 {
		batch = 50
	}
	if batch > MaxReembedBatch {
		batch = MaxReembedBatch
	}

	// Phase 1: Identify stale IDs and gather content under read lock.
	a.engine.RLock()
	if a.engine.Embedder() == nil {
		a.engine.RUnlock()
		return ReembedResponse{}, ErrUnavailable("no embedding provider configured")
	}

	currentModel := a.engine.Embedder().ModelID()
	maxEmbedAttempts := a.engine.Config().LLM.Curation.Retries.MaxEmbedAttempts

	type reembedTarget struct {
		nodeID string
		texts  []string
		keys   []string
	}
	var targets []reembedTarget

	rit := a.engine.Graph().NodeIterator()
	for rit.Next() {
		if len(targets) >= batch {
			break
		}
		n := rit.Node()
		id := n.ID
		// Skip nodes with no embeddable text. Memory records gate on
		// content_full; collection items gate on RecordIndexText
		// (which returns the wide concat of field.* strings).
		_, hasContentFull := n.Properties.GetString("content_full")
		if !hasContentFull && core.RecordIndexText(n) == "" {
			continue
		}
		// Skip records that have exhausted their reembed retry budget.
		// Without this guard, a record whose embedding consistently
		// fails (oversized after halving truncation, content-policy
		// refusal, persistent dimension issue) would re-enter the
		// candidate set every gramaton_reembed invocation and re-pay
		// the full embed cost.
		if maxEmbedAttempts > 0 {
			if attempts, ok := n.Properties.GetInt64("embed_attempts"); ok && attempts >= int64(maxEmbedAttempts) {
				continue
			}
		}
		model, ok := n.Properties.GetString("embedding_model")
		if ok && model == currentModel {
			hasGap := false
			if _, has := n.Properties.GetString("content_short"); has {
				if _, has := n.Properties["embedding_short"]; !has {
					hasGap = true
				}
			}
			if !hasGap {
				continue
			}
		}

		// Embed three sources when present: full, keywords, short.
		// The short embedding wins as the node's primary vector
		// (it is the semantic anchor used by the vec index). For
		// collection items (no content_full), the full vector is
		// regenerated from RecordContent (content_fields-driven
		// text), aligning insert-time embedding with reembed.
		embedSources := []struct {
			sourceKey string
			embedKey  string
		}{
			{"content_full", "embedding_full"},
			{"content_keywords", "embedding_keywords"},
			{"content_short", "embedding_short"},
		}

		var texts []string
		var keys []string
		for _, src := range embedSources {
			var text string
			if sl, ok := n.Properties.GetStringList(src.sourceKey); ok {
				text = strings.Join(sl, " ")
			} else if s, ok := n.Properties.GetString(src.sourceKey); ok {
				text = s
			}
			if text != "" {
				texts = append(texts, text)
				keys = append(keys, src.embedKey)
			}
		}
		// Collection items have no content_full; regenerate
		// embedding_full from RecordContent so the vector reflects
		// current text after schema/field updates.
		if !hasContentFull {
			contentFields := a.contentFieldsFor(id)
			if t := core.RecordContent(n, contentFields); t != "" {
				texts = append(texts, t)
				keys = append(keys, "embedding_full")
			}
		}

		if len(texts) > 0 {
			targets = append(targets, reembedTarget{nodeID: id, texts: texts, keys: keys})
		}
	}
	rit.Close()
	a.engine.RUnlock()

	// Phase 2: Embed all texts outside the lock.
	type reembedResult struct {
		target  reembedTarget
		vectors [][]float32
		err     error
	}
	results := make([]reembedResult, 0, len(targets))
	for _, t := range targets {
		if len(t.texts) == 0 {
			results = append(results, reembedResult{target: t})
			continue
		}
		vecs, err := a.engine.Embedder().Embed(ctx, t.texts)
		if err != nil && core.IsContextLengthError(err) {
			vecs = make([][]float32, len(t.texts))
			err = nil
			for i, text := range t.texts {
				v, e := reembedWithRetry(ctx, a.engine.Embedder(), text)
				if e != nil {
					err = e
					break
				}
				vecs[i] = v
			}
		}
		results = append(results, reembedResult{target: t, vectors: vecs, err: err})
	}

	// Phase 3: Apply embeddings under write lock.
	a.engine.Lock()
	defer a.engine.Unlock()

	resp := ReembedResponse{}
	var reembedActions []graph.CommitAction
	for _, res := range results {
		if res.err != nil {
			resp.Errors++
			resp.ErrorIDs = append(resp.ErrorIDs, res.target.nodeID)
			a.log.Warn("reembed failed", "component", "reembed", "node", res.target.nodeID, "err", res.err)

			// Per-record retry tracking: increment embed_attempts and
			// capture the truncated reason. Records past
			// MaxEmbedAttempts are skipped at selection time on
			// subsequent invocations. Skipped when MaxEmbedAttempts
			// is 0 (legacy behaviour).
			if maxEmbedAttempts > 0 {
				if n, ok := a.engine.Graph().GetNode(res.target.nodeID); ok {
					var attempts int64
					if v, ok := n.Properties.GetInt64("embed_attempts"); ok {
						attempts = v
					}
					attempts++
					a.engine.SetEmbedAttempts(res.target.nodeID, attempts)
					a.engine.SetProp(res.target.nodeID, "last_embed_error", graph.StringProperty(strutil.TruncateRunes(res.err.Error(), lastEmbedErrorMaxRunes)))
					if attempts >= int64(maxEmbedAttempts) {
						a.log.Warn("reembed: record will be skipped after repeated failures",
							"component", "reembed",
							"node", res.target.nodeID,
							"attempts", attempts,
							"max_attempts", maxEmbedAttempts,
							"last_error", res.err)
					}
				}
			}
			continue
		}
		n, ok := a.engine.Graph().GetNode(res.target.nodeID)
		if !ok {
			resp.Errors++
			resp.ErrorIDs = append(resp.ErrorIDs, res.target.nodeID)
			continue
		}

		for i, vec := range res.vectors {
			prop := graph.VectorProperty(vec)
			a.engine.Graph().SetNodeProperty(res.target.nodeID, res.target.keys[i], prop)
			a.engine.PropIdx().Add(res.target.nodeID, res.target.keys[i], prop)
		}
		if len(res.vectors) > 0 {
			a.engine.VecIdx().Add(res.target.nodeID, res.vectors[len(res.vectors)-1])
			// The record just became similarity-visible; register it in
			// the delta re-scan ring so a save whose off-lock scan
			// predates this commit still sees it under the write lock.
			a.engine.NoteRecentWrite(res.target.nodeID, res.vectors[len(res.vectors)-1])
		}

		modelProp := graph.StringProperty(currentModel)
		a.engine.Graph().SetNodeProperty(res.target.nodeID, "embedding_model", modelProp)
		a.engine.PropIdx().Add(res.target.nodeID, "embedding_model", modelProp)

		// Successful re-embed clears any prior failure tracking so an
		// operator-fixed record passes cleanly on its next run. Skip
		// the write when the counter was never set so happy-path
		// records stay untouched.
		if _, has := n.Properties.GetInt64("embed_attempts"); has {
			a.engine.SetEmbedAttempts(res.target.nodeID, 0)
		}

		// Deferred save-guard check: a record saved during an embedder
		// outage skipped the save-time similarity scan. Now that its
		// vector exists, re-run the scan and surface any hold-grade
		// match -- both records already exist, so this reports rather
		// than holds.
		if pending, _ := n.Properties.GetBool("similar_check_pending"); pending {
			if out := a.engine.ScanSimilar(res.target.nodeID); out.Hold != nil {
				resp.SimilarPairs = append(resp.SimilarPairs, ReembedSimilarPair{
					ID:         res.target.nodeID,
					SimilarTo:  out.Hold.NodeID,
					Similarity: out.Hold.Similarity,
				})
				a.log.Warn("reembed: deferred save-guard match",
					"component", "reembed",
					"node", res.target.nodeID,
					"similar_to", out.Hold.NodeID,
					"similarity", fmt.Sprintf("%.3f", out.Hold.Similarity))
			}
			if old, has := n.Properties["similar_check_pending"]; has {
				a.engine.PropIdx().Remove(res.target.nodeID, "similar_check_pending", old)
				a.engine.Graph().RemoveNodeProperty(res.target.nodeID, "similar_check_pending")
			}
		}

		resp.Reembedded++
		reembedActions = append(reembedActions, graph.CommitAction{
			Kind: graph.ActionReembed, RecordID: res.target.nodeID,
		})
	}
	resp.Skipped = len(targets) - resp.Reembedded - resp.Errors

	if resp.Reembedded > 0 {
		if _, err := a.engine.Save("reembed", reembedActions...); err != nil {
			a.log.Error("reembed save failed", "component", "reembed", "err", err, "reembedded", resp.Reembedded)
			return ReembedResponse{}, ErrInternal("save after reembed failed")
		}
	}

	a.log.Info("reembed complete",
		"component", "reembed",
		"reembedded", resp.Reembedded,
		"errors", resp.Errors,
		"duration_ms", time.Since(start).Milliseconds())
	return resp, nil
}

// reembedWithRetry embeds a single text, halving its length on each
// context-length error until it fits or runs out.
func reembedWithRetry(ctx context.Context, emb embed.Provider, text string) ([]float32, error) {
	for range 5 {
		vecs, err := emb.Embed(ctx, []string{text})
		if err == nil && len(vecs) > 0 {
			return vecs[0], nil
		}
		if !core.IsContextLengthError(err) {
			return nil, err
		}
		text = text[:len(text)/2]
		if len(text) == 0 {
			return nil, fmt.Errorf("text too short after truncation")
		}
	}
	return nil, fmt.Errorf("exceeded retry limit for context length")
}
