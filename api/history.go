package api

import (
	"context"

	"github.com/gramaton-ai/gramaton/graph"
)

// HistoryRequest returns the per-record change history. ID is
// transport-set from the URL path. Limit defaults to 20 and is
// clamped to MaxLogLimit. Since and Until narrow the walk to a
// date range and bypass MaxLogTraversal when set (the timestamp
// index gives us O(log N) entry points).
type HistoryRequest struct {
	ID    string `json:"-" jsonschema:"-"`
	Limit int    `json:"limit,omitempty" jsonschema:"max entries (default 20, max 500)"`
	Since string `json:"since,omitempty" jsonschema:"only include changes on or after this date (YYYY-MM-DD or RFC3339)"`
	Until string `json:"until,omitempty" jsonschema:"only include changes up to this date (YYYY-MM-DD or RFC3339); empty means up to HEAD"`
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
// where the record's serialized property hash changed. Without
// Since/Until, traversal is capped at MaxLogTraversal to bound the
// read-lock hold. With Since/Until, the walk narrows via the D7
// timestamp index: start from CommitAt(Until) (default HEAD), stop
// when a commit's timestamp falls before Since. Date-bounded walks
// bypass the traversal cap -- the range itself bounds the work.
//
// RC-4: a record that's deleted and later recreated should surface
// the recreation as a first-appearance, not a spurious modification
// against the pre-deletion hash. Reset prevHash on found=false so
// the not-found gap breaks the comparison chain cleanly.
func (a *API) History(ctx context.Context, req HistoryRequest) (HistoryResponse, *APIError) {
	if req.ID == "" {
		return HistoryResponse{}, ErrMissing("id is required")
	}
	sinceT, untilT, err := validateSinceUntil(req.Since, req.Until)
	if err != nil {
		return HistoryResponse{}, ErrInvalid(err.Error())
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

	// Pick the walk start. Default HEAD; with Until, snap to the
	// latest commit at-or-before Until via the D7 index.
	hash := a.engine.HeadHashLocked()
	if !untilT.IsZero() {
		if h, ok := tsIdx.CommitAt(untilT); ok {
			hash = h
		} else {
			// Until predates every indexed commit; nothing to walk.
			return HistoryResponse{ID: req.ID, Changes: []HistoryChange{}}, nil
		}
	}

	dateBounded := !sinceT.IsZero() || !untilT.IsZero()

	out := HistoryResponse{ID: req.ID, Changes: []HistoryChange{}}
	var prevHash string
	const maxTraversal = MaxLogTraversal
	traversed := 0
	for hash != "" && len(out.Changes) < limit {
		// Only apply the traversal cap when no date bounds were
		// provided; date bounds supply their own termination condition.
		if !dateBounded && traversed >= maxTraversal {
			break
		}
		traversed++
		commit, err := loadCommitMeta(store, hash)
		if err != nil {
			break
		}

		// Stop once we walk past the Since boundary.
		if !sinceT.IsZero() && commit.Timestamp.Before(sinceT) {
			break
		}

		nodeHash, found, _ := graph.NodeHashInCommit(store, hash, req.ID)
		if found {
			switch {
			case prevHash == "":
				// First appearance since start-of-walk OR recreation
				// after a not-found gap: record as a change entry.
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
		} else {
			// Record not present in this commit. Reset prevHash so a
			// later reappearance is recognised as a first-appearance,
			// not compared against the stale pre-deletion hash (RC-4).
			prevHash = ""
		}

		hash = commit.Parent
	}

	return out, nil
}
