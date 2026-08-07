package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/embed"
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
	ProcessingStatus   string            `json:"processing_status,omitempty" jsonschema:"filter: captured|processed|stuck|deleted (prefix with ! to exclude). Operator triage uses processing_status=stuck to surface records that exhausted classify retries."`
	ConfidenceMin      *float64          `json:"confidence_min,omitempty" jsonschema:"0.0-1.0"`
	ConfidenceMax      *float64          `json:"confidence_max,omitempty" jsonschema:"0.0-1.0"`
	ImportanceMin      *float64          `json:"importance_min,omitempty" jsonschema:"0.0-1.0"`
	ImportanceMax      *float64          `json:"importance_max,omitempty" jsonschema:"0.0-1.0"`
	IncludeConcepts    bool              `json:"include_concepts,omitempty" jsonschema:"include synthesized concept nodes (node_type=concept) in results. Default false: concepts are derivative cross-record summaries and compete with their member records for top-N slots, so they're filtered out of default search. Set true when you specifically want concepts (e.g. browsing what patterns have crystallized)."`
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

	// Pagination fields. Cursor takes precedence: when set, all
	// other filter args are ignored (the cursor encodes the slice
	// against a previously-materialized snapshot) and the response
	// carries `ignored_params` listing what was dropped. PageSize
	// applies to both fresh searches and cursor calls.
	Cursor   string `json:"cursor,omitempty" jsonschema:"opaque pagination cursor from a prior response's pages[].cursor or next_cursor. When set, other query args are ignored and the response slices into the cached snapshot from the original call."`
	PageSize int    `json:"page_size,omitempty" jsonschema:"results per page (default from server config; capped at server-side max)."`
}

// PageRef points at one page's worth of results within a search
// snapshot. The Range is a human-readable 1-indexed slice
// description; the Cursor is the opaque token to pass on a
// subsequent call to retrieve that page.
type PageRef struct {
	Range  string `json:"range"`
	Cursor string `json:"cursor"`
}

// SearchResponse carries ranked results plus aggregates (facets,
// refinement suggestions) plus pagination scaffolding.
type SearchResponse struct {
	Results     []search.Result     `json:"results"`
	Facets      search.Facets       `json:"facets,omitempty"`
	Suggestions *search.Suggestions `json:"suggestions,omitempty"`
	// Warnings surfaces non-fatal degradations -- e.g. the query
	// embedder failed so the request fell back to BM25-only, or the
	// underlying match set exceeded the snapshot's candidate cap.
	// Callers can decide whether to surface them to the user. Empty
	// on the happy path.
	Warnings []string `json:"warnings,omitempty"`

	// Pagination fields. Always populated for fresh searches;
	// populated for cursor calls based on the looked-up snapshot.
	Page       int       `json:"page,omitempty"`
	PageSize   int       `json:"page_size,omitempty"`
	Total      int       `json:"total,omitempty"`
	NextCursor string    `json:"next_cursor,omitempty"`
	QueryID    string    `json:"query_id,omitempty"`
	Pages      []PageRef `json:"pages,omitempty"`

	// IgnoredParams names request fields that were dropped because
	// a Cursor was provided. Empty on fresh searches; non-empty
	// only on cursor calls where the request also carried filter
	// args.
	IgnoredParams []string `json:"ignored_params,omitempty"`
}

