package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/jobs"
	"github.com/gramaton-ai/gramaton/testutil"
)

// chunkedItems builds N CaptureBatchItems each with a unique
// ClientRef ref-i and content "chunked-i".
func chunkedItems(n int) []SaveBatchItem {
	out := make([]SaveBatchItem, n)
	for i := range out {
		out[i] = SaveBatchItem{
			ClientRef:   fmt.Sprintf("ref-%d", i),
			SaveRequest: SaveRequest{Content: fmt.Sprintf("chunked-%d", i)},
		}
	}
	return out
}

// TestSaveBatchChunkedHappyPath: 30 items in 3 chunks of 10. All
// items commit; processed_count reaches 30; status = completed. Uses
// the test-only chunk-size override so the test exercises multi-chunk
// behavior without seeding 1500 items (the production cap).
func TestSaveBatchChunkedHappyPath(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	const N = 30
	f := false
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            chunkedItems(N),
		AllowSimilar: true,
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}

	j := pollUntilTerminal(t, a, resp.JobID, 30*time.Second)
	if j.Status != jobs.StatusCompleted {
		t.Fatalf("status: %q want completed (reason=%q)", j.Status, j.FailureReason)
	}
	if j.ProcessedCount != N {
		t.Errorf("processed: %d want %d", j.ProcessedCount, N)
	}
	if len(j.ClientRefToID) != N {
		t.Errorf("ClientRefToID len: %d want %d", len(j.ClientRefToID), N)
	}
}

// TestSaveBatchChunkedCrossChunkEdges: items span chunks; edges
// reference ClientRefs across chunk boundaries; all edges land in
// the post-chunks fixup commit.
func TestSaveBatchChunkedCrossChunkEdges(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	const N = 30 // 3 chunks of 10
	items := chunkedItems(N)
	// 5 edges: each connects an item in chunk 0 (idx 0..9) to one
	// in chunk 2 (idx 20..29).
	const numEdges = 5
	edges := make([]EdgeSpec, numEdges)
	for i := range edges {
		edges[i] = EdgeSpec{
			SourceClientRef: fmt.Sprintf("ref-%d", i),    // chunk 0
			TargetClientRef: fmt.Sprintf("ref-%d", 20+i), // chunk 2
			Type:            "related_to",
		}
	}

	f := false
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            items,
		Edges:            edges,
		AllowSimilar: true,
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}

	pollUntilTerminal(t, a, resp.JobID, 30*time.Second)

	// Fetch the full result
	full, apiErr := a.SaveBatchResult(context.Background(), SaveBatchResultRequest{
		JobID:     resp.JobID,
		TimeoutMS: 10000,
	})
	if apiErr != nil {
		t.Fatalf("Result: %v", apiErr)
	}
	if full.Status != jobs.StatusCompleted {
		t.Fatalf("status: %q want completed", full.Status)
	}
	if full.Stats.EdgesAdded != numEdges {
		t.Errorf("EdgesAdded: %d want %d", full.Stats.EdgesAdded, numEdges)
	}
	if full.Stats.EdgesFailed != 0 {
		t.Errorf("EdgesFailed: %d want 0; %+v", full.Stats.EdgesFailed, full.EdgesFailed)
	}
	for _, ea := range full.Edges {
		if ea.SourceID == "" || ea.TargetID == "" {
			t.Errorf("edge[%d] missing endpoint id: %+v", ea.Index, ea)
		}
	}
}

// TestSaveBatchChunkedFailedItemEdge: a cross-chunk edge whose
// target item failed Phase 0 validation surfaces as target_item_failed
// in EdgesFailed; the corresponding item lands in Failed[].
func TestSaveBatchChunkedFailedItemEdge(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	const N = 30 // 3 chunks of 10
	items := chunkedItems(N)
	// Make item 15 (in chunk 1) invalid: confidence out of range.
	bad := 2.0
	items[15].Confidence = &bad

	edges := []EdgeSpec{
		{SourceClientRef: "ref-1", TargetClientRef: "ref-15", Type: "related_to"}, // points at failed item
		{SourceClientRef: "ref-2", TargetClientRef: "ref-25", Type: "related_to"}, // valid cross-chunk
	}

	f := false
	resp, _ := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            items,
		Edges:            edges,
		AllowSimilar: true,
	})
	pollUntilTerminal(t, a, resp.JobID, 30*time.Second)

	full, apiErr := a.SaveBatchResult(context.Background(), SaveBatchResultRequest{
		JobID: resp.JobID, TimeoutMS: 5000,
	})
	if apiErr != nil {
		t.Fatalf("Result: %v", apiErr)
	}

	// One edge added, one failed with target_item_failed.
	if len(full.Edges) != 1 {
		t.Errorf("edges added: %d want 1", len(full.Edges))
	}
	if len(full.EdgesFailed) != 1 || full.EdgesFailed[0].Code != "target_item_failed" {
		t.Errorf("expected one target_item_failed edge: %+v", full.EdgesFailed)
	}

	// Item 15 in Failed[].
	foundFailedItem := false
	for _, fa := range full.Failed {
		if fa.Index == 15 {
			foundFailedItem = true
		}
	}
	if !foundFailedItem {
		t.Error("expected failed item 15 in Failed[]")
	}
}

