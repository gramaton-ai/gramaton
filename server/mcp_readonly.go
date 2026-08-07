package server

import "sort"

// Store-level read-only classification of the MCP tool surface.
//
// Every MCP tool name registered by either MCP surface -- the
// server's own bindings (server/bindings_*.go, served over /mcp and
// stdio) and the CLI proxy (cli/mcp_proxy_*.go, served by `gramaton
// mcp`) -- must appear in mcpToolAccess below, classified as read or
// write. Against a frozen store the write-classified tools are
// stripped from the registration (Server.registerMCPTools here,
// registerProxyToolsReadOnly in cli/mcp_proxy.go), so an agent
// attached to a read-only store never sees a tool it cannot use. The
// api-layer guards (api/readonly.go) remain the enforcement backstop
// for anything that reaches a write path anyway; this filtering is
// presentation, not security.
//
// MANUAL INVARIANT, cross-checked against api/readonly_guard_test.go
// (no automated assertion is practical: that classification lives in
// a _test.go file and keys on api method names, not tool names): a
// tool is MCPToolWrite iff at least one operation reachable through
// it is classified guardWrite there. Judgment calls, recorded:
//
//   - gramaton_branch: write. Its `list` action is a read, but
//     create/checkout/merge/discard rewrite HEAD/refs (guardWrite),
//     and a single mutating action makes the whole tool write.
//   - gramaton_backup: read. The MCP tool exposes only status +
//     create, both guardRead -- they write strictly OUTSIDE the data
//     dir, and exporting a frozen store is how it is shared. Restore
//     and import are not MCP-exposed.
//   - gramaton_save_batch_cancel: read, matching SaveBatchCancel's
//     guardRead classification (jobs.db is a derived local cache
//     that stays writable on a frozen store; no batch can start on
//     one, so cancel is leftover-job cleanup, not knowledge mutation).
//   - gramaton_curation: write. action=status is a read, but
//     trigger/dry_run apply the deterministic phase (guardWrite).
//   - gramaton_intake: write; registered only on the server surface
//     (the CLI proxy deliberately excludes it -- see the comment in
//     cli/mcp_proxy.go's registerProxyTools).
//
// When a NEW tool trips TestMCPToolAccessCoversServerSurface (server)
// or TestProxyToolAccessClassification (cli): decide whether ANY
// action the tool exposes reaches a guardWrite api operation. If so,
// add it as MCPToolWrite; otherwise MCPToolRead. Never leave a tool
// unclassified -- an unclassified write tool would stay visible on
// frozen stores.
const (
	// MCPToolRead marks tools that only read store state (or write
	// strictly outside the data dir); they stay registered on a
	// frozen store.
	MCPToolRead = "read"

	// MCPToolWrite marks tools with at least one action that mutates
	// store state; they are not registered on a frozen store.
	MCPToolWrite = "write"
)

