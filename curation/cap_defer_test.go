package curation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/llm"
)

// cap_defer_test.go pins the defer-don't-poison contract: system-
// level LLM errors (cost cap fires, context canceled, context
// deadline exceeded) must NOT count against any record's, pair's,
// or manifest's retry budget. Before the fix, the curation LLM-
// dispatch sites uniformly fed every error into the failure-
// recording machinery, so three cap-fired cycles flipped records to
// processing_status="stuck" despite no record-shaped problem. The
// fix's contract: on isDeferrableLLMError(err) the affected
// machinery is skipped entirely and the record / pair / cache stays
// at its pre-cycle state.
//
// Each phase gets one test with three sub-cases (ErrCapped,
// Canceled, DeadlineExceeded) so a regression in any of the three
// error classes at any of the six wiring sites is independently
// visible in test output.

// deferrableErrors enumerates the three error classes that must
// defer-don't-poison. Used by every phase's sub-test table.
func deferrableErrors() []struct {
	name string
	err  error
} {
	return []struct {
		name string
		err  error
	}{
		{"ErrCapped", fmt.Errorf("%w: max_calls_per_day reached", llm.ErrCapped)},
		{"ContextCanceled", context.Canceled},
		{"ContextDeadlineExceeded", context.DeadlineExceeded},
	}
}

func TestClassifyPendingDefersOnDeferrableError(t *testing.T) {
	for _, tc := range deferrableErrors() {
		t.Run(tc.name, func(t *testing.T) {
			eng := setupEngine(t)
			cfg := eng.Config()
			cfg.LLM.Curation.Retries.MaxClassifyAttempts = 3

			id := addPendingNode(t, eng, "Pending classification content")

			mock := &mockLLM{errors: []error{tc.err}}
			result := &AutonomousResult{}
			classifyPending(context.Background(), eng, mock, cfg, result, 20, 0, nil, false)

			eng.RLock()
			defer eng.RUnlock()
			n, _ := eng.Graph().GetNode(id)
			if attempts, _ := n.Properties.GetInt64("classify_attempts"); attempts != 0 {
				t.Errorf("classify_attempts must stay 0 on deferrable error, got %d", attempts)
			}
			if status, _ := n.Properties.GetString("processing_status"); status != "captured" {
				t.Errorf("processing_status must stay %q on deferrable error, got %q", "captured", status)
			}
			if _, hasErr := n.Properties.GetString("last_classify_error"); hasErr {
				t.Error("last_classify_error must not be written on deferrable error")
			}
		})
	}
}

func TestGenerateSummariesDefersOnDeferrableError(t *testing.T) {
	for _, tc := range deferrableErrors() {
		t.Run(tc.name, func(t *testing.T) {
			eng := setupEngine(t)
			cfg := eng.Config()
			cfg.LLM.Curation.Retries.MaxSummaryAttempts = 3

			id := addProcessedNodeNoSummary(t, eng, "Processed record without summary, needs summarization")

			mock := &mockLLM{errors: []error{tc.err}}
			result := &AutonomousResult{}
			generateSummaries(context.Background(), eng, mock, cfg, result, 20, 0, nil, false)

			eng.RLock()
			defer eng.RUnlock()
			n, _ := eng.Graph().GetNode(id)
			if attempts, _ := n.Properties.GetInt64("summary_attempts"); attempts != 0 {
				t.Errorf("summary_attempts must stay 0 on deferrable error, got %d", attempts)
			}
			if _, hasErr := n.Properties.GetString("last_summary_error"); hasErr {
				t.Error("last_summary_error must not be written on deferrable error")
			}
		})
	}
}

func TestEnrichConceptSynthesesDefersOnDeferrableError(t *testing.T) {
	for _, tc := range deferrableErrors() {
		t.Run(tc.name, func(t *testing.T) {
			eng := setupEngine(t)
			cfg := eng.Config()
			// MaxSynthesisAttempts=1 makes synthesis_status assertion
			// load-bearing: pre-fix code would flip status to "stuck"
			// on the very first failure, not just attempts++. With
			// max=3, the attempts check is the only bug-pin; max=1
			// makes both checks catch the regression independently.
			cfg.LLM.Curation.Retries.MaxSynthesisAttempts = 1
			cfg.LLM.Curation.Concept.MaxPerRun = 5

			now := time.Now().UTC()

			// Pending concept node + one member, mirroring the synthesis
			// fixture used by TestEnrichConceptSynthesesLogsDimMismatch.
			// Whole-batch poisoning is the worst-case path -- one
			// cap-fired call would otherwise bump synthesis_attempts on
			// every concept in the batch.
			eng.Lock()
			concept := eng.Graph().AddNode(graph.Properties{
				"content_full":      graph.StringProperty("Concept synthesis stub"),
				"content_short":     graph.StringProperty("kafka concept"),
				"processing_status": graph.StringProperty("processed"),
				"node_type":         graph.StringProperty("concept"),
				"concept_keyword":   graph.StringProperty("kafka"),
				"synthesis_status":  graph.StringProperty("pending"),
				"created_at":        graph.TimestampProperty(now),
				"access_count":      graph.Int64Property(0),
			})
			for k, v := range concept.Properties {
				eng.PropIdx().Add(concept.ID, k, v)
			}
			member := eng.Graph().AddNode(graph.Properties{
				"content_full":      graph.StringProperty("Kafka member record"),
				"processing_status": graph.StringProperty("processed"),
				"embedding_full":    graph.VectorProperty([]float32{1.0, 0.0, 0.0}),
				"created_at":        graph.TimestampProperty(now),
			})
			for k, v := range member.Properties {
				eng.PropIdx().Add(member.ID, k, v)
			}
			if _, err := eng.Graph().AddEdge(member.ID, concept.ID, "instance_of", 1.0, nil); err != nil {
				t.Fatalf("instance_of edge: %v", err)
			}
			eng.Save("test")
			eng.Unlock()

			mock := &mockLLM{errors: []error{tc.err}}
			result := &AutonomousResult{}
			enrichConceptSyntheses(context.Background(), eng, mock, cfg, result, 20, 0, nil, false)

			eng.RLock()
			defer eng.RUnlock()
			n, _ := eng.Graph().GetNode(concept.ID)
			if attempts, _ := n.Properties.GetInt64("synthesis_attempts"); attempts != 0 {
				t.Errorf("synthesis_attempts must stay 0 on deferrable error, got %d", attempts)
			}
			if status, _ := n.Properties.GetString("synthesis_status"); status != "pending" {
				t.Errorf("synthesis_status must stay %q on deferrable error, got %q", "pending", status)
			}
		})
	}
}

