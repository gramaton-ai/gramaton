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
	if replay.Held != nil || replay.Advisory != nil {
		t.Fatalf("replay reported similarity-guard output: held=%+v advisory=%+v", replay.Held, replay.Advisory)
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

// TestSaveClientTokenReplayBeatsHold pins the check ordering: a
// replay's similarity scan finds the very record the first attempt
// created -- the token check must win, so the retry gets the original
// ID instead of a spurious hold against its own record.
func TestSaveClientTokenReplayBeatsHold(t *testing.T) {
	a, _ := setupSaveAPI(t, nil)

	first, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "replay-ordering seed content",
		ClientToken: testClientToken,
	})
	if apiErr != nil {
		t.Fatalf("first save: %v", apiErr)
	}

	replay, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "replay-ordering seed content",
		ClientToken: testClientToken,
	})
	if apiErr != nil {
		t.Fatalf("replay: %v", apiErr)
	}
	if replay.Held != nil {
		t.Fatalf("replay was held against its own record: %+v", replay.Held)
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

// TestSaveHoldLeavesNoResidue pins that a held save is refused before
// any node is created: nothing needs deleting and nothing leaks --
// including the client_token, which must not be indexed by a save
// that created nothing (a later save reusing the token with different
// content must not be treated as a replay).
func TestSaveHoldLeavesNoResidue(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)

	if _, apiErr := a.Save(context.Background(), SaveRequest{
		Content: "the hold-residue seed phrase",
	}); apiErr != nil {
		t.Fatalf("seed save: %v", apiErr)
	}
	count := eng.NodeCount()
	vecCount := eng.VecIdx().Len()

	held, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "the hold-residue seed phrase",
		ClientToken: testClientToken,
	})
	if apiErr != nil {
		t.Fatalf("duplicate save: %v", apiErr)
	}
	if held.Held == nil || held.ID != "" {
		t.Fatalf("expected hold with no record, got %+v", held)
	}
	if got := eng.NodeCount(); got != count {
		t.Fatalf("held save left node residue: count %d, want %d", got, count)
	}
	// The old reject implementation inserted then deleted, so
	// NodeCount alone cannot distinguish clean-refusal from
	// insert-and-cleanup; the vector index catches a leaked entry
	// either way.
	if got := eng.VecIdx().Len(); got != vecCount {
		t.Fatalf("held save left vector residue: len %d, want %d", got, vecCount)
	}

	// The token from the held save must not have been indexed: using
	// it with unrelated content is a fresh save, not a replay of a
	// record that was never created.
	fresh, apiErr := a.Save(context.Background(), SaveRequest{
		Content:     "completely unrelated follow-up content",
		ClientToken: testClientToken,
	})
	if apiErr != nil {
		t.Fatalf("follow-up save: %v", apiErr)
	}
	if fresh.ID == "" {
		t.Fatalf("follow-up save with the held token must create a record, got %+v", fresh)
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

// TestSaveHoldOnSimilar pins hold-then-judge: an identical second
// save is held -- nothing created -- and the response carries the
// original record with its full content and a version token; a
// re-send with allow_similar acknowledging that record creates it.
func TestSaveHoldOnSimilar(t *testing.T) {
	a, eng := setupSaveAPI(t, nil)

	const seed = "the similarity guard seed phrase"
	first, apiErr := a.Save(context.Background(), SaveRequest{Content: seed})
	if apiErr != nil {
		t.Fatalf("seed save: %v", apiErr)
	}
	baseline := eng.NodeCount()

	second, apiErr := a.Save(context.Background(), SaveRequest{Content: seed})
	if apiErr != nil {
		t.Fatalf("duplicate save: %v", apiErr)
	}
	if second.ID != "" {
		t.Fatalf("held save must not create a record, got id %s", second.ID)
	}
	if second.Held == nil {
		t.Fatal("expected a hold for identical content")
	}
	if second.Held.ID != first.ID {
		t.Fatalf("held against %s, want %s", second.Held.ID, first.ID)
	}
	if second.Held.Similarity < 0.99 {
		t.Fatalf("similarity %.3f, want ~1.0 for identical text", second.Held.Similarity)
	}
	if second.Held.ContentFull != seed {
		t.Fatalf("hold must carry full content, got %q", second.Held.ContentFull)
	}
	if second.Held.Version == "" {
		t.Fatal("hold must carry a version token")
	}
	if got := eng.NodeCount(); got != baseline {
		t.Fatalf("node count %d after hold, want unchanged %d", got, baseline)
	}

	third, apiErr := a.Save(context.Background(), SaveRequest{
		Content:      seed,
		AllowSimilar: []string{first.ID},
	})
	if apiErr != nil {
		t.Fatalf("acknowledged save: %v", apiErr)
	}
	if third.ID == "" {
		t.Fatalf("acknowledged save must create a record, got held=%+v", third.Held)
	}
	if got := eng.NodeCount(); got != baseline+1 {
		t.Fatalf("node count %d after acknowledged save, want %d", got, baseline+1)
	}
}

// TestSaveHoldAckWrongID pins allow_similar scoping: acknowledging a
// DIFFERENT record does not lift a hold against this one.
func TestSaveHoldAckWrongID(t *testing.T) {
	a, _ := setupSaveAPI(t, nil)

	const seed = "the scoped acknowledgment seed phrase"
	if _, apiErr := a.Save(context.Background(), SaveRequest{Content: seed}); apiErr != nil {
		t.Fatalf("seed save: %v", apiErr)
	}
	resp, apiErr := a.Save(context.Background(), SaveRequest{
		Content:      seed,
		AllowSimilar: []string{"01UNRELATEDRECORDIDXXXXXXX"},
	})
	if apiErr != nil {
		t.Fatalf("save: %v", apiErr)
	}
	if resp.Held == nil {
		t.Fatal("hold must survive an acknowledgment naming a different record")
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
