package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/index"
	"github.com/gramaton-ai/gramaton/jobs"
)

// stubBatchEmbedder records calls and returns deterministic vectors.
// nextErr (if non-nil) fails the next batch call; perCallErrs lets a
// test stage a sequence of per-item errors after a batch failure.
type stubBatchEmbedder struct {
	dim        int
	calls      atomic.Int64
	batchCalls atomic.Int64
	itemCalls  atomic.Int64
	failBatch  atomic.Bool
	perItemErr error
}

func (e *stubBatchEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls.Add(1)
	if len(texts) > 1 {
		e.batchCalls.Add(1)
		if e.failBatch.Load() {
			return nil, errors.New("simulated batch embed failure")
		}
	} else {
		e.itemCalls.Add(1)
		if e.perItemErr != nil {
			return nil, e.perItemErr
		}
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = make([]float32, e.dim)
		// Vary the vector by content length so dedup tests get
		// distinct geometries without colliding by accident.
		out[i][0] = float32(len(t)%7+1) * 0.1
		if e.dim > 1 {
			out[i][1] = float32(i%3+1) * 0.1
		}
	}
	return out, nil
}

func (e *stubBatchEmbedder) ModelID() string    { return "stub-batch-embedder" }
func (e *stubBatchEmbedder) ContextWindow() int { return 512 }

// stubFaultInjector returns the first error per phase from its map.
type stubFaultInjector struct {
	errs map[string]error
}

func (f *stubFaultInjector) Inject(phase string) error { return f.errs[phase] }

// setupBatchAPI constructs an API + engine using the stub embedder so
// tests can exercise the embed path without depending on Ollama or
// BERT. Uses setupReembedAPI for the engine wiring.
func setupBatchAPI(t testing.TB) (*API, *core.Engine, *stubBatchEmbedder) {
	t.Helper()
	emb := &stubBatchEmbedder{dim: 4}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), nil)
	return a, eng, emb
}

func mustItems(items ...string) []SaveBatchItem {
	out := make([]SaveBatchItem, len(items))
	for i, c := range items {
		out[i] = SaveBatchItem{
			SaveRequest: SaveRequest{Content: c},
		}
	}
	return out
}

// TestSaveBatchHappyPath: 3 items, all commit, single batch embed,
// Job reaches completed state.
func TestSaveBatchHappyPath(t *testing.T) {
	a, _, emb := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("alpha record", "beta record", "gamma record"),
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if resp.Status != jobs.StatusCompleted {
		t.Errorf("status: got %q want %q", resp.Status, jobs.StatusCompleted)
	}
	if len(resp.Added) != 3 {
		t.Fatalf("added: got %d want 3", len(resp.Added))
	}
	if resp.Stats.TotalItems != 3 || resp.Stats.AddedCount != 3 || resp.Stats.FailedCount != 0 {
		t.Errorf("stats: %+v", resp.Stats)
	}
	if got := emb.batchCalls.Load(); got != 1 {
		t.Errorf("expected 1 batch embed call, got %d", got)
	}
	if got := emb.itemCalls.Load(); got != 0 {
		t.Errorf("expected 0 per-item embed fallback calls, got %d", got)
	}
	for i, ad := range resp.Added {
		if ad.ID == "" {
			t.Errorf("added[%d] missing ID", i)
		}
	}
}

// TestSaveBatchEmpty: zero items rejected with ErrInvalid.
func TestSaveBatchEmpty(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	_, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{Items: nil})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %v", apiErr)
	}
}

// TestSaveBatchOverCap: more than MaxSyncBatchSize items rejected.
func TestSaveBatchOverCap(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	items := make([]SaveBatchItem, MaxSyncBatchSize+1)
	for i := range items {
		items[i] = SaveBatchItem{SaveRequest: SaveRequest{Content: "x"}}
	}
	_, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{Items: items})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %v", apiErr)
	}
	if !strings.Contains(apiErr.Message, fmt.Sprintf("%d", MaxSyncBatchSize)) {
		t.Errorf("expected message to mention cap, got %q", apiErr.Message)
	}
}

