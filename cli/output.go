package cli

import (
	"errors"
	"fmt"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/server"
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
	// Mirror the envelope's only-when-frozen store_readonly flag so
	// CLI consumers see the frozen state on every server-backed
	// response, same as HTTP/MCP consumers do.
	if resp.StoreReadonly {
		data["store_readonly"] = true
	}
	return printJSON(data)
}

// errorOutput is the JSON error structure for CLI output.
type errorOutput struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// silentError is returned by writeError after the structured JSON
// already landed on stdout. Execute() checks for it via errors.As
// and skips the redundant stderr print -- without this, pipe
// consumers see both the JSON (stdout) and the human form (stderr),
// breaking single-stream parsing. silentError still satisfies error
// so Cobra's exit-code handling is unchanged.
type silentError struct{ msg string }

func (e *silentError) Error() string { return e.msg }

// writeError writes a JSON error to stdout and returns a silentError.
// The non-zero exit code propagates to the shell; the JSON on stdout
// is the only human/machine-visible artefact. Used by CLI commands
// that haven't been migrated to the thin client yet.
func writeError(code, message string, retryable bool) error {
	out := errorOutput{
		Error:     code,
		Message:   message,
		Retryable: retryable,
	}
	printJSON(out)
	return &silentError{msg: fmt.Sprintf("%s: %s", code, message)}
}

// writeServerError unifies the server-error return path. If err is a
// typed *server.ErrorDetail (parseResponse preserves these for HTTP
// 4xx/5xx responses), route through writeError so the structured
// Code/Message/Retryable land on stdout as JSON instead of being
// collapsed to a single string by Cobra. Other errors (network,
// timeout, malformed response) get wrapped with the operation name
// and a generic "request_failed" code.
//
// Callers that previously did `return fmt.Errorf("op: %w", err)` should
// switch to `return writeServerError("op", err)` so pipe consumers see
// structured JSON on every error path.
func writeServerError(op string, err error) error {
	var detail *server.ErrorDetail
	if errors.As(err, &detail) && detail != nil {
		return writeError(detail.Code, detail.Message, detail.Retryable)
	}
	return writeError("request_failed", fmt.Sprintf("%s: %s", op, err), false)
}

// defaultLimits returns the default config limits for input validation.
func defaultLimits() config.LimitsConfig {
	return config.Defaults().Limits
}
