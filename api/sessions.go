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
	"github.com/gramaton-ai/gramaton/similarity"
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
const SessionStartDescription = `Start or resume a knowledge session. On fresh start, creates a new session. On resume (--continue), creates a new session chained to the previous one. Returns the active session.`

// SessionGetDescription is the MCP tool description for
// gramaton_session_get.
const SessionGetDescription = `Get the current session state including all topics and segments. Use to review what has been saved so far.`

// SessionPrepareDescription is the MCP tool description for
// gramaton_session_prepare. Leads with "eagerly throughout" language
// to counter the prior-version regression where agents self-triggered
// far less often than the old gramaton_observe tool did.
const SessionPrepareDescription = `Extract save-worthy knowledge from the ongoing conversation. Returns extraction instructions and session state. Call this EAGERLY throughout a conversation, not just at the end: immediately after a decision lands, a rule or principle is articulated, a task completes, or the user pivots topics. Also call before context compaction, when the user signals "save the session" or "we're done", and at least every ~10 substantive turns even without an explicit trigger. Bundling saves at session end is an anti-pattern -- knowledge from early in the conversation becomes harder to reconstruct as context accumulates. You must follow the returned instructions before calling gramaton_session_save.

For saving a single specific record the user handed you, use gramaton_save instead.`

// SessionSaveDescription is the MCP tool description for
// gramaton_session_save.
const SessionSaveDescription = `Submit extracted knowledge segments to finalize a session save. Triggered by save-worthy session events: a work boundary lands (task complete, topic pivot, user says "we're done"), compaction is imminent or just happened, or the user explicitly asks to save the session/conversation. IMPORTANT: You must call gramaton_session_prepare first and follow its instructions. Do not call this tool directly -- the preparation step provides required context for high-quality extraction.

For saving a single specific record the user handed you, use gramaton_save instead.`

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
//
// When leanPreBoundary is true, segments older than the session's
// last_saved_at watermark are returned without their `content` field --
// the agent has already extracted them in a prior save cycle and only
// needs id + topic + summary_short + timestamps to recognise them as
// already-saved. Segments at or after the boundary, and all segments
// when last_saved_at is unset, return the full shape.
func (a *API) buildSessionResponse(sessionID string, session *graph.Node, leanPreBoundary bool) map[string]any {
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
	lastSaved, hasLastSaved := session.Properties.GetTimestamp("last_saved_at")
	if hasLastSaved {
		resp["last_saved_at"] = lastSaved.Format(time.RFC3339)
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
			segCreated, hasCreated := seg.Properties.GetTimestamp("created_at")
			preBoundary := leanPreBoundary && hasLastSaved && hasCreated && segCreated.Before(lastSaved)
			if c, ok := seg.Properties.GetString("content"); ok && !preBoundary {
				segResp["content"] = c
			}
			if cs, ok := seg.Properties.GetString("content_short"); ok {
				segResp["summary_short"] = cs
			}
			if ca, ok := seg.Properties.GetString("captured_as"); ok {
				segResp["captured_as"] = ca
			}
			if ct, ok := seg.Properties.GetTimestamp("captured_at"); ok {
				segResp["captured_at"] = ct.Format(time.RFC3339)
			}
			// Unresolved held promotion: surfaced so every prepare
			// re-presents it until the agent resolves it.
			if held, _ := seg.Properties.GetBool("promotion_held"); held {
				heldInfo := map[string]any{}
				if target, ok := seg.Properties.GetString("promotion_hold_target"); ok {
					heldInfo["similar_to"] = target
				}
				if sim, ok := seg.Properties.GetFloat64("promotion_hold_similarity"); ok {
					heldInfo["similarity"] = sim
				}
				segResp["promotion_held"] = heldInfo
			}
			if hasCreated {
				segResp["created_at"] = segCreated.Format(time.RFC3339)
			}
			segList = append(segList, segResp)
		}
		topicResp["segments"] = segList
		topicList = append(topicList, topicResp)
	}
	resp["topics"] = topicList
	return resp
}

