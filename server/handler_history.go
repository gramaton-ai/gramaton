package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gramaton-ai/gramaton/graph"
)

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	recordID := r.URL.Query().Get("record")
	limit := 20
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxLogLimit {
		limit = maxLogLimit
	}

	s.engine.RLock()
	defer s.engine.RUnlock()

	if recordID != "" {
		s.handleRecordHistory(w, recordID, limit)
		return
	}

	// Walk commit chain from HEAD.
	var commits []map[string]any
	hash := s.engine.HeadHashLocked()
	store := s.engine.Store()

	for hash != "" && len(commits) < limit {
		commit, err := loadCommit(store, hash)
		if err != nil {
			break
		}
		commits = append(commits, map[string]any{
			"hash":      hash[:12],
			"timestamp": commit.Timestamp.Format("2006-01-02T15:04:05Z"),
			"action":    commit.Message,
		})
		hash = commit.Parent
	}

	if commits == nil {
		commits = []map[string]any{}
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{"commits": commits})
}

func (s *Server) handleRecordHistory(w http.ResponseWriter, recordID string, limit int) {
	store := s.engine.Store()
	hash := s.engine.HeadHashLocked()

	var changes []map[string]any
	var prevHash string

	// Walk backwards through commits to find property changes.
	// Cap traversal depth to avoid holding the read lock too long.
	const maxTraversal = 5000
	traversed := 0
	for hash != "" && len(changes) < limit && traversed < maxTraversal {
		traversed++
		commit, err := loadCommit(store, hash)
		if err != nil {
			break
		}

		nodeHash, found, _ := graph.NodeHashInCommit(store, hash, recordID)
		if found {
			if prevHash != "" && nodeHash != prevHash {
				// Properties changed between this commit and the previous one.
				changes = append(changes, map[string]any{
					"commit":    hash[:12],
					"timestamp": commit.Timestamp.Format("2006-01-02T15:04:05Z"),
					"action":    commit.Message,
				})
			} else if prevHash == "" {
				// First appearance = most recent state.
				changes = append(changes, map[string]any{
					"commit":    hash[:12],
					"timestamp": commit.Timestamp.Format("2006-01-02T15:04:05Z"),
					"action":    commit.Message,
				})
			}
			prevHash = nodeHash
		}

		hash = commit.Parent
	}

	if changes == nil {
		changes = []map[string]any{}
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{
		"id":      recordID,
		"changes": changes,
	})
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	sinceStr := r.URL.Query().Get("since")
	topic := r.URL.Query().Get("topic")
	limit := parseIntParam(r, "limit", 50, 1000)

	if len(topic) > maxTopicLength {
		s.writeError(w, http.StatusBadRequest, "invalid_field", "topic too long", true)
		return
	}

	s.engine.RLock()
	defer s.engine.RUnlock()

	store := s.engine.Store()
	headHash := s.engine.HeadHashLocked()

	// Find the "since" commit.
	var sinceHash string
	if sinceStr != "" {
		sinceTime, err := parseDateArg(sinceStr)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_field", err.Error(), true)
			return
		}

		hash := headHash
		traversed := 0
		for hash != "" && traversed < 5000 {
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

	if sinceHash == "" && sinceStr != "" {
		s.writeJSONLocked(w, http.StatusOK, map[string]any{
			"added": []any{}, "modified": []any{}, "removed": []any{},
		})
		return
	}

	headCommit, err := loadCommit(store, headHash)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "load_error", "failed to load HEAD", false)
		return
	}

	var sinceCommit *graph.Commit
	if sinceHash != "" {
		c, err := loadCommit(store, sinceHash)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "load_error", "failed to load since commit", false)
			return
		}
		sinceCommit = c
	}

	diff, err := graph.DiffCommits(store, sinceCommit, headCommit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "diff_error", "failed to compute diff", false)
		return
	}

	// Build added/removed sets for modified detection.
	addedSet := make(map[string]struct{}, len(diff.Added))
	for _, e := range diff.Added {
		addedSet[e.Key] = struct{}{}
	}
	removedSet := make(map[string]struct{}, len(diff.Removed))
	for _, e := range diff.Removed {
		removedSet[e.Key] = struct{}{}
	}

	// Build response: separate added, modified, removed.
	var added, modified, removed []map[string]any
	total := 0
	for _, entry := range diff.Added {
		if total >= limit {
			break
		}
		if topic != "" && !matchesTopic(s, entry.Key, topic) {
			continue
		}
		rec := map[string]any{"id": entry.Key}
		if n, ok := s.engine.Graph().GetNode(entry.Key); ok {
			if v, ok := n.Properties.GetString("content_short"); ok {
				rec["summary_short"] = v
			}
		}
		if _, wasRemoved := removedSet[entry.Key]; wasRemoved {
			modified = append(modified, rec)
		} else {
			added = append(added, rec)
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
		if topic != "" && !matchesTopic(s, entry.Key, topic) {
			continue
		}
		removed = append(removed, map[string]any{"id": entry.Key})
		total++
	}

	if added == nil {
		added = []map[string]any{}
	}
	if modified == nil {
		modified = []map[string]any{}
	}
	if removed == nil {
		removed = []map[string]any{}
	}

	resp := map[string]any{
		"added":    added,
		"modified": modified,
		"removed":  removed,
	}
	if total >= limit {
		resp["truncated"] = true
		resp["limit"] = limit
	}
	s.writeJSONLocked(w, http.StatusOK, resp)
}

// loadCommit loads a commit from the store by hash, returning only
// the commit metadata without loading the full graph state.
func loadCommit(store interface{ Read(string) ([]byte, error) }, hash string) (*graph.Commit, error) {
	data, err := store.Read(hash)
	if err != nil {
		return nil, err
	}
	var commit graph.Commit
	if err := json.Unmarshal(data, &commit); err != nil {
		return nil, err
	}
	commit.Hash = hash
	return &commit, nil
}

// matchesTopic checks if a record matches a topic keyword.
func matchesTopic(s *Server, nodeID, topic string) bool {
	n, ok := s.engine.Graph().GetNode(nodeID)
	if !ok {
		return false
	}
	topicLower := strings.ToLower(topic)

	// Check keywords.
	if kw, ok := n.Properties.GetStringList("content_keywords"); ok {
		for _, k := range kw {
			if strings.Contains(strings.ToLower(k), topicLower) {
				return true
			}
		}
	}
	// Check short summary.
	if v, ok := n.Properties.GetString("content_short"); ok {
		if strings.Contains(strings.ToLower(v), topicLower) {
			return true
		}
	}
	return false
}
