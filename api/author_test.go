package api

import (
	"context"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// The composed identity every author-configured test engine stamps.
const wantTestAuthor = "Ada Lovelace <ada@example.com>"

// withTestAuthor is a setupReembedAPI customize callback that gives
// the engine an author identity.
func withTestAuthor(cfg *config.Config) {
	cfg.Author = config.AuthorConfig{Name: "Ada Lovelace", Email: "ada@example.com"}
}

// setupAuthorAPI builds an API + engine whose config carries the test
// author identity. No embedder (same as setupTestAPI otherwise).
func setupAuthorAPI(t testing.TB) (*API, *core.Engine) {
	t.Helper()
	return setupReembedAPI(t, nil, withTestAuthor)
}

// nodeAuthor fetches the bare `author` property of a node. ok=false
// means the property is absent.
func nodeAuthor(t *testing.T, eng *core.Engine, id string) (string, bool) {
	t.Helper()
	eng.RLock()
	defer eng.RUnlock()
	n, found := eng.Graph().GetNode(id)
	if !found {
		t.Fatalf("node %s not found", id)
	}
	return n.Properties.GetString("author")
}

func assertAuthor(t *testing.T, eng *core.Engine, id, context string) {
	t.Helper()
	got, ok := nodeAuthor(t, eng, id)
	if !ok {
		t.Fatalf("%s: node %s has no author property, want %q", context, id, wantTestAuthor)
	}
	if got != wantTestAuthor {
		t.Errorf("%s: author = %q, want %q", context, got, wantTestAuthor)
	}
}

// TestSaveStampsAuthor: single-record capture stores the composed
// config identity under the bare `author` key.
func TestSaveStampsAuthor(t *testing.T) {
	a, eng := setupAuthorAPI(t)
	resp, apiErr := a.Save(context.Background(), SaveRequest{Content: "author stamp on save"})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	assertAuthor(t, eng, resp.ID, "save")
}

// TestSaveIndexesAuthor: the stamped author property reaches the
// engine's property index, not just the node. The test engine is
// built through the production construction path (core's newIndexSet
// passes index.DefaultIndexedFields to the bbolt property index), so
// this test fails if "author" is dropped from DefaultIndexedFields:
// isIndexed would silently discard the key on Add and the exact
// lookup below would come back empty.
func TestSaveIndexesAuthor(t *testing.T) {
	a, eng := setupAuthorAPI(t)
	resp, apiErr := a.Save(context.Background(), SaveRequest{Content: "author lands in the property index"})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	assertAuthor(t, eng, resp.ID, "save")

	eng.RLock()
	ids := eng.PropIdx().Lookup("author", graph.StringProperty(wantTestAuthor))
	eng.RUnlock()
	for _, id := range ids {
		if id == resp.ID {
			return
		}
	}
	t.Fatalf("property index lookup author=%q returned %v, want it to contain %s", wantTestAuthor, ids, resp.ID)
}

// TestSaveBlankAuthorStampsNothing: with no author configured the
// property is entirely absent -- not present as an empty string.
func TestSaveBlankAuthorStampsNothing(t *testing.T) {
	a, eng := setupTestAPI(t) // default config: blank AuthorConfig
	resp, apiErr := a.Save(context.Background(), SaveRequest{Content: "no author configured"})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	if got, ok := nodeAuthor(t, eng, resp.ID); ok {
		t.Errorf("blank author config: author property present with value %q, want absent", got)
	}
}

// TestSaveBatchStampsAuthor: the sync batch path stamps every item.
func TestSaveBatchStampsAuthor(t *testing.T) {
	emb := &stubBatchEmbedder{dim: 4}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), withTestAuthor)
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Items:            mustItems("batch author one", "batch author two", "batch author three"),
		SkipSupersession: true,
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	if len(resp.Added) != 3 {
		t.Fatalf("added: got %d want 3", len(resp.Added))
	}
	for i, ad := range resp.Added {
		assertAuthor(t, eng, ad.ID, "save_batch item "+resp.Added[i].ID)
	}
}

// TestSaveBatchChunkedStampsAuthor: the async chunked commit path
// stamps every item (author composed once per chunk).
func TestSaveBatchChunkedStampsAuthor(t *testing.T) {
	emb := &stubBatchEmbedder{dim: 4}
	a, eng := setupReembedAPI(t, core.WithEmbedder(emb), withTestAuthor)
	t.Cleanup(func() { _ = a.ShutdownAsync(context.Background()) })
	a.SetChunkSizeForTests(2)
	t.Cleanup(func() { a.SetChunkSizeForTests(0) })

	f := false
	resp, apiErr := a.SaveBatch(context.Background(), SaveBatchRequest{
		Wait:             &f,
		Items:            chunkedItems(5),
		SkipSupersession: true,
	})
	if apiErr != nil {
		t.Fatalf("SaveBatch: %v", apiErr)
	}
	j := pollUntilTerminal(t, a, resp.JobID, 30*time.Second)
	if len(j.ClientRefToID) != 5 {
		t.Fatalf("ClientRefToID len: %d want 5", len(j.ClientRefToID))
	}
	for ref, id := range j.ClientRefToID {
		assertAuthor(t, eng, id, "chunked batch item "+ref)
	}
}