// TestSaveBatchByteBudget: total content bytes exceeds
// MaxBatchBytes -> ErrInvalid.
func TestSaveBatchByteBudget(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	// Forge 10 items that together overflow the cap. Use chunks of
	// MaxBatchBytes/9 so the 10th item tips the total past the limit.
	chunkLen := MaxBatchBytes/9 + 1
	chunk := strings.Repeat("a", chunkLen)
	items := make([]SaveBatchItem, 10)
	for i := range items {
		items[i] = SaveBatchItem{SaveRequest: SaveRequest{Content: chunk}}
	}
	_, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{Items: items})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %v", apiErr)
	}
	if !strings.Contains(apiErr.Message, "byte") {
		t.Errorf("message should mention byte budget, got %q", apiErr.Message)
	}
}

// TestSaveBatchValidationFailures: every Phase 0 per-item rule
// produces an entry in Failed[]; valid items still commit.
func TestSaveBatchValidationFailures(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	nan := math.NaN()
	inf := math.Inf(1)
	bad := 2.0
	cases := []struct {
		name string
		item SaveBatchItem
		want string // substring expected in the error message
	}{
		{
			name: "confidence_out_of_range",
			item: SaveBatchItem{SaveRequest: SaveRequest{Content: "c", Confidence: &bad}},
			want: "confidence",
		},
		{
			name: "confidence_nan",
			item: SaveBatchItem{SaveRequest: SaveRequest{Content: "c", Confidence: &nan}},
			want: "finite",
		},
		{
			name: "confidence_inf",
			item: SaveBatchItem{SaveRequest: SaveRequest{Content: "c", Confidence: &inf}},
			want: "finite",
		},
		{
			name: "client_ref_bad_charset",
			item: SaveBatchItem{ClientRef: "bad ref!", SaveRequest: SaveRequest{Content: "c"}},
			want: "client_ref",
		},
		{
			name: "reserved_meta_namespace",
			item: SaveBatchItem{SaveRequest: SaveRequest{Content: "c", Meta: map[string]any{"_gramaton.foo": "bar"}}},
			want: "reserved",
		},
	}
	items := []SaveBatchItem{
		{SaveRequest: SaveRequest{Content: "valid item one"}},
	}
	for _, tc := range cases {
		items = append(items, tc.item)
	}
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{Items: items})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Added) != 1 {
		t.Errorf("added: got %d want 1", len(resp.Added))
	}
	if len(resp.Failed) != len(cases) {
		t.Fatalf("failed: got %d want %d", len(resp.Failed), len(cases))
	}
	for i, tc := range cases {
		f := resp.Failed[i]
		if f.Index != i+1 {
			t.Errorf("%s: index %d != %d", tc.name, f.Index, i+1)
		}
		if !strings.Contains(strings.ToLower(f.Message), strings.ToLower(tc.want)) {
			t.Errorf("%s: message %q missing %q", tc.name, f.Message, tc.want)
		}
	}
}

// TestSaveBatchDuplicateClientRef: two items with the same
// ClientRef in the same batch -> second fails with duplicate_client_ref.
func TestSaveBatchDuplicateClientRef(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{
			{ClientRef: "ref-1", SaveRequest: SaveRequest{Content: "first"}},
			{ClientRef: "ref-1", SaveRequest: SaveRequest{Content: "second"}},
		},
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Added) != 1 || resp.Added[0].ClientRef != "ref-1" {
		t.Errorf("expected first item added with ref-1, got %+v", resp.Added)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].Code != "duplicate_client_ref" {
		t.Errorf("expected duplicate_client_ref failure, got %+v", resp.Failed)
	}
}

// TestSaveBatchReservedNamespace: top-level _gramaton.* key is
// rejected.
func TestSaveBatchReservedNamespace(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{
			{SaveRequest: SaveRequest{Content: "x", Meta: map[string]any{"_gramaton.import.job_id": "fake"}}},
		},
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Failed) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(resp.Failed))
	}
	if !strings.Contains(strings.ToLower(resp.Failed[0].Message), "reserved") {
		t.Errorf("expected reserved namespace message, got %q", resp.Failed[0].Message)
	}
}

// TestSaveBatchReservedNamespaceNested: meta key containing
// `._gramaton.` substring is also rejected (pin: strict policy).
func TestSaveBatchReservedNamespaceNested(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{
			{SaveRequest: SaveRequest{Content: "x", Meta: map[string]any{"foo._gramaton.bar": "baz"}}},
		},
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Failed) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(resp.Failed))
	}
}

