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
	"github.com/gramaton-ai/gramaton/llm/httpretry"
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
	Model      string        `json:"model"`
	MaxTokens  int           `json:"max_tokens"`
	System     []systemBlock `json:"system,omitempty"`
	Messages   []message     `json:"messages"`
	Tools      []tool        `json:"tools,omitempty"`
	ToolChoice *toolChoice   `json:"tool_choice,omitempty"`
}

// tool is an Anthropic tool-use definition. Used for structured
// output — the API guarantees tool_use.input conforms to InputSchema.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// toolChoice forces the model to use a specific tool. When set to
// {Type: "tool", Name: "<toolname>"}, the model must respond with a
// tool_use block for that tool — ideal for structured output where
// text-format responses are never wanted.
type toolChoice struct {
	Type string `json:"type"` // "tool" | "any" | "auto"
	Name string `json:"name,omitempty"`
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
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("anthropic: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", apiVersion)
		return req, nil
	}

	resp, err := httpretry.DoWithRetry(ctx, c.client, httpretry.DefaultRetryConfig(), buildReq)
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

	telemetry.Record(ctx, telemetry.CallUsage{
		InputTokens:      result.Usage.InputTokens,
		OutputTokens:     result.Usage.OutputTokens,
		CacheReadTokens:  result.Usage.CacheReadInputTokens,
		CacheWriteTokens: result.Usage.CacheCreationInputTokens,
	})

	// Extract text from content blocks.
	var text string
	for _, block := range result.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	return text, nil
}

// SupportsStructuredOutput reports that Anthropic can enforce a
// JSON Schema via the tool-use API at the wire layer.
func (c *Client) SupportsStructuredOutput() bool { return true }

// CompleteStructured sends the prompt with a forced tool-use call
// whose InputSchema is the supplied JSON Schema. The returned raw
// message is the tool_use.input block — guaranteed schema-valid by
// Anthropic's API.
//
// Records usage via telemetry.Record like the other completion paths.
func (c *Client) CompleteStructured(ctx context.Context, schema map[string]any, prompt string) (json.RawMessage, error) {
	req := messagesRequest{
		Model:     c.model,
		MaxTokens: 4096,
		Messages: []message{
			{Role: "user", Content: prompt},
		},
		Tools: []tool{{
			Name:        "emit_output",
			Description: "Emit the structured output.",
			InputSchema: schema,
		}},
		ToolChoice: &toolChoice{Type: "tool", Name: "emit_output"},
	}
	if sys := c.snapshotSystemCache(); len(sys) > 0 {
		req.System = sys
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic structured: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/messages"
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("anthropic structured: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", apiVersion)
		return req, nil
	}

	resp, err := httpretry.DoWithRetry(ctx, c.client, httpretry.DefaultRetryConfig(), buildReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic structured: request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseSize = 10 * 1024 * 1024
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("anthropic structured: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("anthropic structured: %s: %s", errResp.Error.Type, errResp.Error.Message)
		}
		return nil, fmt.Errorf("anthropic structured: HTTP %d", resp.StatusCode)
	}

	// Response parsing uses a wider contentBlock shape because
	// structured output comes back as tool_use blocks, not text blocks.
	var result struct {
		Content []struct {
			Type  string          `json:"type"`
			Input json.RawMessage `json:"input"`
			Name  string          `json:"name"`
		} `json:"content"`
		Usage usage     `json:"usage"`
		Error *apiError `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("anthropic structured: unmarshal response: %w", err)
	}

	telemetry.Record(ctx, telemetry.CallUsage{
		InputTokens:      result.Usage.InputTokens,
		OutputTokens:     result.Usage.OutputTokens,
		CacheReadTokens:  result.Usage.CacheReadInputTokens,
		CacheWriteTokens: result.Usage.CacheCreationInputTokens,
	})

	for _, block := range result.Content {
		if block.Type == "tool_use" && block.Name == "emit_output" {
			if len(block.Input) == 0 {
				return nil, fmt.Errorf("anthropic structured: tool_use block had empty input")
			}
			return block.Input, nil
		}
	}
	return nil, fmt.Errorf("anthropic structured: response had no tool_use block for emit_output")
}

// ModelID returns the model identifier.
func (c *Client) ModelID() string {
	return c.model
}

// ProviderName returns the identifier used in per-provider metrics.
func (c *Client) ProviderName() string { return "anthropic" }