// TestCollectionPathsStampAuthor: collection create, single-item add,
// and batch add all stamp the configured author.
func TestCollectionPathsStampAuthor(t *testing.T) {
	a, eng := setupAuthorAPI(t)
	ctx := context.Background()

	created, apiErr := a.CollectionCreate(ctx, CollectionCreateRequest{
		Name:        "Author Stamp",
		Description: "collection author stamping",
	})
	if apiErr != nil {
		t.Fatalf("CollectionCreate: %v", apiErr)
	}
	assertAuthor(t, eng, created.ID, "collection node")

	added, apiErr := a.CollectionAdd(ctx, created.ID, CollectionAddRequest{
		Fields: map[string]any{"title": "single item"},
	})
	if apiErr != nil {
		t.Fatalf("CollectionAdd: %v", apiErr)
	}
	assertAuthor(t, eng, added.ID, "collection_add item")

	batch, apiErr := a.CollectionAddBatch(ctx, created.ID, CollectionAddBatchRequest{
		Items: []CollectionAddItem{
			{Fields: map[string]any{"title": "batch item one"}},
			{Fields: map[string]any{"title": "batch item two"}},
		},
	})
	if apiErr != nil {
		t.Fatalf("CollectionAddBatch: %v", apiErr)
	}
	if len(batch.Added) != 2 {
		t.Fatalf("batch added: got %d want 2", len(batch.Added))
	}
	for _, s := range batch.Added {
		assertAuthor(t, eng, s.ID, "collection_add_batch item")
	}
}

// TestSessionFlowStampsAuthor: session start plus session save stamp
// the session node, topic node, segment node, AND the promoted
// Memory record.
func TestSessionFlowStampsAuthor(t *testing.T) {
	a, eng := setupAuthorAPI(t)
	ctx := context.Background()

	started, apiErr := a.SessionStart(ctx, "author-stamp-session", "")
	if apiErr != nil {
		t.Fatalf("SessionStart: %v", apiErr)
	}
	sessionID := started["id"].(string)
	assertAuthor(t, eng, sessionID, "session node")

	if _, apiErr := a.SessionPrepare(ctx, sessionID); apiErr != nil {
		t.Fatalf("SessionPrepare: %v", apiErr)
	}
	if _, apiErr := a.SessionSave(ctx, sessionID, []SaveSegment{
		{TopicName: "author-topic", Content: "decided to stamp authors on every create path"},
	}); apiErr != nil {
		t.Fatalf("SessionSave: %v", apiErr)
	}

	// Locate the topic, segment, and promoted Memory record.
	eng.RLock()
	var topicID, segmentID, memoryID string
	it := eng.Graph().NodeIterator()
	for it.Next() {
		n := it.Node()
		switch kt, _ := n.Properties.GetString("knowledge_type"); kt {
		case "topic":
			topicID = n.ID
		case "segment":
			segmentID = n.ID
			memoryID, _ = n.Properties.GetString("captured_as")
		}
	}
	it.Close()
	eng.RUnlock()

	if topicID == "" || segmentID == "" || memoryID == "" {
		t.Fatalf("session commit incomplete: topic=%q segment=%q memory=%q", topicID, segmentID, memoryID)
	}
	assertAuthor(t, eng, topicID, "topic node")
	assertAuthor(t, eng, segmentID, "segment node")
	assertAuthor(t, eng, memoryID, "promoted memory record")
}

// TestAuthorImmutableAcrossUpdatePaths: no update-family operation may
// write the bare `author` property. A meta {"author": ...} update
// lands under the namespaced meta.author key; classify and resolve
// leave author untouched.
func TestAuthorImmutableAcrossUpdatePaths(t *testing.T) {
	a, eng := setupAuthorAPI(t)
	ctx := context.Background()

	resp, apiErr := a.Save(ctx, SaveRequest{Content: "author immutability probe"})
	if apiErr != nil {
		t.Fatalf("Save: %v", apiErr)
	}
	id := resp.ID
	assertAuthor(t, eng, id, "before update")

	// Update with meta.author: must land namespaced, not overwrite.
	if _, apiErr := a.Update(ctx, UpdateRequest{
		ID:   id,
		Meta: map[string]any{"author": "spoof"},
	}); apiErr != nil {
		t.Fatalf("Update: %v", apiErr)
	}
	assertAuthor(t, eng, id, "after meta update")
	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	metaAuthor, hasMeta := n.Properties.GetString("meta.author")
	eng.RUnlock()
	if !hasMeta || metaAuthor != "spoof" {
		t.Errorf("meta.author = %q (present=%v), want %q under the namespaced key", metaAuthor, hasMeta, "spoof")
	}

	// Classify: reclassification must not touch author.
	conf := 0.9
	if _, apiErr := a.Classify(ctx, ClassifyRequest{
		ID: id, Temporality: "durable", Confidence: &conf,
	}); apiErr != nil {
		t.Fatalf("Classify: %v", apiErr)
	}
	assertAuthor(t, eng, id, "after classify")

	// Resolve: lifecycle closure must not touch author.
	if _, apiErr := a.Resolve(ctx, ResolveRequest{
		ID: id, Resolution: "completed",
	}); apiErr != nil {
		t.Fatalf("Resolve: %v", apiErr)
	}
	assertAuthor(t, eng, id, "after resolve")
}
