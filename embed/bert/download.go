package bert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DefaultModel is the default BERT model shipped with Gramaton.
const DefaultModel = "bge-small-en-v1.5"

// DefaultModelRepo is the HuggingFace repository for the default model.
const DefaultModelRepo = "BAAI/bge-small-en-v1.5"

// modelFiles lists the files to download for a BERT model.
var modelFiles = []string{
	"model.safetensors",
	"tokenizer.json",
	"config.json",
}

// sidecarSuffix is the extension for the per-file SHA256 sidecar.
// The sidecar holds the hash recorded at first successful download
// (TOFU) and is checked on every subsequent EnsureModel call.
const sidecarSuffix = ".sha256"

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
// them from HuggingFace Hub. Always verifies every file's SHA256 against
// its sidecar before returning -- catches on-disk corruption between runs.
//
// Integrity model:
//   - Downloads verify Content-Length and write a SHA256 sidecar on success.
//   - Subsequent loads recompute the hash and compare against the sidecar.
//   - Mismatch quarantines the bad file (renamed to .suspect.<unix-ts>)
//     and returns an error; restarting will re-download cleanly while
//     preserving the suspect bytes for forensic analysis.
//   - File present without sidecar (e.g., manually placed) bootstraps the
//     sidecar with a warning log.
//
// This is trust-on-first-use: the first download is whatever HF serves.
// Subsequent corruption, truncation, or tampering is caught.
func EnsureModel(ctx context.Context, repo, model string, onProgress func(string)) error {
	dir := ModelDir(model)

	// Create model directory.
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("bert: create model dir: %w", err)
	}

	// Download any missing files.
	for _, f := range modelFiles {
		dst := filepath.Join(dir, f)
		if _, err := os.Stat(dst); err == nil {
			continue
		}

		url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", repo, f)
		if onProgress != nil {
			onProgress(fmt.Sprintf("Downloading %s...", f))
		}

		if err := downloadFile(ctx, url, dst); err != nil {
			return fmt.Errorf("bert: download %s: %w", f, err)
		}

		if onProgress != nil {
			onProgress(fmt.Sprintf("Downloaded %s", f))
		}
	}

	// Verify integrity of every file (catches on-disk corruption between
	// runs and TOFU-bootstraps any file we didn't download ourselves).
	for _, f := range modelFiles {
		if err := verifyOrBootstrapSidecar(filepath.Join(dir, f)); err != nil {
			return err
		}
	}

	return nil
}

// verifyOrBootstrapSidecar checks the file's SHA256 against its sidecar.
// On mismatch, renames both file and sidecar to .suspect.<unix-ts> for
// forensic analysis and returns a structured error. On missing sidecar
// (legacy or hand-placed file), computes the hash and writes the sidecar
// with a warning log (TOFU bootstrap).
func verifyOrBootstrapSidecar(path string) error {
	sidecar := path + sidecarSuffix

	actual, err := fileChecksum(path)
	if err != nil {
		return fmt.Errorf("bert: checksum %s: %w", filepath.Base(path), err)
	}

	expectedBytes, err := os.ReadFile(sidecar)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("bert: read sidecar %s: %w", filepath.Base(sidecar), err)
		}
		// Sidecar missing -- TOFU bootstrap.
		slog.Warn("bert: model file present without integrity sidecar -- recording current bytes as trusted (trust-on-first-use)",
			"component", "embed",
			"file", filepath.Base(path),
			"sha256", actual)
		return writeSidecar(sidecar, actual)
	}
	expected := string(expectedBytes)
	// Strip optional trailing newline written by some editors.
	if len(expected) > 0 && expected[len(expected)-1] == '\n' {
		expected = expected[:len(expected)-1]
	}

	if actual != expected {
		ts := time.Now().Unix()
		quarantineFile := fmt.Sprintf("%s.suspect.%d", path, ts)
		quarantineSidecar := fmt.Sprintf("%s.suspect.%d", sidecar, ts)
		// Best-effort rename of both so the next start re-downloads.
		_ = os.Rename(path, quarantineFile)
		_ = os.Rename(sidecar, quarantineSidecar)
		return fmt.Errorf("bert: integrity check failed for %s: expected sha256 %s, got %s -- file quarantined to %s, restart to re-download",
			filepath.Base(path), expected, actual, filepath.Base(quarantineFile))
	}
	return nil
}

// writeSidecar atomically writes the sidecar via .tmp + rename.
func writeSidecar(path, hash string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(hash), 0600); err != nil {
		return fmt.Errorf("bert: write sidecar %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("bert: rename sidecar %s: %w", filepath.Base(path), err)
	}
	return nil
}

// downloadFile downloads a URL to a local file using atomic write
// (write to .tmp, rename on success). Verifies Content-Length matches
// body bytes and writes a SHA256 sidecar on success. Supports context
// cancellation.
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

	// Write to temporary file while hashing.
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(f, hasher), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}

	// Verify Content-Length matches body bytes -- catches truncated
	// downloads (server cut connection mid-stream, no error from
	// io.Copy because the connection closed cleanly). resp.ContentLength
	// is -1 when the server omitted the header; in that case we can't
	// verify and skip the check.
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		_ = os.Remove(tmp)
		return fmt.Errorf("size mismatch from %s: got %d bytes, server declared %d (truncated download)",
			url, written, resp.ContentLength)
	}

	// Atomic rename.
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	// Write sidecar so subsequent loads can verify the bytes haven't
	// changed on disk.
	hash := hex.EncodeToString(hasher.Sum(nil))
	return writeSidecar(dst+sidecarSuffix, hash)
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