// saveBoundaryMarker is the bracketed string an LLM substring-scans
// its own conversation history for, to find where its most recent
// successful session_save committed. Format is deliberately compact
// and grep-friendly: timestamp only, no count or session id (those
// live in the surrounding JSON object if the consumer needs them).
//
// The bracketed form was chosen to minimise collision risk with prose
// the agent might output; "gramaton-save-boundary" is unique enough
// that a substring search will not false-positive on normal English.
func saveBoundaryMarker(t time.Time) string {
	return fmt.Sprintf("[gramaton-save-boundary T=%s]", t.UTC().Format(time.RFC3339))
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
	if apiErr := a.rejectIfReadOnly("session_start"); apiErr != nil {
		return nil, apiErr
	}
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
		resp := a.buildSessionResponse(latestID, session, false)
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
	// Author attribution (see api/save.go for the stamping contract).
	if author := a.engine.Config().Author.String(); author != "" {
		props["author"] = graph.StringProperty(author)
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

	if _, err := a.engine.Save("session_create", graph.CommitAction{
		Kind: graph.ActionSessionCreate, RecordID: n.ID,
	}); err != nil {
		a.log.Warn("session save failed", "component", "session", "err", err)
		return nil, ErrInternal("failed to save session")
	}

	a.log.Info("session created", "component", "session",
		"session_id", n.ID, "client_session_id", clientSessionID,
		"source", source, "chained_to", previousSessionID)

	resp := a.buildSessionResponse(n.ID, n, false)
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

	return a.buildSessionResponse(sessionID, session, false), nil
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
	// Prepare is the entry to the two-phase write flow (it arms the
	// prepared flag that session_save requires). Rejecting here tells
	// the agent the store is frozen BEFORE it spends effort extracting
	// segments that save would then refuse.
	if apiErr := a.rejectIfReadOnly("session_prepare"); apiErr != nil {
		return nil, apiErr
	}
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
	sessionState := a.buildSessionResponse(sessionID, session, true)
	clientSessionID, _ := session.Properties.GetString("client_session_id")
	heldPromotions := a.heldPromotionsForSession(sessionID)
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

	// Re-present unresolved held promotions on every prepare: a
	// context-poor agent at hold time can defer without silent loss,
	// because the next prepare keeps asking until each hold is
	// resolved via gramaton_session_resolve_held.
	if len(heldPromotions) > 0 {
		resp["held_promotions"] = heldPromotions
		notes = append(notes, fmt.Sprintf(
			"NOTE: %d earlier segment(s) have HELD Memory promotions -- each closely matched an existing record and was not promoted (see held_promotions). Resolve each via gramaton_session_resolve_held: if the segment revises the similar record, gramaton_update that record first, then resolve with action=update_target; if genuinely distinct, resolve with action=allow_similar to promote it.",
			len(heldPromotions)))
	}

	if len(notes) > 0 {
		resp["instructions"] = strings.Join(notes, "\n\n") + "\n\n" + prompt
	}

	return resp, nil
}

// heldPromotionsForSession lists segments of this session whose
// Memory promotion is still held. Caller must hold at least a read
// lock.
func (a *API) heldPromotionsForSession(sessionID string) []map[string]any {
	var out []map[string]any
	for _, t := range a.sessionTopics(sessionID) {
		topicName, _ := t.Properties.GetString("topic_name")
		for _, seg := range a.topicSegments(t.ID) {
			if held, _ := seg.Properties.GetBool("promotion_held"); !held {
				continue
			}
			entry := map[string]any{
				"segment_id": seg.ID,
				"topic":      topicName,
			}
			if target, ok := seg.Properties.GetString("promotion_hold_target"); ok {
				entry["similar_to"] = target
			}
			if sim, ok := seg.Properties.GetFloat64("promotion_hold_similarity"); ok {
				entry["similarity"] = sim
			}
			out = append(out, entry)
		}
	}
	return out
}

// SaveSegment is a single segment submitted via session_save.
//
// PromoteToMemory implements the two-tier extraction model: when true
// (or unset), the segment becomes both a Session segment (BM25-indexed)
// and a Memory record (vector + BM25 indexed, full epistemic metadata,
// auto-supersession). When false, only the Session segment is created
// -- BM25-searchable but not vector-embedded, no Memory record, no
// extracted_as edge. Use false for exploration, dead ends, open
// questions, and other "valuable context" content that shouldn't
// pollute the Memory store's vector space.
type SaveSegment struct {
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
func (c SaveSegment) shouldPromote() bool {
	if c.PromoteToMemory == nil {
		return true
	}
	return *c.PromoteToMemory
}

// SessionSave appends extracted segments to the session.
// Validates that prepare was called first. Creates new topics as needed.
// Phase 2: stores in Session only (no Memory records, no embedding).
// allowSimilar disables the save-guard promotion holds for the whole
// commit -- the bulk-ingestion escape (benchmark loads, migrations);
// never a standing default for interactive extraction.
func (a *API) SessionSave(ctx context.Context, sessionID string, segments []SaveSegment, allowSimilar bool) (SessionSaveResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("session_save"); apiErr != nil {
		return SessionSaveResponse{}, apiErr
	}
	if sessionID == "" {
		return SessionSaveResponse{}, ErrMissing("session_id is required")
	}
	if len(segments) == 0 {
		return SessionSaveResponse{}, ErrMissing("segments is required and must not be empty")
	}

	// Validate all segments before consuming the prepared flag so that a
	// malformed commit doesn't force the agent to re-prepare.
	maxContent := a.engine.Config().Limits.MaxContentLength
	maxSummary := MaxSummaryShort()
	for i, seg := range segments {
		if strings.TrimSpace(seg.Content) == "" {
			return SessionSaveResponse{}, ErrInvalid(fmt.Sprintf("segment %d: content is required", i))
		}
		if strings.TrimSpace(seg.TopicName) == "" {
			return SessionSaveResponse{}, ErrInvalid(fmt.Sprintf("segment %d: topic name is required", i))
		}
		if maxContent > 0 && len(seg.Content) > maxContent {
			return SessionSaveResponse{}, ErrInvalid(fmt.Sprintf("segment %d: content exceeds maximum length", i))
		}
		if len(seg.TopicName) > MaxTopicLength {
			return SessionSaveResponse{}, ErrInvalid(fmt.Sprintf("segment %d: topic name exceeds maximum length", i))
		}
		// Sanitize summary_short to strip LLM tool-use-format
		// leakage before length-checking. Mutate via the slice
		// index so the sanitized value is what downstream loops
		// (segment persistence, promote-to-memory) will see.
		origSeg := segments[i].SummaryShort
		segments[i].SummaryShort = sanitize.Field(origSeg)
		if err := sanitize.Validate(origSeg, segments[i].SummaryShort, fmt.Sprintf("segment %d: summary_short", i), maxSummary); err != nil {
			return SessionSaveResponse{}, ErrInvalid(err.Error())
		}
		if err := validateFloat64Range("confidence", seg.Confidence, 0.0, 1.0); err != nil {
			return SessionSaveResponse{}, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
		}
		if err := validateEnum("temporality", seg.Temporality, ValidTemporalities); err != nil {
			return SessionSaveResponse{}, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
		}
		if err := validateEnum("knowledge_type", seg.KnowledgeType, ValidKnowledgeTypes); err != nil {
			return SessionSaveResponse{}, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
		}
		if err := validateEnum("epistemic_status", seg.EpistemicStatus, ValidEpistemicStatuses); err != nil {
			return SessionSaveResponse{}, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
		}
		if err := validateKeywords(seg.Keywords); err != nil {
			return SessionSaveResponse{}, ErrInvalid(fmt.Sprintf("segment %d: %s", i, err.Error()))
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
		return SessionSaveResponse{}, ErrPrepareRequired("You must call gramaton_session_prepare first. Prepare returns extraction instructions and session state needed for high-quality knowledge extraction. Call prepare, follow its instructions, then call commit.")
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

	// Save-guard scans off-lock for promoted segments: store scan per
	// segment plus the within-commit sibling pass (two similar
	// segments in one commit are invisible to the index scan; the
	// LATER one's promotion holds, naming the earlier one's Memory
	// record). The write-seq snapshot anchors the under-lock delta
	// re-scan. Skipped wholesale under allowSimilar (bulk ingestion).
	segStoreHold := make(map[int]*similarity.Match)
	segSiblingOf := make(map[int]int)
	var scanSeq uint64
	if !allowSimilar && len(embedVecs) > 0 {
		guardCfg := a.engine.Config().SaveGuard
		a.engine.RLock()
		scanSeq = a.engine.WriteSeq()
		for i, seg := range segments {
			if vec, ok := embedVecs[i]; ok && seg.shouldPromote() {
				if out := a.engine.ScanSimilarVec(vec, seg.Content); out.Hold != nil {
					segStoreHold[i] = out.Hold
				}
			}
		}
		a.engine.RUnlock()
		for i := range segments {
			if _, held := segStoreHold[i]; held || embedVecs[i] == nil || !segments[i].shouldPromote() {
				continue
			}
			for j := 0; j < i; j++ {
				if _, heldJ := segStoreHold[j]; heldJ || embedVecs[j] == nil || !segments[j].shouldPromote() {
					continue
				}
				if _, holds := similarity.SiblingMatch(guardCfg,
					embedVecs[i], segments[i].Content,
					embedVecs[j], segments[j].Content); holds {
					segSiblingOf[i] = j
					break
				}
			}
		}
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	if _, svcErr := a.isSession(sessionID); svcErr != nil {
		return SessionSaveResponse{}, svcErr
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
	var heldPromotions []SessionHeldPromotion

	// Author attribution: composed once for the whole commit; stamped
	// on topic, segment, and promoted Memory nodes below (see
	// api/save.go for the stamping contract).
	author := a.engine.Config().Author.String()

	// Memory record created per segment index, for sibling-hold
	// resolution (a later similar segment holds naming this record).
	promotedByIdx := make(map[int]string, len(segments))

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
			if author != "" {
				props["author"] = graph.StringProperty(author)
			}
			topicNode := a.engine.Graph().AddNode(props)
			a.engine.IndexNode(topicNode.ID, "", nil)

			if _, err := a.engine.Graph().AddEdge(topicNode.ID, sessionID, "topic_of", 1.0, nil); err != nil {
				a.log.Warn("topic_of edge create failed", "component", "session",
					"session_id", sessionID, "topic_id", topicNode.ID, "err", err)
				return SessionSaveResponse{}, ErrInternal("failed to link topic to session")
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
		if author != "" {
			segProps["author"] = graph.StringProperty(author)
		}
		// Persist summary_short on the segment too (not just on the
		// promoted Memory record). The lean-state branch in
		// buildSessionResponse drops `content` for pre-boundary
		// segments and surfaces summary_short as the dedup anchor;
		// without it on the segment, lean state would lose the
		// semantic signal the LLM uses to recognise already-saved work.
		if seg.SummaryShort != "" {
			segProps["content_short"] = graph.StringProperty(seg.SummaryShort)
		}
		segNode := a.engine.Graph().AddNode(segProps)
		// BM25-index the segment content (no vector -- BM25-only per B1).
		a.engine.IndexNode(segNode.ID, seg.Content, nil)

		if _, err := a.engine.Graph().AddEdge(segNode.ID, topicID, "segment_of", 1.0, nil); err != nil {
			a.log.Warn("segment_of edge create failed", "component", "session",
				"session_id", sessionID, "topic_id", topicID, "err", err)
			return SessionSaveResponse{}, ErrInternal("failed to link segment to topic")
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

		// Save-guard hold on the Memory promotion. The segment above
		// always lands (Sessions is append-only); only the Memory
		// record is withheld. Hold state persists on the segment node
		// so the next session_prepare re-presents it until resolved
		// via gramaton_session_resolve_held -- the extraction agent at
		// its lowest-context moment can defer without silent loss.
		if !allowSimilar {
			hold := segStoreHold[i]
			if hold == nil {
				if vec, ok := embedVecs[i]; ok {
					if m, found, _ := a.engine.SimilarInDelta(scanSeq, vec, seg.Content); found {
						held := m
						hold = &held
					}
				}
			}
			if hold == nil {
				if j, ok := segSiblingOf[i]; ok && promotedByIdx[j] != "" {
					sim, _ := similarity.SiblingMatch(a.engine.Config().SaveGuard,
						embedVecs[i], seg.Content, embedVecs[j], segments[j].Content)
					hold = &similarity.Match{NodeID: promotedByIdx[j], Similarity: sim}
				}
			}
			if hold != nil {
				if hs := a.buildHeldSimilar(hold); hs != nil {
					holdMeta, merr := json.Marshal(sessionHoldMeta{
						Temporality:     seg.Temporality,
						Confidence:      seg.Confidence,
						KnowledgeType:   seg.KnowledgeType,
						EpistemicStatus: seg.EpistemicStatus,
						Keywords:        seg.Keywords,
						SummaryShort:    seg.SummaryShort,
					})
					if merr != nil {
						holdMeta = []byte("{}")
					}
					a.engine.SetProp(segNode.ID, "promotion_held", graph.BoolProperty(true))
					a.engine.SetProp(segNode.ID, "promotion_hold_target", graph.StringProperty(hs.ID))
					a.engine.SetProp(segNode.ID, "promotion_hold_similarity", graph.Float64Property(hs.Similarity))
					a.engine.SetProp(segNode.ID, "promotion_hold_meta", graph.StringProperty(string(holdMeta)))
					heldPromotions = append(heldPromotions, SessionHeldPromotion{
						SegmentID: segNode.ID,
						Topic:     seg.TopicName,
						Held:      hs,
					})
					a.log.Info("session promotion held: similar record",
						"component", "session", "session_id", sessionID,
						"segment_id", segNode.ID, "similar_to", hs.ID,
						"similarity", fmt.Sprintf("%.3f", hs.Similarity))
					continue
				}
			}
		}

		// 2. Create the Memory record with segment content + metadata.
		memProps := graph.Properties{
			"content_full":      graph.StringProperty(seg.Content),
			"processing_status": graph.StringProperty("processed"),
			"created_at":        graph.TimestampProperty(now),
			"access_count":      graph.Int64Property(0),
		}
		if author != "" {
			memProps["author"] = graph.StringProperty(author)
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
		if vec != nil {
			// Feed the save-guard delta re-scan ring: a concurrent
			// save whose off-lock scan predates this commit must
			// still see the promoted record under its write lock.
			a.engine.NoteRecentWrite(memNode.ID, vec)
		}

		// Mark the embedding model on success so gramaton_reembed
		// doesn't re-pay for this record on every invocation. IndexNode
		// only sets `embedding_full` from the vec; without this write,
		// every successful session-commit promotion would land in
		// reembed's candidate set forever (selection at api/reembed.go
		// treats missing/different embedding_model as "needs reembed").
		if vec != nil && a.engine.Embedder() != nil {
			a.engine.SetProp(memNode.ID, "embedding_model", graph.StringProperty(a.engine.Embedder().ModelID()))
		}

		promotedByIdx[i] = memNode.ID
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
	}

	// Advance the watermark on the session node. Written inside the
	// same engine write lock as the segment creates so the commit is
	// atomic: either the agent's next prepare sees the new boundary
	// AND all this round's segments, or none of it.
	boundaryTime := time.Now().UTC()
	a.engine.SetProp(sessionID, "last_saved_at", graph.TimestampProperty(boundaryTime))

	if _, err := a.engine.Save("session_save", graph.CommitAction{
		Kind: graph.ActionSessionSave, RecordID: sessionID,
	}); err != nil {
		a.log.Warn("session commit save failed", "component", "session",
			"session_id", sessionID, "err", err)
		return SessionSaveResponse{}, ErrInternal("failed to save session commit")
	}

	dur := time.Since(start)
	a.log.Info("session commit", "component", "session", "session_id", sessionID,
		"segments_submitted", len(segments), "segments_added", segmentsAdded,
		"session_only_segments", sessionOnlySegments,
		"memory_records_created", memoryRecordsCreated, "edges_created", edgesCreated,
		"embed_ms", embedDur.Milliseconds(), "duration", dur)

	resp := SessionSaveResponse{
		SessionID:            sessionID,
		SegmentsAdded:        segmentsAdded,
		SessionOnlySegments:  sessionOnlySegments,
		TopicsCreated:        topicsCreated,
		MemoryRecordsCreated: memoryRecordsCreated,
		EdgesCreated:         edgesCreated,
		Boundary: &SaveBoundary{
			Marker:    saveBoundaryMarker(boundaryTime),
			Timestamp: boundaryTime.Format(time.RFC3339),
			SessionID: sessionID,
		},
	}
	if len(heldPromotions) > 0 {
		resp.Held = heldPromotions
	}
	return resp, nil
}

// sessionHoldMeta is the promotion metadata persisted (JSON-encoded)
// on a segment whose Memory promotion was held, so a later
// gramaton_session_resolve_held can build the Memory record
// faithfully. The segment node itself carries content and
// content_short; this carries the rest of the SaveSegment shape.
type sessionHoldMeta struct {
	Temporality     string   `json:"temporality,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	KnowledgeType   string   `json:"knowledge_type,omitempty"`
	EpistemicStatus string   `json:"epistemic_status,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	SummaryShort    string   `json:"summary_short,omitempty"`
}

// HeldResolution is one resolution submitted via
// gramaton_session_resolve_held.
type HeldResolution struct {
	SegmentID string `json:"segment_id" jsonschema:"segment whose held promotion to resolve"`
	Action    string `json:"action" jsonschema:"update_target (the similar record has been revised with this segment's knowledge -- wire the segment's provenance to it; no new record) or allow_similar (the segment is genuinely distinct -- promote it now)"`
	TargetID  string `json:"target_id,omitempty" jsonschema:"for update_target: the revised record; defaults to the record the hold named"`
}

// HeldResolutionResult reports one applied resolution.
type HeldResolutionResult struct {
	SegmentID      string `json:"segment_id"`
	Action         string `json:"action"`
	MemoryRecordID string `json:"memory_record_id,omitempty"`
	TargetID       string `json:"target_id,omitempty"`
}

// SessionResolveHeldResponse is the output of SessionResolveHeld.
type SessionResolveHeldResponse struct {
	SessionID string                 `json:"session_id"`
	Resolved  []HeldResolutionResult `json:"resolved"`
}

// SessionResolveHeldDescription is shared by HTTP, MCP, and CLI proxy.
const SessionResolveHeldDescription = `Resolve held Memory promotions from an earlier gramaton_session_save. A hold means a segment closely matched an existing record and was not promoted; the segment itself is safely stored. Two actions per segment: update_target -- you have ALREADY revised the similar record (gramaton_update) with this segment's knowledge; the server wires the segment's provenance to that record and no new record is created. allow_similar -- the segment is genuinely distinct; it is promoted to a Memory record now. Unresolved holds are re-presented by every gramaton_session_prepare.`

// SessionResolveHeld applies resolutions for held promotions. All
// resolutions are validated before any mutation; the whole call
// commits atomically or returns an error having changed nothing.
func (a *API) SessionResolveHeld(ctx context.Context, sessionID string, resolutions []HeldResolution) (SessionResolveHeldResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("session_resolve_held"); apiErr != nil {
		return SessionResolveHeldResponse{}, apiErr
	}
	if sessionID == "" {
		return SessionResolveHeldResponse{}, ErrMissing("session_id is required")
	}
	if len(resolutions) == 0 {
		return SessionResolveHeldResponse{}, ErrMissing("resolutions is required and must not be empty")
	}
	if len(resolutions) > MaxHeldResolutions {
		return SessionResolveHeldResponse{}, ErrInvalid(fmt.Sprintf("at most %d resolutions per call", MaxHeldResolutions))
	}
	for i, r := range resolutions {
		if r.SegmentID == "" {
			return SessionResolveHeldResponse{}, ErrInvalid(fmt.Sprintf("resolution %d: segment_id is required", i))
		}
		if r.Action != "update_target" && r.Action != "allow_similar" {
			return SessionResolveHeldResponse{}, ErrInvalid(fmt.Sprintf("resolution %d: action must be update_target or allow_similar", i))
		}
	}

	// Phase 1: snapshot held segments under RLock; collect embed
	// texts for the allow_similar promotions.
	type pending struct {
		res     HeldResolution
		content string
		meta    sessionHoldMeta
		target  string // resolved update_target destination
		vec     []float32
	}
	work := make([]pending, len(resolutions))
	a.engine.RLock()
	if _, svcErr := a.isSession(sessionID); svcErr != nil {
		a.engine.RUnlock()
		return SessionResolveHeldResponse{}, svcErr
	}
	for i, r := range resolutions {
		seg, ok := a.engine.Graph().GetNode(r.SegmentID)
		if !ok {
			a.engine.RUnlock()
			return SessionResolveHeldResponse{}, ErrNotFound(fmt.Sprintf("segment %s not found", r.SegmentID))
		}
		if held, _ := seg.Properties.GetBool("promotion_held"); !held {
			a.engine.RUnlock()
			return SessionResolveHeldResponse{}, ErrInvalid(fmt.Sprintf("segment %s has no held promotion", r.SegmentID))
		}
		content, _ := seg.Properties.GetString("content")
		var meta sessionHoldMeta
		if raw, ok := seg.Properties.GetString("promotion_hold_meta"); ok {
			_ = json.Unmarshal([]byte(raw), &meta)
		}
		target := r.TargetID
		if target == "" {
			target, _ = seg.Properties.GetString("promotion_hold_target")
		}
		if r.Action == "update_target" {
			if target == "" {
				a.engine.RUnlock()
				return SessionResolveHeldResponse{}, ErrInvalid(fmt.Sprintf("resolution %d: no target_id and the hold recorded none", i))
			}
			if _, ok := a.engine.Graph().GetNode(target); !ok {
				a.engine.RUnlock()
				return SessionResolveHeldResponse{}, ErrNotFound(fmt.Sprintf("target record %s not found", target))
			}
		}
		work[i] = pending{res: r, content: content, meta: meta, target: target}
	}
	a.engine.RUnlock()

	// Phase 2: embed allow_similar promotions off-lock. Embedding
	// failure degrades like session_save promotion: the record is
	// created BM25-only and reembed backfills the vector later.
	if a.engine.Embedder() != nil {
		var idxs []int
		var texts []string
		for i := range work {
			if work[i].res.Action != "allow_similar" {
				continue
			}
			text := work[i].meta.SummaryShort
			if text == "" {
				text = work[i].content
				if cap := MaxSummaryShort(); len(text) > cap {
					text = text[:cap]
				}
			}
			idxs = append(idxs, i)
			texts = append(texts, text)
		}
		if len(texts) > 0 {
			if vecs, err := a.engine.Embedder().Embed(ctx, texts); err != nil {
				a.log.Warn("session resolve_held: embedding failed, promoting without vectors",
					"component", "session", "session_id", sessionID, "err", err)
			} else {
				for j, i := range idxs {
					work[i].vec = vecs[j]
				}
			}
		}
	}

	// Phase 3: re-verify and apply under the write lock, one commit.
	a.engine.Lock()
	defer a.engine.Unlock()

	results := make([]HeldResolutionResult, 0, len(work))
	actions := []graph.CommitAction{{Kind: graph.ActionSessionSave, RecordID: sessionID}}
	now := time.Now().UTC()
	author := a.engine.Config().Author.String()
	for _, w := range work {
		seg, ok := a.engine.Graph().GetNode(w.res.SegmentID)
		if !ok {
			return SessionResolveHeldResponse{}, ErrNotFound(fmt.Sprintf("segment %s vanished", w.res.SegmentID))
		}
		if held, _ := seg.Properties.GetBool("promotion_held"); !held {
			return SessionResolveHeldResponse{}, ErrConflict(fmt.Sprintf("segment %s was resolved concurrently", w.res.SegmentID))
		}

		var result HeldResolutionResult
		switch w.res.Action {
		case "update_target":
			if _, ok := a.engine.Graph().GetNode(w.target); !ok {
				return SessionResolveHeldResponse{}, ErrNotFound(fmt.Sprintf("target record %s vanished", w.target))
			}
			if _, err := a.engine.Graph().AddEdge(w.res.SegmentID, w.target, "extracted_as", 1.0, nil); err != nil {
				a.log.Warn("resolve_held: extracted_as edge failed", "component", "session",
					"segment_id", w.res.SegmentID, "target", w.target, "err", err)
			}
			a.engine.SetProp(w.res.SegmentID, "captured_as", graph.StringProperty(w.target))
			a.engine.SetProp(w.res.SegmentID, "captured_at", graph.TimestampProperty(now))
			result = HeldResolutionResult{SegmentID: w.res.SegmentID, Action: w.res.Action, TargetID: w.target}
		case "allow_similar":
			memProps := graph.Properties{
				"content_full":      graph.StringProperty(w.content),
				"processing_status": graph.StringProperty("processed"),
				"created_at":        graph.TimestampProperty(now),
				"access_count":      graph.Int64Property(0),
			}
			if author != "" {
				memProps["author"] = graph.StringProperty(author)
			}
			if w.meta.Temporality != "" {
				memProps["temporality"] = graph.StringProperty(w.meta.Temporality)
			}
			if w.meta.Confidence != nil {
				memProps["confidence"] = graph.Float64Property(*w.meta.Confidence)
			}
			if w.meta.KnowledgeType != "" {
				memProps["knowledge_type"] = graph.StringProperty(w.meta.KnowledgeType)
			} else {
				memProps["knowledge_type"] = graph.StringProperty("episodic")
			}
			if w.meta.EpistemicStatus != "" {
				memProps["epistemic_status"] = graph.StringProperty(w.meta.EpistemicStatus)
			}
			if len(w.meta.Keywords) > 0 {
				memProps["content_keywords"] = graph.StringListProperty(w.meta.Keywords)
			}
			if w.meta.SummaryShort != "" {
				memProps["content_short"] = graph.StringProperty(w.meta.SummaryShort)
			}
			memNode := a.engine.Graph().AddNode(memProps)
			bm25Text := w.content
			if len(w.meta.Keywords) > 0 {
				bm25Text += " " + strings.Join(w.meta.Keywords, " ")
			}
			a.engine.IndexNode(memNode.ID, bm25Text, w.vec)
			if w.vec != nil {
				// Same ring registration as the SessionSave promotion
				// path: the record must be delta-visible to concurrent
				// saves the moment it commits.
				a.engine.NoteRecentWrite(memNode.ID, w.vec)
			}
			if w.vec != nil && a.engine.Embedder() != nil {
				a.engine.SetProp(memNode.ID, "embedding_model", graph.StringProperty(a.engine.Embedder().ModelID()))
			}
			if _, err := a.engine.Graph().AddEdge(w.res.SegmentID, memNode.ID, "extracted_as", 1.0, nil); err != nil {
				a.log.Warn("resolve_held: extracted_as edge failed", "component", "session",
					"segment_id", w.res.SegmentID, "memory_id", memNode.ID, "err", err)
			}
			a.engine.SetProp(w.res.SegmentID, "captured_as", graph.StringProperty(memNode.ID))
			a.engine.SetProp(w.res.SegmentID, "captured_at", graph.TimestampProperty(now))
			actions = append(actions, graph.CommitAction{Kind: graph.ActionSave, RecordID: memNode.ID})
			result = HeldResolutionResult{SegmentID: w.res.SegmentID, Action: w.res.Action, MemoryRecordID: memNode.ID}
		}

		// Clear the persisted hold state either way.
		for _, key := range []string{"promotion_held", "promotion_hold_target", "promotion_hold_similarity", "promotion_hold_meta"} {
			if old, has := seg.Properties[key]; has {
				a.engine.PropIdx().Remove(w.res.SegmentID, key, old)
				a.engine.Graph().RemoveNodeProperty(w.res.SegmentID, key)
			}
		}
		results = append(results, result)
	}

	if _, err := a.engine.Save("session_resolve_held", actions...); err != nil {
		a.log.Warn("session resolve_held save failed", "component", "session",
			"session_id", sessionID, "err", err)
		return SessionResolveHeldResponse{}, ErrInternal("failed to save held-promotion resolutions")
	}

	a.log.Info("session held promotions resolved", "component", "session",
		"session_id", sessionID, "count", len(results))
	return SessionResolveHeldResponse{SessionID: sessionID, Resolved: results}, nil
}

// SessionArchive compresses a conversation transcript and stores it
// as a gzip file referenced from the Session node. The archive is NOT indexed
// or searchable -- it's a break-glass backup of the raw conversation.
func (a *API) SessionArchive(ctx context.Context, sessionID string, sourcePath string) (map[string]any, *APIError) {
	_ = ctx
	// Read-shaped name, genuine writer: archive stamps archive_path /
	// archived_at props on the session node and commits.
	if apiErr := a.rejectIfReadOnly("session_archive"); apiErr != nil {
		return nil, apiErr
	}
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

	if _, err := a.engine.Save("session_archive", graph.CommitAction{
		Kind: graph.ActionSessionArchive, RecordID: sessionID,
	}); err != nil {
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
		"session_id":        sessionID,
		"archive_path":      archivePath,
		"original_size":     originalSize,
		"compressed_size":   compressedSize,
		"compression_ratio": fmt.Sprintf("%.2f", ratio),
	}, nil
}
