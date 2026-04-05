package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPHandler returns an HTTP handler that serves the MCP protocol
// via Streamable HTTP transport.
func (s *Server) MCPHandler() http.Handler {
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "gramaton",
		Version: "0.2.0",
	}, nil)

	s.registerMCPTools(mcpServer)

	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{
		// Allow JSON responses when client sends Accept: application/json.
		// SSE is still used when client requests text/event-stream.
		JSONResponse: true,
	})
}

// MCPServer returns a configured MCP server for use with stdio transport.
func (s *Server) MCPServer() *mcp.Server {
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "gramaton",
		Version: "0.2.0",
	}, nil)

	s.registerMCPTools(mcpServer)
	return mcpServer
}

func (s *Server) registerMCPTools(mcpServer *mcp.Server) {
	type searchInput struct {
		Text              string   `json:"text,omitempty" jsonschema:"search query text (optional -- omit for filter-only queries)"`
		Top               int      `json:"top,omitempty" jsonschema:"integer, number of results (default 10)"`
		Temporality       string   `json:"temporality,omitempty" jsonschema:"filter: immutable|durable|temporal|ephemeral (prefix with ! to exclude, e.g. !ephemeral)"`
		KnowledgeType     string   `json:"knowledge_type,omitempty" jsonschema:"filter: episodic|semantic|procedural|conceptual|reference (prefix with ! to exclude)"`
		EpistemicStatus   string   `json:"epistemic_status,omitempty" jsonschema:"filter: well_established|probable|speculative|contested|refuted (prefix with ! to exclude)"`
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
		top := args.Top
		if top <= 0 {
			top = 10
		}
		if top > maxSearchTop {
			top = maxSearchTop
		}

		if args.Sort != "" && !search.ValidSort(args.Sort) {
			return mcpErr("invalid sort field")
		}
		if args.Order != "" && args.Order != "asc" && args.Order != "desc" {
			return mcpErr("order must be asc or desc")
		}
		if len(args.Keywords) > maxKeywords {
			return mcpErr(fmt.Sprintf("maximum %d keywords allowed", maxKeywords))
		}
		if len(args.Missing) > maxMissingFields {
			return mcpErr(fmt.Sprintf("maximum %d missing fields allowed", maxMissingFields))
		}
		if len(args.Match) > maxMatchLength {
			return mcpErr(fmt.Sprintf("match string exceeds maximum length of %d", maxMatchLength))
		}
		for _, v := range []struct {
			name string
			val  *float64
		}{
			{"confidence_min", args.ConfidenceMin},
			{"confidence_max", args.ConfidenceMax},
			{"importance_min", args.ImportanceMin},
			{"importance_max", args.ImportanceMax},
		} {
			if err := validateFloat64Range(v.name, v.val, 0, 1); err != nil {
				return mcpErr(err.Error())
			}
		}

		q := search.Query{
			Text:              args.Text,
			Top:               top,
			Temporality:       args.Temporality,
			KnowledgeType:     args.KnowledgeType,
			EpistemicStatus:   args.EpistemicStatus,
			IncludeHistorical: args.IncludeHistorical,
			ConfidenceMin:     args.ConfidenceMin,
			ConfidenceMax:     args.ConfidenceMax,
			ImportanceMin:     args.ImportanceMin,
			ImportanceMax:     args.ImportanceMax,
			Missing:           args.Missing,
			Keywords:          args.Keywords,
			AccessCountMin:    args.AccessCountMin,
			AccessCountMax:    args.AccessCountMax,
			Match:             args.Match,
			SimilarTo:         args.SimilarTo,
			MinEdges:          args.MinEdges,
			MaxEdges:          args.MaxEdges,
			Random:            args.Random,
			Sort:              args.Sort,
			Order:             args.Order,
		}
		if args.Since != "" {
			t, err := parseDateArg(args.Since)
			if err != nil {
				return mcpErr(fmt.Sprintf("invalid since date: %s", err))
			}
			q.Since = &t
		}
		if args.LastAccessedAfter != "" {
			t, err := parseDateArg(args.LastAccessedAfter)
			if err != nil {
				return mcpErr(fmt.Sprintf("invalid last_accessed_after date: %s", err))
			}
			q.LastAccessedAfter = &t
		}
		if args.LastAccessedBefore != "" {
			t, err := parseDateArg(args.LastAccessedBefore)
			if err != nil {
				return mcpErr(fmt.Sprintf("invalid last_accessed_before date: %s", err))
			}
			q.LastAccessedBefore = &t
		}
		for _, pair := range []struct {
			raw  string
			name string
			dest **time.Time
		}{
			{args.ValidAfter, "valid_after", &q.ValidAfter},
			{args.ValidBefore, "valid_before", &q.ValidBefore},
			{args.ExpiresAfter, "expires_after", &q.ExpiresAfter},
			{args.ExpiresBefore, "expires_before", &q.ExpiresBefore},
		} {
			if pair.raw != "" {
				t, err := parseDateArg(pair.raw)
				if err != nil {
					return mcpErr(fmt.Sprintf("invalid %s date: %s", pair.name, err))
				}
				*pair.dest = &t
			}
		}

		var queryVec []float32
		if q.Text != "" && s.engine.Embedder() != nil {
			vecs, err := s.engine.Embedder().Embed(ctx, []string{q.Text})
			if err == nil && len(vecs) > 0 {
				queryVec = vecs[0]
			}
		}

		s.engine.RLock()
		results, err := s.engine.Searcher().ExecuteWithVector(ctx, q, queryVec)
		s.engine.RUnlock()

		if err != nil {
			return mcpErr("search failed")
		}

		if len(results) > 0 {
			s.engine.Lock()
			now := time.Now().UTC()
			cfg := s.engine.Config()
			acfg := graph.ActivationConfig{
				BaseAmount:        cfg.Activation.BaseAmount,
				AttenuationFactor: cfg.Activation.AttenuationFactor,
			}
			for _, r := range results {
				s.engine.Graph().RecordAccess(r.ID, now, acfg)
			}
			s.engine.Save("access")
			s.engine.Unlock()
		}

		if results == nil {
			results = []search.Result{}
		}

		return mcpJSONResult(map[string]any{
			"results": results,
			"facets":  search.ComputeFacets(results),
		})
	})

	type captureInput struct {
		Content           string   `json:"content" jsonschema:"the knowledge to store (required)"`
		Temporality       string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
		Confidence        *float64 `json:"confidence,omitempty" jsonschema:"number between 0.0 and 1.0"`
		KnowledgeType     string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus   string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
		Importance        *float64 `json:"importance,omitempty" jsonschema:"number between 0.0 and 1.0"`
		Keywords          []string `json:"keywords,omitempty" jsonschema:"array of keyword strings for search"`
		SummaryShort      string   `json:"summary_short,omitempty" jsonschema:"max 200 chars"`
		SummaryAbstract   string   `json:"summary_abstract,omitempty" jsonschema:"longer summary"`
		SourceRef         string   `json:"source_ref,omitempty" jsonschema:"source URL or path"`
		SourceCredibility *float64 `json:"source_credibility,omitempty" jsonschema:"number between 0.0 and 1.0"`
		ContextAbout      string   `json:"context_about,omitempty" jsonschema:"topic/domain"`
		ContextWho        string   `json:"context_who,omitempty" jsonschema:"entities involved"`
		ContextFindable   string   `json:"context_findable_by,omitempty" jsonschema:"future retrieval terms"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "gramaton_capture",
		Description: `Store a knowledge record in the graph. Returns the new record ID.

Example: gramaton_capture(content="User prefers dark mode", temporality="durable", confidence=0.95, knowledge_type="semantic", keywords=["preference", "ui"], summary_short="User prefers dark mode")

IMPORTANT: confidence must be a number (not a string). keywords must be an array (not a string).`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args captureInput) (*mcp.CallToolResult, any, error) {
		if args.Content == "" {
			return mcpErr("content is required")
		}

		// Pre-embed outside lock.
		capReq := &captureRequest{
			Content:         args.Content,
			Temporality:     args.Temporality,
			Confidence:      args.Confidence,
			KnowledgeType:   args.KnowledgeType,
			EpistemicStatus: args.EpistemicStatus,
			Importance:      args.Importance,
			Keywords:        args.Keywords,
			SummaryShort:    args.SummaryShort,
			SummaryAbstract: args.SummaryAbstract,
			SourceRef:       args.SourceRef,
			SourceCredibility: args.SourceCredibility,
			ContextAbout:    args.ContextAbout,
			ContextWho:      args.ContextWho,
			ContextFindable: args.ContextFindable,
		}

		if err := validateCaptureRequest(capReq); err != nil {
			return mcpErr(err.Error())
		}

		preEmbedded := s.preEmbedContent(capReq)
		preChunked := s.engine.PreChunk(ctx, args.Content)

		s.engine.Lock()
		defer s.engine.Unlock()

		props := graph.Properties{
			"content_full": graph.StringProperty(args.Content),
			"created_at":   graph.TimestampProperty(time.Now().UTC()),
			"access_count": graph.Int64Property(0),
		}

		if args.Temporality != "" || args.Confidence != nil {
			props["processing_status"] = graph.StringProperty("processed")
		} else {
			props["processing_status"] = graph.StringProperty("captured")
		}
		setOptionalProps(props, capReq)

		n := s.engine.Graph().AddNode(props)
		for k, v := range n.Properties {
			s.engine.PropIdx().Add(n.ID, k, v)
		}

		var warnings []string
		if err := s.applyPreEmbedded(n.ID, preEmbedded); err != nil {
			warnings = append(warnings, fmt.Sprintf("embedding failed: %s", err))
		}

		// Auto-supersede near-duplicates.
		var superseded []map[string]any
		if dupID, sim := s.engine.CheckDedup(n.ID); dupID != "" {
			cfg := s.engine.Config()
			if cfg.Dedup.Action == "reject" {
				s.engine.PropIdx().RemoveNode(n.ID, n.Properties)
				s.engine.VecIdx().Remove(n.ID)
				s.engine.Graph().DeleteNode(n.ID)
				return mcpErr(fmt.Sprintf("duplicate of %s (similarity %.3f)", dupID, sim))
			}

			now := time.Now().UTC()
			oldNode, _ := s.engine.Graph().GetNode(dupID)
			if oldNode != nil {
				_, alreadyHistorical := oldNode.Properties.GetTimestamp("valid_until")
				if !alreadyHistorical {
					s.engine.SetProp(dupID, "valid_until", graph.TimestampProperty(now))
					if e, err := s.engine.Graph().AddEdge(n.ID, dupID, "supersedes", sim, nil); err == nil {
						summary := ""
						if v, ok := oldNode.Properties.GetString("content_short"); ok {
							summary = v
						}
						superseded = append(superseded, map[string]any{
							"id":         dupID,
							"summary":    summary,
							"similarity": sim,
							"edge_id":    e.ID,
						})
					}
				}
			}
		}

		if numChunks := s.engine.ApplyChunks(n.ID, preChunked); numChunks > 0 {
			warnings = append(warnings, fmt.Sprintf("content chunked into %d segments", numChunks))
		}

		s.engine.Save("capture")

		resp := map[string]any{"id": n.ID, "warnings": warnings}
		if len(superseded) > 0 {
			resp["superseded"] = superseded
		}
		return mcpJSONResult(resp)
	})

	type inspectInput struct {
		ID string `json:"id" jsonschema:"record ID to inspect"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_inspect",
		Description: "Get full content, metadata, and related records for a specific record.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args inspectInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return mcpErr("id is required")
		}

		s.engine.Lock()
		defer s.engine.Unlock()

		n, ok := s.engine.Graph().GetNode(args.ID)
		if !ok {
			return mcpErr("record not found")
		}

		now := time.Now().UTC()
		cfg := s.engine.Config()
		s.engine.Graph().RecordAccess(args.ID, now, graph.ActivationConfig{
			BaseAmount:        cfg.Activation.BaseAmount,
			AttenuationFactor: cfg.Activation.AttenuationFactor,
		})
		s.engine.Save("access")
		n, _ = s.engine.Graph().GetNode(args.ID)

		props := make(map[string]any, len(n.Properties))
		for k, v := range n.Properties {
			props[k] = v.FormatValue()
		}

		var related []map[string]any
		for _, e := range s.engine.Graph().EdgesFrom(args.ID) {
			rel := map[string]any{"id": e.TargetID, "edge_type": e.Type, "edge_weight": e.Weight, "direction": "outbound"}
			if t, ok := s.engine.Graph().GetNode(e.TargetID); ok {
				if v, ok := t.Properties.GetString("content_short"); ok {
					rel["summary_short"] = v
				}
			}
			related = append(related, rel)
		}
		for _, e := range s.engine.Graph().EdgesTo(args.ID) {
			rel := map[string]any{"id": e.SourceID, "edge_type": e.Type, "edge_weight": e.Weight, "direction": "inbound"}
			if t, ok := s.engine.Graph().GetNode(e.SourceID); ok {
				if v, ok := t.Properties.GetString("content_short"); ok {
					rel["summary_short"] = v
				}
			}
			related = append(related, rel)
		}

		return mcpJSONResult(map[string]any{
			"id": n.ID, "properties": props,
			"metadata_summary": inspectMetadataSummary(n.Properties),
			"related":          related,
		})
	})

	type updateInput struct {
		ID              string   `json:"id" jsonschema:"record ID to update"`
		Confidence      *float64 `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
		Temporality     string   `json:"temporality,omitempty"`
		KnowledgeType   string   `json:"knowledge_type,omitempty"`
		EpistemicStatus string   `json:"epistemic_status,omitempty"`
		Importance      *float64 `json:"importance,omitempty" jsonschema:"0.0-1.0"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_update",
		Description: "Update metadata properties on an existing record.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args updateInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return mcpErr("id is required")
		}

		s.engine.Lock()
		defer s.engine.Unlock()

		if _, ok := s.engine.Graph().GetNode(args.ID); !ok {
			return mcpErr("record not found")
		}

		updated := false
		if args.Confidence != nil {
			s.engine.SetProp(args.ID, "confidence", graph.Float64Property(*args.Confidence))
			updated = true
		}
		if args.Temporality != "" {
			s.engine.SetProp(args.ID, "temporality", graph.StringProperty(args.Temporality))
			updated = true
		}
		if args.KnowledgeType != "" {
			s.engine.SetProp(args.ID, "knowledge_type", graph.StringProperty(args.KnowledgeType))
			updated = true
		}
		if args.EpistemicStatus != "" {
			s.engine.SetProp(args.ID, "epistemic_status", graph.StringProperty(args.EpistemicStatus))
			updated = true
		}
		if args.Importance != nil {
			s.engine.SetProp(args.ID, "importance", graph.Float64Property(*args.Importance))
			updated = true
		}
		if updated {
			s.engine.Save("update")
		}

		return mcpJSONResult(map[string]any{"id": args.ID, "updated": updated})
	})

	type linkInput struct {
		ID         string   `json:"id" jsonschema:"source record ID"`
		TargetID   string   `json:"target_id" jsonschema:"target record ID"`
		EdgeType   string   `json:"edge_type" jsonschema:"relationship type (e.g. related_to, discusses, justifies)"`
		EdgeWeight *float64 `json:"edge_weight,omitempty" jsonschema:"0.0-1.0 (default 0.5)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_link",
		Description: "Create an edge between two records in the knowledge graph.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args linkInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" || args.TargetID == "" || args.EdgeType == "" {
			return mcpErr("id, target_id, and edge_type are required")
		}

		s.engine.Lock()
		defer s.engine.Unlock()

		weight := 0.5
		if args.EdgeWeight != nil {
			weight = *args.EdgeWeight
		}

		e, err := s.engine.Graph().AddEdge(args.ID, args.TargetID, args.EdgeType, weight, nil)
		if err != nil {
			return mcpErr(err.Error())
		}
		s.engine.Save("link")

		return mcpJSONResult(map[string]any{"id": args.ID, "edge_id": e.ID})
	})

	type classifyInput struct {
		ID              string   `json:"id" jsonschema:"record ID to classify"`
		Temporality     string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
		Confidence      *float64 `json:"confidence,omitempty" jsonschema:"number between 0.0 and 1.0"`
		KnowledgeType   string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
		Keywords        []string `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
		SummaryShort    string   `json:"summary_short,omitempty" jsonschema:"max 200 chars"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_classify",
		Description: "Classify a pending record with metadata. Sets processing_status to processed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args classifyInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return mcpErr("id is required")
		}

		s.engine.Lock()
		defer s.engine.Unlock()

		if _, ok := s.engine.Graph().GetNode(args.ID); !ok {
			return mcpErr("record not found")
		}

		if args.Temporality != "" {
			s.engine.SetProp(args.ID, "temporality", graph.StringProperty(args.Temporality))
		}
		if args.Confidence != nil {
			s.engine.SetProp(args.ID, "confidence", graph.Float64Property(*args.Confidence))
		}
		if args.KnowledgeType != "" {
			s.engine.SetProp(args.ID, "knowledge_type", graph.StringProperty(args.KnowledgeType))
		}
		if args.EpistemicStatus != "" {
			s.engine.SetProp(args.ID, "epistemic_status", graph.StringProperty(args.EpistemicStatus))
		}
		if len(args.Keywords) > 0 {
			s.engine.SetProp(args.ID, "content_keywords", graph.StringListProperty(args.Keywords))
		}
		if args.SummaryShort != "" {
			s.engine.SetProp(args.ID, "content_short", graph.StringProperty(args.SummaryShort))
		}
		s.engine.SetProp(args.ID, "processing_status", graph.StringProperty("processed"))
		s.engine.Save("classify")

		return mcpJSONResult(map[string]any{"id": args.ID, "updated": true})
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
		if args.NodeID == "" {
			return mcpErr("node_id is required")
		}
		depth := args.Depth
		if depth <= 0 {
			depth = 2
		}

		s.engine.RLock()
		defer s.engine.RUnlock()

		if _, ok := s.engine.Graph().GetNode(args.NodeID); !ok {
			return mcpErr("record not found")
		}

		sub := s.engine.Graph().Traverse(args.NodeID, graph.TraverseOptions{
			MaxDepth: depth, EdgeTypes: args.EdgeTypes, MinEdgeWeight: args.MinWeight,
		})
		return mcpJSONResult(sub)
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_pending",
		Description: "List records awaiting classification (processing_status=captured).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		s.engine.RLock()
		defer s.engine.RUnlock()

		captured := s.engine.PropIdx().Lookup("processing_status", graph.StringProperty("captured"))
		var records []map[string]any
		for _, id := range captured {
			entry := map[string]any{"id": id}
			if n, ok := s.engine.Graph().GetNode(id); ok {
				if v, ok := n.Properties.GetString("content_short"); ok {
					entry["summary_short"] = v
				}
			}
			records = append(records, entry)
		}
		return mcpJSONResult(map[string]any{"records": records})
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_status",
		Description: "Get knowledge store health: node/edge counts, embedding status, curation status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		s.engine.RLock()
		defer s.engine.RUnlock()

		curation := computeCuration(s.engine)
		return mcpJSONResult(map[string]any{
			"nodes":     s.engine.Graph().NodeCount(),
			"edges":     s.engine.Graph().EdgeCount(),
			"embedding": s.engine.Embedder() != nil,
			"curation":  curation,
		})
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_stats",
		Description: "Get aggregate statistics: counts by temporality, knowledge_type, epistemic_status, confidence distribution.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		s.engine.RLock()
		defer s.engine.RUnlock()

		g := s.engine.Graph()
		temp := make(map[string]int)
		kt := make(map[string]int)
		es := make(map[string]int)
		var total int
		var confHigh, confMed, confMod, confLow, confUnset int

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
			total++
			if v, ok := n.Properties.GetString("temporality"); ok {
				temp[v]++
			}
			if v, ok := n.Properties.GetString("knowledge_type"); ok {
				kt[v]++
			}
			if v, ok := n.Properties.GetString("epistemic_status"); ok {
				es[v]++
			}
			if c, ok := n.Properties.GetFloat64("confidence"); ok {
				switch {
				case c >= 0.9:
					confHigh++
				case c >= 0.7:
					confMed++
				case c >= 0.4:
					confMod++
				default:
					confLow++
				}
			} else {
				confUnset++
			}
		}

		return mcpJSONResult(map[string]any{
			"total_records":   total,
			"temporality":     temp,
			"knowledge_type":  kt,
			"epistemic_status": es,
			"confidence": map[string]int{
				"high":     confHigh,
				"medium":   confMed,
				"moderate": confMod,
				"low":      confLow,
				"unset":    confUnset,
			},
		})
	})

	type branchInput struct {
		Action string `json:"action" jsonschema:"create|list|checkout|merge|discard"`
		Name   string `json:"name,omitempty" jsonschema:"branch name (required for create/checkout/merge/discard)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_branch",
		Description: "Manage branches: create, list, checkout, merge, or discard.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args branchInput) (*mcp.CallToolResult, any, error) {
		switch args.Action {
		case "list":
			s.engine.RLock()
			defer s.engine.RUnlock()

			dataDir := s.engine.Config().DataDir
			active := core.ActiveBranch(dataDir)
			dir := core.RefsDir(dataDir)
			entries, _ := os.ReadDir(dir)
			var branches []map[string]any
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				hash, _ := core.ReadRef(dataDir, e.Name())
				b := map[string]any{"name": e.Name(), "commit": core.TruncHash(hash)}
				if e.Name() == active {
					b["active"] = true
				}
				branches = append(branches, b)
			}
			if branches == nil {
				branches = []map[string]any{}
			}
			return mcpJSONResult(map[string]any{"branches": branches, "current": active})

		case "create":
			if args.Name == "" {
				return mcpErr("name is required for create")
			}
			if err := core.ValidBranchName(args.Name); err != nil {
				return mcpErr(err.Error())
			}

			s.engine.Lock()
			defer s.engine.Unlock()

			dataDir := s.engine.Config().DataDir
			if _, err := core.ReadRef(dataDir, args.Name); err == nil {
				return mcpErr(fmt.Sprintf("branch %q already exists", args.Name))
			}
			headHash := s.engine.HeadHashLocked()
			if err := core.WriteRef(dataDir, args.Name, headHash); err != nil {
				return mcpErr("failed to create branch")
			}
			return mcpJSONResult(map[string]any{"name": args.Name, "commit": core.TruncHash(headHash), "created": true})

		case "checkout":
			if args.Name == "" {
				return mcpErr("name is required for checkout")
			}
			if err := core.ValidBranchName(args.Name); err != nil {
				return mcpErr(err.Error())
			}

			s.engine.Lock()
			defer s.engine.Unlock()

			dataDir := s.engine.Config().DataDir
			hash, err := core.ReadRef(dataDir, args.Name)
			if err != nil {
				return mcpErr(fmt.Sprintf("branch %q not found", args.Name))
			}
			headPath := filepath.Join(dataDir, "HEAD")
			if err := core.AtomicWriteFile(headPath, []byte(hash), 0o600); err != nil {
				return mcpErr("failed to update HEAD")
			}
			if err := core.SetActiveBranch(dataDir, args.Name); err != nil {
				return mcpErr("failed to set active branch")
			}
			s.engine.Graph().Load(s.engine.Store(), hash)
			s.engine.RebuildAllIndexes()
			return mcpJSONResult(map[string]any{"name": args.Name, "commit": core.TruncHash(hash), "checked_out": true})

		case "merge":
			if args.Name == "" {
				return mcpErr("name is required for merge")
			}
			if args.Name == "main" {
				return mcpErr("cannot merge main into itself")
			}

			s.engine.Lock()
			defer s.engine.Unlock()

			dataDir := s.engine.Config().DataDir
			core.SetActiveBranch(dataDir, "main")
			branchHash, err := core.ReadRef(dataDir, args.Name)
			if err != nil {
				return mcpErr(fmt.Sprintf("branch %q not found", args.Name))
			}
			s.engine.Graph().Load(s.engine.Store(), branchHash)
			s.engine.RebuildAllIndexes()
			commit, err := s.engine.Save(fmt.Sprintf("merge branch %q", args.Name))
			if err != nil {
				return mcpErr("failed to save merge")
			}
			core.WriteRef(dataDir, "main", commit.Hash)
			core.DeleteRef(dataDir, args.Name)
			return mcpJSONResult(map[string]any{"merged": args.Name, "new_commit": core.TruncHash(commit.Hash)})

		case "discard":
			if args.Name == "" {
				return mcpErr("name is required for discard")
			}
			if args.Name == "main" {
				return mcpErr("cannot discard main")
			}

			s.engine.Lock()
			defer s.engine.Unlock()

			dataDir := s.engine.Config().DataDir
			if _, err := core.ReadRef(dataDir, args.Name); err != nil {
				return mcpErr(fmt.Sprintf("branch %q not found", args.Name))
			}
			if core.ActiveBranch(dataDir) == args.Name {
				mainHash, err := core.ReadRef(dataDir, "main")
				if err == nil {
					headPath := filepath.Join(dataDir, "HEAD")
					core.AtomicWriteFile(headPath, []byte(mainHash), 0o600)
				}
				core.SetActiveBranch(dataDir, "main")
			}
			core.DeleteRef(dataDir, args.Name)
			return mcpJSONResult(map[string]any{"discarded": args.Name})

		default:
			return mcpErr("action must be one of: create, list, checkout, merge, discard")
		}
	})

	type diffInput struct {
		Since string `json:"since,omitempty" jsonschema:"show changes after date (YYYY-MM-DD)"`
		Topic string `json:"topic,omitempty" jsonschema:"filter by topic keyword"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_diff",
		Description: "Show what changed in the knowledge store since a date, optionally filtered by topic.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args diffInput) (*mcp.CallToolResult, any, error) {
		if len(args.Topic) > maxTopicLength {
			return mcpErr(fmt.Sprintf("topic exceeds maximum length of %d", maxTopicLength))
		}

		s.engine.RLock()
		defer s.engine.RUnlock()

		store := s.engine.Store()
		headHash := s.engine.HeadHashLocked()

		var sinceHash string
		if args.Since != "" {
			sinceTime, err := parseDateArg(args.Since)
			if err != nil {
				return mcpErr("invalid since date")
			}
			hash := headHash
			for hash != "" {
				commit, err := loadCommit(store, hash)
				if err != nil {
					break
				}
				if commit.Timestamp.Before(sinceTime) {
					sinceHash = hash
					break
				}
				hash = commit.Parent
			}
		}

		if sinceHash == "" && args.Since != "" {
			return mcpJSONResult(map[string]any{"added": []any{}, "removed": []any{}})
		}

		headCommit, err := loadCommit(store, headHash)
		if err != nil {
			return mcpErr("failed to load HEAD")
		}

		var sinceCommit *graph.Commit
		if sinceHash != "" {
			c, err := loadCommit(store, sinceHash)
			if err != nil {
				return mcpErr("failed to load since commit")
			}
			sinceCommit = c
		}

		diff, err := graph.DiffCommits(store, sinceCommit, headCommit)
		if err != nil {
			return mcpErr("failed to compute diff")
		}

		var added, removed []map[string]any
		for _, entry := range diff.Added {
			if args.Topic != "" && !matchesTopic(s, entry.Key, args.Topic) {
				continue
			}
			rec := map[string]any{"id": entry.Key}
			if n, ok := s.engine.Graph().GetNode(entry.Key); ok {
				if v, ok := n.Properties.GetString("content_short"); ok {
					rec["summary_short"] = v
				}
			}
			added = append(added, rec)
		}
		for _, entry := range diff.Removed {
			if args.Topic != "" && !matchesTopic(s, entry.Key, args.Topic) {
				continue
			}
			removed = append(removed, map[string]any{"id": entry.Key})
		}
		if added == nil {
			added = []map[string]any{}
		}
		if removed == nil {
			removed = []map[string]any{}
		}
		return mcpJSONResult(map[string]any{"added": added, "removed": removed})
	})

	type logInput struct {
		Limit  int    `json:"limit,omitempty" jsonschema:"max entries (default 20)"`
		Record string `json:"record,omitempty" jsonschema:"record ID for per-record history"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_log",
		Description: "View commit history or per-record change history.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args logInput) (*mcp.CallToolResult, any, error) {
		limit := args.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > maxLogLimit {
			limit = maxLogLimit
		}

		s.engine.RLock()
		defer s.engine.RUnlock()

		if args.Record != "" {
			// Per-record history.
			store := s.engine.Store()
			hash := s.engine.HeadHashLocked()
			var changes []map[string]any
			var prevHash string
			depth := 0

			for hash != "" && len(changes) < limit && depth < maxLogTraversal {
				depth++
				commit, err := loadCommit(store, hash)
				if err != nil {
					break
				}
				nodeHash, found, _ := graph.NodeHashInCommit(store, hash, args.Record)
				if found {
					if prevHash != "" && nodeHash != prevHash {
						changes = append(changes, map[string]any{
							"commit":    hash[:12],
							"timestamp": commit.Timestamp.Format("2006-01-02T15:04:05Z"),
							"action":    commit.Message,
						})
					} else if prevHash == "" {
						changes = append(changes, map[string]any{
							"commit":    hash[:12],
							"timestamp": commit.Timestamp.Format("2006-01-02T15:04:05Z"),
							"action":    commit.Message,
						})
					}
					prevHash = nodeHash
				}
				hash = commit.Parent
			}
			if changes == nil {
				changes = []map[string]any{}
			}
			return mcpJSONResult(map[string]any{"id": args.Record, "changes": changes})
		}

		// Commit history.
		var commits []map[string]any
		hash := s.engine.HeadHashLocked()
		store := s.engine.Store()

		for hash != "" && len(commits) < limit {
			commit, err := loadCommit(store, hash)
			if err != nil {
				break
			}
			commits = append(commits, map[string]any{
				"hash":      hash[:12],
				"timestamp": commit.Timestamp.Format("2006-01-02T15:04:05Z"),
				"action":    commit.Message,
			})
			hash = commit.Parent
		}
		if commits == nil {
			commits = []map[string]any{}
		}
		return mcpJSONResult(map[string]any{"commits": commits})
	})

	type reembedInput struct {
		Batch int `json:"batch,omitempty" jsonschema:"max records to process (default 50)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_reembed",
		Description: "Regenerate stale embeddings (model changed or missing).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reembedInput) (*mcp.CallToolResult, any, error) {
		batch := args.Batch
		if batch <= 0 {
			batch = 50
		}

		s.engine.Lock()
		defer s.engine.Unlock()

		if s.engine.Embedder() == nil {
			return mcpErr("no embedding provider configured")
		}

		currentModel := s.engine.Embedder().ModelID()
		var staleIDs []string
		for _, id := range s.engine.Graph().AllNodeIDs() {
			n, ok := s.engine.Graph().GetNode(id)
			if !ok {
				continue
			}
			model, ok := n.Properties.GetString("embedding_model")
			if !ok {
				if _, hasContent := n.Properties.GetString("content_full"); hasContent {
					staleIDs = append(staleIDs, id)
				}
				continue
			}
			if model != currentModel {
				staleIDs = append(staleIDs, id)
			}
		}
		if len(staleIDs) > batch {
			staleIDs = staleIDs[:batch]
		}

		reembedded := 0
		for _, id := range staleIDs {
			if err := s.engine.GenerateEmbeddings(ctx, id); err != nil {
				continue
			}
			reembedded++
		}
		if reembedded > 0 {
			s.engine.Save("reembed")
		}

		return mcpJSONResult(map[string]any{"reembedded": reembedded, "total_stale": len(staleIDs)})
	})

	type duplicatesInput struct {
		Threshold float64 `json:"threshold,omitempty" jsonschema:"number between 0.0 and 1.0, minimum similarity to consider a duplicate (default 0.92)"`
		MaxPairs  int     `json:"max_pairs,omitempty" jsonschema:"integer, maximum number of pairs to return (default 50)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_duplicates",
		Description: "Find near-duplicate records by comparing stored embeddings. Returns pairs above the similarity threshold.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args duplicatesInput) (*mcp.CallToolResult, any, error) {
		threshold := args.Threshold
		if threshold <= 0 || threshold > 1.0 {
			threshold = 0.92
		}
		maxPairs := args.MaxPairs
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

		return mcpJSONResult(map[string]any{
			"pairs":     pairs,
			"threshold": threshold,
			"count":     len(pairs),
		})
	})
}

// mcpError returns an MCP tool result indicating an error.
func mcpErr(msg string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}, nil, nil
}

// mcpJSONResult converts a value to a TextContent MCP result.
func mcpJSONResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcpErr("failed to marshal result")
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, nil, nil
}