// TestSaveBatchEmbedFallback: batch embed fails; per-item fallback
// runs. Each item still commits; warnings collected only when the
// per-item retry also fails.
func TestSaveBatchEmbedFallback(t *testing.T) {
	a, _, emb := setupBatchAPI(t)
	emb.failBatch.Store(true)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("a", "b", "c"),
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Added) != 3 {
		t.Fatalf("added: got %d want 3", len(resp.Added))
	}
	if got := emb.itemCalls.Load(); got != 3 {
		t.Errorf("expected 3 per-item fallback calls, got %d", got)
	}
	for _, ad := range resp.Added {
		for _, w := range ad.Warnings {
			if strings.Contains(w, "embedding failed") {
				t.Errorf("unexpected fallback warning when retry succeeded: %q", w)
			}
		}
	}
}

// TestSaveBatchEmbedFallbackWarnsOnRetryFailure: when both batch and
// per-item embed fail, the item still commits but with a warning.
func TestSaveBatchEmbedFallbackWarnsOnRetryFailure(t *testing.T) {
	a, _, emb := setupBatchAPI(t)
	emb.failBatch.Store(true)
	emb.perItemErr = errors.New("per-item provider down")
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("a"),
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(resp.Added))
	}
	if len(resp.Added[0].Warnings) == 0 {
		t.Errorf("expected an embedding-failed warning, got none")
	}
}

// (TestSaveBatchSupersession moved to capture_batch_review_test.go
// as TestSaveBatchSupersessionDeterministic — the prior version
// gated its assertion on `if len(Superseded) > 0` which the stub
// embedder rarely satisfied, making the test vacuous.)

// TestSaveBatchSkipSupersession: SkipSupersession=true disables
// dedup-driven supersession entirely.
func TestSaveBatchSkipSupersession(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("identical phrase one"),
	})
	if apiErr != nil {
		t.Fatalf("seed: %v", apiErr)
	}
	if len(resp.Added) != 1 {
		t.Fatalf("seed added: %d", len(resp.Added))
	}
	resp2, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items:            mustItems("identical phrase one"),
		SkipSupersession: true,
	})
	if apiErr != nil {
		t.Fatalf("skip: %v", apiErr)
	}
	if len(resp2.Added) != 1 {
		t.Fatalf("skip added: %d", len(resp2.Added))
	}
	if len(resp2.Added[0].Superseded) != 0 {
		t.Errorf("expected no supersession with SkipSupersession=true, got %+v", resp2.Added[0].Superseded)
	}
	if resp2.Stats.SupersededCount != 0 {
		t.Errorf("expected SupersededCount=0, got %d", resp2.Stats.SupersededCount)
	}
}

// TestSaveBatchClientRefEcho: ClientRef round-trips in Added[].
func TestSaveBatchClientRefEcho(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{
			{ClientRef: "alpha-1", SaveRequest: SaveRequest{Content: "first"}},
			{ClientRef: "beta_2", SaveRequest: SaveRequest{Content: "second"}},
		},
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	want := map[string]bool{"alpha-1": false, "beta_2": false}
	for _, ad := range resp.Added {
		want[ad.ClientRef] = true
	}
	for k, found := range want {
		if !found {
			t.Errorf("ClientRef %q not echoed back", k)
		}
	}
}

// TestSaveBatchClientTokenIdempotent: same token + same body
// returns same JobID, no duplicate records.
func TestSaveBatchClientTokenIdempotent(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	tok := "01234567-89ab-cdef-0123-456789abcdef"
	req := SaveBatchRequest{
		ClientToken: tok,
		Items:       mustItems("first call content"),
	}
	resp1, apiErr := a.SaveBatch(context.Background(), req)
	if apiErr != nil {
		t.Fatalf("first: %v", apiErr)
	}
	resp2, apiErr := a.SaveBatch(context.Background(), req)
	if apiErr != nil {
		t.Fatalf("second: %v", apiErr)
	}
	if resp1.JobID != resp2.JobID {
		t.Errorf("expected same JobID, got %q vs %q", resp1.JobID, resp2.JobID)
	}
	// Only one record should exist in the store.
	eng.RLock()
	defer eng.RUnlock()
	if got := len(eng.Graph().AllNodeIDs()); got != 1 {
		t.Errorf("expected 1 stored record, got %d", got)
	}
}