// TestSaveBatchChunkedEdgeFixupFailure: inject FaultPhaseEdgeFixup;
// every node chunk lands on disk; status flips to
// failed/edge_fixup_failed; Result.Added has every node chunk;
// Result.EdgesFailed lists every edge with code fixup_failed.
func TestSaveBatchChunkedEdgeFixupFailure(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	a.SetFaultInjector(&stubFaultInjector{errs: map[string]error{
		FaultPhaseEdgeFixup: errors.New("forced fixup failure"),
	}})
	defer a.SetFaultInjector(nil)

	const N = 30 // 3 chunks of 10
	items := chunkedItems(N)
	edges := []EdgeSpec{
		{SourceClientRef: "ref-0", TargetClientRef: "ref-15", Type: "related_to"},
		{SourceClientRef: "ref-5", TargetClientRef: "ref-25", Type: "supports"},
	}

	f := false
	resp, _ := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            items,
		Edges:            edges,
		AllowSimilar: true,
	})
	j := pollUntilTerminal(t, a, resp.JobID, 30*time.Second)
	if j.Status != jobs.StatusFailed {
		t.Fatalf("status: %q want failed", j.Status)
	}
	if j.FailureReason != "edge_fixup_failed" {
		t.Errorf("reason: %q want edge_fixup_failed", j.FailureReason)
	}

	// Records ARE in store (every node chunk landed).
	eng.RLock()
	gotNodes := len(eng.Graph().AllNodeIDs())
	eng.RUnlock()
	if gotNodes != N {
		t.Errorf("expected %d nodes (chunks committed before fixup), got %d", N, gotNodes)
	}

	// Result has Added + EdgesFailed populated.
	full, _ := a.SaveBatchResult(context.Background(), SaveBatchResultRequest{
		JobID: resp.JobID, TimeoutMS: 5000,
	})
	if full.Stats.AddedCount != N {
		t.Errorf("Added: %d want %d", full.Stats.AddedCount, N)
	}
	if full.Stats.EdgesAdded != 0 {
		t.Errorf("EdgesAdded: %d want 0", full.Stats.EdgesAdded)
	}
	if full.Stats.EdgesFailed != 2 {
		t.Errorf("EdgesFailed: %d want 2", full.Stats.EdgesFailed)
	}
	for _, ef := range full.EdgesFailed {
		if ef.Code != "fixup_failed" {
			t.Errorf("edge[%d] code: %q want fixup_failed", ef.Index, ef.Code)
		}
	}
}

// TestSaveBatchChunkedManualEdgeRecovery: documents the recovery
// path. After an edge_fixup_failed Job, a caller iterates
// Result.EdgesFailed and replays each via gramaton_link.
func TestSaveBatchChunkedManualEdgeRecovery(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	a.SetFaultInjector(&stubFaultInjector{errs: map[string]error{
		FaultPhaseEdgeFixup: errors.New("simulated fixup failure"),
	}})

	const N = 30
	items := chunkedItems(N)
	edges := []EdgeSpec{
		{SourceClientRef: "ref-0", TargetClientRef: "ref-15", Type: "related_to"},
		{SourceClientRef: "ref-5", TargetClientRef: "ref-25", Type: "supports"},
	}
	f := false
	resp, _ := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            items,
		Edges:            edges,
		AllowSimilar: true,
	})
	pollUntilTerminal(t, a, resp.JobID, 30*time.Second)

	full, _ := a.SaveBatchResult(context.Background(), SaveBatchResultRequest{
		JobID: resp.JobID, TimeoutMS: 5000,
	})

	// Now clear the injector and replay each failed edge via Link.
	a.SetFaultInjector(nil)
	refToID := map[string]string{}
	for _, ad := range full.Added {
		if ad.ClientRef != "" {
			refToID[ad.ClientRef] = ad.ID
		}
	}
	for _, ef := range full.EdgesFailed {
		// Map back through the original Edges request.
		spec := edges[ef.Index]
		src := refToID[spec.SourceClientRef]
		tgt := refToID[spec.TargetClientRef]
		if src == "" || tgt == "" {
			t.Fatalf("edge[%d]: couldn't resolve refs (src=%q tgt=%q)", ef.Index, src, tgt)
		}
		_, apiErr := a.Link(context.Background(), LinkRequest{
			SourceID: src, TargetID: tgt, EdgeType: spec.Type,
		})
		if apiErr != nil {
			t.Errorf("Link replay edge[%d]: %v", ef.Index, apiErr)
		}
	}
}

