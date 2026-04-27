package search

import (
	"context"
	"testing"
)

type mockDecomposer struct {
	response string
	err      error
}

func (m *mockDecomposer) CompleteWithModel(_ context.Context, _ string, _ string) (string, error) {
	return m.response, m.err
}

func TestDecomposeQuerySimple(t *testing.T) {
	llm := &mockDecomposer{
		response: `{"sub_queries": []}`,
	}
	result := DecomposeQuery(context.Background(), llm, "test-model", "consciousness")
	if result != nil {
		t.Fatalf("simple query should not decompose, got %v", result)
	}
}

func TestDecomposeQueryMultiConcept(t *testing.T) {
	llm := &mockDecomposer{
		response: `{"sub_queries": ["consciousness", "memory role in cognition"]}`,
	}
	result := DecomposeQuery(context.Background(), llm, "test-model", "consciousness and memory's role")
	if len(result) != 2 {
		t.Fatalf("expected 2 sub-queries, got %d", len(result))
	}
	if result[0] != "consciousness" || result[1] != "memory role in cognition" {
		t.Fatalf("unexpected sub-queries: %v", result)
	}
}

func TestDecomposeQueryNilLLM(t *testing.T) {
	result := DecomposeQuery(context.Background(), nil, "test-model", "test query")
	if result != nil {
		t.Fatal("nil LLM should return nil")
	}
}

func TestDecomposeQueryEmptyQuery(t *testing.T) {
	llm := &mockDecomposer{response: `{"sub_queries": []}`}
	result := DecomposeQuery(context.Background(), llm, "test-model", "")
	if result != nil {
		t.Fatal("empty query should return nil")
	}
}

func TestDecomposeQueryWithCodeFences(t *testing.T) {
	llm := &mockDecomposer{
		response: "```json\n{\"sub_queries\": [\"topic A\", \"topic B\"]}\n```",
	}
	result := DecomposeQuery(context.Background(), llm, "test-model", "topic A and topic B")
	if len(result) != 2 {
		t.Fatalf("expected 2 sub-queries, got %d: %v", len(result), result)
	}
}

func TestMergeResults(t *testing.T) {
	setA := []Result{
		{ID: "a", EffectiveScore: 0.9},
		{ID: "b", EffectiveScore: 0.8},
		{ID: "c", EffectiveScore: 0.7},
	}
	setB := []Result{
		{ID: "b", EffectiveScore: 0.9},
		{ID: "d", EffectiveScore: 0.8},
		{ID: "a", EffectiveScore: 0.7},
	}

	merged := MergeResults([][]Result{setA, setB}, 10)

	if len(merged) != 4 {
		t.Fatalf("expected 4 unique results, got %d", len(merged))
	}

	// "a" and "b" appear in both sets so should rank highest.
	topTwo := map[string]bool{merged[0].ID: true, merged[1].ID: true}
	if !topTwo["a"] || !topTwo["b"] {
		t.Fatalf("expected a and b in top 2, got %s and %s", merged[0].ID, merged[1].ID)
	}
}

func TestMergeResultsTopK(t *testing.T) {
	setA := []Result{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}
	merged := MergeResults([][]Result{setA}, 2)
	if len(merged) != 2 {
		t.Fatalf("expected 2 results with topK=2, got %d", len(merged))
	}
}

func TestMergeResultsEmpty(t *testing.T) {
	merged := MergeResults(nil, 10)
	if len(merged) != 0 {
		t.Fatal("nil input should return nil")
	}
}

func TestShouldDecompose(t *testing.T) {
	if !ShouldDecompose(nil, 0.5) {
		t.Fatal("empty results should trigger decomposition")
	}

	highResults := []Result{{EffectiveScore: 0.8}}
	if ShouldDecompose(highResults, 0.5) {
		t.Fatal("high-scoring results should not trigger decomposition")
	}

	lowResults := []Result{{EffectiveScore: 0.3}}
	if !ShouldDecompose(lowResults, 0.5) {
		t.Fatal("low-scoring results should trigger decomposition")
	}
}
