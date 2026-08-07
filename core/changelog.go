package core

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// bookkeepingProps is the normative BOOKKEEPING SET for version
// identity: a change confined to these fields is operational churn,
// not a new logical version of the record's knowledge. Any
// embedding_* property is bookkeeping by prefix (vectors are
// re-derivable and model-bound; a re-embed sweep must mint no
// versions).
var bookkeepingProps = map[string]bool{
	"access_count":      true,
	"last_accessed":     true,
	"activation_boost":  true,
	"embedding_model":   true,
	"embed_attempts":    true,
	"repaired_at":       true,
	"repair_method":     true,
	"repair_needed_llm": true,
}

func isBookkeepingProp(key string) bool {
	return bookkeepingProps[key] || strings.HasPrefix(key, "embedding_")
}

// maskedNodeBytes serializes a node with bookkeeping fields removed,
// producing the canonical bytes the logical-version comparison
// operates on. MarshalNode's property encoding is deterministic (it
// backs content addressing), so byte equality is value equality.
func maskedNodeBytes(n *graph.Node) []byte {
	masked := &graph.Node{ID: n.ID, Properties: make(graph.Properties, len(n.Properties))}
	for k, v := range n.Properties {
		if isBookkeepingProp(k) {
			continue
		}
		masked.Properties[k] = v
	}
	data, err := graph.MarshalNode(masked)
	if err != nil {
		return nil
	}
	return data
}

// logicalChange reports whether cur differs from the blob at
// prevHash beyond bookkeeping. An empty prevHash (new record) is
// always a logical version; an unreadable previous blob degrades to
// "changed" so a storage hiccup can never silently drop a version.
func (e *Engine) logicalChange(prevHash string, cur *graph.Node) bool {
	if prevHash == "" {
		return true
	}
	data, err := e.store.Read(prevHash)
	if err != nil {
		return true
	}
	prev, err := graph.UnmarshalNode(data)
	if err != nil {
		return true
	}
	return !bytes.Equal(maskedNodeBytes(prev), maskedNodeBytes(cur))
}

// appendChangelog indexes one just-committed commit's logical
// versions. Called after the HEAD write, per the durability
// contract: entries + marker land in one sidecar transaction, and a
// crash between HEAD and here leaves marker != HEAD for the boot gap
// walk to repair. Failures log and never fail the Save -- the same
// gap walk covers them.
func (e *Engine) appendChangelog(commit *graph.Commit, dirty, deleted []string, prevHash map[string]string) {
	if e.changelog == nil {
		return
	}
	entries := make(map[string]index.ChangelogEntry)
	for _, id := range dirty {
		n, ok := e.graph.GetNode(id)
		if !ok {
			continue
		}
		if !e.logicalChange(prevHash[id], n) {
			continue
		}
		entries[id] = index.ChangelogEntry{
			Commit:    commit.Hash,
			NodeHash:  e.graph.NodeHashOf(id),
			Timestamp: commit.Timestamp,
		}
	}
	for _, id := range deleted {
		entries[id] = index.ChangelogEntry{
			Commit:    commit.Hash,
			Timestamp: commit.Timestamp,
		}
	}
	if err := e.changelog.Append(entries, commit.Hash); err != nil {
		slog.Error("changelog append failed; boot gap walk will repair",
			"component", "engine", "commit", commit.Hash[:12], "err", err)
	}
}

