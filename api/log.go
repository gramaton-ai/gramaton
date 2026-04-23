package api

import (
	"context"
)

// LogRequest carries the commit-walk knobs. Since and Until narrow
// the walk to a date range (both optional, accept YYYY-MM-DD or
// RFC3339); when set they bypass MaxLogTraversal because the D7
// timestamp index gives range scans directly.
type LogRequest struct {
	Limit int    `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
	Since string `json:"since,omitempty" jsonschema:"only include commits on or after this date (YYYY-MM-DD or RFC3339)"`
	Until string `json:"until,omitempty" jsonschema:"only include commits up to this date (YYYY-MM-DD or RFC3339); empty means up to HEAD"`
}

// LogEntry is one commit in the chain. Hash is truncated to 12
// characters to keep CLI/MCP output readable; full hashes can be
// fetched via History when needed.
type LogEntry struct {
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp"`
	Action    string `json:"action"`
}

// LogResponse holds commit entries newest-first.
type LogResponse struct {
	Commits []LogEntry `json:"commits"`
}

// LogDescription is shared by HTTP, MCP, and CLI proxy transports.
const LogDescription = "View commit history. Walks the commit chain back from HEAD, returning recent operations newest-first. For per-record change history, use gramaton_history instead."

// Log walks the commit chain back from HEAD under a read lock. By
// default, capped at MaxLogLimit entries and never traverses past
// the chain root. When Since/Until are set, the walk is narrowed
// via the D7 timestamp index: start from CommitAt(Until) (default
// HEAD), stop when a commit's timestamp falls before Since.
// Date-bounded walks bypass MaxLogTraversal because the range
// itself bounds the work.
func (a *API) Log(ctx context.Context, req LogRequest) (LogResponse, *APIError) {
	sinceT, untilT, err := validateSinceUntil(req.Since, req.Until)
	if err != nil {
		return LogResponse{}, ErrInvalid(err.Error())
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

	// Pick walk start: HEAD unless Until is set, in which case snap
	// to the latest commit at-or-before Until via the D7 index.
	hash := a.engine.HeadHashLocked()
	if !untilT.IsZero() {
		if h, ok := tsIdx.CommitAt(untilT); ok {
			hash = h
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
		resp.Commits = append(resp.Commits, LogEntry{
			Hash:      truncCommitHash(hash),
			Timestamp: commit.Timestamp.Format("2006-01-02T15:04:05Z"),
			Action:    commit.Message,
		})
		hash = commit.Parent
	}
	return resp, nil
}
