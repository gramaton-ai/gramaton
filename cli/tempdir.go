package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	// tempSubdir is the subdirectory name owned by gramaton.
	tempSubdir = "gramaton"

	// staleTempAge is how long a temp file can exist before being swept.
	staleTempAge = 1 * time.Hour
)

var tempdirCmd = &cobra.Command{
	Use:   "tempdir",
	Short: "Print the gramaton temp directory path",
	Long: `Prints the path to the gramaton-owned temp directory, creating it
if it does not exist. Agents should write JSON input files here
before passing them to capture, classify, or update via --file.

Files in this directory are automatically deleted after a successful
read, and stale files older than 1 hour are swept on each write command.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := TempDir()
		if err != nil {
			return err
		}
		fmt.Println(dir)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(tempdirCmd)
}

// TempDir returns the gramaton-owned temp directory, creating it if needed.
func TempDir() (string, error) {
	dir := filepath.Join(os.TempDir(), tempSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create temp dir %s: %w", dir, err)
	}
	return dir, nil
}

// sweepStaleTempFiles removes files in the gramaton temp directory
// that are older than staleTempAge. Errors on individual files are
// silently ignored -- this is best-effort cleanup.
func sweepStaleTempFiles() {
	dir := filepath.Join(os.TempDir(), tempSubdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-staleTempAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// isInTempDir reports whether path is inside the gramaton temp directory.
func isInTempDir(path string) bool {
	dir := filepath.Join(os.TempDir(), tempSubdir)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel != "." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..")
}
