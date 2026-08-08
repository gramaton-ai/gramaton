package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/gramaton-ai/gramaton/graph"
)

// LogRequest carries the commit-walk knobs. Since and Until narrow
// the walk to a date range (both optional, accept YYYY-MM-DD or
// RFC3339); when set they bypass MaxLogTraversal because the D7
// timestamp index gives range scans directly. Actions filter by
// structured CommitAction.Kind (Phase 3 D3); ExcludeCuration filters
// via Message-prefix matching so it works against pre-D3 commits.
// IncludeRecordMutations enriches each returned commit with per-
// record mutation summaries, saving a follow-up gramaton_inspect
// round-trip for the common "what records changed yesterday" flow.
type LogRequest struct {
	Limit                  int      `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
	Since                  string   `json:"since,omitempty" jsonschema:"only include commits on or after this date (YYYY-MM-DD or RFC3339)"`
	Until                  string   `json:"until,omitempty" jsonschema:"only include commits up to this date (YYYY-MM-DD or RFC3339); empty means up to HEAD"`
	Actions                []string `json:"actions,omitempty" jsonschema:"filter by CommitAction.Kind (e.g. [resolve, collection_update]). A commit matches if ANY of its actions has a Kind in this list. Empty = no filter."`
	ExcludeCuration        bool     `json:"exclude_curation,omitempty" jsonschema:"skip commits whose message starts with 'curation:' (server-side curation fires ~3-5/min on active stores). Useful when surveying user-driven activity."`
	IncludeRecordMutations bool     `json:"include_record_mutations,omitempty" jsonschema:"enrich each commit with per-record {record_id, kind, field, title, summary_short} from its CommitAction list, capped at 20 mutations per commit. Lets 'what did I close yesterday' land in a single tool call."`
}

// MutationSummary is the enriched per-record description included in
// LogEntry when IncludeRecordMutations=true. Title and SummaryShort
// are pulled from the current HEAD record (may be absent if the
// record was deleted since); both are omitempty so JSON stays lean.
type MutationSummary struct {
	RecordID     string `json:"record_id,omitempty"`
	Kind         string `json:"kind"`
	Field        string `json:"field,omitempty"`
	Title        string `json:"title,omitempty"`
	SummaryShort string `json:"summary_short,omitempty"`
}

// LogEntry is one commit in the chain. Hash is truncated to 12
// characters to keep CLI/MCP output readable; full hashes can be
// fetched via History when needed. Mutations is populated when
// IncludeRecordMutations=true; MutationsTruncated flags when the
// per-commit cap was hit.
type LogEntry struct {
	Hash               string            `json:"hash"`
	Timestamp          string            `json:"timestamp"`
	Action             string            `json:"action"`
	Mutations          []MutationSummary `json:"mutations,omitempty"`
	MutationsTruncated bool              `json:"mutations_truncated,omitempty"`
}

// LogResponse holds commit entries newest-first.
type LogResponse struct {
	Commits []LogEntry `json:"commits"`
}

// LogDescription is shared by HTTP, MCP, and CLI proxy transports.
const LogDescription = "Commit-timeline for the store. USE THIS FOR: 'what did I do yesterday', 'what changed this week', 'what did I close on X', 'what did curation touch overnight'. Pass since/until for date-bounded windows (bypasses the traversal cap). Pass actions=['resolve','collection_update'] to filter by structured CommitAction.Kind. Pass include_record_mutations=true to inline per-commit record IDs + titles. Pass exclude_curation=true to skip server-side curation noise. DO NOT substitute client-side date filtering; the range params are indexed and complete. For per-record change history use gramaton_history instead."

// MaxMutationsPerCommit caps the per-commit mutation slice when
// IncludeRecordMutations=true. A curation batch touching 50 records
// would otherwise balloon a single log entry; truncation keeps
// response size predictable and is flagged via
// LogEntry.MutationsTruncated so callers know to drop to
// gramaton_history if they need the full set.
const MaxMutationsPerCommit = 20

// Log walks the commit chain back from HEAD under a read lock. By
// default, capped at MaxLogLimit entries and never traverses past
// the chain root. When Since/Until are set, the walk is narrowed
// via the D7 timestamp index: start from CommitAt(Until) (default
// HEAD), stop when a commit's timestamp falls before Since.
// Date-bounded walks bypass MaxLogTraversal because the range
// itself bounds the work.
//
// Phase 8 adds three filters + an enrichment flag: Actions filters
// by CommitAction.Kind (D3), ExcludeCuration filters by Message
// prefix so it works against pre-D3 commits, and
// IncludeRecordMutations adds per-record mutation summaries inline.
func (a *API) Log(ctx context.Context, req LogRequest) (LogResponse, *APIError) {
	sinceT, untilT, err := validateSinceUntil(req.Since, req.Until)
	if err != nil {
		return LogResponse{}, ErrInvalid(err.Error())
	}
	if req.Actions != nil && len(req.Actions) == 0 {
		// Explicit empty array is redundant with no filter; rejecting
		// it catches callers that pass a literal [] expecting "match
		// nothing" instead of "no filter".
		return LogResponse{}, ErrInvalid("actions filter must be nil or non-empty")
	}
	if len(req.Actions) > MaxLogActionsFilter {
		return LogResponse{}, ErrInvalid(fmt.Sprintf("actions filter exceeds maximum of %d entries", MaxLogActionsFilter))
	}
	actionFilter := make(map[string]struct{}, len(req.Actions))
	for _, k := range req.Actions {
		actionFilter[k] = struct{}{}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > MaxLogLimit {
		limit = MaxLogLimit
	}

	a.engine.RLock()
	defer a.engine.RUnlock()

	store := a.engine.Store()
	tsIdx := a.engine.TSIndex()

	hash := a.engine.HeadHashLocked()
	if !untilT.IsZero() {
		if h, ok := tsIdx.CommitAt(untilT); ok {
			// Branch-scope the hit: the index spans every lineage.
			if snapped := a.snapToCurrentBranch(h); snapped != "" {
				hash = snapped
			} else {
				return LogResponse{Commits: []LogEntry{}}, nil
			}
		} else {
			return LogResponse{Commits: []LogEntry{}}, nil
		}
	}

	dateBounded := !sinceT.IsZero() || !untilT.IsZero()

	resp := LogResponse{Commits: []LogEntry{}}
	traversed := 0
	for hash != "" && len(resp.Commits) < limit {
		if !dateBounded && traversed >= MaxLogTraversal {
			break
		}
		traversed++
		commit, err := loadCommit(store, hash)
		if err != nil {
			a.log.Warn("log: load commit failed",
				"component", "log", "hash", hash, "err", err)
			break
		}
		if !sinceT.IsZero() && commit.Timestamp.Before(sinceT) {
			break
		}

		if req.ExcludeCuration && strings.HasPrefix(commit.Message, "curation:") {
			hash = commit.Parent
			continue
		}
		if len(actionFilter) > 0 && !commitMatchesAnyAction(commit, actionFilter) {
			hash = commit.Parent
			continue
		}

		entry := LogEntry{
			Hash:      truncCommitHash(hash),
			Timestamp: commit.Timestamp.Format("2006-01-02T15:04:05Z"),
			Action:    commit.Message,
		}
		if req.IncludeRecordMutations {
			entry.Mutations, entry.MutationsTruncated = a.enrichMutations(commit.Actions)
		}
		resp.Commits = append(resp.Commits, entry)
		hash = commit.Parent
	}
	return resp, nil
}

// commitMatchesAnyAction returns true when the commit has at least
// one CommitAction whose Kind is in the filter set. Used by the
// Actions filter on gramaton_log.
func commitMatchesAnyAction(c *graph.Commit, filter map[string]struct{}) bool {
	if len(c.Actions) == 0 {
		return false
	}
	for _, a := range c.Actions {
		if _, ok := filter[a.Kind]; ok {
			return true
		}
	}
	return false
}

// enrichMutations builds the per-commit MutationSummary list from a
// commit's CommitActions, looking up title + summary_short from the
// current HEAD record. Truncates at MaxMutationsPerCommit; second
// return is true when the cap was hit. Called under the engine
// read lock.
func (a *API) enrichMutations(actions []graph.CommitAction) ([]MutationSummary, bool) {
	if len(actions) == 0 {
		return nil, false
	}
	capped := actions
	truncated := false
	if len(capped) > MaxMutationsPerCommit {
		capped = capped[:MaxMutationsPerCommit]
		truncated = true
	}
	out := make([]MutationSummary, 0, len(capped))
	for _, act := range capped {
		m := MutationSummary{
			RecordID: act.RecordID,
			Kind:     act.Kind,
			Field:    act.Field,
		}
		if act.RecordID != "" {
			if n, ok := a.engine.Graph().GetNode(act.RecordID); ok {
				if title, ok := n.Properties.GetString("title"); ok {
					m.Title = title
				} else if title, ok := n.Properties.GetString("field.title"); ok {
					// Collection-item schema stores title under "field.title".
					m.Title = title
				}
				if s, ok := n.Properties.GetString("content_short"); ok {
					m.SummaryShort = s
				}
			}
		}
		out = append(out, m)
	}
	return out, truncated
}
