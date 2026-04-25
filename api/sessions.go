package api

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/internal/sanitize"
)

//go:embed prompts/extraction.md
var embeddedExtractionPrompt string

// Session tool descriptions shared across MCP transports (server bindings
// and CLI proxy). Single-source prevents the two MCP surfaces from
// drifting on help text. Matches the api.XxxDescription convention
// established for capture + collections.

// SessionStartDescription is the MCP tool description for
// gramaton_session_start.
const SessionStartDescription = `Start or resume a knowledge capture session. On fresh start, creates a new session. On resume (--continue), creates a new session chained to the previous one. Returns the active session.`

// SessionGetDescription is the MCP tool description for
// gramaton_session_get.
const SessionGetDescription = `Get the current session state including all topics and segments. Use to review what has been captured so far.`

// SessionPrepareDescription is the MCP tool description for
// gramaton_session_prepare. Leads with "eagerly throughout" language
// to counter the prior-version regression where agents self-triggered
// far less often than the old gramaton_observe tool did.
const SessionPrepareDescription = `Extract knowledge from the ongoing conversation. Returns extraction instructions and session state. Call this EAGERLY throughout a conversation, not just at the end: immediately after a decision lands, a rule or principle is articulated, a task completes, or the user pivots topics. Also call before context compaction, and at least every ~10 substantive turns even without an explicit trigger. Bundling captures at session end is an anti-pattern -- knowledge from early in the conversation becomes harder to reconstruct as context accumulates. You must follow the returned instructions before calling gramaton_session_commit.`

// SessionCommitDescription is the MCP tool description for
// gramaton_session_commit.
const SessionCommitDescription = `Submit extracted knowledge segments to the session. IMPORTANT: You must call gramaton_session_prepare first and follow its instructions. Do not call this tool directly -- the preparation step provides required context for high-quality extraction.`

// loadExtractionPrompt loads the extraction prompt from the config directory,
// falling back to the embedded default. Returns the prompt content and a short
// hash for logging which version was used.
func (a *API) loadExtractionPrompt() (string, string) {
	// Try loading from config directory first (allows user overrides).
	if a.configDir != "" {
		filePath := filepath.Join(a.configDir, "prompts", "extraction.md")
		if data, err := os.ReadFile(filePath); err == nil {
			content := string(data)
			hash := fmt.Sprintf("%x", sha256.Sum256(data))[:8]
			a.log.Debug("extraction prompt loaded from file", "component", "session",
				"path", filePath, "size", len(content), "hash", hash)
			return content, hash
		}
	}

	// Fall back to embedded default.
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(embeddedExtractionPrompt)))[:8]
	a.log.Debug("extraction prompt loaded from embedded", "component", "session",
		"size", len(embeddedExtractionPrompt), "hash", hash)
	return embeddedExtractionPrompt, hash
}

// --- helpers ---

// sessionsByClientID finds all Session nodes with a given client_session_id.
// Returns node IDs sorted by created_at (newest last).
// Caller must hold at least RLock.
func (a *API) sessionsByClientID(clientID string) []string {
	ids := a.engine.PropIdx().Lookup("knowledge_type", graph.StringProperty("session"))
	var matches []string
	for _, id := range ids {
		n, ok := a.engine.Graph().GetNode(id)
		if !ok {
			continue
		}
		if cid, ok := n.Properties.GetString("client_session_id"); ok {
			if cid == clientID {
				matches = append(matches, id)
			}
		}
	}
	return matches
}

