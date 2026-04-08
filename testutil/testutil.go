// Package testutil provides shared test helpers and realistic fixtures
// for the gramaton test suite. Import this package in any *_test.go
// file to get access to engine setup, record builders, and a
// pre-populated store with ~50 records.
package testutil

import (
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// NewEngine creates a minimal engine backed by a temp directory.
// No embedding or LLM providers configured.
func NewEngine(t *testing.T) *core.Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	eng, err := core.LoadEngine(dir)
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	return eng
}

// RecordBuilder constructs a graph node with a fluent API.
type RecordBuilder struct {
	props     graph.Properties
	embedding []float32
}

// Record starts building a new record with the given content.
func Record(content string) *RecordBuilder {
	return &RecordBuilder{
		props: graph.Properties{
			"content_full":      graph.StringProperty(content),
			"processing_status": graph.StringProperty("processed"),
			"created_at":        graph.TimestampProperty(time.Now().UTC()),
			"access_count":      graph.Int64Property(0),
		},
	}
}

func (b *RecordBuilder) Temporality(v string) *RecordBuilder {
	b.props["temporality"] = graph.StringProperty(v)
	return b
}

func (b *RecordBuilder) Confidence(v float64) *RecordBuilder {
	b.props["confidence"] = graph.Float64Property(v)
	return b
}

func (b *RecordBuilder) KnowledgeType(v string) *RecordBuilder {
	b.props["knowledge_type"] = graph.StringProperty(v)
	return b
}

func (b *RecordBuilder) EpistemicStatus(v string) *RecordBuilder {
	b.props["epistemic_status"] = graph.StringProperty(v)
	return b
}

func (b *RecordBuilder) Importance(v float64) *RecordBuilder {
	b.props["importance"] = graph.Float64Property(v)
	return b
}

func (b *RecordBuilder) Keywords(kw ...string) *RecordBuilder {
	b.props["content_keywords"] = graph.StringListProperty(kw)
	return b
}

func (b *RecordBuilder) Summary(v string) *RecordBuilder {
	b.props["content_short"] = graph.StringProperty(v)
	return b
}

func (b *RecordBuilder) CreatedAt(t time.Time) *RecordBuilder {
	b.props["created_at"] = graph.TimestampProperty(t)
	return b
}

func (b *RecordBuilder) AccessCount(n int64) *RecordBuilder {
	b.props["access_count"] = graph.Int64Property(n)
	return b
}

func (b *RecordBuilder) LastAccessed(t time.Time) *RecordBuilder {
	b.props["last_accessed"] = graph.TimestampProperty(t)
	return b
}

func (b *RecordBuilder) ValidUntil(t time.Time) *RecordBuilder {
	b.props["valid_until"] = graph.TimestampProperty(t)
	return b
}

func (b *RecordBuilder) Resolution(v string) *RecordBuilder {
	b.props["resolution"] = graph.StringProperty(v)
	return b
}

func (b *RecordBuilder) ResolvedAt(t time.Time) *RecordBuilder {
	b.props["resolved_at"] = graph.TimestampProperty(t)
	return b
}

func (b *RecordBuilder) Pending() *RecordBuilder {
	b.props["processing_status"] = graph.StringProperty("captured")
	return b
}

func (b *RecordBuilder) Embedding(vec []float32) *RecordBuilder {
	b.embedding = vec
	return b
}

func (b *RecordBuilder) SourceRef(v string) *RecordBuilder {
	b.props["source_ref"] = graph.StringProperty(v)
	return b
}

// Add inserts the record into the engine and returns its ID.
// Acquires and releases the write lock.
func (b *RecordBuilder) Add(t *testing.T, eng *core.Engine) string {
	t.Helper()
	id := b.AddDirect(eng)
	if id == "" {
		t.Fatal("AddDirect returned empty ID")
	}
	return id
}

// AddDirect inserts the record without requiring *testing.T.
// Use in TestMain or other contexts where *testing.T is unavailable.
func (b *RecordBuilder) AddDirect(eng *core.Engine) string {
	eng.Lock()
	defer eng.Unlock()

	n := eng.Graph().AddNode(b.props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}

	if b.embedding != nil {
		prop := graph.VectorProperty(b.embedding)
		eng.Graph().SetNodeProperty(n.ID, "embedding_full", prop)
		eng.PropIdx().Add(n.ID, "embedding_full", prop)
		eng.VecIdx().Add(n.ID, b.embedding)
	}

	eng.Save("test")
	return n.ID
}

// Edge creates an edge between two records.
func Edge(t *testing.T, eng *core.Engine, sourceID, targetID, edgeType string, weight float64) {
	t.Helper()
	EdgeDirect(eng, sourceID, targetID, edgeType, weight)
}

// EdgeDirect creates an edge without requiring *testing.T.
func EdgeDirect(eng *core.Engine, sourceID, targetID, edgeType string, weight float64) {
	eng.Lock()
	defer eng.Unlock()
	eng.Graph().AddEdge(sourceID, targetID, edgeType, weight, nil)
	eng.Save("test")
}