// SearchDescription is the MCP tool description for gramaton_search.
// Leads with triggers (not mechanics) to prompt agents to call it BEFORE
// producing project-state content, not after. The retrieval failure mode
// that motivated this framing was agents writing architecture answers
// from general knowledge without first checking for project-specific
// prior thinking in the store.
const SearchDescription = "Search the knowledge store -- call BEFORE producing content that references project state. HARD TRIGGERS (first action on user prompts that contain any of these, before any other response work): a ticket codename matching T-\\d+ / P0-\\d+ / P1-\\d+ / P2-\\d+ / P3-\\d+ / D\\d+ (collection items or design decisions); the phrases 'our current thoughts on X', 'current plan for X', 'status of X', 'where are we on X', 'what did we decide about X'; the word 'backlog' (any use); any mention of prior-session work ('we discussed this', 'you know where to pick back up'). If the user prompt contains a ULID (01K...), prefer gramaton_inspect(id=...) instead -- it returns the named record plus its related edges in one call. BROADER TRIGGERS: before answering questions about past decisions / architecture / preferences; before writing design content, methodology notes, or claims about the project; before reasoning through a decision that might have project-specific prior art. Empty-search cost is seconds; missing-context cost is reasoning rebuilt from general knowledge instead of informed by what the project already decided. Returns results ranked by composite score across Memory and Sessions with store origin. Text is optional -- omit for filter-only queries (temporality, knowledge_type, since, etc.). Does not search collection items -- use gramaton_collection_items for exhaustive collection listing. Search silently; do not narrate unless results meaningfully change your answer."