// TestSaveBatchClientTokenHashMismatch: same token, different body
// is rejected with conflict.
func TestSaveBatchClientTokenHashMismatch(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	tok := "01234567-89ab-cdef-0123-456789abcdef"
	if _, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		ClientToken: tok,
		Items:       mustItems("first content"),
	}); apiErr != nil {
		t.Fatalf("first: %v", apiErr)
	}
	_, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		ClientToken: tok,
		Items:       mustItems("DIFFERENT content"),
	})
	if apiErr == nil || apiErr.Code != "conflict" {
		t.Fatalf("expected conflict, got %v", apiErr)
	}
}

// TestSaveBatchClientTokenAfterFailure: a failed prior job's token
// is reusable; the new Job links to the prior via SupersedesJobID.
func TestSaveBatchClientTokenAfterFailure(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	tok := "01234567-89ab-cdef-0123-456789abcdef"

	// First call: inject a Save failure so the job ends in failed.
	a.SetFaultInjector(&stubFaultInjector{errs: map[string]error{
		FaultPhaseChunkSave: errors.New("simulated save failure"),
	}})
	_, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		ClientToken: tok,
		Items:       mustItems("hello world"),
	})
	if apiErr == nil || apiErr.Code != "internal_error" {
		t.Fatalf("expected internal_error from save failure, got %v", apiErr)
	}
	a.SetFaultInjector(nil)

	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		ClientToken: tok,
		Items:       mustItems("hello world"),
	})
	if apiErr != nil {
		t.Fatalf("retry after failure: %v", apiErr)
	}
	if len(resp.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(resp.Added))
	}

	// Inspect the new Job; SupersedesJobID should be set.
	store := eng.JobStore()
	if store == nil {
		t.Fatal("nil jobstore")
	}
	j, err := store.Get(resp.JobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if j.SupersedesJobID == "" {
		t.Error("expected SupersedesJobID on retry-after-failure job")
	}
}

// TestSaveBatchSaveFailureRollsBackVectorIndex: inject a save
// failure; verify the in-memory indexes were rolled back so a search
// finds nothing.
func TestSaveBatchSaveFailureRollsBackVectorIndex(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	a.SetFaultInjector(&stubFaultInjector{errs: map[string]error{
		FaultPhaseChunkSave: errors.New("forced"),
	}})
	defer a.SetFaultInjector(nil)
	_, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("rolled-back content one", "rolled-back content two"),
	})
	if apiErr == nil {
		t.Fatal("expected save failure")
	}
	eng.RLock()
	defer eng.RUnlock()
	if got := len(eng.Graph().AllNodeIDs()); got != 0 {
		t.Errorf("expected 0 nodes after rollback, got %d", got)
	}
}

// TestSaveBatchSaveFailureRollsBackBM25: after Save failure, BM25
// search returns nothing for the rolled-back content.
func TestSaveBatchSaveFailureRollsBackBM25(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	a.SetFaultInjector(&stubFaultInjector{errs: map[string]error{
		FaultPhaseChunkSave: errors.New("forced"),
	}})
	defer a.SetFaultInjector(nil)
	_, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("uniqueterm-xyz123 lorem"),
	})
	if apiErr == nil {
		t.Fatal("expected save failure")
	}
	eng.RLock()
	defer eng.RUnlock()
	hits := eng.BM25Full().Search(index.Tokenize("uniqueterm-xyz123"), 10, nil)
	if len(hits) != 0 {
		t.Errorf("expected 0 BM25 hits after rollback, got %d", len(hits))
	}
}

// TestSaveBatchJobStoreUpdateFailureAfterSave: records ARE on disk
// and queryable; only Job bookkeeping failed.
func TestSaveBatchJobStoreUpdateFailureAfterSave(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	a.SetFaultInjector(&stubFaultInjector{errs: map[string]error{
		FaultPhaseJobstoreUpdate: errors.New("forced"),
	}})
	defer a.SetFaultInjector(nil)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("survives jobstore failure"),
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Added) != 1 {
		t.Fatalf("added: %d", len(resp.Added))
	}
	eng.RLock()
	defer eng.RUnlock()
	if got := len(eng.Graph().AllNodeIDs()); got != 1 {
		t.Errorf("expected record persisted despite jobstore failure, got %d", got)
	}
}