// latestSessionByClientID finds the most recent Session with this client_session_id.
// "Most recent" = the tail of the continues_from chain (no inbound continues_from edges).
// Caller must hold at least RLock.
func (a *API) latestSessionByClientID(clientID string) (string, bool) {
	matches := a.sessionsByClientID(clientID)
	if len(matches) == 0 {
		return "", false
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	// Find the tail: the session that has no inbound continues_from edge
	// (nothing continues FROM it, so it's the latest).
	hasNext := make(map[string]bool)
	for _, id := range matches {
		for _, e := range a.engine.Graph().EdgesFrom(id) {
			if e.Type == "continues_from" {
				hasNext[e.TargetID] = true
			}
		}
	}
	for _, id := range matches {
		if !hasNext[id] {
			return id, true
		}
	}
	// Fallback: return last in list.
	return matches[len(matches)-1], true
}

// isSession checks if a node is a Session.
// Caller must hold at least RLock.
func (a *API) isSession(nodeID string) (*graph.Node, *APIError) {
	n, ok := a.engine.Graph().GetNode(nodeID)
	if !ok {
		return nil, ErrNotFound("session not found")
	}
	kt, _ := n.Properties.GetString("knowledge_type")
	if kt != "session" {
		return nil, ErrNotFound("not a session")
	}
	return n, nil
}

// sessionTopics returns all Topic nodes linked to a Session via topic_of edges.
// Caller must hold at least RLock.
func (a *API) sessionTopics(sessionID string) []*graph.Node {
	var topics []*graph.Node
	for _, e := range a.engine.Graph().EdgesTo(sessionID) {
		if e.Type == "topic_of" {
			if n, ok := a.engine.Graph().GetNode(e.SourceID); ok {
				topics = append(topics, n)
			}
		}
	}
	return topics
}

// topicSegments returns all Segment nodes linked to a Topic via segment_of edges.
// Caller must hold at least RLock.
func (a *API) topicSegments(topicID string) []*graph.Node {
	var segments []*graph.Node
	for _, e := range a.engine.Graph().EdgesTo(topicID) {
		if e.Type == "segment_of" {
			if n, ok := a.engine.Graph().GetNode(e.SourceID); ok {
				segments = append(segments, n)
			}
		}
	}
	return segments
}

// buildSessionResponse constructs a full Session response including topics and segments.
// Caller must hold at least RLock.
func (a *API) buildSessionResponse(sessionID string, session *graph.Node) map[string]any {
	resp := map[string]any{
		"id": sessionID,
	}
	if cid, ok := session.Properties.GetString("client_session_id"); ok {
		resp["client_session_id"] = cid
	}
	if ca, ok := session.Properties.GetTimestamp("created_at"); ok {
		resp["created_at"] = ca.Format(time.RFC3339)
	}
	if themes, ok := session.Properties.GetStringList("themes"); ok {
		resp["themes"] = themes
	}
	// Include archive reference if present.
	if path, ok := session.Properties.GetString("archive_path"); ok {
		archive := map[string]any{"path": path}
		if sz, ok := session.Properties.GetInt64("archive_size"); ok {
			archive["compressed_size"] = sz
		}
		if sz, ok := session.Properties.GetInt64("archive_original_size"); ok {
			archive["original_size"] = sz
		}
		if at, ok := session.Properties.GetTimestamp("archived_at"); ok {
			archive["archived_at"] = at.Format(time.RFC3339)
		}
		resp["raw_archive"] = archive
	}

	topics := a.sessionTopics(sessionID)
	topicList := make([]map[string]any, 0, len(topics))
	for _, t := range topics {
		topicResp := map[string]any{
			"id": t.ID,
		}
		if name, ok := t.Properties.GetString("topic_name"); ok {
			topicResp["name"] = name
		}
		if bf, ok := t.Properties.GetString("branched_from"); ok {
			topicResp["branched_from"] = bf
		}
		if ca, ok := t.Properties.GetTimestamp("created_at"); ok {
			topicResp["created_at"] = ca.Format(time.RFC3339)
		}

		segments := a.topicSegments(t.ID)
		segList := make([]map[string]any, 0, len(segments))
		for _, seg := range segments {
			segResp := map[string]any{
				"id": seg.ID,
			}
			if c, ok := seg.Properties.GetString("content"); ok {
				segResp["content"] = c
			}
			if ca, ok := seg.Properties.GetString("captured_as"); ok {
				segResp["captured_as"] = ca
			}
			if ct, ok := seg.Properties.GetTimestamp("captured_at"); ok {
				segResp["captured_at"] = ct.Format(time.RFC3339)
			}
			if ca, ok := seg.Properties.GetTimestamp("created_at"); ok {
				segResp["created_at"] = ca.Format(time.RFC3339)
			}
			segList = append(segList, segResp)
		}
		topicResp["segments"] = segList
		topicList = append(topicList, topicResp)
	}
	resp["topics"] = topicList
	return resp
}

// --- service methods ---

// SessionStart creates a new Session, chains to previous sessions
// on resume, or returns the current session for idempotent agent calls.
//
// source="startup": fresh session, no chaining
// source="resume": new session chained to latest with same client_session_id
// source="" (agent call): return existing if found, create fresh otherwise
func (a *API) SessionStart(ctx context.Context, clientSessionID string, source string) (map[string]any, *APIError) {
	_ = ctx // reserved for future engine calls that accept cancellation
	if err := validateClientSessionID(clientSessionID); err != nil {
		return nil, ErrInvalid(err.Error())
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	latestID, hasExisting := a.latestSessionByClientID(clientSessionID)

	// Agent idempotent call (no source): return existing if found.
	if source == "" && hasExisting {
		session, _ := a.engine.Graph().GetNode(latestID)
		a.log.Debug("session lookup hit", "component", "session",
			"client_session_id", clientSessionID, "session_id", latestID)
		resp := a.buildSessionResponse(latestID, session)
		resp["resumed"] = true
		return resp, nil
	}

	// Create new session.
	now := time.Now().UTC()
	props := graph.Properties{
		"knowledge_type":    graph.StringProperty("session"),
		"client_session_id": graph.StringProperty(clientSessionID),
		"created_at":        graph.TimestampProperty(now),
	}
	n := a.engine.Graph().AddNode(props)
	a.engine.IndexNode(n.ID, "", nil)

	// On resume, chain to the previous session.
	var previousSessionID string
	if source == "resume" && hasExisting {
		if _, err := a.engine.Graph().AddEdge(n.ID, latestID, "continues_from", 1.0, nil); err != nil {
			a.log.Warn("failed to create continues_from edge", "component", "session",
				"new_session", n.ID, "previous_session", latestID, "err", err)
		} else {
			previousSessionID = latestID
			a.log.Info("session chained", "component", "session",
				"new_session", n.ID, "previous_session", latestID)
		}
	}

	if _, err := a.engine.Save("session_create"); err != nil {
		a.log.Warn("session save failed", "component", "session", "err", err)
		return nil, ErrInternal("failed to save session")
	}

	a.log.Info("session created", "component", "session",
		"session_id", n.ID, "client_session_id", clientSessionID,
		"source", source, "chained_to", previousSessionID)

	resp := a.buildSessionResponse(n.ID, n)
	resp["resumed"] = false
	if previousSessionID != "" {
		resp["previous_session_id"] = previousSessionID
	}
	return resp, nil
}

// SessionGet returns the full state of a Session.
func (a *API) SessionGet(ctx context.Context, sessionID string) (map[string]any, *APIError) {
	_ = ctx
	if sessionID == "" {
		return nil, ErrMissing("session_id is required")
	}

	a.engine.RLock()
	defer a.engine.RUnlock()

	session, svcErr := a.isSession(sessionID)
	if svcErr != nil {
		return nil, svcErr
	}

	a.log.Debug("session get", "component", "session", "session_id", sessionID,
		"topic_count", len(a.sessionTopics(sessionID)))

	return a.buildSessionResponse(sessionID, session), nil
}

// compactionFlagTTL bounds how long a PostCompact/PreCompact flag is
// considered fresh. Compaction is normally followed by continued work
// within minutes; 2h covers a long break without surfacing stale nudges
// the next day.
const compactionFlagTTL = 2 * time.Hour

// preparedSessionTTL bounds how long a "prepared but never committed"
// entry stays in a.preparedSessions before the sweeper drops it. The
// flag is in-memory only (B2 resolution) and lives long enough to span
// the realistic gap between a prepare call and the agent's commit.
// Anything older than this is almost certainly orphaned.
//
// Race window: a commit issued at exactly TTL+epsilon races the sweeper
// (which fires every preparedSweepInterval). The same a.mu serialises
// both, so this is observable behaviour, not a data race -- the commit
// just sees prepare_required and the agent retries with a fresh
// prepare. 30 minutes is far longer than any realistic agent flow
// (prepare->extract->commit is seconds to a few minutes), so the race
// is theoretical. Documented here so a future grace-period tweak has
// the rationale.
const preparedSessionTTL = 30 * time.Minute

// preparedSweepInterval is how often the background sweeper runs.
const preparedSweepInterval = 5 * time.Minute

// preparedSessionsFilename is the on-disk file persisting the
// preparedSessions map across server restarts. Without this, a
// restart between an agent's prepare call and its follow-up commit
// permanently breaks the flow with prepare_required, even though
// the agent acted correctly.
const preparedSessionsFilename = "prepared_sessions.json"

// preparedSessionsPath returns the on-disk path for the persisted
// prepared-flag map.
func (a *API) preparedSessionsPath() string {
	return filepath.Join(a.configDir, preparedSessionsFilename)
}

// loadPreparedSessions reads the persisted map from disk and applies
// the TTL filter (so a restart that lands long after the prior
// process died doesn't surface zombie flags). Called during Server
// construction. Best-effort: load failures fall through to an empty
// map and log at Warn.
func (a *API) loadPreparedSessions() {
	data, err := os.ReadFile(a.preparedSessionsPath())
	if err != nil {
		if !os.IsNotExist(err) {
			a.log.Warn("prepared sessions load failed",
				"component", "session", "err", err)
		}
		return
	}
	var stored map[string]time.Time
	if err := json.Unmarshal(data, &stored); err != nil {
		a.log.Warn("prepared sessions parse failed",
			"component", "session", "err", err)
		return
	}
	cutoff := time.Now().Add(-preparedSessionTTL)
	for sessionID, t := range stored {
		if t.After(cutoff) {
			a.preparedSessions[sessionID] = t
		}
	}
	if len(a.preparedSessions) > 0 {
		a.log.Info("prepared sessions restored",
			"component", "session", "count", len(a.preparedSessions))
	}
}

// savePreparedSessionsLocked persists the current preparedSessions
// map to disk via atomic temp+rename. Caller MUST hold a.mu.
// Failures log at Warn but do not return -- a failed persist still
// leaves correct in-memory state for the rest of this process's
// lifetime; the worst case is the same as pre-Wave-4 (lost on
// restart).
func (a *API) savePreparedSessionsLocked() {
	if a.configDir == "" {
		return
	}
	data, err := json.Marshal(a.preparedSessions)
	if err != nil {
		a.log.Warn("prepared sessions marshal failed",
			"component", "session", "err", err)
		return
	}
	if err := core.AtomicWriteFile(a.preparedSessionsPath(), data, 0o600); err != nil {
		a.log.Warn("prepared sessions persist failed",
			"component", "session", "err", err)
	}
}

// sweepPreparedSessions removes prepared-flag entries older than
// preparedSessionTTL. Called by the background sweeper goroutine.
func (a *API) sweepPreparedSessions() {
	cutoff := time.Now().Add(-preparedSessionTTL)
	a.preparedMu.Lock()
	defer a.preparedMu.Unlock()
	removed := 0
	for sessionID, t := range a.preparedSessions {
		if t.Before(cutoff) {
			delete(a.preparedSessions, sessionID)
			removed++
		}
	}
	if removed > 0 {
		a.log.Debug("prepared sessions sweep", "component", "session",
			"removed", removed, "remaining", len(a.preparedSessions))
		a.savePreparedSessionsLocked()
	}
}

// preparedSweeper runs sweepPreparedSessions on a ticker until ctx is
// cancelled.
func (a *API) preparedSweeper(ctx context.Context) {
	ticker := time.NewTicker(preparedSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sweepPreparedSessions()
		}
	}
}

// StartPreparedSweeper launches the background sweeper goroutine and
// stores its cancel function for shutdown. Idempotent -- if a sweeper
// is already running, return without starting a second one. A server
// with multiple entry points (Serve, StartHTTP) could otherwise leak
// goroutines with every additional start.
func (a *API) StartPreparedSweeper() {
	ctx, cancel := context.WithCancel(context.Background())
	a.preparedMu.Lock()
	if a.preparedSweepCancel != nil {
		a.preparedMu.Unlock()
		cancel()
		return
	}
	a.preparedSweepCancel = cancel
	a.preparedMu.Unlock()
	go a.preparedSweeper(ctx)
}

// consumeCompactionFlag checks for and deletes a PostCompact flag written by
// the hook at ~/.gramaton/hook-state/{client_session_id}.compacted. Returns
// the flag's timestamp if fresh (within compactionFlagTTL), or zero time
// otherwise. Single-shot: the flag is deleted whether fresh or stale so the
// nudge fires at most once per compaction.
func (a *API) consumeCompactionFlag(clientSessionID string) time.Time {
	if a.configDir == "" {
		return time.Time{}
	}
	// Defense in depth: only reached via SessionPrepare -> session lookup,
	// so clientSessionID is whatever was stored when the session was
	// created. SessionStart validates the character set, but older
	// sessions may predate that check -- refuse any ID that cannot be
	// safely joined into a filesystem path. Rejecting here also means a
	// manual call path that skipped SessionStart can never reach os.Remove
	// with attacker input.
	if validateClientSessionID(clientSessionID) != nil {
		return time.Time{}
	}
	flagPath := filepath.Join(a.configDir, "hook-state", clientSessionID+".compacted")
	data, err := os.ReadFile(flagPath)
	if err != nil {
		return time.Time{}
	}
	// Delete regardless of parse success -- never surface the same flag twice.
	_ = os.Remove(flagPath)

	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		a.log.Warn("compaction flag parse failed", "component", "session",
			"path", flagPath, "err", err)
		return time.Time{}
	}
	if time.Since(ts) > compactionFlagTTL {
		a.log.Debug("compaction flag stale, discarded", "component", "session",
			"client_session_id", clientSessionID, "age", time.Since(ts))
		return time.Time{}
	}
	return ts
}