func TestGenerateManifestSummaryDefersOnDeferrableError(t *testing.T) {
	for _, tc := range deferrableErrors() {
		t.Run(tc.name, func(t *testing.T) {
			eng := setupEngine(t)
			cfg := eng.Config()
			cfg.LLM.Curation.Retries.MaxManifestAttempts = 3

			// Manifest needs at least one record to compute a fingerprint.
			addNode(t, eng, "Record for manifest", "durable", 0.9, []string{"kafka"}, time.Now().UTC())

			// Fresh cache; FailedAttempts must remain 0 after the deferred
			// call. A non-deferred error would advance it to 1 (and
			// stamp LastFailedHash with the current fingerprint).
			cache := &ManifestCache{}

			mock := &mockLLM{errors: []error{tc.err}}
			result := &AutonomousResult{}
			generateManifestSummary(context.Background(), eng, mock, cfg, result, cache, nil)

			if cache.FailedAttempts != 0 {
				t.Errorf("cache.FailedAttempts must stay 0 on deferrable error, got %d", cache.FailedAttempts)
			}
			if cache.LastFailedHash != "" {
				t.Errorf("cache.LastFailedHash must stay empty on deferrable error, got %q", cache.LastFailedHash)
			}
		})
	}
}

func TestDetectContradictionsDefersOnDeferrableError(t *testing.T) {
	// Run both single-pair and batch paths -- the defer guard lives
	// at distinct callsites in each.
	for _, mode := range []struct {
		name      string
		batchSize int
	}{
		{"SinglePair", 1},
		{"Batch", 5},
	} {
		t.Run(mode.name, func(t *testing.T) {
			for _, tc := range deferrableErrors() {
				t.Run(tc.name, func(t *testing.T) {
					eng := setupEngine(t)
					cfg := eng.Config()
					cfg.LLM.Curation.Contradiction.MaxChecks = 10
					cfg.LLM.Curation.Contradiction.MinSimilarity = 0.5
					cfg.LLM.Curation.Contradiction.MaxSimilarity = 0.95
					cfg.LLM.Curation.Contradiction.BatchSize = mode.batchSize
					cfg.LLM.Curation.Retries.MaxContradictionAttempts = 3

					idA := addProcessedNodeWithEmbedding(t, eng, "JWT auth approach", []float32{1.0, 0.0, 0.0})
					idB := addProcessedNodeWithEmbedding(t, eng, "Session cookie auth approach", []float32{0.7, 0.7, 0.0})

					mock := &mockLLM{errors: []error{tc.err}}
					result := &AutonomousResult{}
					detectContradictions(context.Background(), eng, mock, cfg, result, 20, 0, nil, false)

					eng.RLock()
					defer eng.RUnlock()
					// No contradiction_check_skipped edges should be
					// created on a deferrable error. Walk both directions
					// since the pair could land in either order.
					for _, sourceID := range []string{idA, idB} {
						for _, edge := range eng.Graph().EdgesFrom(sourceID) {
							if edge.Type == contradictionCheckSkippedEdge {
								t.Errorf("contradiction_check_skipped edge must not be created on deferrable error (from %s)", sourceID)
							}
						}
					}
				})
			}
		})
	}
}

// TestIsDeferrableLLMErrorMatches pins the three error classes that
// the helper must return true for, and one that it must NOT match.
// Without this, a future refactor could silently broaden or narrow
// the defer set; either direction is a behavior change worth catching
// in test rather than in production.
func TestIsDeferrableLLMErrorMatches(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		deferrable bool
	}{
		{"ErrCapped_bare", llm.ErrCapped, true},
		{"ErrCapped_wrapped", fmt.Errorf("%w: reason", llm.ErrCapped), true},
		{"ContextCanceled", context.Canceled, true},
		{"ContextCanceled_wrapped", fmt.Errorf("classify call: %w", context.Canceled), true},
		{"ContextDeadlineExceeded", context.DeadlineExceeded, true},
		{"GenericError", errors.New("API timeout"), false},
		{"Nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isDeferrableLLMError(c.err); got != c.deferrable {
				t.Errorf("isDeferrableLLMError(%v) = %v, want %v", c.err, got, c.deferrable)
			}
		})
	}
}

