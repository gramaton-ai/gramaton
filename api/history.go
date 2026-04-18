package api

import (
	"context"

	"github.com/gramaton-ai/gramaton/graph"
)

// HistoryRequest returns the per-record change history. ID is
// transport-set from the URL path. Limit defaults to 20 and is
// clamped to MaxLogLimit.
type HistoryRequest struct {
	ID    string `json:"-" jsonschema:"-"`
	Limit int    `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
}

// HistoryChange represents a single change event for a record.
type HistoryChange struct {
	Commit    string `json:"commit"`
	Timestamp string `json:"timestamp"`
	Action    string `json:"action"`
}

// HistoryResponse lists the changes in reverse-chronological order
// (most recent first).
type HistoryResponse struct {
	ID      string          `json:"id"`
	Changes []HistoryChange `json:"changes"`
}

// HistoryDescription is the MCP tool description for gramaton_history.
const HistoryDescription = "View the change history for a specific record, walking commit metadata backwards from HEAD."

// History walks the commit chain back from HEAD looking for commits
// where the record's serialized property hash changed. Traversal is
// capped at 5000 commits to bound the read-lock hold; callers with
// deeper-history needs should narrow with a search first.
func (a *API) History(ctx context.Context, req HistoryRequest) (HistoryResponse, *APIError) {
	if req.ID == "" {
		return HistoryResponse{}, ErrMissing("id is required")
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
	hash := a.engine.HeadHashLocked()

	out := HistoryResponse{ID: req.ID, Changes: []HistoryChange{}}
	var prevHash string
	const maxTraversal = MaxLogTraversal
	traversed := 0
	for hash != "" && len(out.Changes) < limit && traversed < maxTraversal {
		traversed++
		commit, err := loadCommitMeta(store, hash)
		if err != nil {
			break
		}

		nodeHash, found, _ := graph.NodeHashInCommit(store, hash, req.ID)
		if found {
			switch {
			case prevHash == "":
				// First appearance = most recent state.
				out.Changes = append(out.Changes, HistoryChange{
					Commit: hash[:12], Timestamp: commit.Timestamp.Format("2006-01-02T15:04:05Z"), Action: commit.Message,
				})
			case nodeHash != prevHash:
				// Hash changed -> record was modified between these two commits.
				out.Changes = append(out.Changes, HistoryChange{
					Commit: hash[:12], Timestamp: commit.Timestamp.Format("2006-01-02T15:04:05Z"), Action: commit.Message,
				})
			}
			prevHash = nodeHash
		}

		hash = commit.Parent
	}

	return out, nil
}
