package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// IsReachable checks if the Ollama API is responding. ctx is honoured
// for cancellation; pass context.Background() if no deadline applies.
func IsReachable(ctx context.Context, endpoint string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// FindBinary returns the path to the ollama binary, or empty string if
// not found.
func FindBinary() string {
	path, err := exec.LookPath("ollama")
	if err != nil {
		return ""
	}
	return path
}

// EnsureRunning starts Ollama if it's installed but not running. Returns
// nil if Ollama is reachable (either already running or successfully
// started). Returns an error if Ollama can't be found or started.
func EnsureRunning(ctx context.Context, endpoint string) error {
	if IsReachable(ctx, endpoint) {
		return nil
	}

	bin := FindBinary()
	if bin == "" {
		return fmt.Errorf("ollama not installed (not found in PATH)")
	}

	// Start ollama serve in the background.
	cmd := exec.Command(bin, "serve")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Env = os.Environ()
	// Detach from our process group so signals delivered to gramaton
	// (SIGINT, SIGHUP) don't propagate to ollama. Without this, the
	// child inherits our pgid and dies when we die -- the zombie
	// processes the user has been seeing.
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ollama serve: %w", err)
	}

	// Release the process so it doesn't become a zombie.
	go func() { _ = cmd.Wait() }()

	// Poll until ready, honouring ctx.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		if IsReachable(ctx, endpoint) {
			return nil
		}
	}

	return fmt.Errorf("ollama started but not responding after 15s")
}

// tagsResponse is the response from /api/tags.
type tagsResponse struct {
	Models []modelInfo `json:"models"`
}

type modelInfo struct {
	Name string `json:"name"`
}

// HasModel checks if a model is available locally in Ollama. ctx is
// honoured for cancellation; pass context.Background() if no deadline.
func HasModel(ctx context.Context, endpoint, model string) bool {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var tags tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return false
	}

	for _, m := range tags.Models {
		if m.Name == model || m.Name == model+":latest" {
			return true
		}
	}
	return false
}

// PullModel pulls a model from the Ollama registry. This can take
// several minutes for the first download. The onProgress callback
// is called with status messages if non-nil.
func PullModel(ctx context.Context, endpoint, model string, onProgress func(string)) error {
	if onProgress != nil {
		onProgress(fmt.Sprintf("Pulling model %s (this may take a few minutes on first run)...", model))
	}

	bin := FindBinary()
	if bin == "" {
		return fmt.Errorf("ollama not found in PATH")
	}

	cmd := exec.CommandContext(ctx, bin, "pull", model)
	cmd.Stdout = os.Stderr // Show pull progress to user.
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ollama pull %s: %w", model, err)
	}

	if onProgress != nil {
		onProgress(fmt.Sprintf("Model %s ready.", model))
	}
	return nil
}
