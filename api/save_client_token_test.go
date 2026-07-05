package api

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
)

// setupSaveAPI builds an API with the deterministic dedup embedder
// (same text -> same vector -> cosine 1.0), so single-save dedup and
// idempotency paths run deterministically.
func setupSaveAPI(t testing.TB, customize func(*config.Config)) (*API, *core.Engine) {
	t.Helper()
	a, eng := setupReembedAPI(t, core.WithEmbedder(&dedupEmbedder{dim: 16}), customize)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	return a, eng
}

const testClientToken = "11111111-2222-3333-4444-555555555555"

// TestSaveClientTokenIdempotentReplay pins the core idempotency
// contract: a retry with the same token and body returns the
// original record and creates nothing new.
func TestSaveClientTokenIdempotentReplay(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)

	first, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "idempotent replay seed content",
		ClientToken: testClientToken,
	})
	if apiErr != nil {
		t.Fatalf("first save: %v", apiErr)
	}
	count := eng.NodeCount()

	replay, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "idempotent replay seed content",
		ClientToken: testClientToken,
	})
	if apiErr != nil {
		t.Fatalf("replay save: %v", apiErr)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay returned %s, want original %s", replay.ID, first.ID)
	}
	if got := eng.NodeCount(); got != count {
		t.Fatalf("replay created nodes: count %d, want %d", got, count)
	}
	if len(replay.Superseded) != 0 {
		t.Fatalf("replay reported supersession: %+v", replay.Superseded)
	}
}

// TestSaveClientTokenBodyMismatch pins the misuse contract: the same
// token with a different body is rejected, not silently deduped.
func TestSaveClientTokenBodyMismatch(t *testing.T) {
	a, _ := setupSaveAPI(t, nil)

	if _, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "original body",
		ClientToken: testClientToken,
	}); apiErr != nil {
		t.Fatalf("first save: %v", apiErr)
	}

	_, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "a completely different body",
		ClientToken: testClientToken,
	})
	if apiErr == nil {
		t.Fatal("expected conflict, got success")
	}
	if apiErr.Code != "conflict" {
		t.Fatalf("code = %q, want conflict", apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "client_token") {
		t.Fatalf("message %q should name client_token", apiErr.Message)
	}
}

// TestSaveClientTokenInvalidShape feeds dirty input: a non-UUID
// token must be rejected at validation, before any engine work.
func TestSaveClientTokenInvalidShape(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	count := eng.NodeCount()

	_, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "content with a bad token",
		ClientToken: "not-a-uuid; DROP TABLE nodes",
	})
	if apiErr == nil {
		t.Fatal("expected input_error, got success")
	}
	if apiErr.Code != "input_error" {
		t.Fatalf("code = %q, want input_error", apiErr.Code)
	}
	if got := eng.NodeCount(); got != count {
		t.Fatalf("invalid token created nodes: count %d, want %d", got, count)
	}
}

// TestSaveClientTokenReplayInRejectMode pins the check ordering: in
// dedup.action=reject mode, a replay's duplicate scan finds the very
// record the first attempt created -- the token check must win, so
// the retry gets the original ID instead of a spurious conflict.
func TestSaveClientTokenReplayInRejectMode(t *testing.T) {
	a, _ := setupSaveAPI(t, func(cfg *config.Config) {
		cfg.Dedup.Action = "reject"
	})

	first, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "reject-mode replay content",
		ClientToken: testClientToken,
	})
	if apiErr != nil {
		t.Fatalf("first save: %v", apiErr)
	}

	replay, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "reject-mode replay content",
		ClientToken: testClientToken,
	})
	if apiErr != nil {
		t.Fatalf("replay in reject mode: %v", apiErr)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay returned %s, want original %s", replay.ID, first.ID)
	}
}

// TestSaveBatchItemClientTokenRejected pins that the request-level
// idempotency key cannot be smuggled into batch items (SaveBatchItem
// embeds SaveRequest, which now carries the field).
func TestSaveBatchItemClientTokenRejected(t *testing.T) {
	a, _ := setupSaveAPI(t, nil)

	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{{SaveRequest: SaveRequest{
			Content:     "item content",
			ClientToken: testClientToken,
		}}},
	})
	if apiErr != nil {
		t.Fatalf("batch envelope should succeed with the item failed: %v", apiErr)
	}
	if len(resp.Added) != 0 {
		t.Fatalf("item with client_token was added: %+v", resp.Added)
	}
	if len(resp.Failed) != 1 {
		t.Fatalf("failed = %+v, want exactly one item failure", resp.Failed)
	}
	if !strings.Contains(resp.Failed[0].Message, "batch request") {
		t.Fatalf("message %q should direct callers to the batch request field", resp.Failed[0].Message)
	}
}

