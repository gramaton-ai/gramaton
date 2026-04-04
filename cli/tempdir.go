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
// that are older than staleTempAge. Only removes regular files --
// symlinks and other non-regular entries are removed unconditionally
// since they should not exist in our temp directory. Errors on
// individual files are silently ignored.
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
		path := filepath.Join(dir, entry.Name())

		// Remove symlinks and other non-regular entries unconditionally --
		// they should never be in our temp directory.
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			_ = os.Remove(path)
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

// isInTempDir reports whether path is inside the gramaton temp directory.
// Uses EvalSymlinks to handle platform symlinks (e.g., /var -> /private/var
// on macOS). The path argument should already be resolved via EvalSymlinks
// by the caller when checking against symlink attacks.
func isInTempDir(path string) bool {
	dir := filepath.Join(os.TempDir(), tempSubdir)

	// Resolve both sides with EvalSymlinks to normalize platform symlinks.
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// Dir may not exist yet -- fall back to Abs.
		resolvedDir, err = filepath.Abs(dir)
		if err != nil {
			return false
		}
	}

	// For the path, try EvalSymlinks first. If the file doesn't exist,
	// resolve the parent directory and join the base name -- this handles
	// paths to files that haven't been created yet.
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		parentResolved, perr := filepath.EvalSymlinks(filepath.Dir(path))
		if perr != nil {
			return false
		}
		resolvedPath = filepath.Join(parentResolved, filepath.Base(path))
	}

	rel, err := filepath.Rel(resolvedDir, resolvedPath)
	if err != nil {
		return false
	}
	return rel != "." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..")
}
