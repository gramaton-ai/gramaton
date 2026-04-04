package cli

import (
	"fmt"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/server"
)

// printEnvelope prints a server response in the v0.1-compatible format:
// the data fields merged with the curation field at the top level.
// This preserves backward compatibility with existing agent prompts.
func printEnvelope(resp *server.ResponseEnvelope) error {
	data, ok := resp.Data.(map[string]any)
	if !ok {
		data = map[string]any{"data": resp.Data}
	}
	data["curation"] = resp.Curation
	return printJSON(data)
}

// errorOutput is the JSON error structure for CLI output.
type errorOutput struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// writeError writes a JSON error to stdout and returns an error.
// Used by CLI commands that haven't been migrated to the thin client yet.
func writeError(code, message string, retryable bool) error {
	out := errorOutput{
		Error:     code,
		Message:   message,
		Retryable: retryable,
	}
	printJSON(out)
	return fmt.Errorf("%s: %s", code, message)
}

// defaultLimits returns the default config limits for input validation.
func defaultLimits() config.LimitsConfig {
	return config.Defaults().Limits
}
