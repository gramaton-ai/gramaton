package server

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

//go:embed prompts/extraction.md
var embeddedExtractionPrompt string

// loadExtractionPrompt loads the extraction prompt from the config directory,
// falling back to the embedded default. Returns the prompt content and a short
// hash for logging which version was used.
func (s *Server) loadExtractionPrompt() (string, string) {
	// Try loading from config directory first (allows user overrides).
	if s.cfg.ConfigDir != "" {
		filePath := filepath.Join(s.cfg.ConfigDir, "prompts", "extraction.md")
		if data, err := os.ReadFile(filePath); err == nil {
			content := string(data)
			hash := fmt.Sprintf("%x", sha256.Sum256(data))[:8]
			s.log.Debug("extraction prompt loaded from file", "component", "session",
				"path", filePath, "size", len(content), "hash", hash)
			return content, hash
		}
	}

	// Fall back to embedded default.
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(embeddedExtractionPrompt)))[:8]
	s.log.Debug("extraction prompt loaded from embedded", "component", "session",
		"size", len(embeddedExtractionPrompt), "hash", hash)
	return embeddedExtractionPrompt, hash
}

// --- helpers ---

// sessionsByClientID finds all Session nodes with a given client_session_id.
// Returns node IDs sorted by created_at (newest last).
// Caller must hold at least RLock.
func (s *Server) sessionsByClientID(clientID string) []string {
	ids := s.engine.PropIdx().Lookup("knowledge_type", graph.StringProperty("session"))
	var matches []string
	for _, id := range ids {
		n, ok := s.engine.Graph().GetNode(id)
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
func (s *Server) latestSessionByClientID(clientID string) (string, bool) {
	matches := s.sessionsByClientID(clientID)
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
		for _, e := range s.engine.Graph().EdgesFrom(id) {
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
func (s *Server) isSession(nodeID string) (*graph.Node, *serviceError) {
	n, ok := s.engine.Graph().GetNode(nodeID)
	if !ok {
		return nil, errNotFound("session not found")
	}
	kt, _ := n.Properties.GetString("knowledge_type")
	if kt != "session" {
		return nil, errNotFound("not a session")
	}
	return n, nil
}

// sessionTopics returns all Topic nodes linked to a Session via topic_of edges.
// Caller must hold at least RLock.
func (s *Server) sessionTopics(sessionID string) []*graph.Node {
	var topics []*graph.Node
	for _, e := range s.engine.Graph().EdgesTo(sessionID) {
		if e.Type == "topic_of" {
			if n, ok := s.engine.Graph().GetNode(e.SourceID); ok {
				topics = append(topics, n)
			}
		}
	}
	return topics
}

// topicSegments returns all Segment nodes linked to a Topic via segment_of edges.
// Caller must hold at least RLock.
func (s *Server) topicSegments(topicID string) []*graph.Node {
	var segments []*graph.Node
	for _, e := range s.engine.Graph().EdgesTo(topicID) {
		if e.Type == "segment_of" {
			if n, ok := s.engine.Graph().GetNode(e.SourceID); ok {
				segments = append(segments, n)
			}
		}
	}
	return segments
}

// buildSessionResponse constructs a full Session response including topics and segments.
// Caller must hold at least RLock.
func (s *Server) buildSessionResponse(sessionID string, session *graph.Node) map[string]any {
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

	topics := s.sessionTopics(sessionID)
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

		segments := s.topicSegments(t.ID)
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

// serviceSessionCreate creates a new Session, chains to previous sessions
// on resume, or returns the current session for idempotent agent calls.
//
// source="startup": fresh session, no chaining
// source="resume": new session chained to latest with same client_session_id
// source="" (agent call): return existing if found, create fresh otherwise
func (s *Server) serviceSessionCreate(clientSessionID string, source string) (map[string]any, *serviceError) {
	if clientSessionID == "" {
		return nil, errMissing("client_session_id is required")
	}
	if len(clientSessionID) > 256 {
		return nil, errInvalid("client_session_id exceeds 256 characters")
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	latestID, hasExisting := s.latestSessionByClientID(clientSessionID)

	// Agent idempotent call (no source): return existing if found.
	if source == "" && hasExisting {
		session, _ := s.engine.Graph().GetNode(latestID)
		s.log.Debug("session lookup hit", "component", "session",
			"client_session_id", clientSessionID, "session_id", latestID)
		resp := s.buildSessionResponse(latestID, session)
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
	n := s.engine.Graph().AddNode(props)
	s.engine.IndexNode(n.ID, "", nil)

	// On resume, chain to the previous session.
	var previousSessionID string
	if source == "resume" && hasExisting {
		if _, err := s.engine.Graph().AddEdge(n.ID, latestID, "continues_from", 1.0, nil); err != nil {
			s.log.Warn("failed to create continues_from edge", "component", "session",
				"new_session", n.ID, "previous_session", latestID, "err", err)
		} else {
			previousSessionID = latestID
			s.log.Info("session chained", "component", "session",
				"new_session", n.ID, "previous_session", latestID)
		}
	}

	if _, err := s.engine.Save("session_create"); err != nil {
		return nil, errInternal(err.Error())
	}

	s.log.Info("session created", "component", "session",
		"session_id", n.ID, "client_session_id", clientSessionID,
		"source", source, "chained_to", previousSessionID)

	resp := s.buildSessionResponse(n.ID, n)
	resp["resumed"] = false
	if previousSessionID != "" {
		resp["previous_session_id"] = previousSessionID
	}
	return resp, nil
}

// serviceSessionGet returns the full state of a Session.
func (s *Server) serviceSessionGet(sessionID string) (map[string]any, *serviceError) {
	if sessionID == "" {
		return nil, errMissing("session_id is required")
	}

	s.engine.RLock()
	defer s.engine.RUnlock()

	session, svcErr := s.isSession(sessionID)
	if svcErr != nil {
		return nil, svcErr
	}

	s.log.Debug("session get", "component", "session", "session_id", sessionID,
		"topic_count", len(s.sessionTopics(sessionID)))

	return s.buildSessionResponse(sessionID, session), nil
}

// serviceSessionAddTopic adds a new Topic to a Session.
func (s *Server) serviceSessionAddTopic(sessionID string, name string, branchedFrom string) (map[string]any, *serviceError) {
	if sessionID == "" {
		return nil, errMissing("session_id is required")
	}
	if name == "" {
		return nil, errMissing("topic name is required")
	}
	if len(name) > 256 {
		return nil, errInvalid("topic name exceeds 256 characters")
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, svcErr := s.isSession(sessionID); svcErr != nil {
		return nil, svcErr
	}

	// Validate branched_from if provided.
	if branchedFrom != "" {
		n, ok := s.engine.Graph().GetNode(branchedFrom)
		if !ok {
			return nil, errNotFound(fmt.Sprintf("branched_from topic %q not found", branchedFrom))
		}
		kt, _ := n.Properties.GetString("knowledge_type")
		if kt != "topic" {
			return nil, errInvalid(fmt.Sprintf("branched_from %q is not a topic", branchedFrom))
		}
		// Verify it belongs to this session.
		belongsToSession := false
		for _, e := range s.engine.Graph().EdgesFrom(branchedFrom) {
			if e.Type == "topic_of" && e.TargetID == sessionID {
				belongsToSession = true
				break
			}
		}
		if !belongsToSession {
			return nil, errInvalid(fmt.Sprintf("branched_from topic %q does not belong to this session", branchedFrom))
		}
	}

	now := time.Now().UTC()
	props := graph.Properties{
		"knowledge_type": graph.StringProperty("topic"),
		"topic_name":     graph.StringProperty(name),
		"created_at":     graph.TimestampProperty(now),
	}
	if branchedFrom != "" {
		props["branched_from"] = graph.StringProperty(branchedFrom)
	}
	topicNode := s.engine.Graph().AddNode(props)
	s.engine.IndexNode(topicNode.ID, "", nil)

	// Create structural edge: topic -> session.
	if _, err := s.engine.Graph().AddEdge(topicNode.ID, sessionID, "topic_of", 1.0, nil); err != nil {
		return nil, errInternal(fmt.Sprintf("failed to create topic_of edge: %v", err))
	}

	if _, err := s.engine.Save("session_add_topic"); err != nil {
		return nil, errInternal(err.Error())
	}

	s.log.Info("topic created", "component", "session", "session_id", sessionID,
		"topic_id", topicNode.ID, "topic_name", name, "branched_from", branchedFrom)

	resp := map[string]any{
		"id":         topicNode.ID,
		"name":       name,
		"session_id": sessionID,
		"created_at": now.Format(time.RFC3339),
	}
	if branchedFrom != "" {
		resp["branched_from"] = branchedFrom
	}
	return resp, nil
}

// serviceSessionAddSegment adds a Segment to a Topic within a Session.
func (s *Server) serviceSessionAddSegment(sessionID string, topicID string, content string) (map[string]any, *serviceError) {
	if sessionID == "" {
		return nil, errMissing("session_id is required")
	}
	if topicID == "" {
		return nil, errMissing("topic_id is required")
	}
	if strings.TrimSpace(content) == "" {
		return nil, errMissing("segment content is required")
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	start := time.Now()

	// Validate session exists.
	if _, svcErr := s.isSession(sessionID); svcErr != nil {
		return nil, svcErr
	}

	// Validate topic exists and belongs to this session.
	topicNode, ok := s.engine.Graph().GetNode(topicID)
	if !ok {
		return nil, errNotFound("topic not found")
	}
	kt, _ := topicNode.Properties.GetString("knowledge_type")
	if kt != "topic" {
		return nil, errInvalid("not a topic")
	}
	belongsToSession := false
	for _, e := range s.engine.Graph().EdgesFrom(topicID) {
		if e.Type == "topic_of" && e.TargetID == sessionID {
			belongsToSession = true
			break
		}
	}
	if !belongsToSession {
		return nil, errInvalid("topic does not belong to this session")
	}

	now := time.Now().UTC()
	props := graph.Properties{
		"knowledge_type": graph.StringProperty("segment"),
		"content":        graph.StringProperty(content),
		"created_at":     graph.TimestampProperty(now),
	}
	segNode := s.engine.Graph().AddNode(props)
	// BM25-index the segment content (no vector embedding -- BM25-only per B1).
	s.engine.IndexNode(segNode.ID, content, nil)

	// Create structural edge: segment -> topic.
	if _, err := s.engine.Graph().AddEdge(segNode.ID, topicID, "segment_of", 1.0, nil); err != nil {
		return nil, errInternal(fmt.Sprintf("failed to create segment_of edge: %v", err))
	}

	if _, err := s.engine.Save("session_add_segment"); err != nil {
		return nil, errInternal(err.Error())
	}

	dur := time.Since(start)
	s.log.Info("segment added", "component", "session", "session_id", sessionID,
		"topic_id", topicID, "segment_id", segNode.ID, "content_len", len(content))
	s.log.Debug("segment write timing", "component", "session", "segment_id", segNode.ID, "duration", dur)

	return map[string]any{
		"id":         segNode.ID,
		"topic_id":   topicID,
		"session_id": sessionID,
		"created_at": now.Format(time.RFC3339),
	}, nil
}

// serviceSessionUpdateSegmentCapture updates a Segment's captured_as and captured_at
// fields. This is the only allowed mutation on a Segment (append-only).
func (s *Server) serviceSessionUpdateSegmentCapture(segmentID string, capturedAs string) (map[string]any, *serviceError) {
	if segmentID == "" {
		return nil, errMissing("segment_id is required")
	}
	if capturedAs == "" {
		return nil, errMissing("captured_as is required")
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	n, ok := s.engine.Graph().GetNode(segmentID)
	if !ok {
		return nil, errNotFound("segment not found")
	}
	kt, _ := n.Properties.GetString("knowledge_type")
	if kt != "segment" {
		return nil, errInvalid("not a segment")
	}

	now := time.Now().UTC()
	s.engine.SetProp(segmentID, "captured_as", graph.StringProperty(capturedAs))
	s.engine.SetProp(segmentID, "captured_at", graph.TimestampProperty(now))

	if _, err := s.engine.Save("session_segment_captured"); err != nil {
		return nil, errInternal(err.Error())
	}

	s.log.Info("segment capture status updated", "component", "session",
		"segment_id", segmentID, "captured_as", capturedAs)

	return map[string]any{
		"segment_id":  segmentID,
		"captured_as": capturedAs,
		"captured_at": now.Format(time.RFC3339),
	}, nil
}

// compactionFlagTTL bounds how long a PostCompact flag is considered
// fresh. Compaction is normally followed by continued work within minutes;
// 2h covers a long break without surfacing stale nudges the next day.
const compactionFlagTTL = 2 * time.Hour

// consumeCompactionFlag checks for and deletes a PostCompact flag written by
// the hook at ~/.gramaton/hook-state/{client_session_id}.compacted. Returns
// the flag's timestamp if fresh (within compactionFlagTTL), or zero time
// otherwise. Single-shot: the flag is deleted whether fresh or stale so the
// nudge fires at most once per compaction.
func (s *Server) consumeCompactionFlag(clientSessionID string) time.Time {
	if s.cfg.ConfigDir == "" || clientSessionID == "" {
		return time.Time{}
	}
	flagPath := filepath.Join(s.cfg.ConfigDir, "hook-state", clientSessionID+".compacted")
	data, err := os.ReadFile(flagPath)
	if err != nil {
		return time.Time{}
	}
	// Delete regardless of parse success -- never surface the same flag twice.
	_ = os.Remove(flagPath)

	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		s.log.Warn("compaction flag parse failed", "component", "session",
			"path", flagPath, "err", err)
		return time.Time{}
	}
	if time.Since(ts) > compactionFlagTTL {
		s.log.Debug("compaction flag stale, discarded", "component", "session",
			"client_session_id", clientSessionID, "age", time.Since(ts))
		return time.Time{}
	}
	return ts
}

// serviceSessionPrepare returns extraction instructions and current session state.
// Sets an in-memory prepared flag so commit can validate the two-phase flow.
func (s *Server) serviceSessionPrepare(sessionID string) (map[string]any, *serviceError) {
	if sessionID == "" {
		return nil, errMissing("session_id is required")
	}

	s.engine.RLock()
	defer s.engine.RUnlock()

	session, svcErr := s.isSession(sessionID)
	if svcErr != nil {
		return nil, svcErr
	}

	sessionState := s.buildSessionResponse(sessionID, session)

	// Set prepared flag (protected by mu since preparedSessions is not engine-locked).
	s.mu.Lock()
	s.preparedSessions[sessionID] = time.Now()
	s.mu.Unlock()

	// Count segments for logging.
	segCount := 0
	if topics, ok := sessionState["topics"].([]map[string]any); ok {
		for _, t := range topics {
			if segs, ok := t["segments"].([]map[string]any); ok {
				segCount += len(segs)
			}
		}
	}

	s.log.Info("session prepared", "component", "session", "session_id", sessionID,
		"segment_count", segCount, "prepared_flag", true)

	prompt, promptHash := s.loadExtractionPrompt()
	s.log.Debug("prepare returning prompt", "component", "session",
		"session_id", sessionID, "prompt_hash", promptHash)

	resp := map[string]any{
		"instructions":  prompt,
		"session_state": sessionState,
	}

	// Surface a post-compaction nudge if the hook wrote a fresh flag.
	clientSessionID, _ := session.Properties.GetString("client_session_id")
	if compactedAt := s.consumeCompactionFlag(clientSessionID); !compactedAt.IsZero() {
		resp["recent_compaction"] = map[string]any{
			"at": compactedAt.Format(time.RFC3339),
		}
		resp["instructions"] = "NOTE: your context was recently compacted. Review session state below for already-captured segments before extracting -- do not re-capture knowledge that is already committed.\n\n" + prompt
		s.log.Info("prepare: surfaced compaction nudge", "component", "session",
			"session_id", sessionID, "client_session_id", clientSessionID,
			"compacted_at", compactedAt.Format(time.RFC3339))
	}

	return resp, nil
}

// commitSegment is a single segment submitted via session_commit.
type commitSegment struct {
	Content         string   `json:"content"`
	TopicName       string   `json:"topic"`
	Temporality     string   `json:"temporality,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	KnowledgeType   string   `json:"knowledge_type,omitempty"`
	EpistemicStatus string   `json:"epistemic_status,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	SummaryShort    string   `json:"summary_short,omitempty"`
}

// serviceSessionCommit appends extracted segments to the session.
// Validates that prepare was called first. Creates new topics as needed.
// Phase 2: stores in Session only (no Memory records, no embedding).
func (s *Server) serviceSessionCommit(sessionID string, segments []commitSegment) (map[string]any, *serviceError) {
	if sessionID == "" {
		return nil, errMissing("session_id is required")
	}
	if len(segments) == 0 {
		return nil, errMissing("segments is required and must not be empty")
	}

	// Validate all segments before consuming the prepared flag so that a
	// malformed commit doesn't force the agent to re-prepare.
	for i, seg := range segments {
		if strings.TrimSpace(seg.Content) == "" {
			return nil, errInvalid(fmt.Sprintf("segment %d: content is required", i))
		}
		if strings.TrimSpace(seg.TopicName) == "" {
			return nil, errInvalid(fmt.Sprintf("segment %d: topic name is required", i))
		}
	}

	// Check and consume prepared flag.
	s.mu.Lock()
	_, prepared := s.preparedSessions[sessionID]
	if prepared {
		delete(s.preparedSessions, sessionID)
	}
	s.mu.Unlock()

	if !prepared {
		s.log.Warn("session commit rejected: prepare not called", "component", "session", "session_id", sessionID)
		return nil, &serviceError{
			Status:  400,
			Code:    "prepare_required",
			Message: "You must call gramaton_session_prepare first. Prepare returns extraction instructions and session state needed for high-quality knowledge extraction. Call prepare, follow its instructions, then call commit.",
		}
	}

	start := time.Now()

	// Pre-embed all segment contents for Memory records (outside lock).
	var embedVecs [][]float32
	if s.engine.Embedder() != nil {
		texts := make([]string, len(segments))
		for i, seg := range segments {
			text := seg.SummaryShort
			if text == "" {
				text = seg.Content
				if len(text) > 500 {
					text = text[:500]
				}
			}
			texts[i] = text
		}
		vecs, err := s.engine.Embedder().Embed(context.Background(), texts)
		if err != nil {
			s.log.Warn("session commit: embedding failed, continuing without vectors",
				"component", "session", "session_id", sessionID, "err", err)
		} else {
			embedVecs = vecs
		}
	}
	embedDur := time.Since(start)

	s.engine.Lock()
	defer s.engine.Unlock()

	if _, svcErr := s.isSession(sessionID); svcErr != nil {
		return nil, svcErr
	}

	// Build topic name -> ID map from existing topics.
	topicMap := make(map[string]string) // topic name -> node ID
	for _, t := range s.sessionTopics(sessionID) {
		if name, ok := t.Properties.GetString("topic_name"); ok {
			topicMap[name] = t.ID
		}
	}

	topicsCreated := 0
	segmentsAdded := 0
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
			topicNode := s.engine.Graph().AddNode(props)
			s.engine.IndexNode(topicNode.ID, "", nil)

			if _, err := s.engine.Graph().AddEdge(topicNode.ID, sessionID, "topic_of", 1.0, nil); err != nil {
				return nil, errInternal(fmt.Sprintf("failed to create topic_of edge: %v", err))
			}
			topicID = topicNode.ID
			topicMap[seg.TopicName] = topicID
			topicsCreated++

			s.log.Info("topic created", "component", "session", "session_id", sessionID,
				"topic_id", topicID, "topic_name", seg.TopicName)
		}

		// 1. Create the Session segment node.
		now := time.Now().UTC()
		segProps := graph.Properties{
			"knowledge_type": graph.StringProperty("segment"),
			"content":        graph.StringProperty(seg.Content),
			"created_at":     graph.TimestampProperty(now),
		}
		segNode := s.engine.Graph().AddNode(segProps)
		// BM25-index the segment content (no vector -- BM25-only per B1).
		s.engine.IndexNode(segNode.ID, seg.Content, nil)

		if _, err := s.engine.Graph().AddEdge(segNode.ID, topicID, "segment_of", 1.0, nil); err != nil {
			return nil, errInternal(fmt.Sprintf("failed to create segment_of edge: %v", err))
		}
		segmentsAdded++

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

		memNode := s.engine.Graph().AddNode(memProps)

		// 3. Embed the Memory record (vector + BM25).
		var vec []float32
		if embedVecs != nil && i < len(embedVecs) {
			vec = embedVecs[i]
		}
		bm25Text := seg.Content
		if len(seg.Keywords) > 0 {
			bm25Text += " " + strings.Join(seg.Keywords, " ")
		}
		s.engine.IndexNode(memNode.ID, bm25Text, vec)

		batchIDs[memNode.ID] = struct{}{}
		memoryRecordsCreated++

		s.log.Info("memory record created", "component", "session",
			"memory_record_id", memNode.ID, "session_id", sessionID,
			"segment_id", segNode.ID, "content_len", len(seg.Content),
			"has_vector", vec != nil)

		// 4. Create edge: segment --extracted_as--> memory_record.
		if _, err := s.engine.Graph().AddEdge(segNode.ID, memNode.ID, "extracted_as", 1.0, nil); err != nil {
			s.log.Warn("failed to create extracted_as edge", "component", "session",
				"segment_id", segNode.ID, "memory_id", memNode.ID, "err", err)
		} else {
			edgesCreated++
			s.log.Debug("edge created", "component", "session",
				"source", segNode.ID, "target", memNode.ID, "type", "extracted_as")
		}

		// 5. Update segment captured_as with Memory record ID.
		s.engine.SetProp(segNode.ID, "captured_as", graph.StringProperty(memNode.ID))
		s.engine.SetProp(segNode.ID, "captured_at", graph.TimestampProperty(now))

		// 6. Auto-supersession on the Memory record (skip within-batch).
		if dupID, sim := s.engine.CheckDedup(memNode.ID); dupID != "" {
			if _, inBatch := batchIDs[dupID]; inBatch {
				s.log.Debug("auto-supersession skipped: within-commit batch",
					"component", "session", "new_id", memNode.ID, "dup_id", dupID)
				continue
			}
			cfg := s.engine.Config()
			if cfg.Dedup.Action != "reject" {
				oldNode, _ := s.engine.Graph().GetNode(dupID)
				if oldNode != nil {
					_, alreadyHistorical := oldNode.Properties.GetTimestamp("valid_until")
					if !alreadyHistorical {
						s.engine.SetProp(dupID, "valid_until", graph.TimestampProperty(now))
						s.engine.SetProp(dupID, "resolution", graph.StringProperty("superseded"))
						s.engine.SetProp(dupID, "resolved_at", graph.TimestampProperty(now))
						if e, err := s.engine.Graph().AddEdge(memNode.ID, dupID, "supersedes", sim, nil); err == nil {
							summary := ""
							if v, ok := oldNode.Properties.GetString("content_short"); ok {
								summary = v
							}
							superseded = append(superseded, map[string]any{
								"id": dupID, "summary": summary,
								"similarity": sim, "edge_id": e.ID,
							})
							s.log.Info("auto-supersession triggered", "component", "session",
								"new_record_id", memNode.ID, "superseded_id", dupID,
								"similarity", fmt.Sprintf("%.3f", sim))
						}
					}
				}
			}
		}
	}

	if _, err := s.engine.Save("session_commit"); err != nil {
		return nil, errInternal(err.Error())
	}

	dur := time.Since(start)
	s.log.Info("session commit", "component", "session", "session_id", sessionID,
		"segments_submitted", len(segments), "segments_added", segmentsAdded,
		"memory_records_created", memoryRecordsCreated, "edges_created", edgesCreated,
		"embed_ms", embedDur.Milliseconds(), "duration", dur)

	resp := map[string]any{
		"session_id":             sessionID,
		"segments_added":         segmentsAdded,
		"topics_created":         topicsCreated,
		"memory_records_created": memoryRecordsCreated,
		"edges_created":          edgesCreated,
	}
	if len(superseded) > 0 {
		resp["superseded"] = superseded
	}
	return resp, nil
}

// serviceSessionArchive compresses a conversation transcript and stores it
// as a gzip file referenced from the Session node. The archive is NOT indexed
// or searchable -- it's a break-glass backup of the raw conversation.
func (s *Server) serviceSessionArchive(sessionID string, sourcePath string) (map[string]any, *serviceError) {
	if sessionID == "" {
		return nil, errMissing("session_id is required")
	}
	if sourcePath == "" {
		return nil, errMissing("source file path is required")
	}

	start := time.Now()

	// Read source file.
	sourceData, err := os.ReadFile(sourcePath)
	if err != nil {
		s.log.Warn("archive: failed to read source", "component", "session",
			"session_id", sessionID, "path", sourcePath, "err", err)
		return nil, errInvalid(fmt.Sprintf("cannot read source file: %v", err))
	}
	originalSize := len(sourceData)

	// Determine archive directory.
	archiveDir := filepath.Join(s.cfg.ConfigDir, "archives")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		s.log.Warn("archive: failed to create directory", "component", "session",
			"path", archiveDir, "err", err)
		return nil, errInternal("failed to create archive directory")
	}
	s.log.Debug("archive directory ready", "component", "session", "path", archiveDir)

	// Write compressed archive via atomic temp file.
	archiveName := fmt.Sprintf("%s.gz", sessionID)
	archivePath := filepath.Join(archiveDir, archiveName)
	tmpPath := archivePath + ".tmp"

	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return nil, errInternal(fmt.Sprintf("failed to create temp file: %v", err))
	}

	gzWriter := gzip.NewWriter(tmpFile)
	if _, err := gzWriter.Write(sourceData); err != nil {
		gzWriter.Close()
		tmpFile.Close()
		os.Remove(tmpPath)
		return nil, errInternal(fmt.Sprintf("gzip write failed: %v", err))
	}
	if err := gzWriter.Close(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return nil, errInternal(fmt.Sprintf("gzip close failed: %v", err))
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, errInternal(fmt.Sprintf("file close failed: %v", err))
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, archivePath); err != nil {
		os.Remove(tmpPath)
		return nil, errInternal(fmt.Sprintf("rename failed: %v", err))
	}

	// Get compressed size.
	info, err := os.Stat(archivePath)
	if err != nil {
		return nil, errInternal("failed to stat archive")
	}
	compressedSize := info.Size()

	// Update Session node with archive reference.
	s.engine.Lock()
	defer s.engine.Unlock()

	if _, svcErr := s.isSession(sessionID); svcErr != nil {
		return nil, svcErr
	}

	now := time.Now().UTC()
	s.engine.SetProp(sessionID, "archive_path", graph.StringProperty(archivePath))
	s.engine.SetProp(sessionID, "archive_size", graph.Int64Property(compressedSize))
	s.engine.SetProp(sessionID, "archive_original_size", graph.Int64Property(int64(originalSize)))
	s.engine.SetProp(sessionID, "archived_at", graph.TimestampProperty(now))

	if _, err := s.engine.Save("session_archive"); err != nil {
		return nil, errInternal(err.Error())
	}

	dur := time.Since(start)
	ratio := float64(compressedSize) / float64(originalSize)
	s.log.Info("archive created", "component", "session",
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

// serviceSessionReadArchive decompresses and returns the raw archive content.
func (s *Server) serviceSessionReadArchive(sessionID string) (map[string]any, *serviceError) {
	if sessionID == "" {
		return nil, errMissing("session_id is required")
	}

	s.engine.RLock()
	session, svcErr := s.isSession(sessionID)
	if svcErr != nil {
		s.engine.RUnlock()
		return nil, svcErr
	}
	archivePath, ok := session.Properties.GetString("archive_path")
	s.engine.RUnlock()

	if !ok || archivePath == "" {
		return nil, errNotFound("no archive for this session")
	}

	s.log.Info("archive read requested", "component", "session",
		"session_id", sessionID, "archive_path", archivePath)

	f, err := os.Open(archivePath)
	if err != nil {
		s.log.Warn("archive read failed", "component", "session",
			"session_id", sessionID, "path", archivePath, "err", err)
		return nil, errInternal(fmt.Sprintf("cannot open archive: %v", err))
	}
	defer f.Close()

	gzReader, err := gzip.NewReader(f)
	if err != nil {
		s.log.Warn("archive decompress failed", "component", "session",
			"session_id", sessionID, "path", archivePath, "err", err)
		return nil, errInternal(fmt.Sprintf("corrupt archive: %v", err))
	}
	defer gzReader.Close()

	data, err := io.ReadAll(gzReader)
	if err != nil {
		return nil, errInternal(fmt.Sprintf("read failed: %v", err))
	}

	return map[string]any{
		"session_id":    sessionID,
		"archive_path":  archivePath,
		"content_size":  len(data),
		"content":       string(data),
	}, nil
}
