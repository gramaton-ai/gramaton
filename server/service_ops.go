package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/graph"
)

// embedWithRetry embeds a single text, truncating by half on each
// context length error until it fits. Returns the embedding vector.
func embedWithRetry(ctx context.Context, emb embed.Provider, text string) ([]float32, error) {
	for attempt := 0; attempt < 5; attempt++ {
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

// serviceStats returns aggregate statistics for the knowledge store.
func (s *Server) serviceStats() (statsResponse, *serviceError) {
	s.engine.RLock()
	defer s.engine.RUnlock()

	g := s.engine.Graph()
	resp := statsResponse{
		Temporality:     make(map[string]int),
		KnowledgeType:   make(map[string]int),
		EpistemicStatus: make(map[string]int),
	}

	it := g.NodeIterator()
	defer it.Close()
	for it.Next() {
		n := it.Node()
		id := n.ID
		if isChunkNode(g, id) {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}

		resp.TotalRecords++

		if v, ok := n.Properties.GetString("temporality"); ok {
			resp.Temporality[v]++
		}
		if v, ok := n.Properties.GetString("knowledge_type"); ok {
			resp.KnowledgeType[v]++
		}
		if v, ok := n.Properties.GetString("epistemic_status"); ok {
			resp.EpistemicStatus[v]++
		}
		if c, ok := n.Properties.GetFloat64("confidence"); ok {
			switch {
			case c >= 0.9:
				resp.Confidence.High++
			case c >= 0.7:
				resp.Confidence.Medium++
			case c >= 0.4:
				resp.Confidence.Moderate++
			default:
				resp.Confidence.Low++
			}
		} else {
			resp.Confidence.Unset++
		}
	}

	return resp, nil
}

// servicePending lists records awaiting classification.
func (s *Server) servicePending(limit int) (map[string]any, *serviceError) {
	if limit <= 0 {
		limit = 50
	}

	s.engine.RLock()
	defer s.engine.RUnlock()

	captured := s.engine.PropIdx().Lookup("processing_status",
		graph.StringProperty("captured"))

	var records []map[string]any
	for _, id := range captured {
		if len(records) >= limit {
			break
		}
		entry := map[string]any{"id": id}
		if n, ok := s.engine.Graph().GetNode(id); ok {
			if v, ok := n.Properties.GetString("content_short"); ok {
				entry["summary_short"] = v
			}
			if v, ok := n.Properties.GetTimestamp("created_at"); ok {
				entry["created_at"] = v.Format("2006-01-02T15:04:05Z")
			}
		}
		records = append(records, entry)
	}

	if records == nil {
		records = []map[string]any{}
	}

	resp := map[string]any{"records": records, "total": len(captured)}
	if len(captured) > limit {
		resp["truncated"] = true
	}
	return resp, nil
}

// serviceReembed regenerates stale embeddings using a 3-phase approach
// to avoid holding the lock during embedding I/O. Records with long
// content that lack chunk children are automatically chunked before
// embedding (same as capture does for new records).
func (s *Server) serviceReembed(ctx context.Context, batch int) (map[string]any, *serviceError) {
	start := time.Now()
	if batch <= 0 {
		batch = 50
	}
	if batch > maxReembedBatch {
		batch = maxReembedBatch
	}

	// Phase 1: Identify stale IDs and gather content under read lock.
	s.engine.RLock()
	if s.engine.Embedder() == nil {
		s.engine.RUnlock()
		return nil, errUnavailable("no embedding provider configured")
	}

	currentModel := s.engine.Embedder().ModelID()
	chunkThreshold := s.engine.Config().Chunking.Threshold * 4 // chars

	type reembedTarget struct {
		nodeID    string
		texts     []string
		keys      []string
		needsChunk bool   // content_full exceeds chunk threshold, no chunks exist
		content   string  // raw content_full for chunking (only if needsChunk)
		summary   string  // content_short for chunk fallback embedding
		props     graph.Properties // for metadata inheritance during chunking
	}
	var targets []reembedTarget

	rit := s.engine.Graph().NodeIterator()
	defer rit.Close()
	for rit.Next() {
		if len(targets) >= batch {
			break
		}
		n := rit.Node()
		id := n.ID
		contentFull, hasContent := n.Properties.GetString("content_full")
		if !hasContent {
			continue
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

		// Check if this record needs chunking: long content, no chunk children.
		needsChunk := false
		if len(contentFull) > chunkThreshold {
			hasChunks := false
			for _, e := range s.engine.Graph().EdgesTo(id) {
				if e.Type == "chunk_of" || e.Type == "section_of" {
					hasChunks = true
					break
				}
			}
			needsChunk = !hasChunks
		}

		// Build embed sources. Skip content_full for records that need
		// chunking -- the chunks will be embedded instead.
		embedSources := []struct {
			sourceKey string
			embedKey  string
		}{
			{"content_keywords", "embedding_keywords"},
			{"content_short", "embedding_short"},
			{"content_medium", "embedding_medium"},
		}
		if !needsChunk {
			embedSources = append(embedSources, struct {
				sourceKey string
				embedKey  string
			}{"content_full", "embedding_full"})
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

		t := reembedTarget{
			nodeID:     id,
			texts:      texts,
			keys:       keys,
			needsChunk: needsChunk,
		}
		if needsChunk {
			t.content = contentFull
			if cs, ok := n.Properties.GetString("content_short"); ok {
				t.summary = cs
			}
			t.props = n.Properties.Clone()
		}
		if len(texts) > 0 || needsChunk {
			targets = append(targets, t)
		}
	}
	s.engine.RUnlock()

	// Phase 1b: Chunk records that need it (outside lock, involves embedding).
	type chunkResult struct {
		nodeID string
		pre    *core.PreChunkResult
	}
	var chunkResults []chunkResult
	for _, t := range targets {
		if !t.needsChunk {
			continue
		}
		pre := s.engine.PreChunk(ctx, t.content, "", t.summary)
		if pre != nil {
			chunkResults = append(chunkResults, chunkResult{nodeID: t.nodeID, pre: pre})
		}
	}

	// Phase 2: Embed all texts outside the lock (no I/O under lock).
	// If a text exceeds the model's context window, truncate by half
	// and retry until it fits.
	type reembedResult struct {
		target  reembedTarget
		vectors [][]float32
		err     error
	}
	var results []reembedResult
	for _, t := range targets {
		if len(t.texts) == 0 {
			results = append(results, reembedResult{target: t})
			continue
		}
		vecs, err := s.engine.Embedder().Embed(ctx, t.texts)
		if err != nil && core.IsContextLengthError(err) {
			// Retry each text individually, truncating until it fits.
			vecs = make([][]float32, len(t.texts))
			err = nil
			for i, text := range t.texts {
				v, e := embedWithRetry(ctx, s.engine.Embedder(), text)
				if e != nil {
					err = e
					break
				}
				vecs[i] = v
			}
		}
		results = append(results, reembedResult{target: t, vectors: vecs, err: err})
	}

	// Phase 3: Apply embeddings and chunks under write lock.
	s.engine.Lock()
	defer s.engine.Unlock()

	// Apply chunks first.
	chunked := 0
	for _, cr := range chunkResults {
		n, ok := s.engine.Graph().GetNode(cr.nodeID)
		if !ok {
			continue
		}
		numChunks := s.engine.ApplyChunks(cr.nodeID, cr.pre, n.Properties)
		if numChunks > 0 {
			chunked++
			s.log.Info("reembed chunked record", "component", "reembed", "node", cr.nodeID, "chunks", numChunks)
		}
	}

	// Apply embeddings.
	reembedded := 0
	errors := 0
	var errorIDs []string
	for _, res := range results {
		if res.err != nil {
			errors++
			errorIDs = append(errorIDs, res.target.nodeID)
			s.log.Warn("reembed failed", "component", "reembed", "node", res.target.nodeID, "err", res.err)
			continue
		}
		if _, ok := s.engine.Graph().GetNode(res.target.nodeID); !ok {
			errors++
			errorIDs = append(errorIDs, res.target.nodeID)
			continue
		}

		for i, vec := range res.vectors {
			prop := graph.VectorProperty(vec)
			s.engine.Graph().SetNodeProperty(res.target.nodeID, res.target.keys[i], prop)
			s.engine.PropIdx().Add(res.target.nodeID, res.target.keys[i], prop)
		}
		if len(res.vectors) > 0 {
			s.engine.VecIdx().Add(res.target.nodeID, res.vectors[len(res.vectors)-1])
		}

		modelProp := graph.StringProperty(currentModel)
		s.engine.Graph().SetNodeProperty(res.target.nodeID, "embedding_model", modelProp)
		s.engine.PropIdx().Add(res.target.nodeID, "embedding_model", modelProp)

		reembedded++
	}

	if reembedded > 0 || chunked > 0 {
		s.engine.Save("reembed")
	}

	s.log.Info("reembed complete", "component", "reembed", "reembedded", reembedded, "chunked", chunked, "errors", errors, "duration_ms", time.Since(start).Milliseconds())

	result := map[string]any{
		"reembedded": reembedded,
		"skipped":    len(targets) - reembedded - errors,
		"errors":     errors,
	}
	if chunked > 0 {
		result["chunked"] = chunked
	}
	if len(errorIDs) > 0 {
		result["error_ids"] = errorIDs
	}
	return result, nil
}

// serviceObserve validates and fires off an asynchronous observation.
// Fixes Bug 4: applies full input validation (message counts, content
// lengths, roles) that MCP previously skipped.
func (s *Server) serviceObserve(req observeRequest) (map[string]any, *serviceError) {
	cfg := s.engine.Config()
	if !cfg.Observe.Enabled {
		return nil, errUnavailable("observe pipeline is not enabled")
	}

	if len(req.Messages) == 0 && len(req.Facts) == 0 {
		return nil, errMissing("messages or facts is required")
	}

	// Input validation.
	const maxMessages = 100
	const maxFacts = 100
	const maxMessageContentLen = 50000
	const maxFactLen = 10000

	if len(req.Messages) > maxMessages {
		return nil, errInvalid(fmt.Sprintf("maximum %d messages allowed", maxMessages))
	}
	if len(req.Facts) > maxFacts {
		return nil, errInvalid(fmt.Sprintf("maximum %d facts allowed", maxFacts))
	}
	for i, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" && m.Role != "system" {
			return nil, errInvalid(fmt.Sprintf("messages[%d].role must be user, assistant, or system", i))
		}
		if len(m.Content) > maxMessageContentLen {
			return nil, errInvalid(fmt.Sprintf("messages[%d].content exceeds %d bytes", i, maxMessageContentLen))
		}
	}
	for i, f := range req.Facts {
		if len(f) > maxFactLen {
			return nil, errInvalid(fmt.Sprintf("facts[%d] exceeds %d bytes", i, maxFactLen))
		}
	}

	if len(req.Messages) > 0 && s.engine.LLM() == nil {
		return nil, errUnavailable("messages mode requires a configured LLM provider. Send facts instead.")
	}

	// Bounded fire-and-forget.
	select {
	case s.observeSem <- struct{}{}:
		go func() {
			defer func() { <-s.observeSem }()
			s.processObservation(req)
		}()
		return map[string]any{"accepted": true}, nil
	default:
		return nil, errTooMany("too many concurrent observe operations, try again later")
	}
}
