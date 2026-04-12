package search

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// Tool implements the search command -- Tier 1 of the retrieval funnel.
type Tool struct {
	graph      graph.NodeReader
	propIdx    index.PropertyIndex
	vecIdx     index.VectorIndex
	bm25Full   index.BM25Index // content_full (detail match)
	bm25Medium index.BM25Index // content_medium (theme match)
	bm25Short  index.BM25Index // content_short (topic match)
	embedder   embedder
	cfg        config.Config
}

type embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// New creates a search tool. embedder and bm25 indexes may be nil.
func New(g graph.NodeReader, propIdx index.PropertyIndex, vecIdx index.VectorIndex, bm25Full, bm25Medium, bm25Short index.BM25Index, emb embedder, cfg config.Config) *Tool {
	return &Tool{
		graph:      g,
		propIdx:    propIdx,
		vecIdx:     vecIdx,
		bm25Full:   bm25Full,
		bm25Medium: bm25Medium,
		bm25Short:  bm25Short,
		embedder:   emb,
		cfg:        cfg,
	}
}

// Sort field constants.
const (
	SortScore         = ""              // default: effective_score (or created_at if no text)
	SortCreatedAt     = "created_at"
	SortLastAccessed  = "last_accessed"
	SortAccessCount   = "access_count"
	SortConfidence    = "confidence"
	SortImportance    = "importance"
	SortContentLength = "content_length"
	SortEdgeCount     = "edge_count"
	SortStaleness     = "staleness"
)

// ValidSort reports whether s is a recognized sort field.
func ValidSort(s string) bool {
	switch s {
	case SortScore, SortCreatedAt, SortLastAccessed, SortAccessCount,
		SortConfidence, SortImportance, SortContentLength, SortEdgeCount,
		SortStaleness:
		return true
	}
	return false
}

// Query specifies search parameters.
type Query struct {
	Text            string
	ConfidenceMin   *float64
	ConfidenceMax   *float64
	ImportanceMin   *float64
	ImportanceMax   *float64
	Temporality     string // exact match, or "!value" for negation
	KnowledgeType   string // exact match, or "!value" for negation
	EpistemicStatus string // exact match, or "!value" for negation
	Resolution      string // exact match, "!value" for negation, or "unresolved" (no resolution set)
	Missing         []string // field names that must not be set
	Keywords          []string   // exact keyword match (all must be present)
	AccessCountMin    *int64     // minimum access count
	AccessCountMax    *int64     // maximum access count
	LastAccessedAfter  *time.Time // accessed after this time
	LastAccessedBefore *time.Time // accessed before this time
	ValidAfter         *time.Time // valid_from after this time
	ValidBefore        *time.Time // valid_from before this time
	ExpiresAfter       *time.Time // valid_until after this time
	ExpiresBefore      *time.Time // valid_until before this time
	Match              string     // literal substring match across content fields (case-insensitive)
	SimilarTo          string     // record ID -- use its stored embedding as query vector
	NearNode           string     // graph filter: only return nodes within MaxHops of this node
	MaxHops            int        // max graph distance from NearNode (default 2)
	Since              *time.Time
	IncludeHistorical bool
	Top             int
	MinEdges        *int   // minimum total edge count (in + out)
	MaxEdges        *int   // maximum total edge count
	Sort            string            // field to sort by (default: effective_score)
	Order           string            // "asc" or "desc" (default: "desc")
	Random          bool              // return random results (ignores sort/score)
	Meta            map[string]string // meta.* property filters (key -> value, exact match)
}

// Facets holds per-field value counts across a result set.
type Facets struct {
	Temporality     map[string]int `json:"temporality,omitempty"`
	KnowledgeType   map[string]int `json:"knowledge_type,omitempty"`
	EpistemicStatus map[string]int `json:"epistemic_status,omitempty"`
	Resolution      map[string]int `json:"resolution,omitempty"`
}

// Suggestions holds refinement hints returned when search results
// may benefit from additional filtering. Available filters lists
// meta.* field values found across results so agents can narrow
// their query.
type Suggestions struct {
	Reason           string                       `json:"reason"`
	AvailableFilters map[string]map[string]int    `json:"available_filters,omitempty"`
}