// TestSaveBatchChunkedPerChunkProgress: a status reader sees
// processed_count grow as chunks land. Uses the blockingInjector to
// pause inside chunk_save so we can observe a mid-batch snapshot.
func TestSaveBatchChunkedPerChunkProgress(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	inj := newBlockingInjector()
	release := inj.blockOn(FaultPhaseChunkSave)
	a.SetFaultInjector(inj)
	defer a.SetFaultInjector(nil)

	// Smaller batch (3 chunks of small size) so the runner reaches
	// chunk_save quickly in this race-tolerant test. The semantics
	// are validated regardless of N: ProcessedCount=0 before first
	// chunk commits, =N after last.
	const N = 30 // 3 chunks of: chunkSize10 + chunkSize10 + chunkSize10
	t.Setenv("GRAMATON_TEST_CHUNK_SIZE", "10")
	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	f := false
	resp, _ := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            chunkedItems(N),
		AllowSimilar: true,
	})
	// Wait for the runner to enter the first chunk's chunk_save.
	inj.waitEntered(t, FaultPhaseChunkSave, 10*time.Second)

	// Pre-first-chunk-commit snapshot: ProcessedCount=0.
	st0, _ := a.SaveBatchStatus(context.Background(), SaveBatchStatusRequest{JobID: resp.JobID})
	if st0.ProcessedCount != 0 {
		t.Errorf("pre-first-chunk processed: %d want 0", st0.ProcessedCount)
	}

	// Release the runner.
	release()
	pollUntilTerminal(t, a, resp.JobID, 30*time.Second)

	// Final snapshot: ProcessedCount = N.
	st1, _ := a.SaveBatchStatus(context.Background(), SaveBatchStatusRequest{JobID: resp.JobID})
	if st1.ProcessedCount != N {
		t.Errorf("final processed: %d want %d", st1.ProcessedCount, N)
	}
}

// TestSaveBatchChunkedCancelMidImport: cancel arrives while runner
// is parked at chunk 1's chunk_save. The runner releases, chunk 1
// lands, then shouldStopChunked observes the cancel before chunk 2
// runs. Status = cancelled with partial state.
func TestSaveBatchChunkedCancelMidImport(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	inj := newBlockingInjector()
	release := inj.blockOn(FaultPhaseChunkSave)
	a.SetFaultInjector(inj)
	defer a.SetFaultInjector(nil)

	const N = 30 // 3 chunks of 10
	f := false
	resp, _ := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            chunkedItems(N),
		AllowSimilar: true,
	})
	inj.waitEntered(t, FaultPhaseChunkSave, 10*time.Second)

	// Cancel before the first chunk commits.
	c, apiErr := a.SaveBatchCancel(context.Background(), SaveBatchCancelRequest{JobID: resp.JobID})
	if apiErr != nil {
		t.Fatalf("Cancel: %v", apiErr)
	}
	if !c.Cancelled {
		t.Error("expected Cancelled=true")
	}

	// Release the runner — it should observe cancel and exit before
	// any chunk lands. (The first chunk's chunk_save hook returns
	// nil since we didn't setErr; Save runs and chunk 1 lands. Then
	// shouldStopChunked observes status=cancelled and the runner
	// finalizes with chunk 1's progress.)
	release()
	pollUntilTerminal(t, a, resp.JobID, 30*time.Second)

	j, _ := a.engine.JobStore().Get(resp.JobID)
	if j.Status != jobs.StatusCancelled {
		t.Fatalf("status: %q want cancelled (reason=%q)", j.Status, j.FailureReason)
	}

	eng.RLock()
	gotNodes := len(eng.Graph().AllNodeIDs())
	eng.RUnlock()
	if gotNodes >= N {
		t.Errorf("expected partial commit, got all %d nodes", gotNodes)
	}
	if gotNodes == 0 {
		t.Error("expected first chunk to have committed before cancel was observed")
	}
}

