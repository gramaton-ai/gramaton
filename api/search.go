package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/search"
)

// SearchRequest is the canonical search input. Most fields are filters
// evaluated inside search.Execute; see package search for semantics.
type SearchRequest struct {
	Text               string            `json:"text,omitempty" jsonschema:"search query text (optional -- omit for filter-only queries)"`
	Top                int               `json:"top,omitempty" jsonschema:"integer, number of results (default 10)"`
	Temporality        string            `json:"temporality,omitempty" jsonschema:"filter: immutable|durable|temporal|ephemeral (prefix with ! to exclude, e.g. !ephemeral)"`
	KnowledgeType      string            `json:"knowledge_type,omitempty" jsonschema:"filter: episodic|semantic|procedural|conceptual|reference (prefix with ! to exclude)"`
	EpistemicStatus    string            `json:"epistemic_status,omitempty" jsonschema:"filter: well_established|probable|speculative|contested|refuted (prefix with ! to exclude)"`
	Resolution         string            `json:"resolution,omitempty" jsonschema:"filter: completed|superseded|abandoned|obsolete|unresolved (unresolved = no resolution set)"`
	ConfidenceMin      *float64          `json:"confidence_min,omitempty" jsonschema:"0.0-1.0"`
	ConfidenceMax      *float64          `json:"confidence_max,omitempty" jsonschema:"0.0-1.0"`
	ImportanceMin      *float64          `json:"importance_min,omitempty" jsonschema:"0.0-1.0"`
	ImportanceMax      *float64          `json:"importance_max,omitempty" jsonschema:"0.0-1.0"`
	IncludeHistorical  bool              `json:"include_historical,omitempty" jsonschema:"include records past valid_until"`
	Since              string            `json:"since,omitempty" jsonschema:"filter: created after date (YYYY-MM-DD or RFC3339)"`
	Missing            []string          `json:"missing,omitempty" jsonschema:"array of field names that must be unset (e.g. [temporality, confidence])"`
	Keywords           []string          `json:"keywords,omitempty" jsonschema:"array of keywords that must all be present on the record (exact match)"`
	AccessCountMin     *int64            `json:"access_count_min,omitempty" jsonschema:"integer, minimum access count"`
	AccessCountMax     *int64            `json:"access_count_max,omitempty" jsonschema:"integer, maximum access count"`
	LastAccessedAfter  string            `json:"last_accessed_after,omitempty" jsonschema:"filter: accessed after date (YYYY-MM-DD or RFC3339)"`
	LastAccessedBefore string            `json:"last_accessed_before,omitempty" jsonschema:"filter: accessed before date (YYYY-MM-DD or RFC3339)"`
	ValidAfter         string            `json:"valid_after,omitempty" jsonschema:"filter: valid_from after date"`
	ValidBefore        string            `json:"valid_before,omitempty" jsonschema:"filter: valid_from before date"`
	ExpiresAfter       string            `json:"expires_after,omitempty" jsonschema:"filter: valid_until after date (find records expiring after X)"`
	ExpiresBefore      string            `json:"expires_before,omitempty" jsonschema:"filter: valid_until before date (find records expiring before X)"`
	Match              string            `json:"match,omitempty" jsonschema:"literal substring search across content fields (case-insensitive). Distinct from text (vector similarity)"`
	SimilarTo          string            `json:"similar_to,omitempty" jsonschema:"record ID -- find records similar to this one using its stored embedding"`
	NearNode           string            `json:"near_node,omitempty" jsonschema:"record ID -- restrict results to records within MaxHops of this node in the graph"`
	MaxHops            int               `json:"max_hops,omitempty" jsonschema:"integer, max BFS hops from NearNode (default unbounded when NearNode set)"`
	MinEdges           *int              `json:"min_edges,omitempty" jsonschema:"integer, minimum total edge count (orphan detection: max_edges=0)"`
	MaxEdges           *int              `json:"max_edges,omitempty" jsonschema:"integer, maximum total edge count"`
	Random             bool              `json:"random,omitempty" jsonschema:"return random results (ignores sort/score). Useful for serendipitous discovery or review"`
	Sort               string            `json:"sort,omitempty" jsonschema:"sort by: created_at|last_accessed|access_count|confidence|importance|content_length|edge_count|staleness (default: effective_score, or created_at if no text)"`
	Order              string            `json:"order,omitempty" jsonschema:"asc or desc (default: desc)"`
	Meta               map[string]string `json:"meta,omitempty" jsonschema:"filter by structured metadata (e.g. {assignee: Sarah Chen, status: in_progress})"`
	Store              string            `json:"store,omitempty" jsonschema:"filter by store: memory|sessions|all (default: all)"`
}

