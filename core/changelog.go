package core

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"

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
	// Reembed retry bookkeeping and the deferred save-guard flag:
	// both are written by sweeps that must never mint versions.
	"last_embed_error":      true,
	"similar_check_pending": true,
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
	// An adopted-graph commit (revert/merge) carries no dirty nodes;
	// advancing the marker here would strand the commit's real
	// versions if the process dies before the explicit tree-diff
	// indexing runs -- the gap walk early-returns on marker == HEAD.
	// Leave the marker behind; IndexCommitDiffByHash advances it.
	if e.adoptedCommitPending {
		e.adoptedCommitPending = false
		return
	}
	entries := make(map[string]index.ChangelogEntry)
	for _, id := range dirty {
		n, ok := e.graph.GetNode(id)
		if !ok {
			continue
		}
		// Concept churn (synthesis rewrites, alias merges, evidence
		// counts) is curation regenerating derived data, not knowledge
		// history -- concepts mint no versions.
		if graph.IsConcept(n.Properties) {
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
		if e.blobIsConcept(prevHash[id]) {
			continue
		}
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
		if e.blobIsConcept(entry.Value) {
			continue
		}
		if e.blobLogicalChange(oldHash[entry.Key], entry.Value) {
			entries[entry.Key] = index.ChangelogEntry{
				Commit:    commit.Hash,
				NodeHash:  entry.Value,
				Timestamp: commit.Timestamp,
			}
		}
	}
	for _, entry := range diff.Removed {
		if !seen[entry.Key] && !e.blobIsConcept(entry.Value) {
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

// blobIsConcept reports whether the blob at hash is a concept node.
// Unreadable or empty hashes report false (the record path stays the
// default).
func (e *Engine) blobIsConcept(hash string) bool {
	if hash == "" {
		return false
	}
	data, err := e.store.Read(hash)
	if err != nil {
		return false
	}
	n, err := graph.UnmarshalNode(data)
	if err != nil {
		return false
	}
	return graph.IsConcept(n.Properties)
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

// RebuildChangelogFor re-scopes the changelog when HEAD moves to a
// different lineage (branch checkout/merge adoption): entries minted
// by commits exclusive to the abandoned lineage are retracted, and
// commits exclusive to the new lineage are replayed -- O(divergence)
// from the merge-base, never O(chain). Caller must hold the write
// lock. An uninitialized changelog stays uninitialized; a marker
// whose lineage cannot be reconciled within the walk bound resets
// coverage to the new head with a pointer at the offline backfill.
func (e *Engine) RebuildChangelogFor(newHead string) {
	if e.changelog == nil || newHead == "" {
		return
	}
	marker := e.changelog.Marker()
	if marker == "" || marker == newHead {
		return
	}

	const maxWalk = 100000

	// Two-frontier interleaved walk: step the old lineage (from the
	// marker) and the new lineage (from the new head) alternately,
	// checking each step against the other side's visited set. The
	// walk stops at the merge-base after O(divergence) steps -- a
	// single-sided walk would pay O(chain) building the full new
	// ancestry before ever looking at the divergence, a 100k-commit
	// disk walk under the write lock on the motivating stores.
	oldSeen := map[string]bool{}
	newSeen := map[string]bool{}
	var newChain []*graph.Commit // newHead -> ... in walk order
	oldCur, newCur := marker, newHead
	base := ""
	for range maxWalk {
		if base != "" || (oldCur == "" && newCur == "") {
			break
		}
		if oldCur != "" {
			if newSeen[oldCur] {
				base = oldCur
				break
			}
			oldSeen[oldCur] = true
			c, err := loadCommit(e.store, oldCur)
			if err != nil {
				oldCur = ""
			} else {
				oldCur = c.Parent
			}
		}
		if newCur != "" {
			if oldSeen[newCur] {
				base = newCur
				break
			}
			newSeen[newCur] = true
			c, err := loadCommit(e.store, newCur)
			if err != nil {
				newCur = ""
			} else {
				newChain = append(newChain, c)
				newCur = c.Parent
			}
		}
	}
	// old-exclusive = everything the old walk visited strictly before
	// the base (the base itself may sit in oldSeen when the new
	// frontier discovered it there).
	oldExclusive := oldSeen
	delete(oldExclusive, base)
	if base == "" {
		slog.Warn("changelog rebuild: no merge-base with the new lineage; resetting coverage (run 'gramaton backfill changelog' to re-index history)",
			"component", "engine", "marker", trunc12(marker), "new_head", trunc12(newHead))
		if err := e.changelog.SetMarker(newHead); err != nil {
			slog.Error("changelog marker reset failed", "component", "engine", "err", err)
		}
		return
	}

	if err := e.changelog.RetractCommits(oldExclusive); err != nil {
		slog.Error("changelog rebuild: retract failed", "component", "engine", "err", err)
	}

	// Replay the new-exclusive commits oldest-first; each Append
	// advances the marker, so a crash mid-replay resumes cleanly.
	// When the OLD frontier discovered the base, newChain holds only
	// commits strictly above it (replay all); when the NEW frontier
	// walked onto it, the base sits in newChain and replay starts
	// just above its index. A pure rewind replays nothing.
	replayFrom := len(newChain) - 1
	for i, c := range newChain {
		if c.Hash == base {
			replayFrom = i - 1
			break
		}
	}
	if err := e.changelog.SetMarker(base); err != nil {
		slog.Error("changelog rebuild: marker set failed", "component", "engine", "err", err)
	}
	for i := replayFrom; i >= 0; i-- {
		c := newChain[i]
		var parent *graph.Commit
		if c.Parent != "" {
			if p, err := loadCommit(e.store, c.Parent); err == nil {
				parent = p
			}
		}
		e.indexCommitDiff(parent, c)
	}
}

// IndexCommitDiffByHash appends one commit's logical versions from
// its tree diff against an explicit parent. Used after adopting a
// staged graph (revert, merge): the adopted graph carries no dirty
// nodes, so the Save-time append sees an empty mutation set even
// though the commit's tree may differ wholesale from its parent's.
// Idempotent per node (a commit already at a node's list tail is
// skipped). Caller must hold the write lock.
func (e *Engine) IndexCommitDiffByHash(parentHash, commitHash string) {
	if e.changelog == nil || commitHash == "" {
		return
	}
	commit, err := loadCommit(e.store, commitHash)
	if err != nil {
		slog.Error("changelog: load commit for diff failed", "component", "engine",
			"commit", trunc12(commitHash), "err", err)
		return
	}
	var parent *graph.Commit
	if parentHash != "" {
		if p, err := loadCommit(e.store, parentHash); err == nil {
			parent = p
		}
	}
	e.indexCommitDiff(parent, commit)
}

// BackfillChangelog walks the ENTIRE commit chain root-to-HEAD and
// indexes every logical version -- the offline migration for stores
// whose history predates the changelog. Commits labeled access_flush
// are skipped outright (they persisted only the retired read-churn
// bookkeeping; the next pair diffs against them as baseline, so the
// churn is never nominated). Every hash-diff-nominated pair still
// passes the masked blob comparison, which is what keeps pre-sidecar
// history honest: access churn rode INSIDE logical commits, and a
// label-only backfill would mint phantom versions at 10-40x on
// heavily-read stores. Appends batch across commits to amortize
// fsync; the marker advances per batch, so an interrupted run
// resumes idempotently. progress is called per batch when non-nil.
func (e *Engine) BackfillChangelog(progress func(done, total int)) (int, error) {
	if e.changelog == nil {
		return 0, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.headHash == "" {
		return 0, nil
	}

	// Collect the chain HEAD-back-to-root, then process in reverse.
	var chain []*graph.Commit
	cur := e.headHash
	for cur != "" {
		c, err := loadCommit(e.store, cur)
		if err != nil {
			return 0, err
		}
		chain = append(chain, c)
		cur = c.Parent
	}

	const batchCommits = 500
	total := len(chain)
	indexed := 0
	batch := make(map[string][]index.ChangelogEntry)
	var batchMarker string
	flush := func() error {
		if err := e.changelog.AppendBatch(batch, batchMarker); err != nil {
			return err
		}
		batch = make(map[string][]index.ChangelogEntry)
		return nil
	}

	for i := len(chain) - 1; i >= 0; i-- {
		c := chain[i]
		batchMarker = c.Hash
		done := total - i
		if strings.HasPrefix(c.Message, "access_flush") {
			if done%batchCommits == 0 {
				if err := flush(); err != nil {
					return indexed, err
				}
				if progress != nil {
					progress(done, total)
				}
			}
			continue
		}
		var parent *graph.Commit
		if c.Parent != "" {
			if p, err := loadCommit(e.store, c.Parent); err == nil {
				parent = p
			}
		}
		diff, err := graph.DiffCommits(e.store, parent, c)
		if err != nil {
			return indexed, err
		}
		oldHash := make(map[string]string, len(diff.Removed))
		for _, entry := range diff.Removed {
			oldHash[entry.Key] = entry.Value
		}
		seen := make(map[string]bool)
		for _, entry := range diff.Added {
			seen[entry.Key] = true
			if e.blobIsConcept(entry.Value) {
				continue
			}
			if e.blobLogicalChange(oldHash[entry.Key], entry.Value) {
				batch[entry.Key] = append(batch[entry.Key], index.ChangelogEntry{
					Commit: c.Hash, NodeHash: entry.Value, Timestamp: c.Timestamp,
				})
				indexed++
			}
		}
		for _, entry := range diff.Removed {
			if !seen[entry.Key] && !e.blobIsConcept(entry.Value) {
				batch[entry.Key] = append(batch[entry.Key], index.ChangelogEntry{
					Commit: c.Hash, Timestamp: c.Timestamp,
				})
				indexed++
			}
		}
		if done%batchCommits == 0 {
			if err := flush(); err != nil {
				return indexed, err
			}
			if progress != nil {
				progress(done, total)
			}
		}
	}
	if err := flush(); err != nil {
		return indexed, err
	}
	if progress != nil {
		progress(total, total)
	}
	return indexed, nil
}

// DiffVersionFields lists the property names that differ between two
// stored node blobs, bookkeeping fields masked -- the timeline's
// mechanical per-version diff. Removed fields are suffixed
// "(removed)". An empty prevHash (the record's first indexed
// version) reports every non-bookkeeping field, i.e. the creation
// shape. Sorted for stable output.
func (e *Engine) DiffVersionFields(prevHash, curHash string) []string {
	loadProps := func(h string) graph.Properties {
		if h == "" {
			return nil
		}
		data, err := e.store.Read(h)
		if err != nil {
			return nil
		}
		n, err := graph.UnmarshalNode(data)
		if err != nil {
			return nil
		}
		return n.Properties
	}
	prev := loadProps(prevHash)
	cur := loadProps(curHash)
	if cur == nil {
		return nil
	}

	var out []string
	for k, v := range cur {
		if isBookkeepingProp(k) {
			continue
		}
		pv, had := prev[k]
		if !had {
			out = append(out, k)
			continue
		}
		if !bytes.Equal(propBytes(v), propBytes(pv)) {
			out = append(out, k)
		}
	}
	for k := range prev {
		if isBookkeepingProp(k) {
			continue
		}
		if _, still := cur[k]; !still {
			out = append(out, k+" (removed)")
		}
	}
	sort.Strings(out)
	return out
}

// propBytes canonicalizes one property value for comparison.
func propBytes(p graph.Property) []byte {
	n := &graph.Node{ID: "x", Properties: graph.Properties{"v": p}}
	data, err := graph.MarshalNode(n)
	if err != nil {
		return nil
	}
	return data
}

// ancestry is the memoized ancestor set of the current head, built
// lazily for OnCurrentBranch and invalidated whenever HEAD moves
// (Save, checkout). Guarded by its own mutex: readers hold the
// engine RLock, under which engine fields must not be mutated.
type ancestry struct {
	mu   sync.Mutex
	head string
	set  map[string]bool
}

// OnCurrentBranch reports whether hash is an ancestor of (or equal
// to) the current head. The first call after a head move walks the
// chain once (bounded) and memoizes; subsequent calls are a map
// lookup. Caller must hold at least the engine read lock.
func (e *Engine) OnCurrentBranch(hash string) bool {
	head := e.headHash
	if head == "" {
		return false
	}
	e.anc.mu.Lock()
	defer e.anc.mu.Unlock()
	if e.anc.head != head {
		set := make(map[string]bool)
		cur := head
		const bound = 200000
		for range bound {
			if cur == "" {
				break
			}
			set[cur] = true
			c, err := loadCommit(e.store, cur)
			if err != nil {
				break
			}
			cur = c.Parent
		}
		e.anc.head = head
		e.anc.set = set
	}
	return e.anc.set[hash]
}

// invalidateAncestry drops the memoized ancestor set (head moved).
func (e *Engine) invalidateAncestry() {
	e.anc.mu.Lock()
	e.anc.head = ""
	e.anc.set = nil
	e.anc.mu.Unlock()
}

// advanceAncestry extends the memoized ancestor set when the head
// moves by ONE appended commit (every Save). Cheap incremental
// maintenance -- invalidating instead would re-walk the chain on the
// next as_of call after every write.
func (e *Engine) advanceAncestry(newHead string) {
	e.anc.mu.Lock()
	if e.anc.set != nil {
		e.anc.set[newHead] = true
		e.anc.head = newHead
	}
	e.anc.mu.Unlock()
}
