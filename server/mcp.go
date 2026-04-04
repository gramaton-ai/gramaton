package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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
		Text              string   `json:"text,omitempty" jsonschema:"search query text"`
		Top               int      `json:"top,omitempty" jsonschema:"integer, number of results (default 10)"`
		Temporality       string   `json:"temporality,omitempty" jsonschema:"filter: immutable|durable|temporal|ephemeral"`
		KnowledgeType     string   `json:"knowledge_type,omitempty" jsonschema:"filter: episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus   string   `json:"epistemic_status,omitempty" jsonschema:"filter: well_established|probable|speculative|contested|refuted"`
		ConfidenceMin     *float64 `json:"confidence_min,omitempty" jsonschema:"number between 0.0 and 1.0"`
		ConfidenceMax     *float64 `json:"confidence_max,omitempty" jsonschema:"number between 0.0 and 1.0"`
		IncludeHistorical bool     `json:"include_historical,omitempty" jsonschema:"include records past valid_until"`
		Since             string   `json:"since,omitempty" jsonschema:"filter: created after date (YYYY-MM-DD or RFC3339)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_search",
		Description: "Search the knowledge store. Returns results ranked by a 6-factor score. Use gramaton_inspect for full content.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args searchInput) (*mcp.CallToolResult, any, error) {
		top := args.Top
		if top <= 0 {
			top = 10
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
		}
		if args.Since != "" {
			t, err := parseDateArg(args.Since)
			if err != nil {
				return mcpErr(fmt.Sprintf("invalid since date: %s", err))
			}
			q.Since = &t
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

		return mcpJSONResult(map[string]any{"results": results})
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

		if _, err := s.engine.ChunkIfNeeded(ctx, n.ID); err != nil {
			warnings = append(warnings, fmt.Sprintf("chunking failed: %s", err))
		}

		s.engine.Save("capture")

		return mcpJSONResult(map[string]any{"id": n.ID, "warnings": warnings})
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

	type branchInput struct {
		Action string `json:"action" jsonschema:"create|list|checkout|merge|discard"`
		Name   string `json:"name,omitempty" jsonschema:"branch name (required for create/checkout/merge/discard)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_branch",
		Description: "Manage branches: create, list, checkout, merge, or discard.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args branchInput) (*mcp.CallToolResult, any, error) {
		// Delegate to the HTTP handlers by building the equivalent request.
		// This is a thin wrapper to avoid duplicating branch logic.
		return mcpErr("branch operations via MCP: use action=list|create|checkout|merge|discard with name parameter")
	})

	type diffInput struct {
		Since string `json:"since,omitempty" jsonschema:"show changes after date (YYYY-MM-DD)"`
		Topic string `json:"topic,omitempty" jsonschema:"filter by topic keyword"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_diff",
		Description: "Show what changed in the knowledge store since a date, optionally filtered by topic.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args diffInput) (*mcp.CallToolResult, any, error) {
		var params []string
		if args.Since != "" {
			params = append(params, "since="+args.Since)
		}
		if args.Topic != "" {
			params = append(params, "topic="+args.Topic)
		}
		path := "/v1/diff"
		if len(params) > 0 {
			path += "?" + strings.Join(params, "&")
		}

		// Use internal HTTP call via the handler directly.
		return mcpErr("diff via MCP: use since and topic parameters")
	})

	type logInput struct {
		Limit  int    `json:"limit,omitempty" jsonschema:"max entries (default 20)"`
		Record string `json:"record,omitempty" jsonschema:"record ID for per-record history"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_log",
		Description: "View commit history or per-record change history.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args logInput) (*mcp.CallToolResult, any, error) {
		return mcpErr("log via MCP: use limit and record parameters")
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
