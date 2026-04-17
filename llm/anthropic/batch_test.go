package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// batchTestClient builds a *Client wired to a test server with the
// real batchClient (5-min timeout) so the new ctx-aware batch
// methods can run without nil-deref. Used by every batch test.
func batchTestClient(serverURL string) *Client {
	return &Client{
		baseURL:     serverURL,
		apiKey:      "test-key",
		model:       "test",
		batchClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

func TestSubmitBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/messages/batches" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Fatal("missing or wrong API key")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":                "batch_123",
			"processing_status": "in_progress",
			"request_counts":    map[string]int{"processing": 2},
		})
	}))
	defer server.Close()

	c := batchTestClient(server.URL)

	batchID, err := c.SubmitBatch(context.Background(), []BatchRequest{
		{CustomID: "r1", Params: BatchParams{Model: "haiku", MaxTokens: 100, Messages: []BatchMessage{{Role: "user", Content: "test"}}}},
		{CustomID: "r2", Params: BatchParams{Model: "sonnet", MaxTokens: 100, Messages: []BatchMessage{{Role: "user", Content: "test2"}}}},
	})
	if err != nil {
		t.Fatalf("SubmitBatch: %v", err)
	}
	if batchID != "batch_123" {
		t.Fatalf("expected batch_123, got %s", batchID)
	}
}

func TestPollBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/messages/batches/batch_123" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":                "batch_123",
			"processing_status": "ended",
			"request_counts":    map[string]int{"succeeded": 2, "processing": 0},
		})
	}))
	defer server.Close()

	c := batchTestClient(server.URL)

	status, err := c.PollBatch(context.Background(), "batch_123")
	if err != nil {
		t.Fatalf("PollBatch: %v", err)
	}
	if status.ProcessingStatus != "ended" {
		t.Fatalf("expected ended, got %s", status.ProcessingStatus)
	}
	if status.RequestCounts.Succeeded != 2 {
		t.Fatalf("expected 2 succeeded, got %d", status.RequestCounts.Succeeded)
	}
}

func TestFetchResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/batches/batch_123/results" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		// Return JSONL (one object per line).
		w.Write([]byte(`{"custom_id":"r1","result":{"type":"succeeded","message":{"content":[{"type":"text","text":"{\"temporality\":\"durable\"}"}]}}}` + "\n"))
		w.Write([]byte(`{"custom_id":"r2","result":{"type":"errored","error":{"type":"invalid_request","message":"bad"}}}` + "\n"))
	}))
	defer server.Close()

	c := batchTestClient(server.URL)

	results, err := c.FetchResults(context.Background(), "batch_123")
	if err != nil {
		t.Fatalf("FetchResults: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].CustomID != "r1" || results[0].Result.Type != "succeeded" {
		t.Fatalf("unexpected first result: %+v", results[0])
	}
	if results[1].CustomID != "r2" || results[1].Result.Type != "errored" {
		t.Fatalf("unexpected second result: %+v", results[1])
	}
}

func TestBatchAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"type": "rate_limit", "message": "slow down"},
		})
	}))
	defer server.Close()

	c := batchTestClient(server.URL)

	_, err := c.SubmitBatch(context.Background(), []BatchRequest{{CustomID: "r1", Params: BatchParams{Model: "haiku", MaxTokens: 100}}})
	if err == nil {
		t.Fatal("expected error for 429")
	}
}

func TestRequestCountsTotal(t *testing.T) {
	rc := RequestCounts{Processing: 10, Succeeded: 5, Errored: 2, Expired: 1}
	if rc.Total() != 18 {
		t.Fatalf("expected 18, got %d", rc.Total())
	}
}