// TestSaveBatchOrphanStamp: every record has
// meta._gramaton.import.job_id matching the Job.
func TestSaveBatchOrphanStamp(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("alpha", "beta"),
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	eng.RLock()
	defer eng.RUnlock()
	for _, ad := range resp.Added {
		n, _ := eng.Graph().GetNode(ad.ID)
		if n == nil {
			t.Fatalf("missing node %s", ad.ID)
		}
		got, ok := n.Properties.GetString("meta._gramaton.import.job_id")
		if !ok || got != resp.JobID {
			t.Errorf("node %s: meta._gramaton.import.job_id = %q (ok=%v); want %q",
				ad.ID, got, ok, resp.JobID)
		}
	}
}

// TestSaveBatchCommitActions: Save emits one ActionSave per
// committed item.
func TestSaveBatchCommitActions(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("one", "two", "three"),
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Added) != 3 {
		t.Fatalf("added: %d", len(resp.Added))
	}
	eng.RLock()
	defer eng.RUnlock()
	commit, err := loadCommitMeta(eng.Store(), eng.HeadHashLocked())
	if err != nil {
		t.Fatalf("loadCommitMeta: %v", err)
	}
	if len(commit.Actions) != 3 {
		t.Errorf("expected 3 ActionSave, got %d", len(commit.Actions))
	}
}

// --- Canonicalization ---

// TestCanonicalizeRequestStable: timestamp normalization, meta key
// reordering, and Wait-field changes all produce identical bytes.
func TestCanonicalizeRequestStable(t *testing.T) {
	mkReq := func(wait *bool, ts string, metaA, metaB any) SaveBatchRequest {
		return SaveBatchRequest{
			Wait: wait,
			Items: []SaveBatchItem{
				{
					ClientRef: "ref",
					SaveRequest: SaveRequest{
						Content:      "x",
						AssertedAsOf: ts,
						Meta: map[string]any{
							"a": metaA,
							"b": metaB,
						},
					},
				},
			},
		}
	}
	tval := true
	fval := false
	a, _ := canonicalizeRequest(mkReq(&tval, "2024-01-01T00:00:00Z", "alpha", "beta"))
	b, _ := canonicalizeRequest(mkReq(&fval, "2024-01-01T00:00:00+00:00", "alpha", "beta"))
	c, _ := canonicalizeRequest(mkReq(nil, "2024-01-01T00:00:00.000Z", "alpha", "beta"))
	if string(a) != string(b) || string(b) != string(c) {
		t.Errorf("canonical mismatch:\n a=%s\n b=%s\n c=%s", a, b, c)
	}
}

// TestCanonicalizeRequestDistinguishes: different content produces
// different canonical bytes.
func TestCanonicalizeRequestDistinguishes(t *testing.T) {
	a, _ := canonicalizeRequest(SaveBatchRequest{Items: mustItems("alpha")})
	b, _ := canonicalizeRequest(SaveBatchRequest{Items: mustItems("beta")})
	if string(a) == string(b) {
		t.Errorf("canonical should differ:\n a=%s\n b=%s", a, b)
	}
}

// TestCanonicalizeRequestSkipSupersessionDistinguishes: SkipSupersession
// affects semantics so it must affect the hash.
func TestCanonicalizeRequestSkipSupersessionDistinguishes(t *testing.T) {
	base := SaveBatchRequest{Items: mustItems("x")}
	a, _ := canonicalizeRequest(base)
	base.SkipSupersession = true
	b, _ := canonicalizeRequest(base)
	if string(a) == string(b) {
		t.Errorf("SkipSupersession should change canonical bytes")
	}
}

// TestValidateClientToken_FormatGate: only UUID-shaped tokens pass.
func TestValidateClientToken_FormatGate(t *testing.T) {
	good := []string{
		"01234567-89ab-cdef-0123-456789abcdef",
		"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE",
	}
	bad := []string{
		"not-a-uuid",
		"01234567-89ab-cdef-0123-456789abcde",   // short
		"01234567-89ab-cdef-0123-456789abcdef0", // long
		"abc",
	}
	for _, t1 := range good {
		if err := validateClientToken(t1); err != nil {
			t.Errorf("good token %q: unexpected err %v", t1, err)
		}
	}
	for _, t1 := range bad {
		if err := validateClientToken(t1); err == nil {
			t.Errorf("bad token %q: expected error, got nil", t1)
		}
	}
}

