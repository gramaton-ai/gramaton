package server

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/search"
)

// serviceSearch executes a search query with validation, embedding,
// access recording, and retrieval tracking.
func (s *Server) serviceSearch(ctx context.Context, req *searchRequest) (map[string]any, *serviceError) {
	if req.Top <= 0 {
		req.Top = 10
	}
	if req.Top > maxSearchTop {
		req.Top = maxSearchTop
	}

	if req.Sort != "" && !search.ValidSort(req.Sort) {
		return nil, errInvalid("invalid sort field")
	}
	if req.Order != "" && req.Order != "asc" && req.Order != "desc" {
		return nil, errInvalid("order must be asc or desc")
	}
	if err := validateKeywords(req.Keywords); err != nil {
		return nil, errInvalid(err.Error())
	}
	if len(req.Missing) > maxMissingFields {
		return nil, errInvalid(fmt.Sprintf("maximum %d missing fields allowed", maxMissingFields))
	}
	if len(req.Match) > maxMatchLength {
		return nil, errInvalid(fmt.Sprintf("match string exceeds maximum length of %d", maxMatchLength))
	}
	for _, v := range []struct {
		name string
		val  *float64
	}{
		{"confidence_min", req.ConfidenceMin},
		{"confidence_max", req.ConfidenceMax},
		{"importance_min", req.ImportanceMin},
		{"importance_max", req.ImportanceMax},
	} {
		if err := validateFloat64Range(v.name, v.val, 0, 1); err != nil {
			return nil, errInvalid(err.Error())
		}
	}
	if req.AccessCountMin != nil && *req.AccessCountMin < 0 {
		return nil, errInvalid("access_count_min must be >= 0")
	}
	if req.AccessCountMax != nil && *req.AccessCountMax < 0 {
		return nil, errInvalid("access_count_max must be >= 0")
	}
	if req.MinEdges != nil && *req.MinEdges < 0 {
		return nil, errInvalid("min_edges must be >= 0")
	}
	if req.MaxEdges != nil && *req.MaxEdges < 0 {
		return nil, errInvalid("max_edges must be >= 0")
	}

	// Build search query.
	q := search.Query{
		Text:              req.Text,
		Top:               req.Top,
		Temporality:       req.Temporality,
		KnowledgeType:     req.KnowledgeType,
		EpistemicStatus:   req.EpistemicStatus,
		Resolution:        req.Resolution,
		IncludeHistorical: req.IncludeHistorical,
		ConfidenceMin:     req.ConfidenceMin,
		ConfidenceMax:     req.ConfidenceMax,
		ImportanceMin:     req.ImportanceMin,
		ImportanceMax:     req.ImportanceMax,
		Missing:           req.Missing,
		Keywords:          req.Keywords,
		AccessCountMin:    req.AccessCountMin,
		AccessCountMax:    req.AccessCountMax,
		Match:             req.Match,
		SimilarTo:         req.SimilarTo,
		NearNode:          req.NearNode,
		MaxHops:           req.MaxHops,
		MinEdges:          req.MinEdges,
		MaxEdges:          req.MaxEdges,
		Random:            req.Random,
		Sort:              req.Sort,
		Order:             req.Order,
		Meta:              req.Meta,
	}

	// Parse date filters.
	if req.Since != "" {
		t, err := parseDateArg(req.Since)
		if err != nil {
			return nil, errInvalid(fmt.Sprintf("invalid since date: %s", err))
		}
		q.Since = &t
	}
	if req.LastAccessedAfter != "" {
		t, err := parseDateArg(req.LastAccessedAfter)
		if err != nil {
			return nil, errInvalid(fmt.Sprintf("invalid last_accessed_after date: %s", err))
		}
		q.LastAccessedAfter = &t
	}
	if req.LastAccessedBefore != "" {
		t, err := parseDateArg(req.LastAccessedBefore)
		if err != nil {
			return nil, errInvalid(fmt.Sprintf("invalid last_accessed_before date: %s", err))
		}
		q.LastAccessedBefore = &t
	}
	for _, pair := range []struct {
		raw  string
		name string
		dest **time.Time
	}{
		{req.ValidAfter, "valid_after", &q.ValidAfter},
		{req.ValidBefore, "valid_before", &q.ValidBefore},
		{req.ExpiresAfter, "expires_after", &q.ExpiresAfter},
		{req.ExpiresBefore, "expires_before", &q.ExpiresBefore},
	} {
		if pair.raw != "" {
			t, err := parseDateArg(pair.raw)
			if err != nil {
				return nil, errInvalid(fmt.Sprintf("invalid %s date: %s", pair.name, err))
			}
			*pair.dest = &t
		}
	}

	// Pre-embed query text outside the lock.
	var queryVec []float32
	if q.Text != "" && s.engine.Embedder() != nil {
		vecs, err := s.engine.Embedder().Embed(ctx, []string{q.Text})
		if err == nil && len(vecs) > 0 {
			queryVec = vecs[0]
		}
	}

	// Search under read lock.
	s.engine.RLock()
	results, err := s.engine.Searcher().ExecuteWithVector(ctx, q, queryVec)
	s.engine.RUnlock()

	if err != nil {
		return nil, errInternal("search failed")
	}

	// Record access under write lock. Disk persistence is deferred
	// to a periodic background flush to avoid a full save on every search.
	if len(results) > 0 {
		s.engine.Lock()
		now := time.Now().UTC()
		cfg := s.engine.Config()
		activationCfg := graph.ActivationConfig{
			BaseAmount:        cfg.Activation.BaseAmount,
			AttenuationFactor: cfg.Activation.AttenuationFactor,
		}
		for _, r := range results {
			s.engine.Graph().RecordAccess(r.ID, now, activationCfg)
		}
		s.engine.MarkAccessDirty()
		s.engine.Unlock()
	}

	if results == nil {
		results = []search.Result{}
	}

	// Annotate results that are collection members so agents know
	// to use gramaton_collection_items for exhaustive listing.
	if len(results) > 0 {
		s.engine.RLock()
		for i := range results {
			if colls := s.nodeCollectionNames(results[i].ID); len(colls) > 0 {
				results[i].Collections = colls
			}
		}
		s.engine.RUnlock()
	}

	// Track retrieved IDs for observe feedback loop detection.
	if len(results) > 0 {
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		s.retrieval.Track(ids...)
	}

	resp := map[string]any{
		"results": results,
		"facets":  search.ComputeFacets(results),
	}

	// Add refinement suggestions when result quality is low.
	s.engine.RLock()
	threshold := s.engine.Config().Search.SuggestionThreshold
	if threshold <= 0 {
		threshold = 0.75
	}
	suggestions := search.ComputeSuggestions(results, s.engine.Graph(), threshold)
	s.engine.RUnlock()
	if suggestions != nil {
		resp["suggestions"] = suggestions
	}

	return resp, nil
}