// TestSaveBatchChunkedChunk2SaveFailure: inject save failure on
// the second chunk specifically. Chunk 1 stays on disk; chunk 2's
// nodes roll back; status = failed/chunk_2_save_failed.
func TestSaveBatchChunkedChunk2SaveFailure(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	// Custom injector that fails ONLY on the second chunk_save call.
	inj := &chunkNumInjector{phase: FaultPhaseChunkSave, failOn: 2, err: errors.New("forced chunk-2 failure")}
	a.SetFaultInjector(inj)
	defer a.SetFaultInjector(nil)

	const N = 30 // 3 chunks of 10
	f := false
	resp, _ := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            chunkedItems(N),
		AllowSimilar: true,
	})
	j := pollUntilTerminal(t, a, resp.JobID, 30*time.Second)
	if j.Status != jobs.StatusFailed {
		t.Fatalf("status: %q want failed", j.Status)
	}
	if j.FailureReason != "chunk_2_save_failed" {
		t.Errorf("reason: %q want chunk_2_save_failed", j.FailureReason)
	}

	// Chunk 1 (10 items) committed; chunks 2 & 3 didn't.
	eng.RLock()
	gotNodes := len(eng.Graph().AllNodeIDs())
	eng.RUnlock()
	if gotNodes != 10 {
		t.Errorf("expected 10 nodes (chunk 1 only), got %d", gotNodes)
	}
}

// chunkNumInjector returns err only on the Nth call to the named phase.
type chunkNumInjector struct {
	phase  string
	failOn int
	err    error
	mu     sync.Mutex
	count  int
}

func (c *chunkNumInjector) Inject(phase string) error {
	if phase != c.phase {
		return nil
	}
	c.mu.Lock()
	c.count++
	hit := c.count == c.failOn
	c.mu.Unlock()
	if hit {
		return c.err
	}
	return nil
}

// TestChunkRanges sanity-checks the chunker.
func TestChunkRanges(t *testing.T) {
	cases := []struct {
		total, size int
		want        []chunkRange
	}{
		{0, 500, nil},
		{1, 500, []chunkRange{{0, 1}}},
		{500, 500, []chunkRange{{0, 500}}},
		{501, 500, []chunkRange{{0, 500}, {500, 501}}},
		{1500, 500, []chunkRange{{0, 500}, {500, 1000}, {1000, 1500}}},
		{1100, 500, []chunkRange{{0, 500}, {500, 1000}, {1000, 1100}}},
	}
	for _, tc := range cases {
		got := chunkRanges(tc.total, tc.size)
		if len(got) != len(tc.want) {
			t.Errorf("chunkRanges(%d,%d): got %v, want %v", tc.total, tc.size, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("chunkRanges(%d,%d)[%d]: got %v, want %v",
					tc.total, tc.size, i, got[i], tc.want[i])
			}
		}
	}
}

// TestSaveBatchChunkedSyncStillSingleSave confirms the sync path
// (Wait=true, the default) is unchanged: items + edges land in ONE
// commit, not two. L6 only restructures the async path.
func TestSaveBatchChunkedSyncStillSingleSave(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items: []SaveBatchItem{
			{ClientRef: "ref-0", SaveRequest: SaveRequest{Content: "a"}},
			{ClientRef: "ref-1", SaveRequest: SaveRequest{Content: "b"}},
		},
		Edges: []EdgeSpec{
			{SourceClientRef: "ref-0", TargetClientRef: "ref-1", Type: "related_to"},
		},
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Edges) != 1 {
		t.Fatalf("edges: %d want 1", len(resp.Edges))
	}

	// Walk the graph: HEAD commit must contain BOTH ActionSave (2)
	// and ActionLink (1) — sync still writes one combined commit.
	eng.RLock()
	defer eng.RUnlock()
	commit, err := loadCommitMeta(eng.Store(), eng.HeadHashLocked())
	if err != nil {
		t.Fatalf("loadCommitMeta: %v", err)
	}
	if len(commit.Actions) != 3 {
		t.Errorf("sync path expected 3 actions in one commit, got %d", len(commit.Actions))
	}
}