// TestSaveBatchJobStateOnSuccess: completed job has correct stats.
func TestSaveBatchJobStateOnSuccess(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("one", "two"),
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	j, err := eng.JobStore().Get(resp.JobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if j.Status != jobs.StatusCompleted {
		t.Errorf("status: %q", j.Status)
	}
	if j.TotalItems != 2 || j.ProcessedCount != 2 {
		t.Errorf("counts: total=%d processed=%d", j.TotalItems, j.ProcessedCount)
	}
	if !j.CompletedAt.After(j.StartedAt) && !j.CompletedAt.Equal(j.StartedAt) {
		t.Errorf("CompletedAt %v should be at or after StartedAt %v", j.CompletedAt, j.StartedAt)
	}
}

// TestSaveBatchJobStateOnSaveFailure: failed job has correct
// status + reason.
func TestSaveBatchJobStateOnSaveFailure(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	a.SetFaultInjector(&stubFaultInjector{errs: map[string]error{
		FaultPhaseChunkSave: errors.New("forced"),
	}})
	defer a.SetFaultInjector(nil)
	_, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: mustItems("x"),
	})
	if apiErr == nil {
		t.Fatal("expected error")
	}
	// Find the most recent job by listing all and picking newest.
	all, err := eng.JobStore().List(jobs.ListFilter{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no jobs in store")
	}
	j, _ := eng.JobStore().Get(all[0].ID)
	if j.Status != jobs.StatusFailed {
		t.Errorf("status: %q", j.Status)
	}
	if j.FailureReason != "save_failed" {
		t.Errorf("reason: %q", j.FailureReason)
	}
}

// TestSaveBatchClientTokenInvalidShape: non-UUID token rejected
// at the envelope level.
func TestSaveBatchClientTokenInvalidShape(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	_, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		ClientToken: "not-a-uuid",
		Items:       mustItems("x"),
	})
	if apiErr == nil || apiErr.Code != "input_error" {
		t.Fatalf("expected input_error, got %v", apiErr)
	}
}

// TestSaveBatchAsyncReturnsPending: wait=false returns a JobID
// with status=pending immediately; the async runner picks it up and
// commits the work in the background.
func TestSaveBatchAsyncReturnsPending(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	f := false
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:  &f,
		Items: mustItems("x"),
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if resp.JobID == "" {
		t.Error("expected JobID")
	}
	if resp.Status != jobs.StatusPending && resp.Status != jobs.StatusRunning {
		t.Errorf("expected pending or running, got %q", resp.Status)
	}
	// Drain so the runner doesn't outlive the test.
	if err := a.ShutdownAsync(context.Background()); err != nil {
		t.Errorf("ShutdownAsync: %v", err)
	}
}

// TestSaveBatchLockHoldTime: ratio gate. Compare batch lock-hold
// against N * single-capture lock-hold; should be cheaper, not 3x
// worse.
func TestSaveBatchLockHoldTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping lock-hold gate in -short mode")
	}
	a1, _, _ := setupBatchAPI(t)
	t1Start := time.Now()
	for i := 0; i < 10; i++ {
		_, apiErr := a1.Save(context.Background(), SaveRequest{Content: fmt.Sprintf("baseline %d", i)})
		if apiErr != nil {
			t.Fatalf("baseline capture %d: %v", i, apiErr)
		}
	}
	t1 := time.Since(t1Start) / 10

	a2, _, _ := setupBatchAPI(t)
	items := make([]SaveBatchItem, 10)
	for i := range items {
		items[i] = SaveBatchItem{SaveRequest: SaveRequest{Content: fmt.Sprintf("batch %d", i)}}
	}
	tBatchStart := time.Now()
	if _, apiErr := a2.SaveBatch(context.Background(), SaveBatchRequest{Items: items}); apiErr != nil {
		t.Fatalf("batch: %v", apiErr)
	}
	tBatch := time.Since(tBatchStart)

	// Batch should be faster than 3*N*t1; this is a wide gate so CI
	// noise doesn't make it flake.
	bound := 3 * 10 * t1
	if tBatch > bound {
		t.Errorf("batch wall-clock %v exceeded bound 3*N*t1 = %v (t1=%v)", tBatch, bound, t1)
	}
	t.Logf("batch=%v vs serial=10*%v = %v", tBatch, t1, 10*t1)
}