// ComputeSuggestions builds refinement suggestions from search results.
// Returns nil if results are high quality (top score above threshold).
// The NodeReader is used to read meta.* properties from result nodes.
func ComputeSuggestions(results []Result, reader graph.NodeReader, threshold float64) *Suggestions {
	if len(results) == 0 {
		return nil
	}

	// Check quality: if top result scores well, no suggestions needed.
	if results[0].EffectiveScore >= threshold {
		return nil
	}

	// Collect meta.* property values across all results.
	metaFacets := make(map[string]map[string]int)
	for _, r := range results {
		n, ok := reader.GetNode(r.ID)
		if !ok {
			continue
		}
		for k, v := range n.Properties {
			if len(k) > 5 && k[:5] == "meta." {
				field := k[5:]
				if metaFacets[field] == nil {
					metaFacets[field] = make(map[string]int)
				}
				metaFacets[field][v.FormatValue()]++
			}
		}
	}

	if len(metaFacets) == 0 {
		return nil
	}

	return &Suggestions{
		Reason:           "low_confidence",
		AvailableFilters: metaFacets,
	}
}

// ComputeFacets counts the distribution of enum fields across results.
func ComputeFacets(results []Result) Facets {
	f := Facets{
		Temporality:     make(map[string]int),
		KnowledgeType:   make(map[string]int),
		EpistemicStatus: make(map[string]int),
		Resolution:      make(map[string]int),
	}
	for _, r := range results {
		if r.Temporality != "" {
			f.Temporality[r.Temporality]++
		}
		if r.KnowledgeType != "" {
			f.KnowledgeType[r.KnowledgeType]++
		}
		if r.EpistemicStatus != "" {
			f.EpistemicStatus[r.EpistemicStatus]++
		}
		if r.Resolution != "" {
			f.Resolution[r.Resolution]++
		}
	}
	return f
}

// Result is a single search result.
type Result struct {
	ID              string  `json:"id"`
	Keywords        []string `json:"keywords,omitempty"`
	SummaryShort    string  `json:"summary_short,omitempty"`
	MetadataSummary string  `json:"metadata_summary"`
	Confidence      float64 `json:"confidence"`
	Temporality     string  `json:"temporality"`
	KnowledgeType   string  `json:"knowledge_type,omitempty"`
	EpistemicStatus string  `json:"epistemic_status,omitempty"`
	Resolution      string  `json:"resolution,omitempty"`
	ValidFrom       string  `json:"valid_from,omitempty"`
	ValidUntil      string  `json:"valid_until,omitempty"`
	AssertedAsOf    string  `json:"asserted_as_of,omitempty"`
	EffectiveScore  float64 `json:"effective_score"`
	LastAccessed    string  `json:"last_accessed,omitempty"`
	CreatedAt       string  `json:"created_at,omitempty"`
	AccessCount     int64   `json:"access_count,omitempty"`
	Importance      float64 `json:"importance,omitempty"`
	ContentLength   int     `json:"content_length,omitempty"`
	EdgeCount       int      `json:"edge_count,omitempty"`
	Staleness       float64  `json:"staleness,omitempty"`
	Collections     []string `json:"collections,omitempty"`
}

// Execute runs the search query and returns results. This calls
// the embedder inline -- for the server, use ExecuteWithVector to
// pre-embed outside the lock.
func (t *Tool) Execute(ctx context.Context, q Query) ([]Result, error) {
	var queryVec []float32
	if q.Text != "" && t.embedder != nil {
		vecs, err := t.embedder.Embed(ctx, []string{q.Text})
		if err != nil {
			return nil, fmt.Errorf("search: embed query: %w", err)
		}
		if len(vecs) > 0 {
			queryVec = vecs[0]
		}
	}
	return t.ExecuteWithVector(ctx, q, queryVec)
}

