package api

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/graph"
)

// DiffRequest carries the diff window + optional topic filter.
// Since is empty -> diff against the chain root (full history).
type DiffRequest struct {
	Since string `json:"since,omitempty" jsonschema:"show changes after date (YYYY-MM-DD or RFC3339); empty means against chain root"`
	Topic string `json:"topic,omitempty" jsonschema:"filter by topic substring (matches content_keywords + content_short, case-insensitive)"`
	Limit int    `json:"limit,omitempty" jsonschema:"max changes to return (default 50, max 1000)"`
}

// DiffEntry is one record in the diff. SummaryShort is omitted when
// the record no longer exists (Removed entries) or has no short.
type DiffEntry struct {
	ID           string `json:"id"`
	SummaryShort string `json:"summary_short,omitempty"`
}

// DiffResponse partitions the changed nodes into added/modified/
// removed buckets. Modified means the node ID appeared in both the
// added and removed sets at the prolly-tree level (content hash
// changed).
type DiffResponse struct {
	Added     []DiffEntry `json:"added"`
	Modified  []DiffEntry `json:"modified"`
	Removed   []DiffEntry `json:"removed"`
	Truncated bool        `json:"truncated,omitempty"`
	Limit     int         `json:"limit,omitempty"`
}

// DiffDescription is shared by HTTP, MCP, and CLI proxy transports.
const DiffDescription = "Show what changed since a date: added, modified, and removed records with summaries. Use to audit curation, catch up after time away, or review what other agents captured."

// Diff computes added/modified/removed records since a given date.
// When since is empty, diffs against the chain root (full history).
// When the date is provided but no commit before it exists in the
// chain (newer store), returns empty buckets rather than an error.
func (a *API) Diff(ctx context.Context, req DiffRequest) (DiffResponse, *APIError) {
	if len(req.Topic) > MaxTopicLength {
		return DiffResponse{}, ErrInvalid(fmt.Sprintf("topic exceeds maximum length of %d", MaxTopicLength))
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}

	a.engine.RLock()
	defer a.engine.RUnlock()

	store := a.engine.Store()
	headHash := a.engine.HeadHashLocked()

	// Find the commit immediately before `since` so the diff window
	// captures every change AT or after that timestamp.
	var sinceHash string
	if req.Since != "" {
		sinceTime, err := parseDateArg(req.Since)
		if err != nil {
			return DiffResponse{}, ErrInvalid(err.Error())
		}
		hash := headHash
		traversed := 0
		for hash != "" && traversed < MaxLogTraversal {
			traversed++
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

	// since requested but the entire chain post-dates it -> nothing
	// to compare against.
	if sinceHash == "" && req.Since != "" {
		return DiffResponse{
			Added:    []DiffEntry{},
			Modified: []DiffEntry{},
			Removed:  []DiffEntry{},
		}, nil
	}

	headCommit, err := loadCommit(store, headHash)
	if err != nil {
		a.log.Warn("diff: load HEAD failed", "component", "diff", "err", err)
		return DiffResponse{}, ErrInternal("failed to load HEAD commit")
	}

	var sinceCommit *graph.Commit
	if sinceHash != "" {
		c, err := loadCommit(store, sinceHash)
		if err != nil {
			a.log.Warn("diff: load since commit failed", "component", "diff", "hash", sinceHash, "err", err)
			return DiffResponse{}, ErrInternal("failed to load since commit")
		}
		sinceCommit = c
	}

	diff, err := graph.DiffCommits(store, sinceCommit, headCommit)
	if err != nil {
		a.log.Warn("diff: compute failed", "component", "diff", "err", err)
		return DiffResponse{}, ErrInternal("failed to compute diff")
	}

	addedSet := make(map[string]struct{}, len(diff.Added))
	for _, e := range diff.Added {
		addedSet[e.Key] = struct{}{}
	}
	removedSet := make(map[string]struct{}, len(diff.Removed))
	for _, e := range diff.Removed {
		removedSet[e.Key] = struct{}{}
	}

	resp := DiffResponse{
		Added:    []DiffEntry{},
		Modified: []DiffEntry{},
		Removed:  []DiffEntry{},
	}
	total := 0
	for _, entry := range diff.Added {
		if total >= limit {
			break
		}
		if req.Topic != "" && !a.matchesTopic(entry.Key, req.Topic) {
			continue
		}
		rec := DiffEntry{ID: entry.Key}
		if n, ok := a.engine.Graph().GetNode(entry.Key); ok {
			if v, ok := n.Properties.GetString("content_short"); ok {
				rec.SummaryShort = v
			}
		}
		if _, wasRemoved := removedSet[entry.Key]; wasRemoved {
			resp.Modified = append(resp.Modified, rec)
		} else {
			resp.Added = append(resp.Added, rec)
		}
		total++
	}
	for _, entry := range diff.Removed {
		if total >= limit {
			break
		}
		if _, wasAdded := addedSet[entry.Key]; wasAdded {
			continue // already counted as modified
		}
		if req.Topic != "" && !a.matchesTopic(entry.Key, req.Topic) {
			continue
		}
		resp.Removed = append(resp.Removed, DiffEntry{ID: entry.Key})
		total++
	}

	if total >= limit {
		resp.Truncated = true
		resp.Limit = limit
	}
	return resp, nil
}