// (Sanity) TestSaveBatchChunkedAsyncTwoCommitsForItemsAndEdges
// confirms the async path now writes the items in chunks AND a
// separate fixup commit for edges.
func TestSaveBatchChunkedAsyncTwoCommitsForItemsAndEdges(t *testing.T) {
	a, eng, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	preHead := func() string {
		eng.RLock()
		defer eng.RUnlock()
		return eng.HeadHashLocked()
	}()

	const N = 20 // 2 chunks of 10
	items := chunkedItems(N)
	edges := []EdgeSpec{
		{SourceClientRef: "ref-0", TargetClientRef: "ref-15", Type: "related_to"},
	}
	f := false
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            items,
		Edges:            edges,
		AllowSimilar: true,
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	pollUntilTerminal(t, a, resp.JobID, 30*time.Second)

	// Walk commits from current HEAD back to preHead. Should be at
	// least 3: chunk 1, chunk 2, edge fixup. (Could be more if other
	// background commits land — but in this isolated test the
	// runner's commits are the only writers.)
	eng.RLock()
	defer eng.RUnlock()
	hash := eng.HeadHashLocked()
	count := 0
	for hash != preHead && hash != "" && count < 10 {
		commit, err := loadCommitMeta(eng.Store(), hash)
		if err != nil {
			t.Fatalf("loadCommitMeta: %v", err)
		}
		count++
		if commit.Parent == "" {
			break
		}
		hash = commit.Parent
	}
	if count < 3 {
		t.Errorf("expected at least 3 commits (2 item chunks + 1 edge fixup), got %d", count)
	}
}

// TestSaveBatchChunkedRefMapPersistedAcrossChunks: after each
// chunk, Job.ClientRefToID accumulates new refs from the just-
// committed chunk so a Status reader sees a consistent partial map.
func TestSaveBatchChunkedRefMapPersistedAcrossChunks(t *testing.T) {
	a, _, _ := setupBatchAPI(t)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })

	a.SetChunkSizeForTests(10)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	// Block on chunk 2's chunk_save (chunk 1 has fully committed
	// when we observe the parked state).
	once := &chunkNumBlocker{phase: FaultPhaseChunkSave, blockOn: 2}
	a.SetFaultInjector(once)
	defer func() { once.release(); a.SetFaultInjector(nil) }()

	const N = 30 // 3 chunks of 10
	f := false
	resp, _ := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            chunkedItems(N),
		AllowSimilar: true,
	})

	// Wait for the runner to be parked at chunk 2's chunk_save.
	once.waitParked(t, 10*time.Second)

	// Status snapshot should reflect chunk 1's refs (10) only.
	st, _ := a.SaveBatchStatus(context.Background(), SaveBatchStatusRequest{JobID: resp.JobID})
	if len(st.ClientRefToID) != 10 {
		t.Errorf("after chunk 1: ClientRefToID=%d want 10", len(st.ClientRefToID))
	}
	if !strings.HasPrefix(st.ClientRefToID["ref-0"], "01") {
		t.Errorf("expected ULID for ref-0, got %q", st.ClientRefToID["ref-0"])
	}

	once.release()
	pollUntilTerminal(t, a, resp.JobID, 30*time.Second)

	st = pollStatus(t, a, resp.JobID, 5*time.Second)
	if len(st.ClientRefToID) != N {
		t.Errorf("final: ClientRefToID=%d want %d", len(st.ClientRefToID), N)
	}
}

// chunkNumBlocker blocks on the Nth call to phase, then unblocks
// when release() is called.
type chunkNumBlocker struct {
	phase   string
	blockOn int
	mu      sync.Mutex
	count   int
	parked  chan struct{}
	gate    chan struct{}
	once    bool
}

func (b *chunkNumBlocker) Inject(phase string) error {
	if phase != b.phase {
		return nil
	}
	b.mu.Lock()
	b.count++
	shouldBlock := b.count == b.blockOn
	if shouldBlock {
		b.parked = make(chan struct{})
		b.gate = make(chan struct{})
	}
	parked := b.parked
	gate := b.gate
	b.mu.Unlock()
	if shouldBlock {
		close(parked)
		<-gate
	}
	return nil
}

func (b *chunkNumBlocker) waitParked(t *testing.T, within time.Duration) {
	t.Helper()
	// Scale the budget for Windows runners under race detector. Same
	// pattern as pollUntilTerminal in capture_batch_async_test.go:20.
	// On POSIX this is a no-op; on Windows the underlying chunk Save +
	// AdvanceStatus + chunk-2 prep can spill past 10s under load.
	within = testutil.Timeout(within)
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		ch := b.parked
		b.mu.Unlock()
		if ch != nil {
			<-ch
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.mu.Lock()
	count := b.count
	b.mu.Unlock()
	t.Fatalf("chunkNumBlocker: never parked at chunk %d (count=%d)", b.blockOn, count)
}

func (b *chunkNumBlocker) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gate != nil && !b.once {
		close(b.gate)
		b.once = true
	}
}

func pollStatus(t *testing.T, a *API, jobID string, within time.Duration) SaveBatchStatusResponse {
	t.Helper()
	st, apiErr := a.SaveBatchStatus(context.Background(), SaveBatchStatusRequest{JobID: jobID})
	if apiErr != nil {
		t.Fatalf("Status: %v", apiErr)
	}
	return st
}