// ExecuteWithVector runs the search query using a pre-computed query
// vector. If queryVec is nil, similarity scoring is skipped (metadata-
// only search). This allows the caller to embed outside a lock.
func (t *Tool) ExecuteWithVector(_ context.Context, q Query, queryVec []float32) ([]Result, error) {
	if q.Top <= 0 {
		q.Top = 10
	}
	now := time.Now().UTC()

	// Determine sort field. When no text and no explicit sort, default
	// to created_at desc (most recent first) rather than effective_score
	// which is meaningless without similarity.
	sortField := q.Sort
	if sortField == "" && q.Text == "" && queryVec == nil {
		sortField = SortCreatedAt
	}
	ascending := q.Order == "asc"

	// If SimilarTo is set, use the record's stored embedding as query vector.
	if q.SimilarTo != "" && queryVec == nil {
		n, ok := t.graph.GetNode(q.SimilarTo)
		if ok {
			if v, ok := n.Properties["embedding_full"]; ok {
				queryVec = v.Vector()
			}
		}
	}

	// Step 1: Build candidate set via metadata filters.
	candidates := t.filterCandidates(q, now)

	// Exclude the source record from its own similar-to results.
	if q.SimilarTo != "" {
		filtered := candidates[:0]
		for _, id := range candidates {
			if id != q.SimilarTo {
				filtered = append(filtered, id)
			}
		}
		candidates = filtered
	}

	// Random mode: partial shuffle and take top-k, skip scoring.
	// Uses partial Fisher-Yates to avoid O(n) shuffle when Top << n.
	if q.Random {
		n := len(candidates)
		k := q.Top
		if k > n {
			k = n
		}
		for i := 0; i < k; i++ {
			j := i + rand.Intn(n-i)
			candidates[i], candidates[j] = candidates[j], candidates[i]
		}
		candidates = candidates[:k]
		results := make([]Result, 0, len(candidates))
		for _, id := range candidates {
			n, ok := t.graph.GetNode(id)
			if !ok {
				continue
			}
			results = append(results, t.buildResult(n, 0))
		}
		return results, nil
	}

	// Step 2: Compute similarity via hybrid search (vector + BM25 with RRF).
	candidateSet := make(map[string]struct{}, len(candidates))
	for _, id := range candidates {
		candidateSet[id] = struct{}{}
	}
	similarities := t.computeSimilarities(q, queryVec, candidateSet)

	// Step 3: Score each candidate and collect sort values.
	type scored struct {
		id       string
		score    float64
		sortVal  float64 // numeric sort key when using field sort
		sortTime time.Time // time sort key
	}
	var scoredResults []scored

	for _, id := range candidates {
		n, ok := t.graph.GetNode(id)
		if !ok {
			continue
		}

		inputs := t.buildScoreInputs(n, similarities[id])
		inputs.HasTextQuery = q.Text != ""
		score := ComputeScore(inputs, now, t.cfg)
		sr := scored{id: id, score: score}

		// Extract sort key if sorting by a field.
		switch sortField {
		case SortCreatedAt:
			if v, ok := n.Properties.GetTimestamp("created_at"); ok {
				sr.sortTime = v
			}
		case SortLastAccessed:
			if v, ok := n.Properties.GetTimestamp("last_accessed"); ok {
				sr.sortTime = v
			}
		case SortAccessCount:
			if v, ok := n.Properties.GetInt64("access_count"); ok {
				sr.sortVal = float64(v)
			}
		case SortConfidence:
			if v, ok := n.Properties.GetFloat64("confidence"); ok {
				sr.sortVal = v
			}
		case SortImportance:
			if v, ok := n.Properties.GetFloat64("importance"); ok {
				sr.sortVal = v
			}
		case SortContentLength:
			if v, ok := n.Properties.GetString("content_full"); ok {
				sr.sortVal = float64(len(v))
			}
		case SortEdgeCount:
			sr.sortVal = float64(edgeCount(t.graph, id))
		case SortStaleness:
			sr.sortVal = ComputeStaleness(n, now, t.cfg.Decay)
		}

		scoredResults = append(scoredResults, sr)
	}

	// Step 5: Sort.
	useTimeSort := sortField == SortCreatedAt || sortField == SortLastAccessed
	sort.Slice(scoredResults, func(i, j int) bool {
		var less bool
		switch {
		case sortField == "" || sortField == SortScore:
			less = scoredResults[i].score < scoredResults[j].score
		case useTimeSort:
			less = scoredResults[i].sortTime.Before(scoredResults[j].sortTime)
		default:
			less = scoredResults[i].sortVal < scoredResults[j].sortVal
		}
		if ascending {
			return less
		}
		return !less
	})
	if q.Top < len(scoredResults) {
		scoredResults = scoredResults[:q.Top]
	}

	// Step 6: Build results.
	results := make([]Result, 0, len(scoredResults))
	for _, sr := range scoredResults {
		n, ok := t.graph.GetNode(sr.id)
		if !ok {
			continue // node deleted between search and result assembly
		}
		results = append(results, t.buildResult(n, sr.score))
	}

	return results, nil
}

