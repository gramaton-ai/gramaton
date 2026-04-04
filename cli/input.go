package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unicode/utf8"


	"github.com/brandonlattin/gramaton/config"
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

// readFileJSON reads and parses JSON from a file with the same validation
// as readStdinJSON. Deletes the file after successful parse if it is
// inside the gramaton temp directory.
func readFileJSON(path string, target any, limits config.LimitsConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file %s: %w", path, err)
	}

	if int64(len(data)) > int64(limits.MaxJSONSize) {
		return fmt.Errorf("file exceeds maximum size (%d bytes)", limits.MaxJSONSize)
	}

	if len(data) == 0 {
		return fmt.Errorf("file %s is empty", path)
	}

	// Strip UTF-8 BOM if present.
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}

	// Validate UTF-8.
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8 in file %s", path)
	}

	// Check for null bytes.
	for i, b := range data {
		if b == 0 {
			return fmt.Errorf("null byte at position %d in file %s", i, path)
		}
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("JSON parse error in file %s: %w", path, err)
	}

	// Clean up if the file is in our temp directory.
	if isInTempDir(path) {
		_ = os.Remove(path)
	}

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

// validateFloat64Range checks that a float64 is in [min, max].
func validateFloat64Range(name string, val *float64, min, max float64) error {
	if val == nil {
		return nil
	}
	if *val < min || *val > max {
		return fmt.Errorf("%s must be between %g and %g, got %g", name, min, max, *val)
	}
	return nil
}

// validateEnum checks that a string is in the allowed set.
func validateEnum(name, val string, allowed map[string]bool) error {
	if val == "" {
		return nil
	}
	if !allowed[val] {
		keys := make([]string, 0, len(allowed))
		for k := range allowed {
			keys = append(keys, k)
		}
		return fmt.Errorf("%s must be one of %v, got %q", name, keys, val)
	}
	return nil
}

// validateStringLength checks a string doesn't exceed a max length.
func validateStringLength(name, val string, maxLen int) error {
	if len(val) > maxLen {
		return fmt.Errorf("%s exceeds maximum length (%d bytes)", name, maxLen)
	}
	return nil
}

// validateStringFieldUTF8 checks that a string field is valid UTF-8
// and contains no null bytes.
func validateStringFieldUTF8(name, val string) error {
	if !utf8.ValidString(val) {
		return fmt.Errorf("invalid UTF-8 in field %q", name)
	}
	for i := range val {
		if val[i] == 0 {
			return fmt.Errorf("null byte in field %q at position %d", name, i)
		}
	}
	return nil
}
