package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/search"
)

// HistorySearchDescription is the MCP tool description for
// gramaton_history_search. The three scopes form a cost ladder; the
// description states each rung's cost upfront (Tenet 12) so an agent
// can spend deliberately.
const HistorySearchDescription = "Search PAST VERSIONS of records -- knowledge that has since been revised or deleted. Lexical match over the logical-version history (content and change_notes). Three scopes by cost: with 'id', one record's versions are scanned (milliseconds). Default ('candidates'): normal retrieval nominates the top matching records, then their histories are scanned (sub-second). scope='store' scans EVERY logical version in the store -- the only scope that finds knowledge revised away entirely, but tens of seconds on large stores; the response reports how much was scanned and whether the budget truncated it. Every hit names its version commit (usable as gramaton_inspect's as_of) and shows the record's current summary for contrast. For current knowledge use gramaton_search; for one record's timeline use gramaton_history."

// HistorySearchRequest carries the query. Scope is normally implied:
// a non-empty ID forces the single-record scope; otherwise
// "candidates" unless "store" is requested explicitly.
type HistorySearchRequest struct {
	Text   string `json:"text" jsonschema:"lexical query matched against version content and change_notes (case-insensitive substring)"`
	ID     string `json:"id,omitempty" jsonschema:"scan only this record's versions (fastest scope)"`
	Scope  string `json:"scope,omitempty" jsonschema:"'candidates' (default: retrieval nominates records, then their histories are scanned) or 'store' (budgeted scan of every logical version; slow on large stores but finds knowledge revised away entirely)"`
	Budget int    `json:"budget,omitempty" jsonschema:"max version blobs to scan in store scope (default 20000, max 200000)"`
	Since  string `json:"since,omitempty" jsonschema:"only match versions on or after this date (YYYY-MM-DD or RFC3339)"`
	Until  string `json:"until,omitempty" jsonschema:"only match versions up to this date (YYYY-MM-DD or RFC3339)"`
}

// HistorySearchHit is one matching logical version, loudly labeled a
// past version. Commit is as_of-ready: pass it to gramaton_inspect
// to read the record's full frozen state at that version.
type HistorySearchHit struct {
	RecordID string `json:"record_id"`
	// Version labels the hit's place in the record's timeline:
	// "PAST VERSION from <date>", "CURRENT VERSION", or
	// "DELETION from <date>" when the matching entry is the record's
	// deletion (matched via its change_note).
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	Timestamp  string `json:"timestamp"`
	Author     string `json:"author,omitempty"`
	Snippet    string `json:"snippet"`
	ChangeNote string `json:"change_note,omitempty"`
	// CurrentSummary is the record's LIVE summary, for contrast with
	// the matched past content. Empty when the record no longer
	// exists.
	CurrentSummary string `json:"current_summary,omitempty"`
	// RecordSinceDeleted marks a hit on a version of a record that no
	// longer exists at HEAD.
	RecordSinceDeleted bool `json:"record_since_deleted,omitempty"`
}

// HistorySearchResponse reports hits newest-first plus honest
// coverage: how many versions were scanned out of how many were in
// scope, and whether the budget truncated the scan.
type HistorySearchResponse struct {
	Hits      []HistorySearchHit `json:"hits"`
	Scope     string             `json:"scope"`
	Coverage  string             `json:"coverage"`
	Truncated bool               `json:"truncated,omitempty"`
	Semantics string             `json:"semantics"`
}

// historyScanTarget is one record's version list captured under the
// phase-1 read lock for off-lock matching.
type historyScanTarget struct {
	id      string
	entries []index.ChangelogEntry
}

