package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AtomicWriteFile writes data to a file atomically via temp file +
// rename. The parent directory is fsynced after the rename so that a
// crash between the rename(2) syscall and the next sync cannot lose
// the directory entry change. POSIX requires this -- a fsync on the
// regular file does NOT make the rename durable. (Wave 3 P1-42.)
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".gramaton-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	// Apply the requested mode BEFORE the file is closed so there
	// is no window during which the file exists on disk under
	// CreateTemp's default 0o600 (or potentially the umask, on
	// older Go versions). All current callers ask for 0o600 so the
	// observable window is zero today, but doing it this way keeps
	// the function safe for callers that ask for a different mode.
	// (Wave 6 P1-53.)
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpPath, path, err)
	}
	if err := fsyncDir(dir); err != nil {
		// Data is written and renamed; missing dir-fsync risks losing
		// the rename on crash but does not poison the file. Surface
		// as a warning rather than fail the write.
		return fmt.Errorf("fsync parent dir %s: %w", dir, err)
	}

	success = true
	return nil
}

// fsyncDir opens the given directory and fsyncs it. Required after
// rename(2) so that the directory entry change is durable.
func fsyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// RefsDir returns the refs directory for the given data directory.
func RefsDir(dataDir string) string {
	return filepath.Join(dataDir, "refs")
}

// ActiveBranch returns the name of the active branch.
func ActiveBranch(dataDir string) string {
	data, err := os.ReadFile(filepath.Join(dataDir, "BRANCH"))
	if err != nil {
		return "main"
	}
	return strings.TrimSpace(string(data))
}

// SetActiveBranch sets the active branch.
func SetActiveBranch(dataDir, name string) error {
	return AtomicWriteFile(filepath.Join(dataDir, "BRANCH"), []byte(name), 0o600)
}

// ReadRef reads the commit hash for a named branch ref.
func ReadRef(dataDir, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(RefsDir(dataDir), name))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// WriteRef writes a commit hash for a named branch ref.
func WriteRef(dataDir, name, hash string) error {
	dir := RefsDir(dataDir)
	os.MkdirAll(dir, 0o700)
	return AtomicWriteFile(filepath.Join(dir, name), []byte(hash), 0o600)
}

// DeleteRef removes a branch ref.
func DeleteRef(dataDir, name string) error {
	return os.Remove(filepath.Join(RefsDir(dataDir), name))
}

// ValidBranchName validates a branch name.
func ValidBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name is required")
	}
	if len(name) > 128 {
		return fmt.Errorf("branch name too long (max 128 characters)")
	}
	for _, c := range name {
		if c == '/' || c == '\\' || c == '.' || c == '\x00' {
			return fmt.Errorf("branch name contains invalid character")
		}
	}
	if name == "HEAD" || name == "BRANCH" {
		return fmt.Errorf("reserved name %q", name)
	}
	return nil
}

// TruncHash returns the first 12 characters of a hash for display.
func TruncHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
