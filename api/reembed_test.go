package api

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// configurableEmbedder is a test embedder that succeeds or errors
// based on the next entry in `errors`. Used by the reembed retry-bound
// tests below. Thread-safe so it composes cleanly with parallel embed
// callers (although reembed currently calls Embed sequentially).
type configurableEmbedder struct {
	mu     sync.Mutex
	errors []error // returned in order; nil entries succeed
	calls  int
	dim    int
}

func (e *configurableEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
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
		// Deterministic vector so the test fixture isn't all-zero
		// (some downstream code may reject zero-norm vectors).
		out[i][0] = 1.0
	}
	return out, nil
}

func (e *configurableEmbedder) ModelID() string    { return "configurable-embedder" }
func (e *configurableEmbedder) ContextWindow() int { return 512 }

// setupReembedAPI builds an API + engine wired with the given embedder,
// mirroring setupTestAPI. The customize callback runs against a fresh
// config.Defaults() so individual tests can tweak knobs (e.g.
// MaxEmbedAttempts) before the YAML is saved and the engine loaded.
func setupReembedAPI(t *testing.T, emb core.EngineOption, customize func(*config.Config)) (*API, *core.Engine) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Backup.Dir = t.TempDir() + "/backups"
	if customize != nil {
		customize(&cfg)
	}
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	opts := []core.EngineOption{
		core.WithLLM(noopLLM{}),
		core.WithVectorIndex(index.NewFlatIndex()),
	}
	if emb != nil {
		opts = append(opts, emb)
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, opts)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	a := New(Dependencies{
		Engine:    eng,
		Log:       slog.Default(),
		ConfigDir: dir,
	})
	t.Cleanup(a.StopPreparedSweeper)
	return a, eng
}

