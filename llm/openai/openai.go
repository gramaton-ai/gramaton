package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/secret"
	"github.com/gramaton-ai/gramaton/llm/httpretry"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

const defaultBaseURL = "https://api.openai.com"

// maxResponseSize limits response body reads to prevent unbounded
// memory allocation from a malicious or misconfigured server.
const maxResponseSize = 10 * 1024 * 1024 // 10 MB

// Client calls any OpenAI-compatible /v1/chat/completions endpoint.
// Works with OpenAI, Azure OpenAI, vLLM, LiteLLM, Together,
// Fireworks, and any other compatible API.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client

	// ignoredModelWarned dedups the per-override Warn from
	// CompleteWithModel so a tight curation loop doesn't flood logs.
	ignoredModelWarned sync.Map
}

// New creates an OpenAI-compatible LLM client.
func New(cfg config.LLMConfig) (*Client, error) {
	key := secret.ResolveKey(cfg.APIKeyFile, cfg.APIKeyEnv, cfg.APIKey)

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	// Default model used by Complete() (no explicit model). Most call
	// sites pass a model via CompleteWithModel resolved through
	// cfg.ModelForTask; this only fires for callers that don't.
	model := cfg.Models.Medium
	if model == "" {
		return nil, fmt.Errorf("openai llm: cfg.LLM.Models.Medium is required")
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  key,
		client:  &http.Client{Timeout: 120 * time.Second},
	}, nil
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

// responseFormat is the OpenAI structured-output parameter. When Type
// is "json_schema" and JSONSchema.Strict is true, OpenAI guarantees
// the response body is valid JSON matching JSONSchema.Schema before
// returning (supported on gpt-4o and later; strict-mode is the
// enforcement knob — without strict:true, OpenAI treats the schema
// as a hint rather than a contract).
type responseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *jsonSchema `json:"json_schema,omitempty"`
}

type jsonSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
	Error   *apiError    `json:"error,omitempty"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

// chatUsage is the OpenAI token accounting. OpenAI reports a cached
// portion of the input via prompt_tokens_details.cached_tokens (present
// since 2024 prompt caching); writes aren't separately reported, so
// CacheWriteTokens stays zero for this provider.
type chatUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// CompleteWithModel ignores the model override (OpenAI client uses
// the configured model). Logs a one-shot Warn per distinct override
// so callers expecting cross-provider consistency notice that this
// provider treats its model as fixed at construction.
func (c *Client) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	if model != "" && model != c.model {
		c.warnIgnoredModel(model)
	}
	return c.Complete(ctx, prompt)
}

// warnIgnoredModel deduplicates the per-override warning so a tight
// curation loop doesn't flood the logs with the same line.
func (c *Client) warnIgnoredModel(model string) {
	if _, loaded := c.ignoredModelWarned.LoadOrStore(model, struct{}{}); loaded {
		return
	}
	slog.Warn("openai: ignoring CompleteWithModel override",
		"component", "llm",
		"requested", model,
		"using", c.model,
		"hint", "openai client uses the model fixed at construction; configure llm.model or llm.models.* to switch")
}

// Complete sends a prompt via /v1/chat/completions and returns the text.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("openai llm: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/chat/completions"
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("openai llm: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		return req, nil
	}

	resp, err := httpretry.DoWithRetry(ctx, c.client, httpretry.DefaultRetryConfig(), buildReq)
	if err != nil {
		return "", fmt.Errorf("openai llm: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("openai llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return "", fmt.Errorf("openai llm: %s: %s", errResp.Error.Type, errResp.Error.Message)
		}
		return "", fmt.Errorf("openai llm: HTTP %d", resp.StatusCode)
	}

	var result chatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("openai llm: unmarshal response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("openai llm: no choices in response")
	}

	telemetry.Record(ctx, telemetry.CallUsage{
		InputTokens:     result.Usage.PromptTokens,
		OutputTokens:    result.Usage.CompletionTokens,
		CacheReadTokens: result.Usage.PromptTokensDetails.CachedTokens,
	})

	return result.Choices[0].Message.Content, nil
}

// ModelID returns the model identifier.
func (c *Client) ModelID() string {
	return c.model
}

// ProviderName returns the identifier used in per-provider metrics.
func (c *Client) ProviderName() string { return "openai" }

// SupportsStructuredOutput reports that OpenAI can enforce a JSON
// Schema at the wire layer via response_format: json_schema with
// strict=true. Only supported on gpt-4o and later models; older
// models advertised false here would be safer, but we don't know
// the model's capability string-matching on name alone (Azure
// deployments rename models, compatible servers may also support
// it). If the model doesn't support strict schema, the API returns
// an error and the caller's structured-path-error fallback kicks
// in transparently.
func (c *Client) SupportsStructuredOutput() bool { return true }

// CompleteStructured sends the prompt with response_format set to
// json_schema + strict:true. The response content is guaranteed by
// OpenAI to be valid JSON matching the schema — callers can
// Unmarshal directly without the "find first { and last }" dance.
func (c *Client) CompleteStructured(ctx context.Context, schema map[string]any, prompt string) (json.RawMessage, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
		ResponseFormat: &responseFormat{
			Type: "json_schema",
			JSONSchema: &jsonSchema{
				Name:   "emit_output",
				Strict: true,
				Schema: schema,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("openai structured: marshal request: %w", err)
	}

	url := c.baseURL + "/v1/chat/completions"
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("openai structured: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		return req, nil
	}

	resp, err := httpretry.DoWithRetry(ctx, c.client, httpretry.DefaultRetryConfig(), buildReq)
	if err != nil {
		return nil, fmt.Errorf("openai structured: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("openai structured: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error apiError `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("openai structured: %s: %s", errResp.Error.Type, errResp.Error.Message)
		}
		return nil, fmt.Errorf("openai structured: HTTP %d", resp.StatusCode)
	}

	var result chatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("openai structured: unmarshal response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("openai structured: no choices in response")
	}

	telemetry.Record(ctx, telemetry.CallUsage{
		InputTokens:     result.Usage.PromptTokens,
		OutputTokens:    result.Usage.CompletionTokens,
		CacheReadTokens: result.Usage.PromptTokensDetails.CachedTokens,
	})

	// The content is the schema-valid JSON as a string. Cast to
	// RawMessage without re-validation — strict mode means OpenAI
	// already checked.
	return json.RawMessage(result.Choices[0].Message.Content), nil
}

