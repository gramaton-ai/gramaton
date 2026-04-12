package bert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DefaultModel is the default BERT model shipped with Gramaton.
const DefaultModel = "bge-small-en-v1.5"

// DefaultModelRepo is the HuggingFace repository for the default model.
const DefaultModelRepo = "BAAI/bge-small-en-v1.5"

// Known SHA256 checksums for the default model files.
// These are verified after download for integrity.
var knownChecksums = map[string]string{
	// These will be populated after first successful download verification.
	// For now, download succeeds without checksum validation for unknown models.
}

// modelFiles lists the files to download for a BERT model.
var modelFiles = []string{
	"model.safetensors",
	"tokenizer.json",
	"config.json",
}

// ModelDir returns the cache directory for a model.
// Default: ~/.gramaton/models/<model>/
func ModelDir(model string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".gramaton", "models", model)
}

// EnsureModel checks if the model files exist locally. If not, downloads
// them from HuggingFace Hub. Returns the model directory path.
func EnsureModel(ctx context.Context, repo, model string, onProgress func(string)) error {
	dir := ModelDir(model)

	// Check if all required files exist.
	allPresent := true
	for _, f := range modelFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			allPresent = false
			break
		}
	}
	if allPresent {
		return nil
	}

	// Create model directory.
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("bert: create model dir: %w", err)
	}

	// Download each missing file.
	for _, f := range modelFiles {
		dst := filepath.Join(dir, f)
		if _, err := os.Stat(dst); err == nil {
			continue // already exists
		}

		url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", repo, f)
		if onProgress != nil {
			onProgress(fmt.Sprintf("Downloading %s...", f))
		}

		if err := downloadFile(ctx, url, dst); err != nil {
			return fmt.Errorf("bert: download %s: %w", f, err)
		}

		// Verify checksum if known.
		if expected, ok := knownChecksums[f]; ok {
			actual, err := fileChecksum(dst)
			if err != nil {
				os.Remove(dst)
				return fmt.Errorf("bert: checksum %s: %w", f, err)
			}
			if actual != expected {
				os.Remove(dst)
				return fmt.Errorf("bert: checksum mismatch for %s: got %s, want %s", f, actual, expected)
			}
		}

		if onProgress != nil {
			onProgress(fmt.Sprintf("Downloaded %s", f))
		}
	}

	return nil
}

// downloadFile downloads a URL to a local file using atomic write
// (write to .tmp, rename on success). Supports context cancellation.
func downloadFile(ctx context.Context, url, dst string) error {
	client := &http.Client{Timeout: 10 * time.Minute}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return fmt.Errorf("model file not found at %s (HTTP 404) -- check model name", url)
		case resp.StatusCode >= 500:
			return fmt.Errorf("HuggingFace server error (HTTP %d) -- try again later", resp.StatusCode)
		default:
			return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
		}
	}

	// Write to temporary file.
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	_, err = io.Copy(f, resp.Body)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}

	// Atomic rename.
	return os.Rename(tmp, dst)
}

func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
