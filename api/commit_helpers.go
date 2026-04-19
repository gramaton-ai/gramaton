package api

import (
	"encoding/json"
	"strings"

	"github.com/gramaton-ai/gramaton/graph"
)

// loadCommit reads commit metadata for a hash without materialising
// the full graph state. The interface lets tests pass a mock; the
// production caller is engine.Store().
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

// truncCommitHash returns the leading 12 characters of a commit hash
// (or the whole hash when shorter). Matches the convention used by
// gramaton_log + gramaton_history output for display readability.
func truncCommitHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// matchesTopic returns true when the node's keywords or summary_short
// contain the topic substring (case-insensitive). Used by Diff for
// the optional Topic filter; kept private since topic filtering is
// only meaningful in the diff context.
func (a *API) matchesTopic(nodeID, topic string) bool {
	n, ok := a.engine.Graph().GetNode(nodeID)
	if !ok {
		return false
	}
	topicLower := strings.ToLower(topic)

	if kw, ok := n.Properties.GetStringList("content_keywords"); ok {
		for _, k := range kw {
			if strings.Contains(strings.ToLower(k), topicLower) {
				return true
			}
		}
	}
	if v, ok := n.Properties.GetString("content_short"); ok {
		if strings.Contains(strings.ToLower(v), topicLower) {
			return true
		}
	}
	return false
}