// precompactUncapturedNudge is what the PreCompact hook leaves behind
// when it detects uncaptured segments: the count, when it warned, and
// the archive path where the raw transcript was preserved (empty if the
// archive step failed or the hook didn't receive a transcript_path).
type precompactUncapturedNudge struct {
	Count       int    `json:"count"`
	WarnedAt    string `json:"warned_at"`
	ArchivePath string `json:"archive_path"`
}

// consumePrecompactUncapturedFlag reads and deletes the PreCompact nudge
// file at ~/.gramaton/hook-state/{client_session_id}.precompact-uncaptured.
// Returns the nudge if fresh (within compactionFlagTTL), nil otherwise.
// Single-shot: the flag is deleted whether fresh or stale.
func (a *API) consumePrecompactUncapturedFlag(clientSessionID string) *precompactUncapturedNudge {
	if a.configDir == "" {
		return nil
	}
	// See consumeCompactionFlag for the rationale.
	if validateClientSessionID(clientSessionID) != nil {
		return nil
	}
	flagPath := filepath.Join(a.configDir, "hook-state", clientSessionID+".precompact-uncaptured")
	data, err := os.ReadFile(flagPath)
	if err != nil {
		return nil
	}
	_ = os.Remove(flagPath)

	var nudge precompactUncapturedNudge
	if err := json.Unmarshal(data, &nudge); err != nil {
		a.log.Warn("precompact-uncaptured flag parse failed", "component", "session",
			"path", flagPath, "err", err)
		return nil
	}
	ts, err := time.Parse(time.RFC3339, nudge.WarnedAt)
	if err != nil {
		a.log.Warn("precompact-uncaptured warned_at parse failed", "component", "session",
			"path", flagPath, "warned_at", nudge.WarnedAt, "err", err)
		return nil
	}
	if time.Since(ts) > compactionFlagTTL {
		a.log.Debug("precompact-uncaptured flag stale, discarded", "component", "session",
			"client_session_id", clientSessionID, "age", time.Since(ts))
		return nil
	}
	return &nudge
}