// filterCandidates returns node IDs matching the query's metadata filters.
// computeSimilarities runs vector search, BM25 search across three
// indexes, or both with weighted RRF fusion. Returns nodeID ->
// normalized similarity score in [0, 1].
func (t *Tool) computeSimilarities(q Query, queryVec []float32, candidateSet map[string]struct{}) map[string]float64 {
	similarities := make(map[string]float64)

	hasVec := queryVec != nil && t.vecIdx != nil
	hasBM25Full := t.bm25Full != nil && q.Text != ""
	hasBM25Medium := t.bm25Medium != nil && q.Text != ""
	hasBM25Short := t.bm25Short != nil && q.Text != ""
	hasBM25 := hasBM25Full || hasBM25Medium || hasBM25Short

	if !hasVec && !hasBM25 {
		return similarities
	}

	topK := q.Top * 3
	if topK > len(candidateSet) {
		topK = len(candidateSet)
	}

	rrfK := t.cfg.Search.RRFK
	if rrfK <= 0 {
		rrfK = 60
	}

	tokens := index.Tokenize(q.Text)

	// Run vector search.
	var vecResults []index.SearchResult
	if hasVec {
		vecResults = t.vecIdx.Search(queryVec, topK, candidateSet)
	}

	// Run BM25 search on each index.
	var bm25FullResults, bm25MediumResults, bm25ShortResults []index.SearchResult
	if hasBM25Full {
		bm25FullResults = t.bm25Full.Search(tokens, topK, candidateSet)
	}
	if hasBM25Medium {
		bm25MediumResults = t.bm25Medium.Search(tokens, topK, candidateSet)
	}
	if hasBM25Short {
		bm25ShortResults = t.bm25Short.Search(tokens, topK, candidateSet)
	}

	// RRF weights from config.
	wFull := t.cfg.Search.BM25WeightFull
	if wFull <= 0 {
		wFull = 1.0
	}
	wMedium := t.cfg.Search.BM25WeightMedium
	if wMedium <= 0 {
		wMedium = 2.0
	}
	wShort := t.cfg.Search.BM25WeightShort
	if wShort <= 0 {
		wShort = 3.0
	}

	switch {
	case hasVec && hasBM25:
		// Weighted RRF with BM25 gate:
		// score(d) = 1.0 * 1/(k+rank_vec)
		//          + w_full * 1/(k+rank_full)
		//          + w_medium * 1/(k+rank_medium)
		//          + w_short * 1/(k+rank_short)
		//
		// Records that appear ONLY in vector results (no BM25 term
		// overlap) are penalized: their score is reduced to 10% to
		// prevent high-cosine but irrelevant results from ranking.
		rrfScores := make(map[string]float64)
		bm25Hits := make(map[string]struct{})
		for rank, r := range vecResults {
			rrfScores[r.NodeID] += 1.0 / float64(rrfK+rank+1)
		}
		for rank, r := range bm25FullResults {
			rrfScores[r.NodeID] += wFull / float64(rrfK+rank+1)
			bm25Hits[r.NodeID] = struct{}{}
		}
		for rank, r := range bm25MediumResults {
			rrfScores[r.NodeID] += wMedium / float64(rrfK+rank+1)
			bm25Hits[r.NodeID] = struct{}{}
		}
		for rank, r := range bm25ShortResults {
			rrfScores[r.NodeID] += wShort / float64(rrfK+rank+1)
			bm25Hits[r.NodeID] = struct{}{}
		}
		maxRRF := 0.0
		for _, s := range rrfScores {
			if s > maxRRF {
				maxRRF = s
			}
		}
		if maxRRF > 0 {
			for id, s := range rrfScores {
				norm := s / maxRRF
				// Penalize vector-only matches (no term overlap).
				if _, hasBM25Match := bm25Hits[id]; !hasBM25Match {
					penalty := t.cfg.Search.VectorOnlyPenalty
					if penalty <= 0 {
						penalty = 0.1
					}
					norm *= penalty
				}
				similarities[id] = norm
			}
		}

	case hasVec:
		// Vector only (no BM25 indexes) -- use cosine similarity directly.
		for _, r := range vecResults {
			similarities[r.NodeID] = float64(r.Similarity)
		}

	default:
		// BM25 only -- weighted RRF across the three BM25 indexes,
		// then normalize to [0, 1].
		rrfScores := make(map[string]float64)
		for rank, r := range bm25FullResults {
			rrfScores[r.NodeID] += wFull / float64(rrfK+rank+1)
		}
		for rank, r := range bm25MediumResults {
			rrfScores[r.NodeID] += wMedium / float64(rrfK+rank+1)
		}
		for rank, r := range bm25ShortResults {
			rrfScores[r.NodeID] += wShort / float64(rrfK+rank+1)
		}
		maxRRF := 0.0
		for _, s := range rrfScores {
			if s > maxRRF {
				maxRRF = s
			}
		}
		if maxRRF > 0 {
			for id, s := range rrfScores {
				similarities[id] = s / maxRRF
			}
		}
	}

	return similarities
}

