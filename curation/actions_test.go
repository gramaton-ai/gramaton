package curation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// readHeadActions returns the slice of CommitAction descriptors carried
// by the engine's current HEAD commit. A small helper to avoid leaking
// commit-loading details into every action-emission test.
func readHeadActions(t *testing.T, eng *core.Engine) []graph.CommitAction {
	t.Helper()
	hash := eng.HeadHash()
	if hash == "" {
		t.Fatal("HEAD is empty -- nothing was committed")
	}
	c, err := graph.LoadCommitMeta(eng.Store(), hash)
	if err != nil {
		t.Fatalf("LoadCommitMeta(%s): %v", hash, err)
	}
	return c.Actions
}

// hasAction returns true if the slice contains an action with the
// matching kind and (when non-empty) record id.
func hasAction(actions []graph.CommitAction, kind, recordID string) bool {
	for _, a := range actions {
		if a.Kind == kind && (recordID == "" || a.RecordID == recordID) {
			return true
		}
	}
	return false
}

// TestClassifyEmitsCurationClassifyAction pins that classifyPending
// produces a HEAD commit whose Actions slice contains
// curation:classify entries -- one per record that got classified --
// so gramaton_log(actions=["curation:classify"]) finds the cycle.
//
// Pre-Phase-3-follow-on, classifyPending called e.SaveOrLog without
// any actions, so the resulting commit had Actions == nil and
// action-filter queries returned empty for curation-touched records.
func TestClassifyEmitsCurationClassifyAction(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	id := addPendingNode(t, eng, "Kafka event streaming architecture for microservices")

	llm := &mockLLM{
		responses: []string{`{
			"temporality": "durable",
			"confidence": 0.9,
			"knowledge_type": "semantic",
			"epistemic_status": "well_established",
			"keywords": ["kafka", "streaming"],
			"summary_short": "Kafka event streaming for microservices"
		}`},
	}

	result := &AutonomousResult{}
	classifyPending(context.Background(), eng, llm, cfg, result, 20, 0, nil, false)
	if result.Classified != 1 {
		t.Fatalf("classify pre-condition failed: expected 1 classified, got %d", result.Classified)
	}

	actions := readHeadActions(t, eng)
	if !hasAction(actions, graph.ActionCurationClassify, id) {
		t.Errorf("HEAD commit missing %s action for %s; actions=%v",
			graph.ActionCurationClassify, id, actions)
	}
}

// TestSummarizeEmitsCurationSummaryAction pins generateSummaries.
func TestSummarizeEmitsCurationSummaryAction(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()

	id := addProcessedNodeWithoutSummary(t, eng,
		"Kafka offers higher throughput than RabbitMQ for the event pipeline use case.")

	llm := &mockLLM{
		responses: []string{`{"summary_short": "Kafka beats RabbitMQ on throughput for our event pipeline."}`},
	}

	result := &AutonomousResult{}
	generateSummaries(context.Background(), eng, llm, cfg, result, 10, 3, nil, false)
	if result.SummariesGenerated != 1 {
		t.Fatalf("summarize pre-condition failed: expected 1 summary, got %d", result.SummariesGenerated)
	}

	actions := readHeadActions(t, eng)
	if !hasAction(actions, graph.ActionCurationSummary, id) {
		t.Errorf("HEAD commit missing %s action for %s; actions=%v",
			graph.ActionCurationSummary, id, actions)
	}
}

// addProcessedNodeWithoutSummary creates a processed record that
// generateSummaries will pick up (has content_full + classified
// metadata, missing content_short). Mirrors setupEngine + addPendingNode
// helpers in autonomous_test.go but specialized for the summarize path.
func addProcessedNodeWithoutSummary(t *testing.T, eng *core.Engine, content string) string {
	t.Helper()
	now := time.Now().UTC()
	eng.Lock()
	defer eng.Unlock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
		"temporality":       graph.StringProperty("durable"),
		"confidence":        graph.Float64Property(0.8),
		"knowledge_type":    graph.StringProperty("semantic"),
		"epistemic_status":  graph.StringProperty("probable"),
		"content_keywords":  graph.StringListProperty([]string{"kafka", "rabbitmq"}),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	return n.ID
}

// TestActionKindsAreCanonicalStrings spot-checks that the curation
// constants stringify to the documented "curation:<verb>" shape.
// Catches a refactor that accidentally renames a constant value
// (which would silently break gramaton_log filters depending on
// the old string).
func TestActionKindsAreCanonicalStrings(t *testing.T) {
	cases := map[string]string{
		graph.ActionCurationClassify:           "curation:classify",
		graph.ActionCurationSummary:            "curation:summary",
		graph.ActionCurationLink:               "curation:link",
		graph.ActionCurationSupersede:          "curation:supersede",
		graph.ActionCurationContradictionCheck: "curation:contradiction_check",
		graph.ActionCurationConceptEmerge:      "curation:concept_emerge",
		graph.ActionCurationConceptEnrich:      "curation:concept_enrich",
		graph.ActionCurationSectionLink:        "curation:section_link",
		graph.ActionCurationObservationExtract: "curation:observation_extract",
		graph.ActionCurationLifecycle:          "curation:lifecycle",
		graph.ActionCurationQualityRepair:      "curation:quality_repair",
		graph.ActionCurationGC:                 "curation:gc",
		graph.ActionCurationSelfHeal:           "curation:self_heal",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("kind value drifted: got %q, want %q", got, want)
		}
		if !strings.HasPrefix(got, "curation:") {
			t.Errorf("kind %q does not have curation: prefix", got)
		}
	}
	_ = config.Defaults() // package import sanity
}