// HistorySearch scans logical versions for a lexical match.
// Three-phase: scope resolution and changelog snapshot under RLock
// (candidates scope also runs retrieval nomination there, with the
// query embed done before the lock); version-blob matching runs
// off-lock -- CAS blobs are immutable, so no lock is needed to read
// them; a brief final RLock resolves the live state of hit records
// only. Never records access: reading history is not using the live
// record.
func (a *API) HistorySearch(ctx context.Context, req HistorySearchRequest) (HistorySearchResponse, *APIError) {
	needle := strings.ToLower(strings.TrimSpace(req.Text))
	if needle == "" {
		return HistorySearchResponse{}, ErrMissing("text is required")
	}
	if len(req.Text) > MaxHistorySearchTextLen {
		return HistorySearchResponse{}, ErrInvalid(fmt.Sprintf("text exceeds maximum length of %d", MaxHistorySearchTextLen))
	}
	if len(req.ID) > MaxIDArgLen {
		return HistorySearchResponse{}, ErrInvalid(fmt.Sprintf("id exceeds maximum length of %d", MaxIDArgLen))
	}
	scope := req.Scope
	switch {
	case req.ID != "":
		if scope != "" && scope != "id" {
			return HistorySearchResponse{}, ErrInvalid("id and scope are mutually exclusive; a given id always scopes the scan to that record")
		}
		scope = "id"
	case scope == "" || scope == "candidates":
		scope = "candidates"
	case scope == "store":
		// explicit opt-in to the full scan
	default:
		return HistorySearchResponse{}, ErrInvalid(`scope must be "candidates" or "store" (or pass id for the single-record scope)`)
	}
	budget := req.Budget
	if budget <= 0 {
		budget = DefaultHistorySearchBudget
	}
	if budget > MaxHistorySearchBudget {
		budget = MaxHistorySearchBudget
	}
	var since, until *time.Time
	for _, pair := range []struct {
		raw  string
		name string
		dest **time.Time
	}{
		{req.Since, "since", &since},
		{req.Until, "until", &until},
	} {
		if pair.raw == "" {
			continue
		}
		t, err := parseDateArg(pair.raw)
		if err != nil {
			return HistorySearchResponse{}, ErrInvalid(fmt.Sprintf("invalid %s date: %s", pair.name, err))
		}
		*pair.dest = &t
	}

	// Candidates scope embeds the query before taking the lock, same
	// as live search; an embed failure degrades to BM25 nomination.
	var queryVec []float32
	if scope == "candidates" && a.engine.Embedder() != nil {
		if vec, err := embed.EmbedForQuery(ctx, a.engine.Embedder(), req.Text); err == nil {
			queryVec = vec
		} else {
			a.log.Warn("history search: query embed failed, nominating by BM25 only",
				"component", "history", "err", err)
		}
	}

	// Phase 1: capture scan targets under the read lock. Only
	// changelog rows and (for candidates) the retrieval nomination --
	// node materialization for hit context waits for phase 3, so a
	// store-wide scan never lazy-loads the whole graph under lock.
	a.engine.RLock()
	targets, totalVersions, apiErr := a.historyScanTargets(ctx, scope, req.ID, needle, queryVec)
	a.engine.RUnlock()
	if apiErr != nil {
		return HistorySearchResponse{}, apiErr
	}
	a.fireHistorySearchSnapshotHook()

	// Phase 2: match off-lock. Blobs are content-addressed and
	// immutable; commits load through a small cache since many
	// versions share one commit.
	store := a.engine.Store()
	commitCache := make(map[string]*graph.Commit)
	loadCachedCommit := func(hash string) *graph.Commit {
		if c, ok := commitCache[hash]; ok {
			return c
		}
		c, err := loadCommitMeta(store, hash)
		if err != nil {
			c = nil
		}
		commitCache[hash] = c
		return c
	}

	scanned := 0
	truncated := false
	var hits []HistorySearchHit
scan:
	for _, tgt := range targets {
		last := len(tgt.entries) - 1
		for vi, entry := range tgt.entries {
			if since != nil && entry.Timestamp.Before(*since) {
				continue
			}
			if until != nil && entry.Timestamp.After(*until) {
				continue
			}
			if scanned >= budget {
				truncated = true
				break scan
			}
			scanned++

			commit := loadCachedCommit(entry.Commit)
			note := changeNoteFor(commit, tgt.id)
			matchedContent, snippet := matchVersionBlob(store, entry.NodeHash, needle)
			matchedNote := note != "" && strings.Contains(strings.ToLower(note), needle)
			if !matchedContent && !matchedNote {
				continue
			}
			if !matchedContent {
				snippet = note
			}

			day := entry.Timestamp.UTC().Format("2006-01-02")
			label := "PAST VERSION from " + day
			switch {
			case entry.NodeHash == "":
				label = "DELETION from " + day
			case vi == last:
				// Whether "current" is honest depends on the record
				// still existing; phase 3 downgrades this label if
				// the live lookup fails.
				label = "CURRENT VERSION"
			}
			hit := HistorySearchHit{
				RecordID:   tgt.id,
				Version:    label,
				Commit:     entry.Commit,
				Timestamp:  entry.Timestamp.UTC().Format(time.RFC3339),
				Snippet:    snippet,
				ChangeNote: note,
			}
			if commit != nil {
				hit.Author = commit.Author
			}
			hits = append(hits, hit)
		}
	}

	// Newest first, then bounded for a prompt-sized response.
	sort.Slice(hits, func(i, j int) bool { return hits[i].Timestamp > hits[j].Timestamp })
	if len(hits) > MaxHistorySearchHits {
		hits = hits[:MaxHistorySearchHits]
		truncated = true
	}

	// Phase 3: live-state contrast for hit records only.
	if len(hits) > 0 {
		a.engine.RLock()
		liveSummary := make(map[string]string, len(hits))
		alive := make(map[string]bool, len(hits))
		for _, h := range hits {
			if _, done := alive[h.RecordID]; done {
				continue
			}
			n, ok := a.engine.Graph().GetNode(h.RecordID)
			alive[h.RecordID] = ok
			if ok {
				if s, ok := n.Properties.GetString("content_short"); ok {
					liveSummary[h.RecordID] = s
				} else if s, ok := n.Properties.GetString("summary_short"); ok {
					liveSummary[h.RecordID] = s
				}
			}
		}
		a.engine.RUnlock()
		for i := range hits {
			if !alive[hits[i].RecordID] {
				hits[i].RecordSinceDeleted = true
				if hits[i].Version == "CURRENT VERSION" {
					hits[i].Version = "PAST VERSION from " + hits[i].Timestamp[:10]
				}
				continue
			}
			hits[i].CurrentSummary = liveSummary[hits[i].RecordID]
		}
	}

	if hits == nil {
		hits = []HistorySearchHit{}
	}
	coverage := fmt.Sprintf("scanned %d of %d versions in scope", scanned, totalVersions)
	if truncated {
		coverage += "; truncated at budget -- raise budget or narrow with since/until for full coverage"
	}
	if totalVersions == 0 {
		coverage = "no indexed versions in scope; if this store predates the changelog index, run 'gramaton backfill changelog'"
	}
	return HistorySearchResponse{
		Hits:      hits,
		Scope:     scope,
		Coverage:  coverage,
		Truncated: truncated,
		Semantics: "past_versions",
	}, nil
}

