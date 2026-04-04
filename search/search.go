package search

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/index"
)

// Tool implements the search command -- Tier 1 of the retrieval funnel.
type Tool struct {
	graph    nodeReader
	propIdx  *index.PropertyIndex
	vecIdx   index.VectorIndex
	embedder embedder
	cfg      config.Config
}

// Consumer-defined interfaces. The search tool only needs these methods.

type nodeReader interface {
	GetNode(id string) (*graph.Node, bool)
	AllNodeIDs() []string
	NodeCount() int
}

type embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// New creates a search tool. embedder may be nil (no vector search).
func New(g nodeReader, propIdx *index.PropertyIndex, vecIdx index.VectorIndex, emb embedder, cfg config.Config) *Tool {
	return &Tool{
		graph:    g,
		propIdx:  propIdx,
		vecIdx:   vecIdx,
		embedder: emb,
		cfg:      cfg,
	}
}

// Query specifies search parameters.
type Query struct {
	Text            string
	ConfidenceMin   *float64
	ConfidenceMax   *float64
	Temporality     string
	KnowledgeType   string
	EpistemicStatus string
	Since           *time.Time
	IncludeHistorical bool
	Top             int
}

// Result is a single search result.
type Result struct {
	ID              string    `json:"id"`
	Keywords        []string  `json:"keywords,omitempty"`
	SummaryShort    string    `json:"summary_short,omitempty"`
	MetadataSummary string    `json:"metadata_summary"`
	Confidence      float64   `json:"confidence"`
	Temporality     string    `json:"temporality"`
	KnowledgeType   string    `json:"knowledge_type,omitempty"`
	EpistemicStatus string    `json:"epistemic_status,omitempty"`
	EffectiveScore  float64   `json:"effective_score"`
	LastAccessed    string    `json:"last_accessed,omitempty"`
}

// Execute runs the search query and returns results.
func (t *Tool) Execute(ctx context.Context, q Query) ([]Result, error) {
	if q.Top <= 0 {
		q.Top = 10
	}
	now := time.Now().UTC()

	// Step 1: Build candidate set via metadata filters.
	candidates := t.filterCandidates(q, now)

	// Step 2: If we have query text and an embedder, compute vector similarity.
	similarities := make(map[string]float64)
	if q.Text != "" && t.embedder != nil && t.vecIdx != nil {
		vecs, err := t.embedder.Embed(ctx, []string{q.Text})
		if err != nil {
			return nil, fmt.Errorf("search: embed query: %w", err)
		}
		if len(vecs) > 0 {
			candidateSet := make(map[string]struct{}, len(candidates))
			for _, id := range candidates {
				candidateSet[id] = struct{}{}
			}

			results := t.vecIdx.Search(vecs[0], len(candidates), candidateSet)
			for _, r := range results {
				similarities[r.NodeID] = float64(r.Similarity)
			}
		}
	}

	// Step 3: Compute max access count for frequency normalization.
	var maxAccess int64
	for _, id := range candidates {
		n, ok := t.graph.GetNode(id)
		if !ok {
			continue
		}
		if ac, ok := n.Properties["access_count"]; ok {
			if ac.Int64() > maxAccess {
				maxAccess = ac.Int64()
			}
		}
	}

	// Step 4: Score each candidate.
	type scored struct {
		id    string
		score float64
	}
	var scoredResults []scored

	for _, id := range candidates {
		n, ok := t.graph.GetNode(id)
		if !ok {
			continue
		}

		inputs := t.buildScoreInputs(n, similarities[id], maxAccess)
		score := ComputeScore(inputs, now, t.cfg)
		scoredResults = append(scoredResults, scored{id: id, score: score})
	}

	// Step 5: Sort by descending score, take top-k.
	sort.Slice(scoredResults, func(i, j int) bool {
		return scoredResults[i].score > scoredResults[j].score
	})
	if q.Top < len(scoredResults) {
		scoredResults = scoredResults[:q.Top]
	}

	// Step 6: Build results.
	results := make([]Result, 0, len(scoredResults))
	for _, sr := range scoredResults {
		n, _ := t.graph.GetNode(sr.id)
		results = append(results, t.buildResult(n, sr.score))
	}

	return results, nil
}