// serviceExplore traverses the knowledge graph from a starting node.
func (s *Server) serviceExplore(req *exploreRequest) (map[string]any, *serviceError) {
	if req.NodeID == "" {
		return nil, errMissing("node_id is required")
	}
	if req.Depth <= 0 {
		req.Depth = 2
	}
	if req.Depth > maxExploreDepth {
		req.Depth = maxExploreDepth
	}
	if len(req.EdgeTypes) > maxEdgeTypes {
		return nil, errInvalid(fmt.Sprintf("maximum %d edge types allowed", maxEdgeTypes))
	}

	s.engine.RLock()
	defer s.engine.RUnlock()

	if _, ok := s.engine.Graph().GetNode(req.NodeID); !ok {
		return nil, errNotFound("record not found")
	}

	opts := graph.TraverseOptions{
		MaxDepth:      req.Depth,
		EdgeTypes:     req.EdgeTypes,
		MinEdgeWeight: req.MinWeight,
	}

	sub := s.engine.Graph().Traverse(req.NodeID, opts)

	// Track explored IDs for observe feedback loop detection.
	ids := make([]string, 0, len(sub.Nodes)+1)
	ids = append(ids, req.NodeID)
	for _, n := range sub.Nodes {
		ids = append(ids, n.ID)
	}
	s.retrieval.Track(ids...)

	// Apply node limit.
	maxNodes := req.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 100
	}
	truncated := false
	if len(sub.Nodes) > maxNodes {
		sub.Nodes = sub.Nodes[:maxNodes]
		truncated = true
	}

	resp := map[string]any{
		"nodes": sub.Nodes,
		"edges": sub.Edges,
	}
	if truncated {
		resp["truncated"] = true
		resp["max_nodes"] = maxNodes
	}
	return resp, nil
}

// serviceDuplicates finds near-duplicate records by embedding similarity.
func (s *Server) serviceDuplicates(threshold float64, maxPairs int) (map[string]any, *serviceError) {
	if threshold <= 0 || threshold > 1.0 {
		threshold = 0.92
	}
	if maxPairs <= 0 {
		maxPairs = 50
	}
	if maxPairs > maxDuplicatePairs {
		maxPairs = maxDuplicatePairs
	}

	s.engine.RLock()
	pairs := search.FindDuplicates(s.engine.Graph(), s.engine.VecIdx(), threshold, maxPairs)
	s.engine.RUnlock()

	if pairs == nil {
		pairs = []search.DuplicatePair{}
	}

	return map[string]any{
		"pairs":     pairs,
		"threshold": threshold,
		"count":     len(pairs),
	}, nil
}
