// Package kirocli implements an LLM provider that shells out to the
// Kiro CLI (kiro-cli chat --no-interactive). Uses the user's Kiro
// subscription credits. No API key required.
package kirocli

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/gramaton-ai/gramaton/internal/strutil"
)

// modelAliases maps short names to Kiro CLI model identifiers.
var modelAliases = map[string]string{
	"haiku":  "claude-haiku-4.5",
	"sonnet": "claude-sonnet-4",
	"opus":   "claude-opus-4.5",
	"auto":   "auto",
}

// modelPattern restricts model strings to a conservative shape:
// alphanumeric, hyphen, dot, underscore, colon, and slash. argv form
// blocks shell metacharacters today, but a future kiro-cli parser
// change that splits on whitespace within an arg could turn an
// unvalidated user-supplied "auto --trust-all-tools" into two argv
// entries. Reject anything that doesn't match the pattern. (Wave 4
// P1-22.)
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]+$`)

// ansiRe strips ANSI escape sequences from kiro-cli output.
// Covers CSI (7-bit and 8-bit), OSC, DCS, APC, and PM sequences.
var ansiRe = regexp.MustCompile(
	`\x1b\[[0-9;]*[a-zA-Z]` + // CSI sequences (e.g., colors, cursor)
		`|\x1b\[\?[0-9;]*[a-zA-Z]` + // CSI private mode (e.g., ?25h/l)
		`|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC sequences (terminated by BEL or ST)
		`|\x1b[P_^][^\x1b]*\x1b\\` + // DCS, APC, PM sequences
		`|\x9b[0-9;]*[a-zA-Z]` + // 8-bit CSI (C1 control)
		`|\x1b[()][0-9A-B]`, // Character set selection
)

// Client wraps the Kiro CLI for LLM completions.
type Client struct {
	binary string
	model  string
}

// New creates a Kiro CLI provider. Looks for kiro-cli on PATH.
func New(model string) (*Client, error) {
	binary, err := exec.LookPath("kiro-cli")
	if err != nil {
		return nil, fmt.Errorf("kirocli: kiro-cli binary not found on PATH")
	}
	if model == "" {
		model = "auto"
	}
	return &Client{binary: binary, model: model}, nil
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

func (c *Client) ModelID() string { return "kiro-cli:" + c.model }

// ProviderName returns the identifier used in per-provider metrics.
func (c *Client) ProviderName() string { return "kiro-cli" }

func (c *Client) run(ctx context.Context, model, prompt string) (string, error) {
	if !modelPattern.MatchString(model) {
		return "", fmt.Errorf("kirocli: rejected model %q (must match [A-Za-z0-9._:/-]+)", model)
	}
	args := []string{
		"chat",
		"--no-interactive",
		"--model", model,
	}

	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Stdin = bytes.NewBufferString(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("kirocli: command failed (%v): %s", err, strutil.Truncate(stderr.String(), 200))
	}

	return extractResponse(stdout.String())
}

// extractResponse strips ANSI codes and kiro-cli formatting from
// the raw output to get the model's response text.
func extractResponse(raw string) (string, error) {
	// Strip ANSI escape sequences.
	clean := ansiRe.ReplaceAllString(raw, "")

	// Split into lines, filter out kiro-cli chrome.
	var content []string
	for _, line := range strings.Split(clean, "\n") {
		line = strings.TrimSpace(line)

		// Skip empty lines.
		if line == "" {
			continue
		}
		// Skip the Credits/Time footer.
		if strings.Contains(line, "Credits:") || strings.Contains(line, "Time:") {
			continue
		}
		// Skip trust warnings.
		if strings.Contains(line, "tools are now trusted") || strings.Contains(line, "kiro.dev/docs") || strings.Contains(line, "Agents can sometimes") {
			continue
		}
		// Strip the "> " prefix kiro adds.
		line = strings.TrimPrefix(line, "> ")
		// Strip control characters (e.g., \x1b[1G cursor reset).
		line = strings.Map(func(r rune) rune {
			if r < 32 && r != '\n' && r != '\t' {
				return -1
			}
			return r
		}, line)
		line = strings.TrimSpace(line)
		if line != "" {
			content = append(content, line)
		}
	}

	if len(content) == 0 {
		return "", fmt.Errorf("kirocli: no content in response (raw length: %d)", len(raw))
	}

	result := strings.Join(content, "\n")

	// Strip markdown code fences if the model wrapped JSON in them.
	result = stripCodeFences(result)

	return result, nil
}

// stripCodeFences removes ```json ... ``` wrappers from the response.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Find the end of the opening fence line.
		idx := strings.Index(s, "\n")
		if idx > 0 {
			s = s[idx+1:]
		}
	}
	if strings.HasSuffix(s, "```") {
		s = s[:len(s)-3]
	}
	return strings.TrimSpace(s)
}