// historyScanTargets resolves the scope into per-record version
// lists. Caller holds the read lock.
func (a *API) historyScanTargets(ctx context.Context, scope, id, needle string, queryVec []float32) ([]historyScanTarget, int, *APIError) {
	cl := a.engine.Changelog()
	if cl == nil {
		return nil, 0, ErrUnavailable("version history index is not available on this store")
	}
	switch scope {
	case "id":
		entries := cl.Versions(id)
		if len(entries) == 0 {
			if _, ok := a.engine.Graph().GetNode(id); !ok {
				return nil, 0, ErrNotFound("record not found and no version history for it")
			}
			return nil, 0, nil
		}
		return []historyScanTarget{{id: id, entries: entries}}, len(entries), nil

	case "candidates":
		q := search.Query{Text: needle, Top: HistorySearchCandidates, ExcludeConcepts: true}
		results, err := a.engine.Searcher().ExecuteWithVector(ctx, q, queryVec)
		if err != nil {
			a.log.Warn("history search: candidate nomination failed", "component", "history", "err", err)
			return nil, 0, ErrInternal("candidate nomination failed")
		}
		var targets []historyScanTarget
		total := 0
		for _, r := range results {
			entries := cl.Versions(r.ID)
			if len(entries) == 0 {
				continue
			}
			targets = append(targets, historyScanTarget{id: r.ID, entries: entries})
			total += len(entries)
		}
		return targets, total, nil

	default: // "store"
		var targets []historyScanTarget
		total := 0
		_ = cl.ForEach(func(nodeID string, entries []index.ChangelogEntry) error {
			targets = append(targets, historyScanTarget{id: nodeID, entries: entries})
			total += len(entries)
			return nil
		})
		return targets, total, nil
	}
}

// matchVersionBlob loads one version's node blob and reports whether
// its content matches the lowercase needle, with a snippet around the
// first match. A deletion entry (empty hash) or unreadable blob never
// matches -- the change_note path still can.
func matchVersionBlob(store interface{ Read(string) ([]byte, error) }, nodeHash, needle string) (bool, string) {
	if nodeHash == "" {
		return false, ""
	}
	data, err := store.Read(nodeHash)
	if err != nil {
		return false, ""
	}
	n, err := graph.UnmarshalNode(data)
	if err != nil {
		return false, ""
	}
	content := ""
	for _, key := range []string{"content_full", "content", "content_short", "summary_short"} {
		if s, ok := n.Properties.GetString(key); ok && s != "" {
			content = s
			break
		}
	}
	if content == "" {
		return false, ""
	}
	idx := strings.Index(strings.ToLower(content), needle)
	if idx < 0 {
		return false, ""
	}
	return true, snippetAround(content, idx, len(needle))
}

// snippetAround cuts a window around the match. Byte-indexed slicing
// could split a UTF-8 rune, so bounds snap outward/inward to rune
// starts.
func snippetAround(content string, idx, matchLen int) string {
	const before, after = 60, 160
	start := idx - before
	if start < 0 {
		start = 0
	}
	for start > 0 && !isRuneStart(content[start]) {
		start--
	}
	end := idx + matchLen + after
	if end > len(content) {
		end = len(content)
	}
	for end < len(content) && !isRuneStart(content[end]) {
		end++
	}
	snippet := content[start:end]
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(content) {
		snippet += "..."
	}
	return snippet
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// changeNoteFor extracts the change_note a commit carries for one
// record, if any.
func changeNoteFor(commit *graph.Commit, recordID string) string {
	if commit == nil {
		return ""
	}
	for _, act := range commit.Actions {
		if act.RecordID == recordID && act.Note != "" {
			return act.Note
		}
	}
	return ""
}
