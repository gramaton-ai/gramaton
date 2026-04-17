package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// BatchRequest is a single request in a message batch.
type BatchRequest struct {
	CustomID string          `json:"custom_id"`
	Params   BatchParams     `json:"params"`
}

// BatchParams mirrors the Messages API params for a single batch item.
type BatchParams struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    []BatchSystemBlock `json:"system,omitempty"`
	Messages  []BatchMessage     `json:"messages"`
}

// BatchSystemBlock is an exported system block for batch requests.
type BatchSystemBlock struct {
	Type         string             `json:"type"`
	Text         string             `json:"text"`
	CacheControl *BatchCacheControl `json:"cache_control,omitempty"`
}

// BatchCacheControl marks a block for prompt caching.
type BatchCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// BatchMessage is an exported message for batch requests.
type BatchMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// batchCreateRequest is the POST body for /v1/messages/batches.
type batchCreateRequest struct {
	Requests []BatchRequest `json:"requests"`
}

// BatchStatus represents the state of a message batch.
type BatchStatus struct {
	ID               string       `json:"id"`
	ProcessingStatus string       `json:"processing_status"` // in_progress, ended, canceling, canceled, expired
	RequestCounts    RequestCounts `json:"request_counts"`
	CreatedAt        string       `json:"created_at"`
	EndedAt          string       `json:"ended_at,omitempty"`
	ResultsURL       string       `json:"results_url,omitempty"`
}

// RequestCounts tracks per-status counts in a batch.
type RequestCounts struct {
	Processing int `json:"processing"`
	Succeeded  int `json:"succeeded"`
	Errored    int `json:"errored"`
	Canceled   int `json:"canceled"`
	Expired    int `json:"expired"`
}

// Total returns the total number of requests in the batch.
func (rc RequestCounts) Total() int {
	return rc.Processing + rc.Succeeded + rc.Errored + rc.Canceled + rc.Expired
}

// BatchResult is a single result from a completed batch.
type BatchResult struct {
	CustomID string          `json:"custom_id"`
	Result   BatchResultData `json:"result"`
}

// BatchResultData holds the outcome of a single batch request.
type BatchResultData struct {
	Type    string           `json:"type"` // succeeded, errored, expired, canceled
	Message *messagesResponse `json:"message,omitempty"`
	Error   *apiError        `json:"error,omitempty"`
}

// SubmitBatch creates a new message batch. Returns the batch ID.
// The supplied ctx cancels the underlying HTTP request.
func (c *Client) SubmitBatch(ctx context.Context, requests []BatchRequest) (string, error) {
	body, err := json.Marshal(batchCreateRequest{Requests: requests})
	if err != nil {
		return "", fmt.Errorf("anthropic batch: marshal: %w", err)
	}

	resp, err := c.doBatchRequest(ctx, "POST", "/v1/messages/batches", body)
	if err != nil {
		return "", err
	}

	var status BatchStatus
	if err := json.Unmarshal(resp, &status); err != nil {
		return "", fmt.Errorf("anthropic batch: parse response: %w", err)
	}
	return status.ID, nil
}

// PollBatch checks the status of a batch. The supplied ctx cancels
// the underlying HTTP request.
func (c *Client) PollBatch(ctx context.Context, batchID string) (BatchStatus, error) {
	resp, err := c.doBatchRequest(ctx, "GET", "/v1/messages/batches/"+batchID, nil)
	if err != nil {
		return BatchStatus{}, err
	}

	var status BatchStatus
	if err := json.Unmarshal(resp, &status); err != nil {
		return BatchStatus{}, fmt.Errorf("anthropic batch: parse status: %w", err)
	}
	return status, nil
}

// FetchResults retrieves the results of a completed batch as JSONL.
// The supplied ctx cancels the underlying HTTP request.
func (c *Client) FetchResults(ctx context.Context, batchID string) ([]BatchResult, error) {
	resp, err := c.doBatchRequest(ctx, "GET", "/v1/messages/batches/"+batchID+"/results", nil)
	if err != nil {
		return nil, err
	}

	// Results come as JSONL (one JSON object per line).
	const maxBatchResults = 100_000
	var results []BatchResult
	dec := json.NewDecoder(bytes.NewReader(resp))
	for dec.More() {
		if len(results) >= maxBatchResults {
			return results, fmt.Errorf("anthropic batch: results exceeded maximum (%d)", maxBatchResults)
		}
		var r BatchResult
		if err := dec.Decode(&r); err != nil {
			return results, fmt.Errorf("anthropic batch: parse result line: %w", err)
		}
		results = append(results, r)
	}
	return results, nil
}

// CancelBatch cancels an in-progress batch. The supplied ctx
// cancels the underlying HTTP request.
func (c *Client) CancelBatch(ctx context.Context, batchID string) error {
	_, err := c.doBatchRequest(ctx, "POST", "/v1/messages/batches/"+batchID+"/cancel", nil)
	return err
}

func (c *Client) doBatchRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	url := c.baseURL + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("anthropic batch: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", apiVersion)

	// Use the shared batch client (5-min timeout, connection-pooled).
	resp, err := c.batchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic batch: request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseSize = 100 * 1024 * 1024 // 100 MB (batch results can be large)
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("anthropic batch: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("anthropic batch: %s: %s", errResp.Error.Type, truncate(errResp.Error.Message, 200))
		}
		return nil, fmt.Errorf("anthropic batch: HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	return respBody, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
