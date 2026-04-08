package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/gramaton-ai/gramaton/graph"
)

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

	for _, id := range g.AllNodeIDs() {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}
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
func (s *Server) servicePending() (map[string]any, *serviceError) {
	s.engine.RLock()
	defer s.engine.RUnlock()

	captured := s.engine.PropIdx().Lookup("processing_status",
		graph.StringProperty("captured"))

	var records []map[string]any
	for _, id := range captured {
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

	return map[string]any{"records": records}, nil
}

// serviceReembed regenerates stale embeddings using a 3-phase approach
// to avoid holding the lock during embedding I/O. Fixes Bug 3: MCP
// previously held a write lock during all embedding calls.
func (s *Server) serviceReembed(ctx context.Context, batch int) (map[string]any, *serviceError) {
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

	type reembedTarget struct {
		nodeID string
		texts  []string
		keys   []string
	}
	var targets []reembedTarget

	for _, id := range s.engine.Graph().AllNodeIDs() {
		if len(targets) >= batch {
			break
		}
		n, ok := s.engine.Graph().GetNode(id)
		if !ok {
			continue
		}
		if _, hasContent := n.Properties.GetString("content_full"); !hasContent {
			continue
		}
		model, ok := n.Properties.GetString("embedding_model")
		if ok && model == currentModel {
			// Model matches, but check for missing embedding layers.
			// A record might have embedding_full but not embedding_short
			// (e.g., concept nodes after summary repair).
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

		embedSources := []struct {
			sourceKey string
			embedKey  string
		}{
			{"content_keywords", "embedding_keywords"},
			{"content_short", "embedding_short"},
			{"content_abstract", "embedding_abstract"},
			{"content_full", "embedding_full"},
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
		if len(texts) > 0 {
			targets = append(targets, reembedTarget{nodeID: id, texts: texts, keys: keys})
		}
	}
	s.engine.RUnlock()

	// Phase 2: Embed all texts outside the lock (no I/O under lock).
	type reembedResult struct {
		target  reembedTarget
		vectors [][]float32
		err     error
	}
	var results []reembedResult
	for _, t := range targets {
		vecs, err := s.engine.Embedder().Embed(ctx, t.texts)
		results = append(results, reembedResult{target: t, vectors: vecs, err: err})
	}

	// Phase 3: Apply embeddings under write lock (fast, no I/O).
	s.engine.Lock()
	defer s.engine.Unlock()

	reembedded := 0
	errors := 0
	for _, res := range results {
		if res.err != nil {
			errors++
			continue
		}
		if _, ok := s.engine.Graph().GetNode(res.target.nodeID); !ok {
			errors++
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

	if reembedded > 0 {
		s.engine.Save("reembed")
	}

	return map[string]any{
		"reembedded": reembedded,
		"skipped":    len(targets) - reembedded - errors,
		"errors":     errors,
	}, nil
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
