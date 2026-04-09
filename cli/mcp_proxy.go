package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerProxyTools registers all MCP tools as HTTP proxy handlers.
// Each tool forwards its request to the gramaton HTTP server and returns
// the response. The MCP process is stateless -- all state lives in the
// server process.
func registerProxyTools(mcpServer *mcp.Server) {
	registerSearchProxy(mcpServer)
	registerCaptureProxy(mcpServer)
	registerInspectProxy(mcpServer)
	registerUpdateProxy(mcpServer)
	registerResolveProxy(mcpServer)
	registerLinkProxy(mcpServer)
	registerClassifyProxy(mcpServer)
	registerExploreProxy(mcpServer)
	registerObserveProxy(mcpServer)
	registerPendingProxy(mcpServer)
	registerStatusProxy(mcpServer)
	registerStatsProxy(mcpServer)
	registerCurationProxy(mcpServer)
	registerBranchProxy(mcpServer)
	registerDiffProxy(mcpServer)
	registerLogProxy(mcpServer)
	registerReembedProxy(mcpServer)
	registerDuplicatesProxy(mcpServer)
	registerBackupProxy(mcpServer)
	// registerDeleteProxy intentionally excluded -- destructive operations
	// should not be available to agents via MCP. Use the CLI or HTTP API.
	registerUnlinkProxy(mcpServer)
	registerHistoryProxy(mcpServer)
	registerCollectionProxyTools(mcpServer)
}

// --- helpers ---

func proxyErr(msg string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}, nil, nil
}

func proxyResult(data any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return proxyErr("failed to marshal result")
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}, nil, nil
}

func proxyPost(path string, args any) (*mcp.CallToolResult, any, error) {
	env, err := serverPost(path, args)
	if err != nil {
		return proxyErr(err.Error())
	}
	return proxyResult(env.Data)
}

func proxyGet(path string) (*mcp.CallToolResult, any, error) {
	env, err := serverGet(path)
	if err != nil {
		return proxyErr(err.Error())
	}
	return proxyResult(env.Data)
}

// Slow variants for I/O-heavy operations (backup, reembed).
func proxyPostSlow(path string, args any) (*mcp.CallToolResult, any, error) {
	env, err := serverPostSlow(path, args)
	if err != nil {
		return proxyErr(err.Error())
	}
	return proxyResult(env.Data)
}

func proxyGetSlow(path string) (*mcp.CallToolResult, any, error) {
	env, err := serverGetSlow(path)
	if err != nil {
		return proxyErr(err.Error())
	}
	return proxyResult(env.Data)
}

func proxyPatch(path string, args any) (*mcp.CallToolResult, any, error) {
	env, err := serverPatch(path, args)
	if err != nil {
		return proxyErr(err.Error())
	}
	return proxyResult(env.Data)
}

func proxyDelete(path string) (*mcp.CallToolResult, any, error) {
	env, err := serverDelete(path)
	if err != nil {
		return proxyErr(err.Error())
	}
	return proxyResult(env.Data)
}

// --- search ---