func (t *Tool) filterCandidates(q Query, now time.Time) []string {
	// Start with all nodes if no filters narrow the set via index.
	var sets []map[string]struct{}

	// Handle enum filters with negation support ("!value" excludes).
	enumFilter := func(key, val string) {
		if val == "" {
			return
		}
		if strings.HasPrefix(val, "!") {
			// Negation: all nodes with this key minus those matching the value.
			exclude := toSet(t.propIdx.Lookup(key, graph.StringProperty(val[1:])))
			have := t.propIdx.NodesWithKey(key)
			if have == nil {
				// No nodes have this key at all -- empty set.
				sets = append(sets, map[string]struct{}{})
				return
			}
			result := make(map[string]struct{})
			for id := range have {
				if _, excluded := exclude[id]; !excluded {
					result[id] = struct{}{}
				}
			}
			sets = append(sets, result)
		} else {
			ids := t.propIdx.Lookup(key, graph.StringProperty(val))
			sets = append(sets, toSet(ids))
		}
	}

	enumFilter("temporality", q.Temporality)
	enumFilter("knowledge_type", q.KnowledgeType)
	enumFilter("epistemic_status", q.EpistemicStatus)

	// Resolution filter: "unresolved" is special -- means no resolution
	// property set. Other values use the standard enum filter.
	if q.Resolution == "unresolved" {
		// Nodes that do NOT have a resolution property.
		have := t.propIdx.NodesWithKey("resolution")
		result := make(map[string]struct{})
		it := t.graph.NodeIterator()
		for it.Next() {
			id := it.Node().ID
			if _, has := have[id]; !has {
				result[id] = struct{}{}
			}
		}
		it.Close()
		sets = append(sets, result)
	} else {
		enumFilter("resolution", q.Resolution)
	}

	// Missing filter: exclude nodes that have the specified properties.
	for _, field := range q.Missing {
		have := t.propIdx.NodesWithKey(field)
		result := make(map[string]struct{})
		it := t.graph.NodeIterator()
		for it.Next() {
			id := it.Node().ID
			if _, has := have[id]; !has {
				result[id] = struct{}{}
			}
		}
		it.Close()
		sets = append(sets, result)
	}

	// Keyword exact match: all specified keywords must be present.
	for _, kw := range q.Keywords {
		ids := t.propIdx.LookupKeyword("content_keywords", kw)
		sets = append(sets, toSet(ids))
	}

	// Meta property filters: exact match on meta.* properties.
	for k, v := range q.Meta {
		propKey := "meta." + k
		ids := t.propIdx.Lookup(propKey, graph.StringProperty(v))
		sets = append(sets, toSet(ids))
	}

	// Full-text substring match: union across content fields.
	if q.Match != "" {
		matchSet := make(map[string]struct{})
		for _, key := range []string{"content_full", "content_short"} {
			for _, id := range t.propIdx.ContainsFold(key, q.Match) {
				matchSet[id] = struct{}{}
			}
		}
		sets = append(sets, matchSet)
	}

	// Intersect all filter sets, or start with all nodes if no index filters.
	var candidateSet map[string]struct{}
	if len(sets) == 0 {
		candidateSet = make(map[string]struct{}, t.graph.NodeCount())
		it := t.graph.NodeIterator()
		for it.Next() {
			candidateSet[it.Node().ID] = struct{}{}
		}
		it.Close()
	} else {
		candidateSet = sets[0]
		for i := 1; i < len(sets); i++ {
			candidateSet = intersect(candidateSet, sets[i])
		}
	}

	// Graph proximity filter: restrict to nodes within MaxHops of NearNode.
	if q.NearNode != "" {
		maxHops := q.MaxHops
		if maxHops <= 0 {
			maxHops = 2
		}
		nearby := bfsReachable(t.graph, q.NearNode, maxHops)
		candidateSet = intersect(candidateSet, nearby)
	}

	// Apply remaining filters that require property reads.
	var result []string
	for id := range candidateSet {
		n, ok := t.graph.GetNode(id)
		if !ok {
			continue
		}

		// Exclude legacy chunk nodes (dumb fragments). Section nodes
		// (structural splits with metadata) are included in results.
		if isLegacyChunk(t.graph, id) {
			continue
		}

		// Exclude collection items and collection containers -- they're
		// accessed via gramaton_collection_items, not search.
		if isCollectionItem(t.graph, id) {
			continue
		}
		if kt, ok := n.Properties.GetString("knowledge_type"); ok && kt == "collection" {
			continue
		}

		if q.ConfidenceMin != nil {
			if c, ok := n.Properties.GetFloat64("confidence"); ok {
				if c < *q.ConfidenceMin {
					continue
				}
			}
		}
		if q.ConfidenceMax != nil {
			if c, ok := n.Properties.GetFloat64("confidence"); ok {
				if c > *q.ConfidenceMax {
					continue
				}
			}
		}
		if q.ImportanceMin != nil {
			if imp, ok := n.Properties.GetFloat64("importance"); ok {
				if imp < *q.ImportanceMin {
					continue
				}
			}
		}
		if q.ImportanceMax != nil {
			if imp, ok := n.Properties.GetFloat64("importance"); ok {
				if imp > *q.ImportanceMax {
					continue
				}
			}
		}
		if q.AccessCountMin != nil {
			ac, ok := n.Properties.GetInt64("access_count")
			if !ok || ac < *q.AccessCountMin {
				continue
			}
		}
		if q.AccessCountMax != nil {
			ac, _ := n.Properties.GetInt64("access_count")
			if ac > *q.AccessCountMax {
				continue
			}
		}
		if q.LastAccessedAfter != nil {
			la, ok := n.Properties.GetTimestamp("last_accessed")
			if !ok || la.Before(*q.LastAccessedAfter) {
				continue
			}
		}
		if q.LastAccessedBefore != nil {
			la, ok := n.Properties.GetTimestamp("last_accessed")
			if !ok || !la.Before(*q.LastAccessedBefore) {
				continue
			}
		}
		if q.ValidAfter != nil {
			vf, ok := n.Properties.GetTimestamp("valid_from")
			if !ok || vf.Before(*q.ValidAfter) {
				continue
			}
		}
		if q.ValidBefore != nil {
			vf, ok := n.Properties.GetTimestamp("valid_from")
			if !ok || !vf.Before(*q.ValidBefore) {
				continue
			}
		}
		if q.ExpiresAfter != nil {
			vu, ok := n.Properties.GetTimestamp("valid_until")
			if !ok || vu.Before(*q.ExpiresAfter) {
				continue
			}
		}
		if q.ExpiresBefore != nil {
			vu, ok := n.Properties.GetTimestamp("valid_until")
			if !ok || !vu.Before(*q.ExpiresBefore) {
				continue
			}
		}
		if q.Since != nil {
			if ca, ok := n.Properties.GetTimestamp("created_at"); ok {
				if ca.Before(*q.Since) {
					continue
				}
			}
		}

		if q.MinEdges != nil || q.MaxEdges != nil {
			ec := edgeCount(t.graph, id)
			if q.MinEdges != nil && ec < *q.MinEdges {
				continue
			}
			if q.MaxEdges != nil && ec > *q.MaxEdges {
				continue
			}
		}

		// Validity filtering: by default, prefer current records.
		if !q.IncludeHistorical {
			if vu, ok := n.Properties["valid_until"]; ok {
				if vu.Timestamp().Before(now) {
					// Historical record -- still included but with penalty
					// (handled in scoring via validity_multiplier).
				}
			}
		}

		result = append(result, id)
	}

	return result
}