// SearchResponse carries ranked results plus aggregates (facets,
// refinement suggestions).
type SearchResponse struct {
	Results     []search.Result     `json:"results"`
	Facets      search.Facets       `json:"facets,omitempty"`
	Suggestions *search.Suggestions `json:"suggestions,omitempty"`
	// Warnings surfaces non-fatal degradations -- e.g. the query
	// embedder failed so the request fell back to BM25-only. Callers
	// can decide whether to surface them to the user. Empty on the
	// happy path.
	Warnings []string `json:"warnings,omitempty"`
}

// SearchDescription is the MCP tool description for gramaton_search.
// Leads with triggers (not mechanics) to prompt agents to call it BEFORE
// producing project-state content, not after. The retrieval failure mode
// that motivated this framing was agents writing architecture answers
// from general knowledge without first checking for project-specific
// prior thinking in the store.
const SearchDescription = "Search the knowledge store -- call BEFORE producing content that references project state. Call immediately when: the user asks about past decisions, architecture, or prior thinking; you are about to write a design doc, methodology note, or claim about the project; the user references prior-session work ('we discussed this', 'you know where to pick back up'); you are reasoning through a decision that might have project-specific prior art. Empty-search cost is seconds; missing-context cost is reasoning rebuilt from general knowledge instead of informed by what the project already decided. Returns results ranked by composite score across Memory and Sessions with store origin. Text is optional -- omit for filter-only queries (temporality, knowledge_type, since, etc.). Does not search collection items -- use gramaton_collection_items for exhaustive collection listing. Search silently; do not narrate unless results meaningfully change your answer."