type proxySearchInput struct {
	Text               string   `json:"text,omitempty" jsonschema:"search query text (optional -- omit for filter-only queries)"`
	Top                int      `json:"top,omitempty" jsonschema:"integer, number of results (default 10)"`
	Temporality        string   `json:"temporality,omitempty" jsonschema:"filter: immutable|durable|temporal|ephemeral (prefix with ! to exclude, e.g. !ephemeral)"`
	KnowledgeType      string   `json:"knowledge_type,omitempty" jsonschema:"filter: episodic|semantic|procedural|conceptual|reference (prefix with ! to exclude)"`
	EpistemicStatus    string   `json:"epistemic_status,omitempty" jsonschema:"filter: well_established|probable|speculative|contested|refuted (prefix with ! to exclude)"`
	Resolution         string   `json:"resolution,omitempty" jsonschema:"filter: completed|superseded|abandoned|obsolete|unresolved (unresolved = no resolution set)"`
	ConfidenceMin      *float64 `json:"confidence_min,omitempty" jsonschema:"number between 0.0 and 1.0"`
	ConfidenceMax      *float64 `json:"confidence_max,omitempty" jsonschema:"number between 0.0 and 1.0"`
	ImportanceMin      *float64 `json:"importance_min,omitempty" jsonschema:"number between 0.0 and 1.0"`
	ImportanceMax      *float64 `json:"importance_max,omitempty" jsonschema:"number between 0.0 and 1.0"`
	IncludeHistorical  bool     `json:"include_historical,omitempty" jsonschema:"include records past valid_until"`
	Since              string   `json:"since,omitempty" jsonschema:"filter: created after date (YYYY-MM-DD or RFC3339)"`
	Missing            []string `json:"missing,omitempty" jsonschema:"array of field names that must be unset (e.g. [\"temporality\", \"confidence\"])"`
	Keywords           []string `json:"keywords,omitempty" jsonschema:"array of keywords that must all be present on the record (exact match)"`
	AccessCountMin     *int64   `json:"access_count_min,omitempty" jsonschema:"integer, minimum access count"`
	AccessCountMax     *int64   `json:"access_count_max,omitempty" jsonschema:"integer, maximum access count"`
	LastAccessedAfter  string   `json:"last_accessed_after,omitempty" jsonschema:"filter: accessed after date (YYYY-MM-DD or RFC3339)"`
	LastAccessedBefore string   `json:"last_accessed_before,omitempty" jsonschema:"filter: accessed before date (YYYY-MM-DD or RFC3339)"`
	ValidAfter         string   `json:"valid_after,omitempty" jsonschema:"filter: valid_from after date"`
	ValidBefore        string   `json:"valid_before,omitempty" jsonschema:"filter: valid_from before date"`
	ExpiresAfter       string   `json:"expires_after,omitempty" jsonschema:"filter: valid_until after date (find records expiring after X)"`
	ExpiresBefore      string   `json:"expires_before,omitempty" jsonschema:"filter: valid_until before date (find records expiring before X)"`
	Match              string   `json:"match,omitempty" jsonschema:"literal substring search across content fields (case-insensitive). Distinct from text (vector similarity)"`
	SimilarTo          string   `json:"similar_to,omitempty" jsonschema:"record ID -- find records similar to this one using its stored embedding"`
	MinEdges           *int     `json:"min_edges,omitempty" jsonschema:"integer, minimum total edge count (orphan detection: max_edges=0)"`
	MaxEdges           *int     `json:"max_edges,omitempty" jsonschema:"integer, maximum total edge count"`
	Random             bool     `json:"random,omitempty" jsonschema:"return random results (ignores sort/score). Useful for serendipitous discovery or review"`
	Sort               string            `json:"sort,omitempty" jsonschema:"sort by: created_at|last_accessed|access_count|confidence|importance|content_length|edge_count|staleness (default: effective_score, or created_at if no text)"`
	Order              string            `json:"order,omitempty" jsonschema:"asc or desc (default: desc)"`
	Meta               map[string]string `json:"meta,omitempty" jsonschema:"filter by structured metadata (e.g. {assignee: Sarah Chen, status: in_progress})"`
}

func registerSearchProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_search",
		Description: "Search the knowledge graph. Returns results ranked by composite score. Text is optional -- omit for filter-only queries. Note: this searches knowledge records, not collection items. For exhaustive collection listing, use gramaton_collection_items.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxySearchInput) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/search", args)
	})
}

// --- capture ---

type proxyCaptureInput struct {
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
	ContextFindable   string         `json:"context_findable_by,omitempty" jsonschema:"future retrieval terms"`
	AssertedAsOf      string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (RFC3339). Distinct from created_at (when we captured it)."`
	Meta              map[string]any `json:"meta,omitempty" jsonschema:"structured metadata from source systems (e.g. {assignee: Sarah, priority: P1})"`
}

func registerCaptureProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "gramaton_capture",
		Description: `Store a knowledge record in the graph. Use for decisions, context, research findings, preferences -- things where ranked semantic search is the right retrieval mode.

NOT for tasks, action items, checklists, or anything that needs exhaustive tracking. Use gramaton_collection_add for those.

Example: gramaton_capture(content="User prefers dark mode", temporality="durable", confidence=0.95, knowledge_type="semantic", keywords=["preference", "ui"], summary_short="User prefers dark mode")

IMPORTANT: confidence must be a number (not a string). keywords must be an array (not a string).`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCaptureInput) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/records", args)
	})
}