// mcpToolAccess is the audit trail for which MCP tools a frozen
// store's registration omits, one entry per tool name across both
// MCP surfaces. A single map (rather than two sets) makes
// "classified in exactly one category" structural.
var mcpToolAccess = map[string]string{
	// Records cluster.
	"gramaton_save":              MCPToolWrite,
	"gramaton_save_batch":        MCPToolWrite,
	"gramaton_save_batch_status": MCPToolRead,
	"gramaton_save_batch_result": MCPToolRead,
	"gramaton_save_batch_cancel": MCPToolRead, // matches SaveBatchCancel guardRead; see doc comment
	"gramaton_jobs_list":         MCPToolRead,
	"gramaton_inspect":           MCPToolRead,
	"gramaton_update":            MCPToolWrite,
	"gramaton_classify":          MCPToolWrite,
	"gramaton_resolve":           MCPToolWrite,
	"gramaton_link":              MCPToolWrite,
	"gramaton_unlink":            MCPToolWrite,
	"gramaton_history":           MCPToolRead,
	"gramaton_history_search":    MCPToolRead,

	// Search + ops cluster.
	"gramaton_search":     MCPToolRead,
	"gramaton_explore":    MCPToolRead,
	"gramaton_duplicates": MCPToolRead,
	"gramaton_pending":    MCPToolRead,
	"gramaton_stats":      MCPToolRead,
	"gramaton_status":     MCPToolRead,

	// Maintenance cluster.
	"gramaton_curation": MCPToolWrite, // trigger/dry_run mutate; see doc comment
	"gramaton_reembed":  MCPToolWrite,

	// History cluster.
	"gramaton_log":  MCPToolRead,
	"gramaton_diff": MCPToolRead,

	// Admin cluster.
	"gramaton_branch": MCPToolWrite, // checkout/merge/... rewrite refs; see doc comment
	"gramaton_backup": MCPToolRead,  // status + create only; see doc comment

	// Collections cluster.
	"gramaton_collection_create":    MCPToolWrite,
	"gramaton_collection_list":      MCPToolRead,
	"gramaton_collection_items":     MCPToolRead,
	"gramaton_collection_add":       MCPToolWrite,
	"gramaton_collection_add_batch": MCPToolWrite,
	"gramaton_collection_update":    MCPToolWrite,
	"gramaton_collection_move":      MCPToolWrite,
	"gramaton_collection_remove":    MCPToolWrite,
	"gramaton_collection_rename":    MCPToolWrite,
	"gramaton_collection_delete":    MCPToolWrite,
	"gramaton_collection_schema":    MCPToolRead, // read-only endpoint; schema UPDATE is not MCP-exposed
	"gramaton_collection_migrate":   MCPToolWrite,

	// Sessions cluster. Prepare is the entry to the two-phase write
	// flow (SessionPrepare is guardWrite).
	"gramaton_session_start":        MCPToolWrite,
	"gramaton_session_get":          MCPToolRead,
	"gramaton_session_prepare":      MCPToolWrite,
	"gramaton_session_save":         MCPToolWrite,
	"gramaton_session_resolve_held": MCPToolWrite,

	// Guide cluster.
	"gramaton_guide": MCPToolRead,

	// Server-only surface (not proxy-registered).
	"gramaton_intake": MCPToolWrite,
}

// MCPToolAccess returns the read/write classification for an MCP
// tool name, and ok=false for names that have not been classified.
func MCPToolAccess(name string) (access string, ok bool) {
	access, ok = mcpToolAccess[name]
	return access, ok
}

// MCPWriteToolNames returns every write-classified MCP tool name,
// sorted. Callers pass the result to mcp.Server.RemoveTools when the
// store is frozen; removing a name that was never registered is a
// no-op, so the proxy (which registers a subset of the server
// surface) can use the same list.
func MCPWriteToolNames() []string {
	names := make([]string, 0, len(mcpToolAccess))
	for name, access := range mcpToolAccess {
		if access == MCPToolWrite {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// mcpRemoteExcludedTools lists the MCP tools stripped from the
// surface a plain authenticated remote caller sees (see MCPHandler).
//
// This is the hook for keeping a genuinely path-taking MCP tool off
// the remote surface (a bearer token proves identity, not path
// safety). No MCP tool takes a host path today: the path-taking
// operations (restore, store carve/add, session archive, local-path
// ingest) are REST/CLI-only and are gated on the REST side via
// adminAllowed, so nothing here mirrors them.
//
// gramaton_intake is excluded for a different, non-security reason:
// it is the redundant legacy capture tool (agents should use
// gramaton_save), already excluded from the `gramaton mcp` CLI
// proxy, so a remote agent connecting straight to /mcp sees the same
// surface a proxy-connected agent does. Its REST twin /v1/intake is
// pathless and stays open -- matching gramaton_save.
//
// Keep in lockstep with the REST tier gates: a NEW path-taking MCP
// tool must be added here AND its REST route must call adminAllowed.
var mcpRemoteExcludedTools = []string{
	"gramaton_intake",
}

// MCPRemoteExcludedToolNames returns the tools removed from the
// trimmed remote MCP surface, sorted. Removing a name that was never
// registered is a no-op.
func MCPRemoteExcludedToolNames() []string {
	names := append([]string(nil), mcpRemoteExcludedTools...)
	sort.Strings(names)
	return names
}
