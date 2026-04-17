package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/secret"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

const defaultBaseURL = "https://api.anthropic.com"
const apiVersion = "2023-06-01"

// Client calls the Anthropic Messages API.
//
// Concurrency: Complete and CompleteWithModel are safe to call from
// multiple goroutines (curation runs them in parallel). The system
// prompt cache is protected by systemMu so SetSystemPrompt and
// in-flight Completes don't race on systemCache.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client

	// batchClient has a longer timeout (5 min) for the message
	// batches API which streams results that can take noticeably
	// longer than chat-style single-message calls. Connection pool
	// is reused across batch operations.
	batchClient *http.Client

	systemMu    sync.RWMutex
	systemCache []systemBlock // cached system prompt, set via SetSystemPrompt
}

// New creates an Anthropic LLM client from config.
func New(cfg config.LLMConfig) (*Client, error) {
	key := secret.ResolveKey(cfg.APIKeyFile, cfg.APIKeyEnv, cfg.APIKey)
	if key == "" {
		return nil, fmt.Errorf("anthropic: API key not configured (set api_key_file or api_key_env, or run 'gramaton configure')")
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	model := cfg.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		model:       model,
		apiKey:      key,
		client:      &http.Client{Timeout: 120 * time.Second},
		batchClient: &http.Client{Timeout: 5 * time.Minute},
	}, nil
}


// messagesRequest is the Anthropic Messages API request body.
type messagesRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    []systemBlock  `json:"system,omitempty"`
	Messages  []message      `json:"messages"`
}

// systemBlock is a content block in the system message array.
// Using the array form (vs plain string) enables cache_control.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// messagesResponse is the Anthropic Messages API response.
type messagesResponse struct {
	Content []contentBlock `json:"content"`
	Usage   usage          `json:"usage"`
	Error   *apiError      `json:"error,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// SetSystemPrompt configures a system prompt that will be included
// in all subsequent Complete/CompleteWithModel calls. The system
// prompt is marked with cache_control for Anthropic prompt caching.
// Pass empty string to clear. Safe to call concurrently with Complete.
func (c *Client) SetSystemPrompt(text string) {
	c.systemMu.Lock()
	defer c.systemMu.Unlock()
	if text == "" {
		c.systemCache = nil
		return
	}
	c.systemCache = []systemBlock{{
		Type:         "text",
		Text:         text,
		CacheControl: &cacheControl{Type: "ephemeral"},
	}}
}

// snapshotSystemCache returns a defensive copy of the current system
// cache for use in a request body. Holding the read lock for the
// duration of the in-flight HTTP request would needlessly serialise
// concurrent Completes, so we copy and release.
func (c *Client) snapshotSystemCache() []systemBlock {
	c.systemMu.RLock()
	defer c.systemMu.RUnlock()
	if len(c.systemCache) == 0 {
		return nil
	}
	cp := make([]systemBlock, len(c.systemCache))
	copy(cp, c.systemCache)
	return cp
}

// CompleteWithModel sends a prompt using a specific model override.
func (c *Client) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	if model == "" {
		model = c.model
	}
	return c.completeImpl(ctx, model, prompt)
}

// Complete sends a prompt and returns the completion text.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	return c.completeImpl(ctx, c.model, prompt)
}

func (c *Client) completeImpl(ctx context.Context, model, prompt string) (string, error) {
	req := messagesRequest{
		Model:     model,
		MaxTokens: 4096,
		Messages: []message{
			{Role: "user", Content: prompt},
		},
	}
	if sys := c.snapshotSystemCache(); len(sys) > 0 {
		req.System = sys
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("anthropic: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("anthropic: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Limit response reads to prevent unbounded memory allocation.
	const maxResponseSize = 10 * 1024 * 1024 // 10 MB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("anthropic: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return "", fmt.Errorf("anthropic: %s: %s", errResp.Error.Type, errResp.Error.Message)
		}
		return "", fmt.Errorf("anthropic: HTTP %d", resp.StatusCode)
	}

	var result messagesResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("anthropic: unmarshal response: %w", err)
	}

	// Report token usage via any recorder attached to ctx. No-op when
	// no recorder is set (e.g., direct calls outside the Metered wrapper).
	if recorder := telemetry.RecorderFromContext(ctx); recorder != nil {
		recorder.Record(telemetry.TaskFromContext(ctx), telemetry.CallUsage{
			InputTokens:      result.Usage.InputTokens,
			OutputTokens:     result.Usage.OutputTokens,
			CacheReadTokens:  result.Usage.CacheReadInputTokens,
			CacheWriteTokens: result.Usage.CacheCreationInputTokens,
		})
	}

	// Extract text from content blocks.
	var text string
	for _, block := range result.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	return text, nil
}

// ModelID returns the model identifier.
func (c *Client) ModelID() string {
	return c.model
}