// --- inspect ---

type proxyInspectInput struct {
	ID string `json:"id" jsonschema:"record ID to inspect"`
}

func registerInspectProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_inspect",
		Description: "Get full content, metadata, and related records for a specific record.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyInspectInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		return proxyGet(fmt.Sprintf("/v1/records/%s", url.PathEscape(args.ID)))
	})
}

// --- update ---

type proxyUpdateInput struct {
	ID              string   `json:"id" jsonschema:"record ID to update"`
	Confidence      *float64 `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
	Temporality     string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
	KnowledgeType   string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
	EpistemicStatus string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
	Importance      *float64 `json:"importance,omitempty" jsonschema:"0.0-1.0"`
	Keywords        []string `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
	SummaryShort    string   `json:"summary_short,omitempty" jsonschema:"max 200 chars"`
	ValidUntil      string         `json:"valid_until,omitempty" jsonschema:"expiration date (YYYY-MM-DD or RFC3339) -- marks record as historical. Use 'clear' to remove."`
	AssertedAsOf    string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (YYYY-MM-DD or RFC3339)"`
	Meta            map[string]any `json:"meta,omitempty" jsonschema:"structured metadata (e.g. {assignee: Sarah, status: done})"`
}

func registerUpdateProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_update",
		Description: "Update metadata on a knowledge graph record. For collection item fields, use gramaton_collection_update instead.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyUpdateInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		return proxyPatch(fmt.Sprintf("/v1/records/%s", url.PathEscape(args.ID)), args)
	})
}

// --- resolve ---

type proxyResolveInput struct {
	ID             string `json:"id" jsonschema:"record ID to resolve"`
	Resolution     string `json:"resolution" jsonschema:"completed|superseded|abandoned|obsolete"`
	ResolutionNote string `json:"resolution_note,omitempty" jsonschema:"brief explanation of why (optional)"`
}

func registerResolveProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_resolve",
		Description: "Mark a knowledge record as resolved. Sets resolution status, resolved_at timestamp, and auto-sets valid_until to deprioritize in search. Use for decisions, questions, or knowledge records with a lifecycle. For task completion in collections, use gramaton_collection_update to change the status field instead.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyResolveInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/records/%s/resolve", url.PathEscape(args.ID)), args)
	})
}

// --- link ---

type proxyLinkInput struct {
	ID         string   `json:"id" jsonschema:"source record ID"`
	TargetID   string   `json:"target_id" jsonschema:"target record ID"`
	EdgeType   string   `json:"edge_type" jsonschema:"relationship type (e.g. related_to, discusses, justifies)"`
	EdgeWeight *float64 `json:"edge_weight,omitempty" jsonschema:"0.0-1.0 (default 0.5)"`
}

func registerLinkProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_link",
		Description: "Create an edge between two records in the knowledge graph. Collection items are also graph nodes and can be linked.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyLinkInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/records/%s/edges", url.PathEscape(args.ID)), args)
	})
}

// --- classify ---

