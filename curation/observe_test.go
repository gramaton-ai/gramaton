package curation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// configurableObsEmbedder is a test embedder for the observe tests.
// Errors at index `calls` are returned in order; nil entries succeed.
type configurableObsEmbedder struct {
	mu     sync.Mutex
	errors []error
	calls  int
	dim    int
}

func (e *configurableObsEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := e.calls
	e.calls++
	if idx < len(e.errors) && e.errors[idx] != nil {
		return nil, e.errors[idx]
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, e.dim)
		out[i][0] = 1.0
	}
	return out, nil
}

func (e *configurableObsEmbedder) ModelID() string    { return "configurable-obs-embedder" }
func (e *configurableObsEmbedder) ContextWindow() int { return 512 }

// setupObserveEngine builds an engine with the given embedder. Used
// by the observation retry-bound tests below.
func setupObserveEngine(t *testing.T, emb *configurableObsEmbedder) *core.Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	opts := []core.EngineOption{core.WithVectorIndex(index.NewFlatIndex()), core.WithVolatileStorage()}
	if emb != nil {
		opts = append(opts, core.WithEmbedder(emb))
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, opts)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

// addObserveCandidate seeds a record long enough to qualify for
// observation extraction (>= ObservationMinContentLength, default
// 1500). Returns the parent ID.
func addObserveCandidate(t *testing.T, eng *core.Engine) string {
	t.Helper()
	// Build content well past the 1500-char default. Use varied
	// sentences so the TF-IDF extractor produces multiple
	// observations.
	var sb strings.Builder
	sentences := []string{
		"The first sentence introduces a concept.",
		"This second sentence provides context.",
		"A third statement adds a contrasting view.",
		"The fourth paragraph discusses implications.",
		"Finally, the fifth point synthesizes everything.",
	}
	for sb.Len() < 2000 {
		for _, s := range sentences {
			sb.WriteString(s)
			sb.WriteString(" ")
		}
	}
	content := sb.String()

	eng.Lock()
	defer eng.Unlock()
	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
		"access_count":      graph.Int64Property(0),
	}
	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	if _, err := eng.Save("seed"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return n.ID
}

// TestObserveFailureBumpsAttemptCounter pins the observation-extract
// retry-budget fix. Pre-fix, an embed failure on the observation
// batch left the parent without an observation_of edge, so the next
// cycle re-extracted it and re-paid the embedding cost. Post-fix,
// the failure writes observation_extract_attempts=1.
func TestObserveFailureBumpsAttemptCounter(t *testing.T) {
	emb := &configurableObsEmbedder{
		errors: []error{fmt.Errorf("API timeout")},
		dim:    4,
	}
	eng := setupObserveEngine(t, emb)
	id := addObserveCandidate(t, eng)

	cfg := eng.Config()
	cfg.Curation.MaxObservationAttempts = 5

	if got := extractAndCreateObservations(eng, cfg, nil); got != 0 {
		t.Errorf("extractAndCreateObservations: got %d created on failure, want 0", got)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("observation_extract_attempts")
	if attempts != 1 {
		t.Errorf("observation_extract_attempts: got %d, want 1", attempts)
	}
	reason, _ := n.Properties.GetString("last_observation_extract_error")
	if !strings.Contains(reason, "API timeout") {
		t.Errorf("last_observation_extract_error: got %q, want contains %q", reason, "API timeout")
	}
}

// TestObserveSkipsParentsAtThreshold verifies that after
// MaxObservationAttempts consecutive failures, the parent is
// excluded from the next cycle's candidate selection -- the
// embedder doesn't get called at all.
func TestObserveSkipsParentsAtThreshold(t *testing.T) {
	// Five errors so we have headroom; only the first 5 should fire
	// since the 6th cycle must skip the parent at selection.
	emb := &configurableObsEmbedder{
		errors: []error{
			fmt.Errorf("err 1"), fmt.Errorf("err 2"), fmt.Errorf("err 3"),
			fmt.Errorf("err 4"), fmt.Errorf("err 5"),
		},
		dim: 4,
	}
	eng := setupObserveEngine(t, emb)
	addObserveCandidate(t, eng)

	cfg := eng.Config()
	cfg.Curation.MaxObservationAttempts = 5

	for cycle := 1; cycle <= 5; cycle++ {
		extractAndCreateObservations(eng, cfg, nil)
	}

	callsBeforeSixth := emb.calls
	extractAndCreateObservations(eng, cfg, nil)
	if emb.calls != callsBeforeSixth {
		t.Errorf("at-threshold parent was re-attempted: embedder called %d more times, want 0",
			emb.calls-callsBeforeSixth)
	}
}

// TestObserveMaxAttemptsZeroDisables verifies legacy behavior: when
// MaxObservationAttempts=0, no counter is written and parents
// re-enter the candidate set every cycle.
func TestObserveMaxAttemptsZeroDisables(t *testing.T) {
	emb := &configurableObsEmbedder{
		errors: []error{fmt.Errorf("err")},
		dim:    4,
	}
	eng := setupObserveEngine(t, emb)
	id := addObserveCandidate(t, eng)

	cfg := eng.Config()
	cfg.Curation.MaxObservationAttempts = 0

	extractAndCreateObservations(eng, cfg, nil)

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if _, ok := n.Properties.GetInt64("observation_extract_attempts"); ok {
		t.Error("observation_extract_attempts was written when MaxObservationAttempts=0")
	}
	if _, ok := n.Properties.GetString("last_observation_extract_error"); ok {
		t.Error("last_observation_extract_error was written when MaxObservationAttempts=0")
	}
}

// TestObserveSuccessClearsAttempts verifies that successful
// extraction clears the per-parent retry counter.
func TestObserveSuccessClearsAttempts(t *testing.T) {
	emb := &configurableObsEmbedder{
		errors: []error{fmt.Errorf("err 1"), fmt.Errorf("err 2"), nil},
		dim:    4,
	}
	eng := setupObserveEngine(t, emb)
	id := addObserveCandidate(t, eng)

	cfg := eng.Config()
	cfg.Curation.MaxObservationAttempts = 5

	// Fail twice -- attempts now 2.
	for i := 0; i < 2; i++ {
		extractAndCreateObservations(eng, cfg, nil)
	}

	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	if a, _ := n.Properties.GetInt64("observation_extract_attempts"); a != 2 {
		eng.RUnlock()
		t.Fatalf("intermediate observation_extract_attempts: got %d, want 2", a)
	}
	eng.RUnlock()

	// Now succeed -- counter must reset, observation_of edge written.
	extractAndCreateObservations(eng, cfg, nil)

	eng.RLock()
	defer eng.RUnlock()
	n, _ = eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("observation_extract_attempts")
	if attempts != 0 {
		t.Errorf("observation_extract_attempts after success: got %d, want 0", attempts)
	}

	// The created observation nodes are system-created: they carry the
	// curation author constant, never the operator's configured
	// identity (and not the parent's author -- that one is inherited
	// only by chunking sub-nodes).
	observations := 0
	for _, e := range eng.Graph().EdgesTo(id) {
		if e.Type != "observation_of" {
			continue
		}
		observations++
		obs, ok := eng.Graph().GetNode(e.SourceID)
		if !ok {
			t.Fatalf("observation node %s missing", e.SourceID)
		}
		if author, ok := obs.Properties.GetString("author"); !ok || author != nodeAuthorCuration {
			t.Errorf("observation %s author = %q (present=%v), want %q", e.SourceID, author, ok, nodeAuthorCuration)
		}
	}
	if observations == 0 {
		t.Fatal("expected at least one observation_of edge after successful extraction")
	}
}