func (t *Tool) buildScoreInputs(n *graph.Node, similarity float64) ScoreInputs {
	inputs := ScoreInputs{
		Similarity: similarity,
	}

	if v, ok := n.Properties.GetString("temporality"); ok {
		inputs.Temporality = v
	}
	if v, ok := n.Properties.GetFloat64("confidence"); ok {
		inputs.Confidence = v
	}
	if v, ok := n.Properties.GetFloat64("importance"); ok {
		inputs.Importance = v
	}
	if v, ok := n.Properties.GetInt64("access_count"); ok {
		inputs.AccessCount = v
	}
	if v, ok := n.Properties.GetFloat64("activation_boost"); ok {
		inputs.ActivationBoost = v
	}
	if v, ok := n.Properties.GetTimestamp("valid_from"); ok {
		inputs.ValidFrom = v
	}
	if v, ok := n.Properties.GetTimestamp("valid_until"); ok {
		inputs.ValidUntil = v
	}
	if v, ok := n.Properties.GetTimestamp("created_at"); ok {
		inputs.CreatedAt = v
	}

	return inputs
}

func (t *Tool) buildResult(n *graph.Node, score float64) Result {
	r := Result{
		ID:             n.ID,
		EffectiveScore: score,
	}

	if v, ok := n.Properties.GetStringList("content_keywords"); ok {
		r.Keywords = v
	}
	if v, ok := n.Properties.GetString("content_short"); ok {
		r.SummaryShort = v
	}
	if v, ok := n.Properties.GetFloat64("confidence"); ok {
		r.Confidence = v
	}
	if v, ok := n.Properties.GetString("temporality"); ok {
		r.Temporality = v
	}
	if v, ok := n.Properties.GetString("knowledge_type"); ok {
		r.KnowledgeType = v
	}
	if v, ok := n.Properties.GetString("epistemic_status"); ok {
		r.EpistemicStatus = v
	}
	if v, ok := n.Properties.GetString("resolution"); ok {
		r.Resolution = v
	}
	if v, ok := n.Properties.GetTimestamp("valid_from"); ok {
		r.ValidFrom = v.Format(time.RFC3339)
	}
	if v, ok := n.Properties.GetTimestamp("valid_until"); ok {
		r.ValidUntil = v.Format(time.RFC3339)
	}
	if v, ok := n.Properties.GetTimestamp("asserted_as_of"); ok {
		r.AssertedAsOf = v.Format(time.RFC3339)
	}
	if v, ok := n.Properties.GetTimestamp("last_accessed"); ok {
		r.LastAccessed = v.Format(time.RFC3339)
	}
	if v, ok := n.Properties.GetTimestamp("created_at"); ok {
		r.CreatedAt = v.Format(time.RFC3339)
	}
	if v, ok := n.Properties.GetInt64("access_count"); ok {
		r.AccessCount = v
	}
	if v, ok := n.Properties.GetFloat64("importance"); ok {
		r.Importance = v
	}
	if v, ok := n.Properties.GetString("content_full"); ok {
		r.ContentLength = len(v)
	}
	r.EdgeCount = edgeCount(t.graph, n.ID)
	r.Staleness = ComputeStaleness(n, time.Now().UTC(), t.cfg.Decay)

	r.MetadataSummary = buildMetadataSummary(n.Properties)

	return r
}