type proxyClassifyInput struct {
	ID              string   `json:"id" jsonschema:"record ID to classify"`
	Temporality     string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
	Confidence      *float64 `json:"confidence,omitempty" jsonschema:"number between 0.0 and 1.0"`
	KnowledgeType   string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
	EpistemicStatus string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
	Keywords        []string `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
	SummaryShort    string   `json:"summary_short,omitempty" jsonschema:"max 200 chars"`
}

func registerClassifyProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_classify",
		Description: "Classify a pending record with metadata. Sets processing_status to processed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyClassifyInput) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/records/%s/classify", url.PathEscape(args.ID)), args)
	})
}

// --- explore ---

type proxyExploreInput struct {
	NodeID    string   `json:"node_id" jsonschema:"starting node ID"`
	Depth     int      `json:"depth,omitempty" jsonschema:"max traversal depth (default 2)"`
	EdgeTypes []string `json:"edge_types,omitempty" jsonschema:"edge types to follow"`
	MinWeight float64  `json:"min_weight,omitempty" jsonschema:"minimum edge weight"`
	MaxNodes  int      `json:"max_nodes,omitempty" jsonschema:"max nodes to return (default 100)"`
}

func registerExploreProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_explore",
		Description: "Traverse the knowledge graph from a starting node. Use to understand context around a record, find related knowledge, or map connections. Returns connected nodes and edges.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyExploreInput) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/explore", args)
	})
}

// --- observe ---

type proxyObserveMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type proxyObserveInput struct {
	Messages []proxyObserveMessage `json:"messages,omitempty" jsonschema:"conversation turns [{role, content}]. Server extracts facts (requires LLM)."`
	Facts    []string              `json:"facts,omitempty" jsonschema:"pre-extracted facts. Server runs quality gates only (no LLM needed)."`
}

func registerObserveProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "gramaton_observe",
		Description: `Send conversation for knowledge extraction. Fire-and-forget: returns immediately, processes async.

Send EITHER messages (server extracts facts, requires LLM) OR facts (server runs quality gates only).
Call at natural breakpoints: end of task, topic change, session wind-down. Not every turn.

Extracted knowledge enters the knowledge graph as ephemeral, low-confidence records. It does NOT go into collections.`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyObserveInput) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/observe", args)
	})
}

// --- pending ---

func registerPendingProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_pending",
		Description: "List records awaiting classification (processing_status=captured).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return proxyGet("/v1/pending")
	})
}

// --- status ---

func registerStatusProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_status",
		Description: "Get knowledge graph health: node/edge counts, embedding status, curation status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return proxyGet("/v1/status")
	})
}

// --- stats ---

func registerStatsProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_stats",
		Description: "Get aggregate statistics: counts by temporality, knowledge_type, epistemic_status, confidence distribution.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return proxyGet("/v1/stats")
	})
}

// --- curation ---

type proxyCurationInput struct {
	Action string `json:"action" jsonschema:"status|trigger|dry_run"`
}

func registerCurationProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_curation",
		Description: "View curation status, trigger a curation cycle, or dry-run to see what autonomous curation would change without applying.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyCurationInput) (*mcp.CallToolResult, any, error) {
		switch args.Action {
		case "trigger":
			return proxyPost("/v1/curation/trigger", nil)
		case "dry_run":
			return proxyPost("/v1/curation/trigger", map[string]bool{"dry_run": true})
		default:
			return proxyGet("/v1/curation")
		}
	})
}

// --- branch ---

type proxyBranchInput struct {
	Action string `json:"action" jsonschema:"create|list|checkout|merge|discard"`
	Name   string `json:"name,omitempty" jsonschema:"branch name (required for create/checkout/merge/discard)"`
}

func registerBranchProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_branch",
		Description: "Manage branches: create, list, checkout, merge, or discard. Use for safe experimentation, bulk imports, or testing curation changes before merging.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyBranchInput) (*mcp.CallToolResult, any, error) {
		switch args.Action {
		case "list":
			return proxyGet("/v1/branches")
		case "create":
			return proxyPost("/v1/branches", map[string]string{"name": args.Name})
		case "checkout":
			return proxyPost(fmt.Sprintf("/v1/branches/%s/checkout", url.PathEscape(args.Name)), nil)
		case "merge":
			return proxyPost(fmt.Sprintf("/v1/branches/%s/merge", url.PathEscape(args.Name)), nil)
		case "discard":
			return proxyDelete(fmt.Sprintf("/v1/branches/%s", url.PathEscape(args.Name)))
		default:
			return proxyErr("action must be one of: create, list, checkout, merge, discard")
		}
	})
}

// --- diff ---

type proxyDiffInput struct {
	Since string `json:"since,omitempty" jsonschema:"show changes after date (YYYY-MM-DD)"`
	Topic string `json:"topic,omitempty" jsonschema:"filter by topic keyword"`
	Limit int    `json:"limit,omitempty" jsonschema:"max changes to return (default 50)"`
}

func registerDiffProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_diff",
		Description: "Show what changed since a date: added, modified, and removed records with summaries. Use to audit curation, catch up after time away, or review what other agents captured.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyDiffInput) (*mcp.CallToolResult, any, error) {
		params := url.Values{}
		if args.Since != "" {
			params.Set("since", args.Since)
		}
		if args.Topic != "" {
			params.Set("topic", args.Topic)
		}
		if args.Limit > 0 {
			params.Set("limit", fmt.Sprintf("%d", args.Limit))
		}
		path := "/v1/diff"
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return proxyGet(path)
	})
}

// --- log ---

type proxyLogInput struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"max entries (default 20)"`
	Record string `json:"record,omitempty" jsonschema:"record ID for per-record history"`
}

func registerLogProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_log",
		Description: "View commit history. Use to see what operations modified the knowledge graph and when. For per-record history, use gramaton_history instead.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyLogInput) (*mcp.CallToolResult, any, error) {
		params := url.Values{}
		if args.Limit > 0 {
			params.Set("limit", fmt.Sprintf("%d", args.Limit))
		}
		if args.Record != "" {
			params.Set("record", args.Record)
		}
		path := "/v1/log"
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return proxyGet(path)
	})
}

// --- reembed ---

type proxyReembedInput struct {
	Batch int `json:"batch,omitempty" jsonschema:"max records to process (default 50)"`
}

func registerReembedProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_reembed",
		Description: "Regenerate stale embeddings (model changed or missing).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyReembedInput) (*mcp.CallToolResult, any, error) {
		return proxyPostSlow("/v1/reembed", args)
	})
}

// --- duplicates ---

type proxyDuplicatesInput struct {
	Threshold float64 `json:"threshold,omitempty" jsonschema:"number between 0.0 and 1.0, minimum similarity to consider a duplicate (default 0.92)"`
	MaxPairs  int     `json:"max_pairs,omitempty" jsonschema:"integer, maximum number of pairs to return (default 50)"`
}

func registerDuplicatesProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_duplicates",
		Description: "Find near-duplicate records by comparing stored embeddings. Returns pairs above the similarity threshold.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyDuplicatesInput) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/duplicates", args)
	})
}

// --- backup ---

type proxyBackupInput struct {
	Action string `json:"action" jsonschema:"backup|status"`
}

func registerBackupProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_backup",
		Description: "Create a backup of the knowledge graph or list existing backups.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyBackupInput) (*mcp.CallToolResult, any, error) {
		switch args.Action {
		case "backup":
			return proxyPostSlow("/v1/backup", nil)
		default:
			return proxyGetSlow("/v1/backup")
		}
	})
}

// --- delete record ---

type proxyDeleteInput struct {
	ID     string `json:"id" jsonschema:"record ID to delete"`
	Reason string `json:"reason,omitempty" jsonschema:"reason for deletion"`
}

func registerDeleteProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_delete",
		Description: "Mark a record as deleted (recoverable). Sets processing_status to deleted.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyDeleteInput) (*mcp.CallToolResult, any, error) {
		path := fmt.Sprintf("/v1/records/%s", url.PathEscape(args.ID))
		if args.Reason != "" {
			path += "?reason=" + url.QueryEscape(args.Reason)
		}
		return proxyDelete(path)
	})
}

// --- unlink (delete edge) ---

type proxyUnlinkInput struct {
	EdgeID string `json:"edge_id" jsonschema:"edge ID to delete"`
}

func registerUnlinkProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_unlink",
		Description: "Delete an edge between records.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyUnlinkInput) (*mcp.CallToolResult, any, error) {
		return proxyDelete(fmt.Sprintf("/v1/edges/%s", url.PathEscape(args.EdgeID)))
	})
}

// --- record history ---

type proxyHistoryInput struct {
	ID string `json:"id" jsonschema:"record ID to view history for"`
}

func registerHistoryProxy(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_history",
		Description: "View how a specific record changed over time: what fields were added, modified, or removed at each commit.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proxyHistoryInput) (*mcp.CallToolResult, any, error) {
		return proxyGet(fmt.Sprintf("/v1/records/%s/history", url.PathEscape(args.ID)))
	})
}
