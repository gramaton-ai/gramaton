package cli

import (
	"context"
	"fmt"
	"net/url"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerRecordsProxyTools registers the records-cluster MCP tools
// for the CLI proxy. Each tool uses api.XxxRequest directly (with
// jsonschema tags) as its args type, so there is no per-transport
// struct drift. The handlers forward to the running HTTP server.
func registerRecordsProxyTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_save",
		Description: api.SaveDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.SaveRequest) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/records", args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_save_batch",
		Description: api.SaveBatchDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.SaveBatchRequest) (*mcp.CallToolResult, any, error) {
		return proxyPost("/v1/save/batch", args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_save_batch_status",
		Description: api.SaveBatchStatusDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.SaveBatchStatusRequest) (*mcp.CallToolResult, any, error) {
		if args.JobID == "" {
			return proxyErr("job_id is required")
		}
		return proxyGet(fmt.Sprintf("/v1/save/batch/%s/status", url.PathEscape(args.JobID)))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_save_batch_cancel",
		Description: api.SaveBatchCancelDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.SaveBatchCancelRequest) (*mcp.CallToolResult, any, error) {
		if args.JobID == "" {
			return proxyErr("job_id is required")
		}
		return proxyPost(fmt.Sprintf("/v1/save/batch/%s/cancel", url.PathEscape(args.JobID)), nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_save_batch_result",
		Description: api.SaveBatchResultDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.SaveBatchResultRequest) (*mcp.CallToolResult, any, error) {
		if args.JobID == "" {
			return proxyErr("job_id is required")
		}
		path := fmt.Sprintf("/v1/save/batch/%s/result", url.PathEscape(args.JobID))
		if args.TimeoutMS > 0 {
			path += fmt.Sprintf("?timeout_ms=%d", args.TimeoutMS)
		}
		return proxyGet(path)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_jobs_list",
		Description: api.JobsListDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args api.JobsListRequest) (*mcp.CallToolResult, any, error) {
		params := url.Values{}
		if args.Status != "" {
			params.Set("status", args.Status)
		}
		if args.Kind != "" {
			params.Set("kind", args.Kind)
		}
		if args.ClientToken != "" {
			params.Set("client_token", args.ClientToken)
		}
		if args.Since != "" {
			params.Set("since", args.Since)
		}
		if args.Until != "" {
			params.Set("until", args.Until)
		}
		if args.Limit > 0 {
			params.Set("limit", fmt.Sprintf("%d", args.Limit))
		}
		if args.Offset > 0 {
			params.Set("offset", fmt.Sprintf("%d", args.Offset))
		}
		path := "/v1/jobs"
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return proxyGet(path)
	})

	// Inspect takes the ID as a tool-level field; the api request's ID
	// is JSON-hidden so we redeclare the args struct here.
	type inspectArgs struct {
		ID             string `json:"id" jsonschema:"record ID to inspect"`
		IncludeContent *bool  `json:"include_content,omitempty" jsonschema:"include content_full in response (default true)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_inspect",
		Description: api.InspectDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args inspectArgs) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		path := fmt.Sprintf("/v1/records/%s", url.PathEscape(args.ID))
		if args.IncludeContent != nil && !*args.IncludeContent {
			path += "?include_content=false"
		}
		return proxyGet(path)
	})

	type updateArgs struct {
		ID              string         `json:"id" jsonschema:"record ID to update"`
		Content         string         `json:"content,omitempty" jsonschema:"replacement content_full (mutually exclusive with content_append). Compose from the record's current full content, never from a summary."`
		ContentAppend   string         `json:"content_append,omitempty" jsonschema:"text appended to the current content (mutually exclusive with content)"`
		ExpectedVersion string         `json:"expected_version,omitempty" jsonschema:"version token from a hold response, update, or inspect; the update applies only if the content is unchanged since (version_conflict otherwise)"`
		Confidence      *float64       `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
		Temporality     string         `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
		KnowledgeType   string         `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus string         `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
		Importance      *float64       `json:"importance,omitempty" jsonschema:"0.0-1.0"`
		Keywords        []string       `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
		SummaryShort    string         `json:"summary_short,omitempty" jsonschema:"target ~750 chars, max ~900 (semantic anchor for embedding)"`
		ValidUntil      string         `json:"valid_until,omitempty" jsonschema:"expiration (YYYY-MM-DD or RFC3339); 'clear' removes."`
		AssertedAsOf    string         `json:"asserted_as_of,omitempty" jsonschema:"when the source made this claim (YYYY-MM-DD or RFC3339)"`
		Meta            map[string]any `json:"meta,omitempty" jsonschema:"structured metadata"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_update",
		Description: api.UpdateDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args updateArgs) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		body := map[string]any{}
		if args.Content != "" {
			body["content"] = args.Content
		}
		if args.ContentAppend != "" {
			body["content_append"] = args.ContentAppend
		}
		if args.ExpectedVersion != "" {
			body["expected_version"] = args.ExpectedVersion
		}
		if args.Confidence != nil {
			body["confidence"] = *args.Confidence
		}
		if args.Temporality != "" {
			body["temporality"] = args.Temporality
		}
		if args.KnowledgeType != "" {
			body["knowledge_type"] = args.KnowledgeType
		}
		if args.EpistemicStatus != "" {
			body["epistemic_status"] = args.EpistemicStatus
		}
		if args.Importance != nil {
			body["importance"] = *args.Importance
		}
		if len(args.Keywords) > 0 {
			body["keywords"] = args.Keywords
		}
		if args.SummaryShort != "" {
			body["summary_short"] = args.SummaryShort
		}
		if args.ValidUntil != "" {
			body["valid_until"] = args.ValidUntil
		}
		if args.AssertedAsOf != "" {
			body["asserted_as_of"] = args.AssertedAsOf
		}
		if len(args.Meta) > 0 {
			body["meta"] = args.Meta
		}
		return proxyPatch(fmt.Sprintf("/v1/records/%s", url.PathEscape(args.ID)), body)
	})

	type classifyArgs struct {
		ID              string   `json:"id" jsonschema:"record ID to classify"`
		Temporality     string   `json:"temporality,omitempty" jsonschema:"immutable|durable|temporal|ephemeral"`
		Confidence      *float64 `json:"confidence,omitempty" jsonschema:"0.0-1.0"`
		KnowledgeType   string   `json:"knowledge_type,omitempty" jsonschema:"episodic|semantic|procedural|conceptual|reference"`
		EpistemicStatus string   `json:"epistemic_status,omitempty" jsonschema:"well_established|probable|speculative|contested|refuted"`
		Importance      *float64 `json:"importance,omitempty" jsonschema:"0.0-1.0"`
		Keywords        []string `json:"keywords,omitempty" jsonschema:"array of keyword strings"`
		SummaryShort    string   `json:"summary_short,omitempty" jsonschema:"target ~750 chars, max ~900 (semantic anchor for embedding)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_classify",
		Description: api.ClassifyDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args classifyArgs) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		body := map[string]any{}
		if args.Temporality != "" {
			body["temporality"] = args.Temporality
		}
		if args.Confidence != nil {
			body["confidence"] = *args.Confidence
		}
		if args.KnowledgeType != "" {
			body["knowledge_type"] = args.KnowledgeType
		}
		if args.EpistemicStatus != "" {
			body["epistemic_status"] = args.EpistemicStatus
		}
		if args.Importance != nil {
			body["importance"] = *args.Importance
		}
		if len(args.Keywords) > 0 {
			body["keywords"] = args.Keywords
		}
		if args.SummaryShort != "" {
			body["summary_short"] = args.SummaryShort
		}
		return proxyPost(fmt.Sprintf("/v1/records/%s/classify", url.PathEscape(args.ID)), body)
	})

	type resolveArgs struct {
		ID              string `json:"id" jsonschema:"record ID to resolve"`
		Resolution      string `json:"resolution" jsonschema:"completed|superseded|abandoned|obsolete"`
		ResolutionNote  string `json:"resolution_note,omitempty" jsonschema:"optional free-form note"`
		ExpectedVersion string `json:"expected_version,omitempty" jsonschema:"version token from a hold response, update, or inspect; the resolve applies only if the content is unchanged since (version_conflict otherwise)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_resolve",
		Description: api.ResolveDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args resolveArgs) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		body := map[string]any{"resolution": args.Resolution}
		if args.ResolutionNote != "" {
			body["resolution_note"] = args.ResolutionNote
		}
		if args.ExpectedVersion != "" {
			body["expected_version"] = args.ExpectedVersion
		}
		return proxyPost(fmt.Sprintf("/v1/records/%s/resolve", url.PathEscape(args.ID)), body)
	})

	// gramaton_link: source record ID is 'id' (not 'source_id') to
	// preserve the prior MCP wire contract.
	type linkArgs struct {
		ID         string   `json:"id" jsonschema:"source record ID"`
		TargetID   string   `json:"target_id" jsonschema:"destination record ID"`
		EdgeType   string   `json:"edge_type" jsonschema:"relationship name (e.g. related_to, supports, contradicts)"`
		EdgeWeight *float64 `json:"edge_weight,omitempty" jsonschema:"0.0-1.0, default 0.5"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_link",
		Description: api.LinkDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args linkArgs) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		body := map[string]any{"target_id": args.TargetID, "edge_type": args.EdgeType}
		if args.EdgeWeight != nil {
			body["edge_weight"] = *args.EdgeWeight
		}
		return proxyPost(fmt.Sprintf("/v1/records/%s/edges", url.PathEscape(args.ID)), body)
	})

	type unlinkArgs struct {
		EdgeID string `json:"edge_id" jsonschema:"edge ID to delete"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_unlink",
		Description: api.UnlinkDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args unlinkArgs) (*mcp.CallToolResult, any, error) {
		if args.EdgeID == "" {
			return proxyErr("edge_id is required")
		}
		return proxyDelete(fmt.Sprintf("/v1/edges/%s", url.PathEscape(args.EdgeID)))
	})

	type historyArgs struct {
		ID    string `json:"id" jsonschema:"record ID"`
		Limit int    `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
		Since string `json:"since,omitempty" jsonschema:"only include changes on or after this date (YYYY-MM-DD or RFC3339)"`
		Until string `json:"until,omitempty" jsonschema:"only include changes up to this date (YYYY-MM-DD or RFC3339); empty means up to HEAD"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gramaton_history",
		Description: api.HistoryDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args historyArgs) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return proxyErr("id is required")
		}
		params := url.Values{}
		if args.Limit > 0 {
			params.Set("limit", fmt.Sprintf("%d", args.Limit))
		}
		if args.Since != "" {
			params.Set("since", args.Since)
		}
		if args.Until != "" {
			params.Set("until", args.Until)
		}
		path := fmt.Sprintf("/v1/records/%s/history", url.PathEscape(args.ID))
		if len(params) > 0 {
			path += "?" + params.Encode()
		}
		return proxyGet(path)
	})
}