// buildMetadataSummary generates a one-line LLM-readable summary.
// Format: "Current. Durable, high-confidence (0.85), well-established. Last accessed 3 days ago."
func buildMetadataSummary(props graph.Properties) string {
	now := time.Now().UTC()
	var parts []string

	// Validity status with expiration proximity.
	if vu, ok := props.GetTimestamp("valid_until"); ok {
		if vu.Before(now) {
			days := int(now.Sub(vu).Hours() / 24)
			if days == 0 {
				parts = append(parts, "Historical (expired today).")
			} else if days == 1 {
				parts = append(parts, "Historical (expired yesterday).")
			} else {
				parts = append(parts, fmt.Sprintf("Historical (expired %d days ago).", days))
			}
		} else {
			days := int(vu.Sub(now).Hours() / 24)
			if days == 0 {
				parts = append(parts, "Current (expires today).")
			} else if days == 1 {
				parts = append(parts, "Current (expires tomorrow).")
			} else {
				parts = append(parts, fmt.Sprintf("Current (expires in %d days).", days))
			}
		}
	} else {
		parts = append(parts, "Current.")
	}

	// Temporality.
	if v, ok := props.GetString("temporality"); ok {
		parts = append(parts, capitalize(v))
	}

	// Confidence with qualifier.
	if c, ok := props.GetFloat64("confidence"); ok {
		var qualifier string
		switch {
		case c >= 0.9:
			qualifier = "high-confidence"
		case c >= 0.7:
			qualifier = "confidence"
		case c >= 0.4:
			qualifier = "moderate-confidence"
		default:
			qualifier = "low-confidence"
		}
		parts = append(parts, fmt.Sprintf("%s (%.2f)", qualifier, c))
	}

	// Epistemic status.
	if s, ok := props.GetString("epistemic_status"); ok {
		if s == "well_established" {
			s = "well-established"
		}
		parts = append(parts, s)
	}

	// Build the main line.
	result := parts[0] // "Current." or "Historical."
	for i := 1; i < len(parts); i++ {
		if i == 1 {
			result += " " + parts[i]
		} else {
			result += ", " + parts[i]
		}
	}

	// Resolution status.
	if v, ok := props.GetString("resolution"); ok {
		result += fmt.Sprintf(". Resolved: %s", v)
	}

	// Last accessed.
	if la, ok := props.GetTimestamp("last_accessed"); ok {
		days := int(now.Sub(la).Hours() / 24)
		switch {
		case days == 0:
			result += ". Last accessed today"
		case days == 1:
			result += ". Last accessed yesterday"
		default:
			result += fmt.Sprintf(". Last accessed %d days ago", days)
		}
	}

	return result
}

func capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 32
	}
	return string(r)
}

// computeStaleness returns 0.0-1.0 representing how stale a record is.
// Uses the same decay model as access recency but inverted: 1.0 = maximally
// stale, 0.0 = just accessed. Immutable records always return 0.
// ComputeStaleness returns 0.0-1.0 representing how stale a record is.
// Exported for use by the curation layer.
func ComputeStaleness(n *graph.Node, now time.Time, cfg config.DecayConfig) float64 {
	temp, _ := n.Properties.GetString("temporality")
	if temp == "immutable" {
		return 0
	}
	la, ok := n.Properties.GetTimestamp("last_accessed")
	if !ok {
		// Never accessed -- use created_at as fallback.
		la, ok = n.Properties.GetTimestamp("created_at")
		if !ok {
			return 1.0 // no temporal data at all = maximally stale
		}
	}
	recency := accessRecency(temp, la, now, cfg)
	return 1.0 - recency
}

// edgeCount returns the total number of edges (in + out) for a node,
// excluding chunk_of and section_of edges (structural, not semantic).
// edgeCount returns semantic (non-structural) edge count via graph.IsStructuralEdge.
func edgeCount(g graph.NodeReader, id string) int {
	count := 0
	for _, e := range g.EdgesFrom(id) {
		if !graph.IsStructuralEdge(e.Type) {
			count++
		}
	}
	for _, e := range g.EdgesTo(id) {
		if !graph.IsStructuralEdge(e.Type) {
			count++
		}
	}
	return count
}

// isLegacyChunk checks if a node is a legacy dumb chunk (chunk_of edge).
// Section nodes (section_of) are intentionally NOT excluded from search
// results -- they have metadata and are independently meaningful.
func isLegacyChunk(g graph.NodeReader, id string) bool {
	for _, e := range g.EdgesFrom(id) {
		if e.Type == "chunk_of" {
			return true
		}
	}
	return false
}

// bfsReachable returns all node IDs reachable from startID within maxHops
// via any edge direction. The start node itself is excluded.
func bfsReachable(g graph.NodeReader, startID string, maxHops int) map[string]struct{} {
	visited := map[string]struct{}{startID: {}}
	frontier := []string{startID}

	for hop := 0; hop < maxHops && len(frontier) > 0; hop++ {
		var next []string
		for _, id := range frontier {
			for _, e := range g.EdgesFrom(id) {
				if _, seen := visited[e.TargetID]; !seen {
					visited[e.TargetID] = struct{}{}
					next = append(next, e.TargetID)
				}
			}
			for _, e := range g.EdgesTo(id) {
				if _, seen := visited[e.SourceID]; !seen {
					visited[e.SourceID] = struct{}{}
					next = append(next, e.SourceID)
				}
			}
		}
		frontier = next
	}

	// Remove start node -- we want neighbors, not the node itself.
	delete(visited, startID)
	return visited
}

// isCollectionItem checks if a node has a member_of edge (collection membership).
func isCollectionItem(g graph.NodeReader, id string) bool {
	for _, e := range g.EdgesFrom(id) {
		if e.Type == "member_of" {
			return true
		}
	}
	return false
}

func toSet(ids []string) map[string]struct{} {
	s := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

func intersect(a, b map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{})
	for id := range a {
		if _, ok := b[id]; ok {
			result[id] = struct{}{}
		}
	}
	return result
}