// SessionPrepare returns extraction instructions and current session state.
// Sets an in-memory prepared flag so commit can validate the two-phase flow.
func (a *API) SessionPrepare(ctx context.Context, sessionID string) (map[string]any, *APIError) {
	_ = ctx
	if sessionID == "" {
		return nil, ErrMissing("session_id is required")
	}

	// Snapshot everything we need from the engine under RLock, then drop
	// the lock before any disk I/O (hook-state flag files, extraction
	// prompt). Holding the engine lock across filesystem work throttles
	// every other request.
	a.engine.RLock()
	session, svcErr := a.isSession(sessionID)
	if svcErr != nil {
		a.engine.RUnlock()
		return nil, svcErr
	}
	sessionState := a.buildSessionResponse(sessionID, session)
	clientSessionID, _ := session.Properties.GetString("client_session_id")
	a.engine.RUnlock()

	// Set prepared flag (protected by mu since preparedSessions is not engine-locked).
	// Persist to disk so a restart between prepare and commit doesn't
	// permanently break the agent's flow.
	a.preparedMu.Lock()
	a.preparedSessions[sessionID] = time.Now()
	a.savePreparedSessionsLocked()
	a.preparedMu.Unlock()

	// Count segments for logging.
	segCount := 0
	if topics, ok := sessionState["topics"].([]map[string]any); ok {
		for _, t := range topics {
			if segs, ok := t["segments"].([]map[string]any); ok {
				segCount += len(segs)
			}
		}
	}

	a.log.Info("session prepared", "component", "session", "session_id", sessionID,
		"segment_count", segCount, "prepared_flag", true)

	prompt, promptHash := a.loadExtractionPrompt()
	a.log.Debug("prepare returning prompt", "component", "session",
		"session_id", sessionID, "prompt_hash", promptHash)

	resp := map[string]any{
		"instructions":  prompt,
		"session_state": sessionState,
	}

	// Surface hook-written nudges if present. The PostCompact flag fires
	// after context compaction; the PreCompact nudge fires when uncaptured
	// segments existed at the moment of the last compaction. They are
	// related but distinct -- both can be present and stack.
	var notes []string

	if compactedAt := a.consumeCompactionFlag(clientSessionID); !compactedAt.IsZero() {
		resp["recent_compaction"] = map[string]any{
			"at": compactedAt.Format(time.RFC3339),
		}
		notes = append(notes, "NOTE: your context was recently compacted. Review session state below for already-captured segments before extracting -- do not re-capture knowledge that is already committed.")
		a.log.Info("prepare: surfaced compaction nudge", "component", "session",
			"session_id", sessionID, "client_session_id", clientSessionID,
			"compacted_at", compactedAt.Format(time.RFC3339))
	}

	if nudge := a.consumePrecompactUncapturedFlag(clientSessionID); nudge != nil {
		pending := map[string]any{
			"count":     nudge.Count,
			"warned_at": nudge.WarnedAt,
		}
		if nudge.ArchivePath != "" {
			pending["archive_path"] = nudge.ArchivePath
		}
		resp["pending_uncaptured"] = pending

		note := fmt.Sprintf("NOTE: %d segment(s) were uncaptured at the last compaction.", nudge.Count)
		if nudge.ArchivePath != "" {
			note += fmt.Sprintf(" The raw transcript has been archived at %s -- decompress and read it if the session state below is missing expected knowledge.", nudge.ArchivePath)
		} else {
			note += " The raw transcript could not be archived at the time; knowledge from that compaction may be lost unless still in your context."
		}
		notes = append(notes, note)

		a.log.Info("prepare: surfaced precompact-uncaptured nudge", "component", "session",
			"session_id", sessionID, "client_session_id", clientSessionID,
			"count", nudge.Count, "archive_path", nudge.ArchivePath)
	}

	if len(notes) > 0 {
		resp["instructions"] = strings.Join(notes, "\n\n") + "\n\n" + prompt
	}

	return resp, nil
}