// Search executes a hybrid BM25 + vector query. Pre-embeds query text
// outside the read lock, runs the search under RLock, then records
// access under Lock so retrieval metadata is kept fresh. Retrieved IDs
// are tracked so the observe pipeline can skip re-extracting content
// we just surfaced to the agent.
func (a *API) Search(ctx context.Context, req SearchRequest) (SearchResponse, *APIError) {
	if req.Top <= 0 {
		req.Top = 10
	}
	if req.Top > MaxSearchTop {
		req.Top = MaxSearchTop
	}

	if req.Sort != "" && !search.ValidSort(req.Sort) {
		return SearchResponse{}, ErrInvalid("invalid sort field")
	}
	if req.Order != "" && req.Order != "asc" && req.Order != "desc" {
		return SearchResponse{}, ErrInvalid("order must be asc or desc")
	}
	if err := validateKeywords(req.Keywords); err != nil {
		return SearchResponse{}, ErrInvalid(err.Error())
	}
	if len(req.Missing) > MaxMissingFields {
		return SearchResponse{}, ErrInvalid(fmt.Sprintf("maximum %d missing fields allowed", MaxMissingFields))
	}
	if len(req.Match) > MaxMatchLength {
		return SearchResponse{}, ErrInvalid(fmt.Sprintf("match string exceeds maximum length of %d", MaxMatchLength))
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
			return SearchResponse{}, ErrInvalid(err.Error())
		}
	}
	if req.AccessCountMin != nil && *req.AccessCountMin < 0 {
		return SearchResponse{}, ErrInvalid("access_count_min must be >= 0")
	}
	if req.AccessCountMax != nil && *req.AccessCountMax < 0 {
		return SearchResponse{}, ErrInvalid("access_count_max must be >= 0")
	}
	if req.MinEdges != nil && *req.MinEdges < 0 {
		return SearchResponse{}, ErrInvalid("min_edges must be >= 0")
	}
	if req.MaxEdges != nil && *req.MaxEdges < 0 {
		return SearchResponse{}, ErrInvalid("max_edges must be >= 0")
	}
	if req.MaxHops > MaxSearchHops {
		return SearchResponse{}, ErrInvalid(fmt.Sprintf("max_hops must be <= %d", MaxSearchHops))
	}

	switch req.Store {
	case "", "all":
		req.Store = ""
	case "memory":
		// canonical
	case "sessions", "session":
		req.Store = "sessions"
	default:
		return SearchResponse{}, ErrInvalid(`store must be one of "memory", "sessions", or "all"`)
	}

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
		Store:             req.Store,
	}

	// Parse date filters. Collected in one table so error messages
	// stay consistent.
	for _, pair := range []struct {
		raw  string
		name string
		dest **time.Time
	}{
		{req.Since, "since", &q.Since},
		{req.LastAccessedAfter, "last_accessed_after", &q.LastAccessedAfter},
		{req.LastAccessedBefore, "last_accessed_before", &q.LastAccessedBefore},
		{req.ValidAfter, "valid_after", &q.ValidAfter},
		{req.ValidBefore, "valid_before", &q.ValidBefore},
		{req.ExpiresAfter, "expires_after", &q.ExpiresAfter},
		{req.ExpiresBefore, "expires_before", &q.ExpiresBefore},
	} {
		if pair.raw == "" {
			continue
		}
		t, err := parseDateArg(pair.raw)
		if err != nil {
			return SearchResponse{}, ErrInvalid(fmt.Sprintf("invalid %s date: %s", pair.name, err))
		}
		*pair.dest = &t
	}

	// Pre-embed outside any engine lock.
	a.log.Debug("search: embedding query", "component", "search", "text_len", len(q.Text))
	embedStart := time.Now()
	var queryVec []float32
	var warnings []string
	if q.Text != "" && a.engine.Embedder() != nil {
		vecs, err := a.engine.Embedder().Embed(ctx, []string{q.Text})
		switch {
		case err != nil:
			// Degrade gracefully to BM25-only instead of failing the
			// whole search. Surface the degradation so callers know
			// vector scoring was skipped.
			a.log.Warn("search: query embed failed, falling back to BM25",
				"component", "search", "err", err)
			warnings = append(warnings, "query embedding failed; results ranked by BM25 only")
		case len(vecs) > 0:
			queryVec = vecs[0]
		}
	}
	a.log.Debug("search: embed done, acquiring read lock", "component", "search", "embed_ms", time.Since(embedStart).Milliseconds())

	lockStart := time.Now()
	a.engine.RLock()
	a.log.Debug("search: read lock acquired, executing", "component", "search", "lock_wait_ms", time.Since(lockStart).Milliseconds())
	searchStart := time.Now()
	results, err := a.engine.Searcher().ExecuteWithVector(ctx, q, queryVec)
	a.log.Debug("search: execute done", "component", "search", "search_ms", time.Since(searchStart).Milliseconds(), "results", len(results))
	a.engine.RUnlock()

	if err != nil {
		return SearchResponse{}, ErrInternal("search failed")
	}

	if len(results) > 0 {
		a.engine.Lock()
		now := time.Now().UTC()
		cfg := a.engine.Config()
		activationCfg := graph.ActivationConfig{
			BaseAmount:        cfg.Activation.BaseAmount,
			AttenuationFactor: cfg.Activation.AttenuationFactor,
		}
		for _, r := range results {
			a.engine.Graph().RecordAccess(r.ID, now, activationCfg)
		}
		a.engine.MarkAccessDirty()
		a.engine.Unlock()
	}

	if results == nil {
		results = []search.Result{}
	}

	// Annotate collection-member results so agents are nudged to
	// use gramaton_collection_items for exhaustive lists.
	if len(results) > 0 {
		a.engine.RLock()
		for i := range results {
			if colls := a.nodeCollectionNames(results[i].ID); len(colls) > 0 {
				results[i].Collections = colls
			}
		}
		a.engine.RUnlock()
	}

	if len(results) > 0 {
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		a.retrieval.Track(ids...)
	}

	resp := SearchResponse{
		Results:  results,
		Facets:   search.ComputeFacets(results),
		Warnings: warnings,
	}

	a.engine.RLock()
	threshold := a.engine.Config().Search.SuggestionThreshold
	if threshold <= 0 {
		threshold = 0.75
	}
	suggestions := search.ComputeSuggestions(results, a.engine.Graph(), threshold)
	a.engine.RUnlock()
	if suggestions != nil {
		resp.Suggestions = suggestions
	}

	return resp, nil
}