// Search executes a hybrid BM25 + vector query. Pre-embeds query text
// outside the read lock, runs the search under RLock, then records
// access under Lock so retrieval metadata is kept fresh. Retrieved IDs
// are tracked so the observe pipeline can skip re-extracting content
// we just surfaced to the agent.
//
// Pagination: a fresh call materializes up to cfg.Search.Pagination.
// CandidateCap ranked candidates into a snapshot keyed by a ULID
// query_id, slices the first PageSize for the response, and emits a
// page table covering the snapshot. A subsequent call with the same
// Cursor (from any PageRef.Cursor or NextCursor) slices the same
// snapshot at the encoded boundaries; record content is fetched
// fresh per page so modifications surface immediately while the
// match set stays stable for the snapshot's TTL.
func (a *API) Search(ctx context.Context, req SearchRequest) (SearchResponse, *APIError) {
	paginationCfg := a.engine.Config().Search.Pagination

	// Resolve page size: explicit PageSize wins; legacy Top is the
	// fallback for callers that haven't migrated; otherwise default.
	// Capped at PageSizeMax.
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = req.Top
	}
	if pageSize <= 0 {
		pageSize = paginationCfg.PageSizeDefault
	}
	if pageSize > paginationCfg.PageSizeMax {
		pageSize = paginationCfg.PageSizeMax
	}

	// Cursor branch: slice into a previously-cached snapshot. Other
	// query args (text, filter, etc.) are ignored; the cursor encodes
	// the slice against the original call's match set.
	if req.Cursor != "" {
		return a.searchCursor(req, pageSize)
	}

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

	// Materialize up to candidate_cap candidates so the snapshot is
	// useful for pagination beyond the first page. The legacy req.Top
	// field is preserved for the response's page-size fallback (above)
	// but the underlying search runs to candidate_cap regardless.
	candidateCap := paginationCfg.CandidateCap
	if candidateCap <= 0 || candidateCap > config.MaxCandidateCapHard {
		candidateCap = config.MaxCandidateCapHard
	}

	q := search.Query{
		Text:             req.Text,
		Top:              candidateCap,
		Temporality:      req.Temporality,
		KnowledgeType:    req.KnowledgeType,
		EpistemicStatus:  req.EpistemicStatus,
		Resolution:       req.Resolution,
		ProcessingStatus: req.ProcessingStatus,
		ExcludeConcepts:  !req.IncludeConcepts,
		ConfidenceMin:    req.ConfidenceMin,
		ConfidenceMax:    req.ConfidenceMax,
		ImportanceMin:    req.ImportanceMin,
		ImportanceMax:    req.ImportanceMax,
		Missing:          req.Missing,
		Keywords:         req.Keywords,
		AccessCountMin:   req.AccessCountMin,
		AccessCountMax:   req.AccessCountMax,
		Match:            req.Match,
		SimilarTo:        req.SimilarTo,
		NearNode:         req.NearNode,
		MaxHops:          req.MaxHops,
		MinEdges:         req.MinEdges,
		MaxEdges:         req.MaxEdges,
		Random:           req.Random,
		Sort:             req.Sort,
		Order:            req.Order,
		Meta:             req.Meta,
		Store:            req.Store,
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

	// Pre-embed outside any engine lock. Uses embed.EmbedForQuery so
	// providers that distinguish query-time embeddings (e.g. Cohere
	// on Bedrock, which needs input_type="search_query") pick the
	// right path; others fall back to Embed.
	a.log.Debug("search: embedding query", "component", "search", "text_len", len(q.Text))
	embedStart := time.Now()
	var queryVec []float32
	var warnings []string
	if q.Text != "" && a.engine.Embedder() != nil {
		vec, err := embed.EmbedForQuery(ctx, a.engine.Embedder(), q.Text)
		switch {
		case err != nil:
			// Degrade gracefully to BM25-only instead of failing the
			// whole search. Surface the degradation so callers know
			// vector scoring was skipped.
			a.log.Warn("search: query embed failed, falling back to BM25",
				"component", "search", "err", err)
			warnings = append(warnings, "query embedding failed; results ranked by BM25 only")
		default:
			queryVec = vec
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

	// Access bookkeeping (access_count, last_accessed, activation
	// spread) is knowledge-graph state, so a read-only store skips the
	// whole block -- the read path then never touches the write lock
	// and the frozen records' access metadata stays byte-identical.
	if len(results) > 0 && !a.engine.ReadOnly() {
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

	// Phase 1 concept telemetry: emit a structured event when any
	// concept embedding scores above the threshold against the query.
	// No behavior change -- the matched concepts are NOT injected into
	// results (records mode excludes them). The telemetry exists to
	// gather data on whether concept-based query expansion (PRF) would
	// help before committing to ship it. Sampled review by the
	// operator over weeks of real usage answers "are concepts earning
	// their slot."
	if queryVec != nil {
		cfg := a.engine.Config()
		if cfg.Telemetry.ConceptMatchEnabled {
			a.engine.RLock()
			matches := search.ScanConceptMatches(a.engine.Graph(), queryVec, cfg.Telemetry.ConceptMatchThreshold)
			a.engine.RUnlock()
			if len(matches) > 0 {
				topIDs := make([]string, 0, len(results))
				for _, r := range results {
					topIDs = append(topIDs, r.ID)
				}
				a.log.Info("concept_match",
					"component", "telemetry",
					"query", req.Text,
					"top_k", topIDs,
					"matches", matches,
				)
			}
		}
	}

	// Snapshot population + page-1 slice. Snapshot stores IDs +
	// scores only (content is fetched fresh on each page response).
	// Pagination fields are always emitted, even for single-page
	// results, so callers have a consistent shape.
	store := a.engine.SearchSnapshots()
	queryID := store.NewQueryID()
	ids := make([]string, len(results))
	scores := make([]float32, len(results))
	for i, r := range results {
		ids[i] = r.ID
		scores[i] = float32(r.EffectiveScore)
	}
	// Heuristic: if the underlying search returned exactly
	// candidate_cap, it's likely the actual match set is larger.
	// False positives on the rare exact-match-count case are
	// acceptable; the warning is informational.
	truncated := len(results) >= candidateCap
	store.Put(&core.SearchSnapshot{
		QueryID:   queryID,
		IDs:       ids,
		Scores:    scores,
		Total:     len(results),
		Truncated: truncated,
	})

	// Slice the first page for the response. The full snapshot is
	// available via cursor pagination; the response carries page 1.
	pageEnd := pageSize
	if pageEnd > len(results) {
		pageEnd = len(results)
	}
	pageResults := results[:pageEnd]

	resp := SearchResponse{
		Results:  pageResults,
		Facets:   search.ComputeFacets(results),
		Warnings: warnings,

		Page:     1,
		PageSize: pageSize,
		Total:    len(results),
		QueryID:  queryID,
		Pages:    buildPageTable(queryID, len(results), pageSize),
	}
	if pageEnd < len(results) {
		resp.NextCursor = encodeCursor(queryID, pageEnd, pageSize)
	}
	if truncated {
		resp.Warnings = append(resp.Warnings,
			fmt.Sprintf("ranked candidate set capped at %d (more matches may exist). Use cursor pagination via 'pages' to walk the snapshot, refine the query, or run 'gramaton export --query \"...\" --output results.jsonl' for the full set.",
				len(results)))
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

// searchCursor handles cursor-paginated calls. Decodes the cursor,
// looks up the snapshot, slices the requested range, fetches fresh
// content for each ID in the slice, and builds the response with
// the same page table the original call would have emitted.
//
// Failure modes:
//   - Malformed cursor → ErrInvalid
//   - Snapshot expired or evicted → ErrUnavailable("snapshot_expired");
//     agent's documented response is to re-issue the original query
//   - Record deleted between snapshot and fetch → silently skipped
//     (page returns N-1 results; not surfaced as an error)
func (a *API) searchCursor(req SearchRequest, requestPageSize int) (SearchResponse, *APIError) {
	queryID, start, encodedPageSize, err := decodeCursor(req.Cursor)
	if err != nil {
		return SearchResponse{}, ErrInvalid("invalid cursor")
	}
	store := a.engine.SearchSnapshots()
	if store == nil {
		return SearchResponse{}, ErrUnavailable("snapshot store unavailable")
	}
	snap, ok := store.Get(queryID)
	if !ok {
		return SearchResponse{}, ErrUnavailable("snapshot_expired")
	}

	// Use the cursor's encoded page_size so page boundaries stay
	// stable across calls. The request's PageSize on a cursor call
	// is ignored; if the agent supplied one that differs, it lands
	// in ignored_params.
	pageSize := encodedPageSize
	end := start + pageSize
	if end > len(snap.IDs) {
		end = len(snap.IDs)
	}
	if start > end {
		start = end
	}

	// Slice + fresh fetch under a brief RLock. BuildResultsByID
	// re-reads each node so modifications since the snapshot was
	// taken surface immediately. Records deleted between snapshot
	// and call are silently skipped (page returns N-1).
	a.engine.RLock()
	pageIDs := snap.IDs[start:end]
	pageScores := snap.Scores[start:end]
	pageResults := a.engine.Searcher().BuildResultsByID(pageIDs, pageScores)
	a.engine.RUnlock()

	// Annotate collection memberships (matches fresh-search behavior).
	if len(pageResults) > 0 {
		a.engine.RLock()
		for i := range pageResults {
			if colls := a.nodeCollectionNames(pageResults[i].ID); len(colls) > 0 {
				pageResults[i].Collections = colls
			}
		}
		a.engine.RUnlock()
	}

	// Page number from cursor's start + encoded page size. Always
	// exact because the page table aligns to pageSize boundaries.
	pageNum := (start / pageSize) + 1

	ignored := ignoredParamsForCursor(req)
	if requestPageSize > 0 && requestPageSize != encodedPageSize {
		ignored = append(ignored, "page_size")
	}

	resp := SearchResponse{
		Results:       pageResults,
		Page:          pageNum,
		PageSize:      pageSize,
		Total:         snap.Total,
		QueryID:       queryID,
		Pages:         buildPageTable(queryID, snap.Total, pageSize),
		IgnoredParams: ignored,
	}
	if end < snap.Total {
		resp.NextCursor = encodeCursor(queryID, end, pageSize)
	}
	if snap.Truncated {
		resp.Warnings = append(resp.Warnings,
			fmt.Sprintf("snapshot is truncated (capped at %d candidates; more matches may exist). Refine the original query, or run 'gramaton export --query \"...\" --output results.jsonl' for the full set.",
				snap.Total))
	}
	return resp, nil
}
