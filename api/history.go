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

// VersionEntry is one logical version in the record's timeline:
// what changed, when, by whom, and why (when a change_note was
// given). FieldsChanged is the mechanical diff against the previous
// version with bookkeeping fields masked -- always computed; the
// note is color on top.
type VersionEntry struct {
	Commit     string `json:"commit"`
	Timestamp  string `json:"timestamp"`
	Author     string `json:"author,omitempty"`
	ChangeNote string `json:"change_note,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	// ContentPruned marks a version whose metadata survives but whose
	// content blob was removed by a retention prune -- readable via
	// neither as_of nor the field diff, by policy rather than damage.
	ContentPruned bool     `json:"content_pruned,omitempty"`
	FieldsChanged []string `json:"fields_changed,omitempty"`
}

// HistoryResponse lists the changes in reverse-chronological order
// (most recent first). Versions is the logical-version timeline from
// the changelog index; VersionCoverage carries a caveat when the
// index cannot vouch for the record's full history.
type HistoryResponse struct {
	ID              string          `json:"id"`
	Changes         []HistoryChange `json:"changes"`
	Versions        []VersionEntry  `json:"versions,omitempty"`
	VersionCoverage string          `json:"version_coverage,omitempty"`
}

// HistoryDescription is the MCP tool description for gramaton_history.
const HistoryDescription = "View a record's change history. The versions list is the logical-version timeline: one entry per real knowledge change (bookkeeping like re-embeds never mints a version), newest first, each with its author, optional change_note, and the mechanical field diff against the previous version. version_coverage carries a caveat when history may predate the changelog index. The changes list is the raw commit walk (message-level)."

// History walks the commit chain back from HEAD looking for commits
// where the record's serialized property hash changed. Without
// Since/Until, traversal is capped at MaxLogTraversal to bound the
// read-lock hold. With Since/Until, the walk narrows via the D7
// timestamp index: start from CommitAt(Until) (default HEAD), stop
// when a commit's timestamp falls before Since. Date-bounded walks
// bypass the traversal cap -- the range itself bounds the work.
//
// Attribution: the walk visits consecutive commits newest-first and
// compares each against the previously visited (newer) one. Any
// difference -- created, modified, or deleted -- was made BY that
// newer commit, so the entry carries its hash, timestamp, and
// message. When the walk consumes the whole chain with the record
// still present, the oldest commit introduced it. A walk cut short
// (traversal cap, Since bound) emits nothing for the unseen older
// side; the versions timeline is the authoritative surface there.
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
			// The timestamp index is global across branches; snap the
			// hit onto the active lineage so the walk never serves an
			// abandoned branch's history.
			if snapped := a.snapToCurrentBranch(h); snapped != "" {
				hash = snapped
			} else {
				return HistoryResponse{ID: req.ID, Changes: []HistoryChange{}}, nil
			}
		} else {
			// Until predates every indexed commit; nothing to walk.
			return HistoryResponse{ID: req.ID, Changes: []HistoryChange{}}, nil
		}
	}

	dateBounded := !sinceT.IsZero() || !untilT.IsZero()

	out := HistoryResponse{ID: req.ID, Changes: []HistoryChange{}}
	const maxTraversal = MaxLogTraversal
	traversed := 0
	// Transition state: the previously visited (newer) commit and
	// whether -- and with what hash -- the record existed there. A
	// difference between the current commit and the previous one was
	// made by the previous commit; creation, modification, and
	// deletion entries all attribute there, never to the older side
	// of the pair.
	var prev *graph.Commit
	prevCommitHash := ""
	prevNodeHash := ""
	prevFound := false
	emitPrev := func() {
		out.Changes = append(out.Changes, HistoryChange{
			Commit:    prevCommitHash[:12],
			Timestamp: prev.Timestamp.Format("2006-01-02T15:04:05Z"),
			Action:    prev.Message,
		})
	}
	stoppedEarly := false
	for hash != "" && len(out.Changes) < limit {
		// Only apply the traversal cap when no date bounds were
		// provided; date bounds supply their own termination condition.
		if !dateBounded && traversed >= maxTraversal {
			stoppedEarly = true
			break
		}
		traversed++
		commit, err := loadCommitMeta(store, hash)
		if err != nil {
			stoppedEarly = true
			break
		}

		nodeHash, found, _ := graph.NodeHashInCommit(store, hash, req.ID)
		if prev != nil && (prevFound != found || (found && nodeHash != prevNodeHash)) {
			emitPrev()
		}

		// Stop once we walk past the Since boundary -- checked after
		// the transition so a change made by the oldest in-window
		// commit still surfaces (only its "before" state is out of
		// window).
		if !sinceT.IsZero() && commit.Timestamp.Before(sinceT) {
			stoppedEarly = true
			break
		}

		prev, prevCommitHash, prevNodeHash, prevFound = commit, hash, nodeHash, found
		hash = commit.Parent
	}
	// The walk consumed the whole chain with the record present at
	// the oldest commit: that commit introduced it.
	if !stoppedEarly && hash == "" && prevFound && len(out.Changes) < limit {
		emitPrev()
	}

	a.attachVersionTimeline(&out, req.ID, limit)
	return out, nil
}

// attachVersionTimeline populates the logical-version list from the
// changelog index, newest first, with per-version attribution and
// masked field diffs. Caller must hold at least the read lock.
func (a *API) attachVersionTimeline(out *HistoryResponse, id string, limit int) {
	chlog := a.engine.Changelog()
	if chlog == nil {
		return
	}
	// Concepts are derived data: curation synthesis churn is not
	// knowledge history, so they carry no version timeline at all.
	if n, ok := a.engine.Graph().GetNode(id); ok && graph.IsConcept(n.Properties) {
		out.VersionCoverage = "derived node -- regenerated by curation, no version timeline; its knowledge history lives on the member records"
		return
	}
	if chlog.Marker() == "" {
		out.VersionCoverage = "changelog not initialized on this store; run 'gramaton backfill changelog' to index history"
		return
	}
	entries := chlog.Versions(id)
	if len(entries) == 0 {
		out.VersionCoverage = "no indexed versions; the record's history may predate changelog coverage (run 'gramaton backfill changelog')"
		return
	}

	store := a.engine.Store()
	for i := len(entries) - 1; i >= 0 && len(out.Versions) < limit; i-- {
		e := entries[i]
		v := VersionEntry{
			Commit:    truncHash(e.Commit),
			Timestamp: e.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
			Deleted:   e.NodeHash == "",
		}
		if commit, err := loadCommitMeta(store, e.Commit); err == nil {
			v.Author = commit.Author
			for _, act := range commit.Actions {
				if act.RecordID == id && act.Note != "" {
					v.ChangeNote = act.Note
					break
				}
			}
		}
		if !v.Deleted {
			if floor := a.engine.HistoryFloor(); floor != nil &&
				floor.CoversRecordVersion(id, e.Timestamp) && !store.Has(e.NodeHash) {
				// Metadata retained, content removed by policy.
				v.ContentPruned = true
			} else {
				prevHash := ""
				if i > 0 {
					prevHash = entries[i-1].NodeHash
				}
				v.FieldsChanged = a.engine.DiffVersionFields(prevHash, e.NodeHash)
			}
		}
		out.Versions = append(out.Versions, v)
	}
}

func truncHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
