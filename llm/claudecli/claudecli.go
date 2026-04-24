// Package claudecli implements an LLM provider that shells out to the
// Claude Code CLI (claude -p). Uses the user's subscription auth when
// no API key is set, or API key auth when ANTHROPIC_API_KEY is present.
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"

	"github.com/gramaton-ai/gramaton/internal/strutil"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// modelPattern restricts model strings to a conservative shape:
// alphanumeric, hyphen, dot, underscore, colon, and slash. argv form
// blocks shell metacharacters today, but a future claude CLI parser
// bug that splits on whitespace within an arg could turn an
// unvalidated user-supplied "sonnet --dangerous-flag" into two argv
// entries. Reject anything that doesn't match the pattern.
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]+$`)

// modelAliases maps short names to Claude CLI model identifiers.
var modelAliases = map[string]string{
	"haiku":  "haiku",
	"sonnet": "sonnet",
	"opus":   "opus",
}

// Client wraps the Claude Code CLI for LLM completions.
type Client struct {
	binary string // path to claude binary
	model  string // default model (e.g., "sonnet")
}

// New creates a Claude CLI provider. Looks for the claude binary on PATH.
func New(model string) (*Client, error) {
	binary, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claudecli: claude binary not found on PATH")
	}
	if model == "" {
		model = "sonnet"
	}
	return &Client{binary: binary, model: model}, nil
}

// cliResponse is the JSON output from claude -p --output-format json.
// modelUsage is a per-model breakdown of token counts; we sum across
// models for telemetry. total_cost_usd is the CLI's own authoritative
// cost number (reflects actual subscription billing), but accounting
// flows through the standard token pipeline so cross-provider
// aggregation uses one cost formula.
type cliResponse struct {
	Result     string                        `json:"result"`
	IsError    bool                          `json:"is_error"`
	DurationMs int                           `json:"duration_ms"`
	TotalCost  float64                       `json:"total_cost_usd"`
	ModelUsage map[string]cliModelUsageEntry `json:"modelUsage"`
}

// cliModelUsageEntry captures the per-model token breakdown surfaced
// by the claude CLI's JSON output. Field names match the CLI's shape.
type cliModelUsageEntry struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
}

func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	return c.run(ctx, c.model, prompt)
}

func (c *Client) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	if model == "" {
		model = c.model
	}
	if alias, ok := modelAliases[model]; ok {
		model = alias
	}
	return c.run(ctx, model, prompt)
}

func (c *Client) ModelID() string { return "claude-cli:" + c.model }

// ProviderName returns the identifier used in per-provider metrics.
func (c *Client) ProviderName() string { return "claude-cli" }

// SupportsStructuredOutput reports false. CLI providers exchange
// free text with a subprocess; the binary has no way to accept a
// JSON Schema and enforce it on output. Callers that need schema-
// validated output must fall back to Complete + a text parser
// (e.g. internal/sanitize + curation.parseClassification).
func (c *Client) SupportsStructuredOutput() bool { return false }

// CompleteStructured always errors for claude-cli; the subprocess
// has no structured-output mode.
func (c *Client) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, fmt.Errorf("claude-cli: structured output not supported (subprocess wrapper)")
}

func (c *Client) run(ctx context.Context, model, prompt string) (string, error) {
	if !modelPattern.MatchString(model) {
		return "", fmt.Errorf("claudecli: rejected model %q (must match [A-Za-z0-9._:/-]+)", model)
	}
	args := []string{
		"-p",
		"--output-format", "json",
		"--model", model,
		"--no-allowedTools", // no tool access needed for classification
	}

	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Stdin = bytes.NewBufferString(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claudecli: command failed (%v): %s", err, stderr.String())
	}

	var resp cliResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("claudecli: parse response: %w (raw: %s)", err, strutil.Truncate(stdout.String(), 200))
	}

	if resp.IsError {
		return "", fmt.Errorf("claudecli: model returned error: %s", strutil.Truncate(resp.Result, 500))
	}

	telemetry.Record(ctx, sumModelUsage(resp.ModelUsage))

	return resp.Result, nil
}

// sumModelUsage collapses the CLI's per-model map into a single
// CallUsage so the standard pricing-table pipeline can compute cost
// the same way it does for direct API providers. total_cost_usd from
// the CLI is ignored: it reflects actual subscription billing, while
// telemetry numbers use the shared pricing table so cross-provider
// aggregation is apples-to-apples.
func sumModelUsage(m map[string]cliModelUsageEntry) telemetry.CallUsage {
	var u telemetry.CallUsage
	for _, entry := range m {
		u.InputTokens += entry.InputTokens
		u.OutputTokens += entry.OutputTokens
		u.CacheReadTokens += entry.CacheReadInputTokens
		u.CacheWriteTokens += entry.CacheCreationInputTokens
	}
	return u
}