// addReembedCandidate seeds a record with content but no
// embedding_model, so the reembed candidate selection picks it up.
func addReembedCandidate(t *testing.T, eng *core.Engine, content string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	props := graph.Properties{
		"content_full":      graph.StringProperty(content),
		"processing_status": graph.StringProperty("processed"),
		"created_at":        graph.TimestampProperty(time.Now().UTC()),
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

// TestReembedFailureBumpsAttemptCounter pins the fix for tracker
// 01KQ408WXSTDN5X15TGE24X416. Pre-fix, an embed failure left the
// record with no embedding_model, so every subsequent gramaton_reembed
// invocation would re-pick it up and re-pay the embed cost. Post-fix,
// the failure writes embed_attempts=1 and last_embed_error.
func TestReembedFailureBumpsAttemptCounter(t *testing.T) {
	emb := &configurableEmbedder{
		errors: []error{fmt.Errorf("API timeout")},
		dim:    4,
	}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	id := addReembedCandidate(t, eng, "content that fails to embed")

	resp, apiErr := a.Reembed(context.Background(), ReembedRequest{Batch: 10})
	if apiErr != nil {
		t.Fatalf("Reembed: %v", apiErr)
	}
	if resp.Errors != 1 {
		t.Fatalf("Errors: got %d, want 1", resp.Errors)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("embed_attempts")
	if attempts != 1 {
		t.Errorf("embed_attempts: got %d, want 1", attempts)
	}
	reason, _ := n.Properties.GetString("last_embed_error")
	if reason == "" {
		t.Error("last_embed_error: empty, want truncated reason")
	}
}

// TestReembedSkipsRecordsAtThreshold verifies that after
// MaxEmbedAttempts consecutive failures, the record is excluded from
// the next invocation's candidate set.
func TestReembedSkipsRecordsAtThreshold(t *testing.T) {
	// Five errors so we have headroom; only the first 3 should fire
	// since the 4th invocation must skip the record.
	emb := &configurableEmbedder{
		errors: []error{
			fmt.Errorf("err 1"), fmt.Errorf("err 2"), fmt.Errorf("err 3"),
			fmt.Errorf("err 4"), fmt.Errorf("err 5"),
		},
		dim: 4,
	}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	id := addReembedCandidate(t, eng, "pathological content")

	for cycle := 1; cycle <= 3; cycle++ {
		resp, apiErr := a.Reembed(context.Background(), ReembedRequest{Batch: 10})
		if apiErr != nil {
			t.Fatalf("cycle %d: Reembed: %v", cycle, apiErr)
		}
		if resp.Errors != 1 {
			t.Errorf("cycle %d: Errors: got %d, want 1", cycle, resp.Errors)
		}
	}

	// Verify counter at threshold.
	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("embed_attempts")
	eng.RUnlock()
	if attempts != 3 {
		t.Fatalf("embed_attempts at threshold: got %d, want 3", attempts)
	}

	// Fourth invocation: the record should NOT be in the candidate set,
	// so the embedder should not be called.
	callsBefore := emb.calls
	resp, apiErr := a.Reembed(context.Background(), ReembedRequest{Batch: 10})
	if apiErr != nil {
		t.Fatalf("4th Reembed: %v", apiErr)
	}
	if emb.calls != callsBefore {
		t.Errorf("at-threshold record was re-attempted: embedder called %d more times, want 0",
			emb.calls-callsBefore)
	}
	if resp.Errors != 0 {
		t.Errorf("4th invocation Errors: got %d, want 0 (no candidates)", resp.Errors)
	}
}

// TestReembedMaxAttemptsZeroDisables verifies legacy behavior: when
// MaxEmbedAttempts=0, no counter is written and failed records re-enter
// the candidate set every invocation.
func TestReembedMaxAttemptsZeroDisables(t *testing.T) {
	emb := &configurableEmbedder{
		errors: []error{fmt.Errorf("err"), fmt.Errorf("err"), fmt.Errorf("err")},
		dim:    4,
	}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), func(cfg *config.Config) {
		cfg.LLMCuration.MaxEmbedAttempts = 0
	})

	id := addReembedCandidate(t, eng, "content")

	// Invoke reembed -- should fail without writing the counter.
	if _, apiErr := a.Reembed(context.Background(), ReembedRequest{Batch: 10}); apiErr != nil {
		t.Fatalf("Reembed: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if _, ok := n.Properties.GetInt64("embed_attempts"); ok {
		t.Error("embed_attempts was written when MaxEmbedAttempts=0")
	}
	if _, ok := n.Properties.GetString("last_embed_error"); ok {
		t.Error("last_embed_error was written when MaxEmbedAttempts=0")
	}
}

// TestReembedSuccessClearsAttempts verifies that a successful re-embed
// on a previously-failing record resets the counter so an
// operator-fixed record passes cleanly on its next run.
func TestReembedSuccessClearsAttempts(t *testing.T) {
	emb := &configurableEmbedder{
		errors: []error{fmt.Errorf("err 1"), fmt.Errorf("err 2"), nil},
		dim:    4,
	}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	id := addReembedCandidate(t, eng, "initially failing content")

	// Fail twice -- attempts now 2.
	for i := 0; i < 2; i++ {
		if _, apiErr := a.Reembed(context.Background(), ReembedRequest{Batch: 10}); apiErr != nil {
			t.Fatalf("cycle %d: Reembed: %v", i, apiErr)
		}
	}

	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	if a, _ := n.Properties.GetInt64("embed_attempts"); a != 2 {
		eng.RUnlock()
		t.Fatalf("intermediate embed_attempts: got %d, want 2", a)
	}
	eng.RUnlock()

	// Now succeed.
	if _, apiErr := a.Reembed(context.Background(), ReembedRequest{Batch: 10}); apiErr != nil {
		t.Fatalf("3rd Reembed: %v", apiErr)
	}

	eng.RLock()
	defer eng.RUnlock()
	n, _ = eng.Graph().GetNode(id)
	attempts, _ := n.Properties.GetInt64("embed_attempts")
	if attempts != 0 {
		t.Errorf("embed_attempts after success: got %d, want 0 (cleared)", attempts)
	}
	model, _ := n.Properties.GetString("embedding_model")
	if model != "configurable-embedder" {
		t.Errorf("embedding_model after success: got %q, want %q", model, "configurable-embedder")
	}
}
