package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPSearchTools(mcpServer *mcp.Server) {
	type searchInput struct {
		Text              string   `json:"text,omitempty" jsonschema:"search query text (optional -- omit for filter-only queries)"`
		Top               int      `json:"top,omitempty" jsonschema:"integer, number of results (default 10)"`
		Temporality       string   `json:"temporality,omitempty" jsonschema:"filter: immutable|durable|temporal|ephemeral (prefix with ! to exclude, e.g. !ephemeral)"`
		KnowledgeType     string   `json:"knowledge_type,omitempty" jsonschema:"filter: episodic|semantic|procedural|conceptual|reference (prefix with ! to exclude)"`
		EpistemicStatus   string   `json:"epistemic_status,omitempty" jsonschema:"filter: well_established|probable|speculative|contested|refuted (prefix with ! to exclude)"`
		Resolution        string   `json:"resolution,omitempty" jsonschema:"filter: completed|superseded|abandoned|obsolete|unresolved (unresolved = no resolution set)"`
		ConfidenceMin     *float64 `json:"confidence_min,omitempty" jsonschema:"number between 0.0 and 1.0"`
		ConfidenceMax     *float64 `json:"confidence_max,omitempty" jsonschema:"number between 0.0 and 1.0"`
		ImportanceMin     *float64 `json:"importance_min,omitempty" jsonschema:"number between 0.0 and 1.0"`
		ImportanceMax     *float64 `json:"importance_max,omitempty" jsonschema:"number between 0.0 and 1.0"`
		IncludeHistorical bool     `json:"include_historical,omitempty" jsonschema:"include records past valid_until"`
		Since             string   `json:"since,omitempty" jsonschema:"filter: created after date (YYYY-MM-DD or RFC3339)"`
		Missing           []string `json:"missing,omitempty" jsonschema:"array of field names that must be unset (e.g. [\"temporality\", \"confidence\"])"`
		Keywords            []string `json:"keywords,omitempty" jsonschema:"array of keywords that must all be present on the record (exact match)"`
		AccessCountMin      *int64   `json:"access_count_min,omitempty" jsonschema:"integer, minimum access count"`
		AccessCountMax      *int64   `json:"access_count_max,omitempty" jsonschema:"integer, maximum access count"`
		LastAccessedAfter   string   `json:"last_accessed_after,omitempty" jsonschema:"filter: accessed after date (YYYY-MM-DD or RFC3339)"`
		LastAccessedBefore  string   `json:"last_accessed_before,omitempty" jsonschema:"filter: accessed before date (YYYY-MM-DD or RFC3339)"`
		ValidAfter          string   `json:"valid_after,omitempty" jsonschema:"filter: valid_from after date"`
		ValidBefore         string   `json:"valid_before,omitempty" jsonschema:"filter: valid_from before date"`
		ExpiresAfter        string   `json:"expires_after,omitempty" jsonschema:"filter: valid_until after date (find records expiring after X)"`
		ExpiresBefore       string   `json:"expires_before,omitempty" jsonschema:"filter: valid_until before date (find records expiring before X)"`
		Match               string   `json:"match,omitempty" jsonschema:"literal substring search across content fields (case-insensitive). Distinct from text (vector similarity)"`
		SimilarTo           string   `json:"similar_to,omitempty" jsonschema:"record ID -- find records similar to this one using its stored embedding"`
		MinEdges            *int     `json:"min_edges,omitempty" jsonschema:"integer, minimum total edge count (orphan detection: max_edges=0)"`
		MaxEdges            *int     `json:"max_edges,omitempty" jsonschema:"integer, maximum total edge count"`
		Random              bool     `json:"random,omitempty" jsonschema:"return random results (ignores sort/score). Useful for serendipitous discovery or review"`
		Sort              string   `json:"sort,omitempty" jsonschema:"sort by: created_at|last_accessed|access_count|confidence|importance|content_length|edge_count|staleness (default: effective_score, or created_at if no text)"`
		Order             string   `json:"order,omitempty" jsonschema:"asc or desc (default: desc)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_search",
		Description: "Search the knowledge store. Text is optional -- omit it for filter-only queries (e.g. 'all procedural records'). Returns results ranked by 6-factor score or sorted by a specified field.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args searchInput) (*mcp.CallToolResult, any, error) {
		result, svcErr := s.serviceSearch(ctx, &searchRequest{
			Text:               args.Text,
			Top:                args.Top,
			Temporality:        args.Temporality,
			KnowledgeType:      args.KnowledgeType,
			EpistemicStatus:    args.EpistemicStatus,
			Resolution:         args.Resolution,
			ConfidenceMin:      args.ConfidenceMin,
			ConfidenceMax:      args.ConfidenceMax,
			ImportanceMin:      args.ImportanceMin,
			ImportanceMax:      args.ImportanceMax,
			IncludeHistorical:  args.IncludeHistorical,
			Since:              args.Since,
			Missing:            args.Missing,
			Keywords:           args.Keywords,
			AccessCountMin:     args.AccessCountMin,
			AccessCountMax:     args.AccessCountMax,
			LastAccessedAfter:  args.LastAccessedAfter,
			LastAccessedBefore: args.LastAccessedBefore,
			ValidAfter:         args.ValidAfter,
			ValidBefore:        args.ValidBefore,
			ExpiresAfter:       args.ExpiresAfter,
			ExpiresBefore:      args.ExpiresBefore,
			Match:              args.Match,
			SimilarTo:          args.SimilarTo,
			MinEdges:           args.MinEdges,
			MaxEdges:           args.MaxEdges,
			Random:             args.Random,
			Sort:               args.Sort,
			Order:              args.Order,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	type exploreInput struct {
		NodeID    string   `json:"node_id" jsonschema:"starting node ID"`
		Depth     int      `json:"depth,omitempty" jsonschema:"max traversal depth (default 2)"`
		EdgeTypes []string `json:"edge_types,omitempty" jsonschema:"edge types to follow"`
		MinWeight float64  `json:"min_weight,omitempty" jsonschema:"minimum edge weight"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_explore",
		Description: "Traverse the knowledge graph from a starting node. Returns connected nodes and edges.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args exploreInput) (*mcp.CallToolResult, any, error) {
		result, svcErr := s.serviceExplore(&exploreRequest{
			NodeID:    args.NodeID,
			Depth:     args.Depth,
			EdgeTypes: args.EdgeTypes,
			MinWeight: args.MinWeight,
		})
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})

	type duplicatesInput struct {
		Threshold float64 `json:"threshold,omitempty" jsonschema:"number between 0.0 and 1.0, minimum similarity to consider a duplicate (default 0.92)"`
		MaxPairs  int     `json:"max_pairs,omitempty" jsonschema:"integer, maximum number of pairs to return (default 50)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_duplicates",
		Description: "Find near-duplicate records by comparing stored embeddings. Returns pairs above the similarity threshold.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args duplicatesInput) (*mcp.CallToolResult, any, error) {
		result, svcErr := s.serviceDuplicates(args.Threshold, args.MaxPairs)
		if svcErr != nil {
			return mcpServiceErr(svcErr)
		}
		return mcpJSONResult(result)
	})
}