// CommitSegment is a single segment submitted via session_commit.
//
// PromoteToMemory implements the two-tier extraction model: when true
// (or unset), the segment becomes both a Session segment (BM25-indexed)
// and a Memory record (vector + BM25 indexed, full epistemic metadata,
// auto-supersession). When false, only the Session segment is created
// -- BM25-searchable but not vector-embedded, no Memory record, no
// extracted_as edge. Use false for exploration, dead ends, open
// questions, and other "valuable context" content that shouldn't
// pollute the Memory store's vector space.
type CommitSegment struct {
	Content         string   `json:"content"`
	TopicName       string   `json:"topic"`
	Temporality     string   `json:"temporality,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	KnowledgeType   string   `json:"knowledge_type,omitempty"`
	EpistemicStatus string   `json:"epistemic_status,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	SummaryShort    string   `json:"summary_short,omitempty"`
	PromoteToMemory *bool    `json:"promote_to_memory,omitempty"`
}

// shouldPromote returns true when the segment should be promoted to a
// Memory record. Default (nil) is true for backward compatibility with
// pre-two-tier callers; explicitly setting false makes it Session-only.
func (c CommitSegment) shouldPromote() bool {
	if c.PromoteToMemory == nil {
		return true
	}
	return *c.PromoteToMemory
}