// TestSaveDedupRejectLeavesNoResidue pins the reject path after the
// off-lock restructure: the duplicate is refused before any node is
// created, so nothing needs deleting and nothing leaks.
func TestSaveDedupRejectLeavesNoResidue(t *testing.T) {
	a, eng := setupSaveAPI(t, func(cfg *config.Config) {
		cfg.Dedup.Action = "reject"
	})

	if _, apiErr := a.Save(context.Background(), SaveRequest{
		Content: "the reject-mode seed phrase",
	}); apiErr != nil {
		t.Fatalf("seed save: %v", apiErr)
	}
	count := eng.NodeCount()
	vecCount := eng.VecIdx().Len()

	_, apiErr := a.Save(context.Background(), SaveRequest{
		Content: "the reject-mode seed phrase",
	})
	if apiErr == nil {
		t.Fatal("expected duplicate conflict, got success")
	}
	if apiErr.Code != "conflict" {
		t.Fatalf("code = %q, want conflict", apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "potential duplicate") {
		t.Fatalf("message %q should name the duplicate", apiErr.Message)
	}
	if got := eng.NodeCount(); got != count {
		t.Fatalf("rejected save left node residue: count %d, want %d", got, count)
	}
	// The old implementation inserted then deleted, so NodeCount alone
	// cannot distinguish clean-reject from insert-and-cleanup; the
	// vector index catches a leaked entry either way.
	if got := eng.VecIdx().Len(); got != vecCount {
		t.Fatalf("rejected save left vector residue: len %d, want %d", got, vecCount)
	}
}

// TestSaveClientTokenConcurrentSameToken pins the feature's core
// concurrency guarantee: N concurrent saves sharing one token and
// body collapse to exactly one record, and every caller gets that
// record's ID. Deterministic because the lookup-then-insert sits
// inside a single write-lock hold.
func TestSaveClientTokenConcurrentSameToken(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	baseline := eng.NodeCount()

	const writers = 8
	var wg sync.WaitGroup
	ids := make([]string, writers)
	errs := make([]*APIError, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, apiErr := a.Save(context.Background(), SaveRequest{
				Content:     "concurrent same-token content",
				ClientToken: testClientToken,
			})
			ids[i], errs[i] = resp.ID, apiErr
		}(i)
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("writer %d failed: %v", i, e)
		}
	}
	for i := 1; i < writers; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("writer %d got %s, writer 0 got %s: same token must converge on one record", i, ids[i], ids[0])
		}
	}
	if got := eng.NodeCount(); got != baseline+1 {
		t.Fatalf("node count %d, want %d: same-token saves must create exactly one record", got, baseline+1)
	}
}

// TestSaveDedupSupersedeOffLock pins that auto-supersession survives
// the off-lock candidate scan: an identical second save still marks
// the first historical and links it.
func TestSaveDedupSupersedeOffLock(t *testing.T) {
	a, _ := setupSaveAPI(t, nil)

	first, apiErr := a.Save(context.Background(), SaveRequest{
		Content: "the supersession seed phrase",
	})
	if apiErr != nil {
		t.Fatalf("seed save: %v", apiErr)
	}

	second, apiErr := a.Save(context.Background(), SaveRequest{
		Content: "the supersession seed phrase",
	})
	if apiErr != nil {
		t.Fatalf("duplicate save: %v", apiErr)
	}
	if len(second.Superseded) != 1 {
		t.Fatalf("superseded = %+v, want exactly the seed record", second.Superseded)
	}
	if second.Superseded[0].ID != first.ID {
		t.Fatalf("superseded %s, want %s", second.Superseded[0].ID, first.ID)
	}
	if second.Superseded[0].Similarity < 0.99 {
		t.Fatalf("similarity %.3f, want ~1.0 for identical text", second.Superseded[0].Similarity)
	}
}

// TestSaveConcurrentDistinctContent exercises the restructured
// lock choreography (read-locked scan, write-locked commit) under
// the race detector: concurrent saves of distinct content must all
// land, without deadlock and without losing records.
func TestSaveConcurrentDistinctContent(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)
	baseline := eng.NodeCount()

	const writers = 8
	contents := []string{
		"alpha writes about lock choreography",
		"beta records a design decision",
		"gamma stores a research finding",
		"delta files a reference note",
		"epsilon captures an incident report",
		"zeta saves a meeting outcome",
		"eta persists a benchmark result",
		"theta notes a configuration change",
	}

	var wg sync.WaitGroup
	errs := make([]*APIError, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = a.Save(context.Background(), SaveRequest{Content: contents[i]})
		}(i)
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("writer %d failed: %v", i, e)
		}
	}
	// Distinct contents may still collide in the toy hash-embedder's
	// vector space; count both surviving and superseded records so the
	// assertion is about not LOSING writes, not about dedup outcomes.
	if got := eng.NodeCount(); got != baseline+writers {
		t.Fatalf("node count %d, want %d", got, baseline+writers)
	}
}