// filterCandidates returns node IDs matching the query's metadata filters.
func (t *Tool) filterCandidates(q Query, now time.Time) []string {
	// Start with all nodes if no filters narrow the set via index.
	var sets []map[string]struct{}

	if q.Temporality != "" {
		ids := t.propIdx.Lookup("temporality", graph.StringProperty(q.Temporality))
		sets = append(sets, toSet(ids))
	}
	if q.KnowledgeType != "" {
		ids := t.propIdx.Lookup("knowledge_type", graph.StringProperty(q.KnowledgeType))
		sets = append(sets, toSet(ids))
	}
	if q.EpistemicStatus != "" {
		ids := t.propIdx.Lookup("epistemic_status", graph.StringProperty(q.EpistemicStatus))
		sets = append(sets, toSet(ids))
	}

	// Intersect all filter sets, or start with all nodes if no index filters.
	var candidateSet map[string]struct{}
	if len(sets) == 0 {
		allIDs := t.graph.AllNodeIDs()
		candidateSet = toSet(allIDs)
	} else {
		candidateSet = sets[0]
		for i := 1; i < len(sets); i++ {
			candidateSet = intersect(candidateSet, sets[i])
		}
	}

	// Apply remaining filters that require property reads.
	var result []string
	for id := range candidateSet {
		n, ok := t.graph.GetNode(id)
		if !ok {
			continue
		}

		if q.ConfidenceMin != nil {
			if c, ok := n.Properties["confidence"]; ok {
				if c.Float64() < *q.ConfidenceMin {
					continue
				}
			}
		}
		if q.ConfidenceMax != nil {
			if c, ok := n.Properties["confidence"]; ok {
				if c.Float64() > *q.ConfidenceMax {
					continue
				}
			}
		}
		if q.Since != nil {
			if ca, ok := n.Properties["created_at"]; ok {
				if ca.Timestamp().Before(*q.Since) {
					continue
				}
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

func (t *Tool) buildScoreInputs(n *graph.Node, similarity float64, maxAccess int64) ScoreInputs {
	inputs := ScoreInputs{
		Similarity:     similarity,
		MaxAccessCount: maxAccess,
	}

	if v, ok := n.Properties["temporality"]; ok {
		inputs.Temporality = v.String()
	}
	if v, ok := n.Properties["confidence"]; ok {
		inputs.Confidence = v.Float64()
	}
	if v, ok := n.Properties["importance"]; ok {
		inputs.Importance = v.Float64()
	}
	if v, ok := n.Properties["access_count"]; ok {
		inputs.AccessCount = v.Int64()
	}
	if v, ok := n.Properties["last_accessed"]; ok {
		inputs.LastAccessed = v.Timestamp()
	}
	if v, ok := n.Properties["activation_boost"]; ok {
		inputs.ActivationBoost = v.Float64()
	}
	if v, ok := n.Properties["valid_from"]; ok {
		inputs.ValidFrom = v.Timestamp()
	}
	if v, ok := n.Properties["valid_until"]; ok {
		inputs.ValidUntil = v.Timestamp()
	}
	if v, ok := n.Properties["created_at"]; ok {
		inputs.CreatedAt = v.Timestamp()
	}

	return inputs
}

func (t *Tool) buildResult(n *graph.Node, score float64) Result {
	r := Result{
		ID:             n.ID,
		EffectiveScore: score,
	}

	if v, ok := n.Properties["content_keywords"]; ok {
		r.Keywords = v.StringList()
	}
	if v, ok := n.Properties["content_short"]; ok {
		r.SummaryShort = v.String()
	}
	if v, ok := n.Properties["confidence"]; ok {
		r.Confidence = v.Float64()
	}
	if v, ok := n.Properties["temporality"]; ok {
		r.Temporality = v.String()
	}
	if v, ok := n.Properties["knowledge_type"]; ok {
		r.KnowledgeType = v.String()
	}
	if v, ok := n.Properties["epistemic_status"]; ok {
		r.EpistemicStatus = v.String()
	}
	if v, ok := n.Properties["last_accessed"]; ok {
		r.LastAccessed = v.Timestamp().Format(time.RFC3339)
	}

	r.MetadataSummary = buildMetadataSummary(n.Properties)

	return r
}

// buildMetadataSummary generates a one-line LLM-readable summary.
func buildMetadataSummary(props graph.Properties) string {
	var parts []string

	// Validity status.
	if vu, ok := props["valid_until"]; ok {
		if vu.Timestamp().Before(time.Now().UTC()) {
			parts = append(parts, "Historical")
		} else {
			parts = append(parts, "Current")
		}
	} else {
		parts = append(parts, "Current")
	}

	// Temporality + confidence.
	if v, ok := props["temporality"]; ok {
		parts = append(parts, v.String())
	}
	if v, ok := props["confidence"]; ok {
		parts = append(parts, fmt.Sprintf("confidence %.2f", v.Float64()))
	}
	if v, ok := props["epistemic_status"]; ok {
		s := v.String()
		// Format for readability.
		switch s {
		case "well_established":
			parts = append(parts, "well-established")
		default:
			parts = append(parts, s)
		}
	}

	result := ""
	for i, p := range parts {
		if i == 0 {
			result = p + "."
		} else if i == 1 {
			result += " " + capitalize(p)
		} else {
			result += ", " + p
		}
	}
	return result
}

func capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	return string(s[0]-32) + s[1:] // ASCII uppercase first char
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
