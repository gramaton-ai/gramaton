package api

import (
	"context"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/search"
)

// StatsResponse summarises the knowledge store's composition.
//
// Counts include ALL non-deleted, non-structural-child nodes: user
// records AND derived concept nodes. The manifest produced by
// curation (`records_by_type` etc.) deliberately excludes concept
// nodes since concepts are derived from clusters rather than
// captured by the user; both counts are correct under their own
// semantics.
type StatsResponse struct {
	TotalRecords    int            `json:"total_records"`
	Temporality     map[string]int `json:"temporality"`
	KnowledgeType   map[string]int `json:"knowledge_type"`
	EpistemicStatus map[string]int `json:"epistemic_status"`
	Confidence      ConfidenceDist `json:"confidence"`
}

// ConfidenceDist groups records into confidence bands for summary.
type ConfidenceDist struct {
	High     int `json:"high"`     // >= 0.9
	Medium   int `json:"medium"`   // 0.7-0.9
	Moderate int `json:"moderate"` // 0.4-0.7
	Low      int `json:"low"`      // < 0.4
	Unset    int `json:"unset"`
}

// StatsDescription is the MCP tool description for gramaton_stats.
const StatsDescription = "Get aggregate statistics: counts by temporality, knowledge_type, epistemic_status, confidence distribution, and LLM usage. Counts every non-deleted record including derived concept nodes (knowledge_type=conceptual aggregates user-captured records AND emergent concepts; the curation manifest reports user-only counts under records_by_type)."

// Stats iterates the graph under a read lock and counts records by
// key metadata fields. Excludes chunk nodes and deleted records.
func (a *API) Stats(ctx context.Context) (StatsResponse, *APIError) {
	a.engine.RLock()
	defer a.engine.RUnlock()

	g := a.engine.Graph()
	resp := StatsResponse{
		Temporality:     make(map[string]int),
		KnowledgeType:   make(map[string]int),
		EpistemicStatus: make(map[string]int),
	}

	it := g.NodeIterator()
	defer it.Close()
	for it.Next() {
		n := it.Node()
		if g.IsStructuralChild(n.ID) {
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

// StatusRequest has no inputs.
type StatusRequest struct{}

// StatusResponse summarises server health at a glance.
type StatusResponse struct {
	Nodes     int  `json:"nodes"`
	Edges     int  `json:"edges"`
	Embedding bool `json:"embedding"`
}

// StatusDescription is the MCP tool description for gramaton_status.
const StatusDescription = "Get store health: node/edge counts, embedding status."

// Status returns liveness-adjacent counters. Light enough to call
// every few seconds from a dashboard.
func (a *API) Status(ctx context.Context, _ StatusRequest) (StatusResponse, *APIError) {
	a.engine.RLock()
	defer a.engine.RUnlock()
	return StatusResponse{
		Nodes:     a.engine.Graph().NodeCount(),
		Edges:     a.engine.Graph().EdgeCount(),
		Embedding: a.engine.Embedder() != nil,
	}, nil
}

// PendingRequest limits how many pending records to return.
type PendingRequest struct {
	Limit int `json:"limit,omitempty" jsonschema:"max records to return (default 50, max 500)"`
}

// PendingRecord is a lightweight row in a pending listing.
type PendingRecord struct {
	ID           string `json:"id"`
	SummaryShort string `json:"summary_short,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// PendingResponse reports the pending records and whether the result
// was truncated.
type PendingResponse struct {
	Records   []PendingRecord `json:"records"`
	Total     int             `json:"total"`
	Truncated bool            `json:"truncated,omitempty"`
}

// PendingDescription is the MCP tool description for gramaton_pending.
const PendingDescription = "List records awaiting classification (processing_status=captured)."

// Pending returns records that have been captured but not yet
// classified. Ordering is index-walk order (not time-based) so agents
// that classify-and-move cover the set over multiple calls.
func (a *API) Pending(ctx context.Context, req PendingRequest) (PendingResponse, *APIError) {
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	a.engine.RLock()
	defer a.engine.RUnlock()

	captured := a.engine.PropIdx().Lookup("processing_status", graph.StringProperty("captured"))

	resp := PendingResponse{Total: len(captured), Records: []PendingRecord{}}
	for _, id := range captured {
		if len(resp.Records) >= limit {
			break
		}
		entry := PendingRecord{ID: id}
		if n, ok := a.engine.Graph().GetNode(id); ok {
			if v, ok := n.Properties.GetString("content_short"); ok {
				entry.SummaryShort = v
			}
			if v, ok := n.Properties.GetTimestamp("created_at"); ok {
				entry.CreatedAt = v.Format("2006-01-02T15:04:05Z")
			}
		}
		resp.Records = append(resp.Records, entry)
	}
	if len(captured) > limit {
		resp.Truncated = true
	}
	return resp, nil
}

// DuplicatesRequest carries the similarity threshold + pair cap.
type DuplicatesRequest struct {
	Threshold float64 `json:"threshold,omitempty" jsonschema:"minimum cosine similarity (default 0.92, must be > 0 and <= 1)"`
	MaxPairs  int     `json:"max_pairs,omitempty" jsonschema:"maximum pair count to return (default 50, max 1000)"`
}

// DuplicatesResponse lists duplicate pairs.
type DuplicatesResponse struct {
	Pairs     []search.DuplicatePair `json:"pairs"`
	Count     int                    `json:"count"`
	Truncated bool                   `json:"truncated,omitempty"`
}

// DuplicatesDescription is the MCP tool description.
const DuplicatesDescription = "Find near-duplicate records by embedding similarity."

// Duplicates runs FindDuplicates with the given threshold/cap.
func (a *API) Duplicates(ctx context.Context, req DuplicatesRequest) (DuplicatesResponse, *APIError) {
	threshold := req.Threshold
	if threshold <= 0 || threshold > 1.0 {
		threshold = 0.92
	}
	maxPairs := req.MaxPairs
	if maxPairs <= 0 {
		maxPairs = 50
	}
	if maxPairs > MaxDuplicatePairs {
		maxPairs = MaxDuplicatePairs
	}

	a.engine.RLock()
	defer a.engine.RUnlock()

	pairs := search.FindDuplicates(a.engine.Graph(), a.engine.VecIdx(), threshold, maxPairs+1)
	truncated := false
	if len(pairs) > maxPairs {
		pairs = pairs[:maxPairs]
		truncated = true
	}

	return DuplicatesResponse{
		Pairs:     pairs,
		Count:     len(pairs),
		Truncated: truncated,
	}, nil
}