// SessionCommit appends extracted segments to the session.
// Validates that prepare was called first. Creates new topics as needed.
// Phase 2: stores in Session only (no Memory records, no embedding).
func (a *API) SessionCommit(ctx context.Context, sessionID string, segments []CommitSegment) (map[string]any, *APIError) {
	if sessionID == "" {
		return nil, ErrMissing("session_id is required")
	}
	if len(segments) == 0 {
		return nil, ErrMissing("segments is required and must not be empty")
	}

	// Validate all segments before consuming the prepared flag so that a
	// malformed commit doesn't force the agent to re-prepare.
	maxContent := a.engine.Config().Limits.MaxContentLength
	maxSummary := MaxSummaryShort()
	for i, seg := range segments {
		if strings.TrimSpace(seg.Content) == "" {
			return nil, ErrInvalid(fmt.Sprintf("segment %d: content is required", i))
		}
		if strings.TrimSpace(seg.TopicName) == "" {
			return nil, ErrInvalid(fmt.Sprintf("segment %d: topic name is required", i))
		}
		if maxContent > 0 && len(seg.Content) > maxContent {
			return nil, ErrInvalid(fmt.Sprintf("segment %d: content exceeds maximum length", i))
		}
		if len(seg.TopicName) > MaxTopicLength {
			return nil, ErrInvalid(fmt.Sprintf("segment %d: topic name exceeds maximum length", i))
		}
		// Sanitize summary_short to strip LLM tool-use-format
		// leakage before length-checking. Mutate via the slice
		// index so the sanitized value is what downstream loops
		// (segment persistence, promote-to-memory) will see.
		origSeg := segments[i].SummaryShort
		segments[i].SummaryShort = sanitize.Field(origSeg)
		if err := sanitize.Validate(origSeg, segments[i].SummaryShort, fmt.Sprintf("segment %d: summary_short", i), maxSummary); err != nil {
			return nil, ErrInvalid(err.Error())
		}
		if err := validateFloat64Range("confidence", seg.Confidence, 0.0, 1.0); err != nil {
			return nil, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
		}
		if err := validateEnum("temporality", seg.Temporality, ValidTemporalities); err != nil {
			return nil, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
		}
		if err := validateEnum("knowledge_type", seg.KnowledgeType, ValidKnowledgeTypes); err != nil {
			return nil, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
		}
		if err := validateEnum("epistemic_status", seg.EpistemicStatus, ValidEpistemicStatuses); err != nil {
			return nil, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
		}
		if err := validateKeywords(seg.Keywords); err != nil {
			return nil, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
		}
	}

	// Check and consume prepared flag.
	a.preparedMu.Lock()
	_, prepared := a.preparedSessions[sessionID]
	if prepared {
		delete(a.preparedSessions, sessionID)
		a.savePreparedSessionsLocked()
	}
	a.preparedMu.Unlock()

	if !prepared {
		a.log.Warn("session commit rejected: prepare not called", "component", "session", "session_id", sessionID)
		return nil, ErrPrepareRequired("You must call gramaton_session_prepare first. Prepare returns extraction instructions and session state needed for high-quality knowledge extraction. Call prepare, follow its instructions, then call commit.")
	}

	start := time.Now()

	// Pre-embed only segments that will be promoted to Memory (outside lock).
	// Session-only segments don't need vectors -- they're BM25-only by design.
	embedVecs := make(map[int][]float32)
	if a.engine.Embedder() != nil {
		var promotedIdx []int
		var promotedTexts []string
		for i, seg := range segments {
			if !seg.shouldPromote() {
				continue
			}
			text := seg.SummaryShort
			if text == "" {
				text = seg.Content
				if len(text) > 1000 {
					text = text[:1000]
				}
			}
			promotedIdx = append(promotedIdx, i)
			promotedTexts = append(promotedTexts, text)
		}
		if len(promotedTexts) > 0 {
			vecs, err := a.engine.Embedder().Embed(ctx, promotedTexts)
			if err != nil {
				a.log.Warn("session commit: embedding failed, continuing without vectors",
					"component", "session", "session_id", sessionID, "err", err)
			} else {
				for j, idx := range promotedIdx {
					embedVecs[idx] = vecs[j]
				}
			}
		}
	}
	embedDur := time.Since(start)

	a.engine.Lock()
	defer a.engine.Unlock()

	if _, svcErr := a.isSession(sessionID); svcErr != nil {
		return nil, svcErr
	}

	// Build topic name -> ID map from existing topics.
	topicMap := make(map[string]string) // topic name -> node ID
	for _, t := range a.sessionTopics(sessionID) {
		if name, ok := t.Properties.GetString("topic_name"); ok {
			topicMap[name] = t.ID
		}
	}

	topicsCreated := 0
	segmentsAdded := 0
	sessionOnlySegments := 0
	memoryRecordsCreated := 0
	edgesCreated := 0
	var superseded []map[string]any

	// Track IDs created in this batch for auto-supersession exclusion.
	batchIDs := make(map[string]struct{}, len(segments))

	for i, seg := range segments {
		// Find or create the topic.
		topicID, exists := topicMap[seg.TopicName]
		if !exists {
			now := time.Now().UTC()
			props := graph.Properties{
				"knowledge_type": graph.StringProperty("topic"),
				"topic_name":     graph.StringProperty(seg.TopicName),
				"created_at":     graph.TimestampProperty(now),
			}
			topicNode := a.engine.Graph().AddNode(props)
			a.engine.IndexNode(topicNode.ID, "", nil)

			if _, err := a.engine.Graph().AddEdge(topicNode.ID, sessionID, "topic_of", 1.0, nil); err != nil {
				a.log.Warn("topic_of edge create failed", "component", "session",
					"session_id", sessionID, "topic_id", topicNode.ID, "err", err)
				return nil, ErrInternal("failed to link topic to session")
			}
			topicID = topicNode.ID
			topicMap[seg.TopicName] = topicID
			topicsCreated++

			a.log.Info("topic created", "component", "session", "session_id", sessionID,
				"topic_id", topicID, "topic_name", seg.TopicName)
		}

		// 1. Create the Session segment node.
		now := time.Now().UTC()
		segProps := graph.Properties{
			"knowledge_type": graph.StringProperty("segment"),
			"content":        graph.StringProperty(seg.Content),
			"created_at":     graph.TimestampProperty(now),
		}
		segNode := a.engine.Graph().AddNode(segProps)
		// BM25-index the segment content (no vector -- BM25-only per B1).
		a.engine.IndexNode(segNode.ID, seg.Content, nil)

		if _, err := a.engine.Graph().AddEdge(segNode.ID, topicID, "segment_of", 1.0, nil); err != nil {
			a.log.Warn("segment_of edge create failed", "component", "session",
				"session_id", sessionID, "topic_id", topicID, "err", err)
			return nil, ErrInternal("failed to link segment to topic")
		}
		segmentsAdded++

		// Two-tier extraction: skip Memory promotion when the LLM marked
		// the segment as Session-only (exploration, dead ends, open
		// questions). Session segment is BM25-indexed above and remains
		// searchable; we just don't pollute the vector space or pay the
		// embed/storage cost for content that isn't decision-grade.
		if !seg.shouldPromote() {
			sessionOnlySegments++
			a.log.Info("session-only segment", "component", "session",
				"session_id", sessionID, "segment_id", segNode.ID,
				"topic_id", topicID, "content_len", len(seg.Content))
			continue
		}

		// 2. Create the Memory record with segment content + metadata.
		memProps := graph.Properties{
			"content_full":      graph.StringProperty(seg.Content),
			"processing_status": graph.StringProperty("processed"),
			"created_at":        graph.TimestampProperty(now),
			"access_count":      graph.Int64Property(0),
		}
		if seg.Temporality != "" {
			memProps["temporality"] = graph.StringProperty(seg.Temporality)
		}
		if seg.Confidence != nil {
			memProps["confidence"] = graph.Float64Property(*seg.Confidence)
		}
		if seg.KnowledgeType != "" {
			memProps["knowledge_type"] = graph.StringProperty(seg.KnowledgeType)
		} else {
			memProps["knowledge_type"] = graph.StringProperty("episodic")
		}
		if seg.EpistemicStatus != "" {
			memProps["epistemic_status"] = graph.StringProperty(seg.EpistemicStatus)
		}
		if len(seg.Keywords) > 0 {
			memProps["content_keywords"] = graph.StringListProperty(seg.Keywords)
		}
		if seg.SummaryShort != "" {
			memProps["content_short"] = graph.StringProperty(seg.SummaryShort)
		}

		memNode := a.engine.Graph().AddNode(memProps)

		// 3. Embed the Memory record (vector + BM25). embedVecs is keyed
		// by segment index; nil means embedding wasn't computed (no
		// embedder configured or embed call failed).
		vec := embedVecs[i]
		bm25Text := seg.Content
		if len(seg.Keywords) > 0 {
			bm25Text += " " + strings.Join(seg.Keywords, " ")
		}
		a.engine.IndexNode(memNode.ID, bm25Text, vec)

		batchIDs[memNode.ID] = struct{}{}
		memoryRecordsCreated++

		a.log.Info("memory record created", "component", "session",
			"memory_record_id", memNode.ID, "session_id", sessionID,
			"segment_id", segNode.ID, "content_len", len(seg.Content),
			"has_vector", vec != nil)

		// 4. Create edge: segment --extracted_as--> memory_record.
		if _, err := a.engine.Graph().AddEdge(segNode.ID, memNode.ID, "extracted_as", 1.0, nil); err != nil {
			a.log.Warn("failed to create extracted_as edge", "component", "session",
				"segment_id", segNode.ID, "memory_id", memNode.ID, "err", err)
		} else {
			edgesCreated++
			a.log.Debug("edge created", "component", "session",
				"source", segNode.ID, "target", memNode.ID, "type", "extracted_as")
		}

		// 5. Update segment captured_as with Memory record ID.
		a.engine.SetProp(segNode.ID, "captured_as", graph.StringProperty(memNode.ID))
		a.engine.SetProp(segNode.ID, "captured_at", graph.TimestampProperty(now))

		// 6. Auto-supersession on the Memory record (skip within-batch).
		if dupID, sim := a.engine.CheckDedup(memNode.ID); dupID != "" {
			if _, inBatch := batchIDs[dupID]; inBatch {
				a.log.Debug("auto-supersession skipped: within-commit batch",
					"component", "session", "new_id", memNode.ID, "dup_id", dupID)
				continue
			}
			cfg := a.engine.Config()
			// Default "supersede" semantics: mark the older record historical
			// and link the new record via a supersedes edge. See D37.
			// Session commit does not honor "reject" -- each segment is a
			// deliberate commit, not a capture the caller can cancel.
			if cfg.Dedup.Action != "reject" {
				oldNode, _ := a.engine.Graph().GetNode(dupID)
				if oldNode != nil {
					_, alreadyHistorical := oldNode.Properties.GetTimestamp("valid_until")
					if !alreadyHistorical {
						a.engine.SetProp(dupID, "valid_until", graph.TimestampProperty(now))
						a.engine.SetProp(dupID, "resolution", graph.StringProperty("superseded"))
						a.engine.SetProp(dupID, "resolved_at", graph.TimestampProperty(now))
						if e, err := a.engine.Graph().AddEdge(memNode.ID, dupID, "supersedes", sim, nil); err == nil {
							summary := ""
							if v, ok := oldNode.Properties.GetString("content_short"); ok {
								summary = v
							}
							superseded = append(superseded, map[string]any{
								"id": dupID, "summary": summary,
								"similarity": sim, "edge_id": e.ID,
							})
							a.log.Info("auto-supersession triggered", "component", "session",
								"new_record_id", memNode.ID, "superseded_id", dupID,
								"similarity", fmt.Sprintf("%.3f", sim))
						}
					}
				}
			}
		}
	}

	if _, err := a.engine.Save("session_commit"); err != nil {
		a.log.Warn("session commit save failed", "component", "session",
			"session_id", sessionID, "err", err)
		return nil, ErrInternal("failed to save session commit")
	}

	dur := time.Since(start)
	a.log.Info("session commit", "component", "session", "session_id", sessionID,
		"segments_submitted", len(segments), "segments_added", segmentsAdded,
		"session_only_segments", sessionOnlySegments,
		"memory_records_created", memoryRecordsCreated, "edges_created", edgesCreated,
		"embed_ms", embedDur.Milliseconds(), "duration", dur)

	resp := map[string]any{
		"session_id":             sessionID,
		"segments_added":         segmentsAdded,
		"session_only_segments":  sessionOnlySegments,
		"topics_created":         topicsCreated,
		"memory_records_created": memoryRecordsCreated,
		"edges_created":          edgesCreated,
	}
	if len(superseded) > 0 {
		resp["superseded"] = superseded
	}
	return resp, nil
}

