package api

import (
	"context"
)

// LogRequest carries the commit-walk knobs.
type LogRequest struct {
	Limit int `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
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

// Log walks the commit chain back from HEAD under a read lock.
// Capped at MaxLogLimit entries; never traverses past the chain
// root.
func (a *API) Log(ctx context.Context, req LogRequest) (LogResponse, *APIError) {
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
	hash := a.engine.HeadHashLocked()

	resp := LogResponse{Commits: []LogEntry{}}
	for hash != "" && len(resp.Commits) < limit {
		commit, err := loadCommit(store, hash)
		if err != nil {
			a.log.Warn("log: load commit failed",
				"component", "log", "hash", hash, "err", err)
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
