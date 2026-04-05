// Package backup provides backup/restore, export, and import for the
// knowledge store. Depends on storage and core but not on server or cli.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Create creates a compressed tar.gz backup of the data directory.
// If storeName is provided and non-empty, it is included in the
// backup filename for identification. Returns the path to the
// created archive.
func Create(dataDir, cfgPath, outputDir string, storeName ...string) (string, error) {
	if outputDir == "" {
		outputDir = DefaultBackupDir()
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	// ISO8601 timestamp with dashes instead of colons for filesystem safety.
	// Include fractional seconds to avoid collisions on rapid backups.
	ts := time.Now().UTC().Format("2006-01-02T15-04-05.000Z")
	var filename string
	if len(storeName) > 0 && storeName[0] != "" {
		filename = fmt.Sprintf("gramaton-backup-%s-%s.tar.gz", storeName[0], ts)
	} else {
		filename = fmt.Sprintf("gramaton-backup-%s.tar.gz", ts)
	}
	archivePath := filepath.Join(outputDir, filename)

	f, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return "", fmt.Errorf("create gzip writer: %w", err)
	}
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	// Walk the data directory and add files.
	if err := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Relative path within the archive.
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}

		// Skip excluded files.
		base := filepath.Base(path)
		if shouldExclude(base) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.Join("data", rel)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(tw, file)
		return err
	}); err != nil {
		return "", fmt.Errorf("archive data: %w", err)
	}

	// Include sanitized config if available.
	if cfgPath != "" {
		if err := addSanitizedConfig(tw, cfgPath); err != nil {
			// Non-fatal: backup works without config.
			_ = err
		}
	}

	return archivePath, nil
}

// Restore extracts a backup archive into the data directory.
// Clears existing data directory contents first.
func Restore(archivePath, dataDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	// Validate: scan for HEAD file.
	hasHead := false
	headers := make([]tar.Header, 0)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		headers = append(headers, *header)
		if header.Name == "data/HEAD" || header.Name == "HEAD" {
			hasHead = true
		}
	}
	if !hasHead {
		return fmt.Errorf("invalid backup: no HEAD file found")
	}

	// Clear existing data directory (preserve the directory itself).
	entries, err := os.ReadDir(dataDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read data dir: %w", err)
	}
	for _, e := range entries {
		p := filepath.Join(dataDir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("clear %s: %w", p, err)
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Re-open and extract.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek archive: %w", err)
	}
	gz2, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("reopen gzip: %w", err)
	}
	defer gz2.Close()

	tr2 := tar.NewReader(gz2)
	for {
		header, err := tr2.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive entry: %w", err)
		}

		// Strip "data/" prefix to extract into dataDir.
		name := header.Name
		if strings.HasPrefix(name, "data/") {
			name = strings.TrimPrefix(name, "data/")
		} else if name == "config.yaml" {
			// Skip config -- don't restore environment-specific settings.
			continue
		} else {
			continue
		}

		if name == "" || name == "." {
			continue
		}

		target := filepath.Join(dataDir, name)

		// Path traversal protection.
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dataDir)) {
			return fmt.Errorf("path traversal in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr2); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			// Reject symlinks, hardlinks, and other special file types.
			return fmt.Errorf("unsupported file type in archive: %s (type flag %d)", header.Name, header.Typeflag)
		}
	}

	return nil
}

// ApplyRetention deletes the oldest backups until at most retain files
// remain. Returns paths of deleted files. If retain is 0, does nothing
// (unlimited).
func ApplyRetention(backupDir string, retain int) ([]string, error) {
	if retain <= 0 {
		return nil, nil
	}

	files, err := listBackups(backupDir)
	if err != nil {
		return nil, err
	}

	if len(files) <= retain {
		return nil, nil
	}

	// Delete oldest (files are sorted oldest-first).
	toDelete := files[:len(files)-retain]
	var deleted []string
	for _, f := range toDelete {
		if err := os.Remove(f); err != nil {
			return deleted, fmt.Errorf("delete %s: %w", f, err)
		}
		deleted = append(deleted, f)
	}

	return deleted, nil
}

// DefaultBackupDir returns the default backup directory.
func DefaultBackupDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gramaton", "backups")
}

// IsBackupArchive checks if a file looks like a Gramaton backup.
func IsBackupArchive(path string) bool {
	return strings.HasSuffix(path, ".tar.gz") &&
		strings.Contains(filepath.Base(path), "gramaton-backup-")
}

// listBackups returns backup files sorted by modification time (oldest first).
func listBackups(dir string) ([]string, error) {
	pattern := filepath.Join(dir, "gramaton-backup-*.tar.gz")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	type fileEntry struct {
		path    string
		modTime time.Time
	}
	var entries []fileEntry
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		entries = append(entries, fileEntry{path: m, modTime: info.ModTime()})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.Before(entries[j].modTime)
	})

	result := make([]string, len(entries))
	for i, e := range entries {
		result[i] = e.path
	}
	return result, nil
}

// shouldExclude returns true for files that should not be in backups.
func shouldExclude(name string) bool {
	switch {
	case name == "server.json":
		return true
	case strings.HasPrefix(name, "gramaton.log"):
		return true
	case strings.HasPrefix(name, ".gramaton-"):
		return true // temp files
	case strings.HasPrefix(name, ".chunk-"):
		return true
	}
	return false
}

// addSanitizedConfig reads the config file, strips sensitive fields,
// and adds it to the tar archive.
func addSanitizedConfig(tw *tar.Writer, cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}

	sanitized, err := StripAPIKeys(data)
	if err != nil {
		return err
	}

	header := &tar.Header{
		Name:    "config.yaml",
		Size:    int64(len(sanitized)),
		Mode:    0o600,
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = tw.Write(sanitized)
	return err
}

// StripAPIKeys removes sensitive values from config YAML.
func StripAPIKeys(data []byte) ([]byte, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	// Strip known sensitive fields.
	stripNestedKey(raw, "llm", "api_key_env")
	stripNestedKey(raw, "embedding", "api_key_env")
	stripNestedKey(raw, "embedding", "aws_access_key_id_env")
	stripNestedKey(raw, "embedding", "aws_secret_access_key_env")

	return yaml.Marshal(raw)
}

func stripNestedKey(m map[string]any, section, key string) {
	if sub, ok := m[section]; ok {
		if subMap, ok := sub.(map[string]any); ok {
			delete(subMap, key)
		}
	}
}