// SessionArchive compresses a conversation transcript and stores it
// as a gzip file referenced from the Session node. The archive is NOT indexed
// or searchable -- it's a break-glass backup of the raw conversation.
func (a *API) SessionArchive(ctx context.Context, sessionID string, sourcePath string) (map[string]any, *APIError) {
	_ = ctx
	if sessionID == "" {
		return nil, ErrMissing("session_id is required")
	}
	if sourcePath == "" {
		return nil, ErrMissing("source file path is required")
	}

	start := time.Now()

	// Read source file.
	sourceData, err := os.ReadFile(sourcePath)
	if err != nil {
		a.log.Warn("archive: failed to read source", "component", "session",
			"session_id", sessionID, "err", err)
		return nil, ErrInvalid("cannot read source file")
	}
	originalSize := len(sourceData)

	// Determine archive directory.
	archiveDir := filepath.Join(a.configDir, "archives")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		a.log.Warn("archive: failed to create directory", "component", "session", "err", err)
		return nil, ErrInternal("failed to create archive directory")
	}

	// Write compressed archive via atomic temp file.
	archiveName := fmt.Sprintf("%s.gz", sessionID)
	archivePath := filepath.Join(archiveDir, archiveName)
	tmpPath := archivePath + ".tmp"

	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		a.log.Warn("archive: temp file create failed", "component", "session",
			"session_id", sessionID, "err", err)
		return nil, ErrInternal("failed to create archive temp file")
	}

	gzWriter := gzip.NewWriter(tmpFile)
	if _, err := gzWriter.Write(sourceData); err != nil {
		gzWriter.Close()
		tmpFile.Close()
		os.Remove(tmpPath)
		a.log.Warn("archive: gzip write failed", "component", "session",
			"session_id", sessionID, "err", err)
		return nil, ErrInternal("failed to compress archive")
	}
	if err := gzWriter.Close(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		a.log.Warn("archive: gzip close failed", "component", "session",
			"session_id", sessionID, "err", err)
		return nil, ErrInternal("failed to finalize compression")
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		a.log.Warn("archive: file close failed", "component", "session",
			"session_id", sessionID, "err", err)
		return nil, ErrInternal("failed to close archive file")
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, archivePath); err != nil {
		os.Remove(tmpPath)
		a.log.Warn("archive: rename failed", "component", "session",
			"session_id", sessionID, "err", err)
		return nil, ErrInternal("failed to rename archive")
	}

	// Get compressed size.
	info, err := os.Stat(archivePath)
	if err != nil {
		return nil, ErrInternal("failed to stat archive")
	}
	compressedSize := info.Size()

	// Update Session node with archive reference.
	a.engine.Lock()
	defer a.engine.Unlock()

	if _, svcErr := a.isSession(sessionID); svcErr != nil {
		return nil, svcErr
	}

	now := time.Now().UTC()
	a.engine.SetProp(sessionID, "archive_path", graph.StringProperty(archivePath))
	a.engine.SetProp(sessionID, "archive_size", graph.Int64Property(compressedSize))
	a.engine.SetProp(sessionID, "archive_original_size", graph.Int64Property(int64(originalSize)))
	a.engine.SetProp(sessionID, "archived_at", graph.TimestampProperty(now))

	if _, err := a.engine.Save("session_archive"); err != nil {
		a.log.Warn("archive save failed", "component", "session",
			"session_id", sessionID, "err", err)
		return nil, ErrInternal("failed to save archive metadata")
	}

	dur := time.Since(start)
	ratio := float64(compressedSize) / float64(originalSize)
	a.log.Info("archive created", "component", "session",
		"session_id", sessionID, "archive_path", archivePath,
		"original_size", originalSize, "compressed_size", compressedSize,
		"ratio", fmt.Sprintf("%.2f", ratio), "duration", dur)

	return map[string]any{
		"session_id":      sessionID,
		"archive_path":    archivePath,
		"original_size":   originalSize,
		"compressed_size": compressedSize,
		"compression_ratio": fmt.Sprintf("%.2f", ratio),
	}, nil
}