// BenchmarkSaveSequential / BenchmarkSaveBatch: the former
// TestSaveBatchWallClockSpeedup par-vs-seq comparison, moved out of
// the test budget (#54). Its assertion had been informational-only
// (t.Logf) since the #36 softening, so no regression protection is
// lost; batch-path correctness is covered by the TestSaveBatch*
// suite above. Compare per-item cost with:
//
//	go test -run '^$' -bench 'SaveSequential|SaveBatch' ./api/
//
// Contents are unique per iteration so dedup never enters the path.
//
// The fixture runs under core.WithVolatileStorage, so these numbers
// measure the CPU-side cost only: production sequential saves also
// pay per-record fsyncs that batching amortizes, so the batch
// advantage here UNDERSTATES the production one.
func BenchmarkSaveSequential(b *testing.B) {
	a, _, _ := setupBatchAPI(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, apiErr := a.Save(ctx, SaveRequest{Content: fmt.Sprintf("seq %d", i)}); apiErr != nil {
			b.Fatalf("save[%d]: %v", i, apiErr)
		}
	}
}

// BenchmarkSaveBatch commits one 50-item batch per iteration; the
// extra ns/item metric is the number to hold against
// BenchmarkSaveSequential's ns/op.
func BenchmarkSaveBatch(b *testing.B) {
	a, _, _ := setupBatchAPI(b)
	ctx := context.Background()
	const batchSize = 50
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items := make([]SaveBatchItem, batchSize)
		for j := range items {
			items[j] = SaveBatchItem{SaveRequest: SaveRequest{Content: fmt.Sprintf("par %d-%d", i, j)}}
		}
		if _, apiErr := a.SaveBatch(ctx, SaveBatchRequest{Items: items}); apiErr != nil {
			b.Fatalf("batch[%d]: %v", i, apiErr)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batchSize), "ns/item")
}

// TestSaveBatchIdempotentResponseShape: the cached response from a
// prior identical call decodes the same Added/Failed/Stats.
func TestSaveBatchIdempotentResponseShape(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	tok := "01234567-89ab-cdef-0123-456789abcdef"
	req := SaveBatchRequest{
		ClientToken: tok,
		Items:       mustItems("alpha", "beta", "gamma"),
	}
	resp1, apiErr := a.SaveBatch(context.Background(), req)
	if apiErr != nil {
		t.Fatalf("first: %v", apiErr)
	}
	resp2, apiErr := a.SaveBatch(context.Background(), req)
	if apiErr != nil {
		t.Fatalf("second: %v", apiErr)
	}
	if resp1.JobID != resp2.JobID {
		t.Errorf("JobID changed between idempotent calls")
	}
	if len(resp2.Added) != len(resp1.Added) {
		t.Errorf("Added len: got %d want %d", len(resp2.Added), len(resp1.Added))
	}
	if resp2.Stats != resp1.Stats {
		t.Errorf("stats: %+v vs %+v", resp2.Stats, resp1.Stats)
	}
}

// TestSaveBatchResponseJSONShape: response marshals cleanly with
// the documented field names.
func TestSaveBatchResponseJSONShape(t *testing.T) {
	resp := SaveBatchResponse{
		JobID:  "job-1",
		Status: "completed",
		Added: []CaptureBatchAdded{
			{ID: "n1", ClientRef: "ref"},
		},
		Failed: []BatchItemFailure{
			{Index: 1, Code: "input_error", Message: "msg"},
		},
		Stats: CaptureBatchStats{TotalItems: 2, AddedCount: 1, FailedCount: 1},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := []string{`"job_id":"job-1"`, `"status":"completed"`, `"added":[`, `"failed":[`, `"stats":`, `"client_ref":"ref"`, `"total_items":2`}
	got := string(data)
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("response JSON missing %q: %s", w, got)
		}
	}
}
