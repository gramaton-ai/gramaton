package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"


	"github.com/gramaton-ai/gramaton/config"
)

// readInputJSON reads JSON from a file (if filePath is non-empty) or stdin.
// If the file is inside the gramaton temp directory, it is deleted after
// successful parsing. Also sweeps stale temp files on each call.
func readInputJSON(filePath string, target any, limits config.LimitsConfig) error {
	sweepStaleTempFiles()

	if filePath != "" {
		return readFileJSON(filePath, target, limits)
	}
	return readStdinJSON(target, limits)
}

// readCommandInput is the shared file-or-stdin reader for record-shaped
// CLI subcommands (capture, classify, update, resolve). Returns the
// parsed map or a writeError-formatted error so the caller can `return
// err` directly. Wraps readInputJSON with the standard error code +
// retryable flag the four commands had been duplicating inline.
func readCommandInput(filePath string) (map[string]any, error) {
	var input map[string]any
	if err := readInputJSON(filePath, &input, defaultLimits()); err != nil {
		return nil, writeError("input_error", err.Error(), true)
	}
	return input, nil
}

// extractRequiredID pops the "id" field off input, returning the id or
// a writeError-formatted "missing_field" error if absent/empty. Used
// by classify/update/resolve which thread the id into the URL path.
func extractRequiredID(input map[string]any) (string, error) {
	id, _ := input["id"].(string)
	if id == "" {
		return "", writeError("missing_field", "id is required", true)
	}
	delete(input, "id")
	return id, nil
}

// readFileJSON reads and parses JSON from a file with the same validation
// as readStdinJSON. Only accepts files inside the gramaton temp directory.
// Deletes the file after successful parse.
func readFileJSON(path string, target any, limits config.LimitsConfig) error {
	// Resolve symlinks before any checks to prevent symlink-based escapes.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve file path: %w", err)
	}

	// Reject files outside the gramaton temp directory.
	if !isInTempDir(resolved) {
		return fmt.Errorf("--file path must be inside the gramaton temp directory (use 'gramaton tempdir' to find it)")
	}

	// Open the file and verify it is a regular file (not a device,
	// pipe, or anything that appeared between EvalSymlinks and Open).
	f, err := os.Open(resolved)
	if err != nil {
		return fmt.Errorf("open input file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat input file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("input file is not a regular file")
	}

	// Read with size limit. Using the fd avoids TOCTOU between
	// validation and read.
	maxSize := int64(limits.MaxJSONSize)
	if info.Size() > maxSize {
		return fmt.Errorf("file exceeds maximum size (%d bytes)", limits.MaxJSONSize)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return fmt.Errorf("read input file: %w", err)
	}

	if len(data) == 0 {
		return fmt.Errorf("input file is empty")
	}

	if int64(len(data)) > maxSize {
		return fmt.Errorf("file exceeds maximum size (%d bytes)", limits.MaxJSONSize)
	}

	// Strip UTF-8 BOM if present.
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}

	// Validate UTF-8.
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8 in input file")
	}

	// Check for null bytes.
	for i, b := range data {
		if b == 0 {
			return fmt.Errorf("null byte at position %d in input file", i)
		}
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("JSON parse error in input file: %w", err)
	}

	// Clean up -- we already verified we're in the temp dir.
	_ = os.Remove(resolved)

	return nil
}

// readStdinJSON reads JSON from stdin with size limits and timeout,
// validates UTF-8, strips BOM, and unmarshals into the target.
func readStdinJSON(target any, limits config.LimitsConfig) error {
	// Apply timeout via context.
	ctx, cancel := context.WithTimeout(context.Background(), limits.StdinTimeout)
	defer cancel()

	// Read with size limit. Add 1 byte to detect over-limit input.
	maxSize := int64(limits.MaxJSONSize)
	reader := io.LimitReader(os.Stdin, maxSize+1)

	done := make(chan struct{})
	var data []byte
	var readErr error

	go func() {
		data, readErr = io.ReadAll(reader)
		close(done)
	}()

	select {
	case <-done:
		// Read completed.
	case <-ctx.Done():
		return fmt.Errorf("stdin read timeout after %s: input appears incomplete", limits.StdinTimeout)
	}

	if readErr != nil {
		return fmt.Errorf("failed to read stdin: %w", readErr)
	}

	if len(data) == 0 {
		return fmt.Errorf("no input received on stdin")
	}

	if int64(len(data)) > maxSize {
		return fmt.Errorf("input exceeds maximum size (%d bytes)", limits.MaxJSONSize)
	}

	// Strip UTF-8 BOM if present.
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}

	// Validate UTF-8.
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8 in input")
	}

	// Check for null bytes.
	for i, b := range data {
		if b == 0 {
			return fmt.Errorf("null byte at position %d: null bytes not allowed", i)
		}
	}

	// Unmarshal.
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("JSON parse error: %w", err)
	}

	return nil
}

// validTemporalities is the set of valid temporality values.
var validTemporalities = map[string]bool{
	"immutable": true, "durable": true, "temporal": true, "ephemeral": true,
}

// validKnowledgeTypes is the set of valid knowledge_type values.
var validKnowledgeTypes = map[string]bool{
	"episodic": true, "semantic": true, "procedural": true, "conceptual": true, "reference": true,
}

// validEpistemicStatuses is the set of valid epistemic_status values.
var validEpistemicStatuses = map[string]bool{
	"well_established": true, "probable": true, "speculative": true, "contested": true, "refuted": true,
}