// changelogGapWalk repairs marker drift at boot: when the marker
// trails HEAD (a crash between the HEAD write and the changelog
// append), the commits in between re-derive their entries from the
// chain. An empty marker means the changelog was never initialized
// on this store -- coverage starts at the next Save, and the offline
// backfill command indexes prior history; the walk never scans a
// full uninitialized chain at boot.
func (e *Engine) changelogGapWalk() {
	if e.changelog == nil || e.headHash == "" {
		return
	}
	marker := e.changelog.Marker()
	if marker == "" || marker == e.headHash {
		return
	}

	// Collect the commits from HEAD back to the marker. A marker not
	// on the current chain (the store was reverted or checked out
	// while this changelog was offline) resets coverage to HEAD --
	// logged, and repairable by the offline backfill.
	const maxGap = 100000
	var chain []*graph.Commit
	cur := e.headHash
	found := false
	for range maxGap {
		c, err := loadCommit(e.store, cur)
		if err != nil {
			break
		}
		chain = append(chain, c)
		if c.Parent == marker {
			found = true
			break
		}
		if c.Parent == "" {
			break
		}
		cur = c.Parent
	}
	if !found {
		slog.Warn("changelog marker not on current chain; resetting coverage to HEAD (run 'gramaton backfill changelog' to re-index history)",
			"component", "engine", "marker", trunc12(marker))
		if err := e.changelog.SetMarker(e.headHash); err != nil {
			slog.Error("changelog marker reset failed", "component", "engine", "err", err)
		}
		return
	}

	// Replay oldest-first so the marker advances commit by commit;
	// a crash mid-walk resumes where it left off.
	slog.Info("changelog gap walk: repairing drift",
		"component", "engine", "commits", len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		c := chain[i]
		var parent *graph.Commit
		if c.Parent != "" {
			p, err := loadCommit(e.store, c.Parent)
			if err == nil {
				parent = p
			}
		}
		e.indexCommitDiff(parent, c)
	}
}

// indexCommitDiff derives one commit's logical versions from its
// tree diff against the parent and appends them (advancing the
// marker to that commit). Shared by the boot gap walk and the
// checkout/revert incremental rebuild.
func (e *Engine) indexCommitDiff(parent, commit *graph.Commit) {
	diff, err := graph.DiffCommits(e.store, parent, commit)
	if err != nil {
		slog.Error("changelog: commit diff failed", "component", "engine",
			"commit", trunc12(commit.Hash), "err", err)
		return
	}
	oldHash := make(map[string]string, len(diff.Removed))
	for _, entry := range diff.Removed {
		oldHash[entry.Key] = entry.Value
	}
	entries := make(map[string]index.ChangelogEntry)
	seen := make(map[string]bool)
	for _, entry := range diff.Added {
		seen[entry.Key] = true
		if e.blobLogicalChange(oldHash[entry.Key], entry.Value) {
			entries[entry.Key] = index.ChangelogEntry{
				Commit:    commit.Hash,
				NodeHash:  entry.Value,
				Timestamp: commit.Timestamp,
			}
		}
	}
	for _, entry := range diff.Removed {
		if !seen[entry.Key] {
			entries[entry.Key] = index.ChangelogEntry{
				Commit:    commit.Hash,
				Timestamp: commit.Timestamp,
			}
		}
	}
	if err := e.changelog.Append(entries, commit.Hash); err != nil {
		slog.Error("changelog: append failed during walk", "component", "engine",
			"commit", trunc12(commit.Hash), "err", err)
	}
}

// blobLogicalChange is logicalChange over two stored blobs (the
// walk's variant: neither side is in memory).
func (e *Engine) blobLogicalChange(prevHash, newHash string) bool {
	if prevHash == "" {
		return true
	}
	prevData, err := e.store.Read(prevHash)
	if err != nil {
		return true
	}
	newData, err := e.store.Read(newHash)
	if err != nil {
		return true
	}
	prev, err := graph.UnmarshalNode(prevData)
	if err != nil {
		return true
	}
	cur, err := graph.UnmarshalNode(newData)
	if err != nil {
		return true
	}
	return !bytes.Equal(maskedNodeBytes(prev), maskedNodeBytes(cur))
}

// loadCommit reads a commit chunk by hash.
func loadCommit(s interface{ Read(string) ([]byte, error) }, hash string) (*graph.Commit, error) {
	data, err := s.Read(hash)
	if err != nil {
		return nil, err
	}
	var c graph.Commit
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	c.Hash = hash
	return &c, nil
}

func trunc12(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
