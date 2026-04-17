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
)

// modelPattern restricts model strings to a conservative shape:
// alphanumeric, hyphen, dot, underscore, colon, and slash. argv form
// blocks shell metacharacters today, but a future claude CLI parser
// bug that splits on whitespace within an arg could turn an
// unvalidated user-supplied "sonnet --dangerous-flag" into two argv
// entries. Reject anything that doesn't match the pattern. (Wave 4
// P1-22.)
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
type cliResponse struct {
	Result      string         `json:"result"`
	IsError     bool           `json:"is_error"`
	DurationMs  int            `json:"duration_ms"`
	TotalCost   float64        `json:"total_cost_usd"`
	ModelUsage  map[string]any `json:"modelUsage"`
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
		return "", fmt.Errorf("claudecli: parse response: %w (raw: %s)", err, truncate(stdout.String(), 200))
	}

	if resp.IsError {
		return "", fmt.Errorf("claudecli: model returned error: %s", truncate(resp.Result, 500))
	}

	return resp.Result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
