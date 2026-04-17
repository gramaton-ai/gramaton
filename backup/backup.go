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
	// Cleanup-on-error: if we return without success, drop the
	// half-written archive so the user doesn't see a corrupt file.
	success := false
	defer func() {
		if !success {
			f.Close()
			os.Remove(archivePath)
		}
	}()

	gz, err := gzip.NewWriterLevel(f, gzip.DefaultCompression)
	if err != nil {
		return "", fmt.Errorf("create gzip writer: %w", err)
	}

	tw := tar.NewWriter(gz)

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

	// Include sanitized config if available. Distinguish file-read
	// errors (non-fatal: backup works without the config sidecar)
	// from tar-write errors (fatal: archive is now corrupt).
	if cfgPath != "" {
		if err := addSanitizedConfig(tw, cfgPath); err != nil {
			if isTarWriteError(err) {
				return "", fmt.Errorf("archive config: %w", err)
			}
			// File-read failure -- log via a hint in the error path
			// would be ideal, but Create has no logger. Silent skip
			// matches the prior policy.
		}
	}

	// Explicit Close + error checks. Defer would swallow errors from
	// these flushes -- and gzip/tar buffer their final blocks until
	// Close, so Close failure means the archive is truncated.
	// (Wave 4 P1-04.)
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("close tar writer: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("close gzip writer: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("fsync archive: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close archive: %w", err)
	}

	success = true
	return archivePath, nil
}

// isTarWriteError returns true when err originates from tar.Writer
// (vs the underlying file read in addSanitizedConfig). Used to
// distinguish "config file missing on disk" (non-fatal) from "tar
// stream broken" (fatal).
func isTarWriteError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "tar:") ||
		strings.Contains(s, "tar.Writer") ||
		strings.Contains(s, "write tar")
}

// Restore extracts a backup archive into the data directory.
// Clears existing data directory contents first.
func Restore(archivePath, dataDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	// Restore is destructive: if it fails midway, the user's data
	// directory must remain intact. Strategy (P0-01 rewrite):
	//   1. Extract the archive into a sibling staging directory
	//      created next to dataDir (same filesystem, so the later
	//      rename is atomic).
	//   2. Verify HEAD is present in the staged content. If not,
	//      abort and remove the staging directory; dataDir untouched.
	//   3. Atomically swap: rename dataDir -> dataDir.replaced-<ts>,
	//      then rename staging -> dataDir, then fsync the parent.
	//   4. Remove the replaced directory (best effort; if it fails
	//      the swap is already complete, so it's just leftover disk).
	//
	// On any failure during 1 or 2, dataDir is unchanged. On failure
	// between the two renames in 3, we attempt to roll back the first
	// rename so the user is not left without a data directory.
	parentDir := filepath.Dir(dataDir)
	tsSuffix := time.Now().UTC().Format("20060102-150405.000000")
	stagingDir := filepath.Join(parentDir, filepath.Base(dataDir)+".restore-staging-"+tsSuffix)
	replacedDir := filepath.Join(parentDir, filepath.Base(dataDir)+".replaced-"+tsSuffix)

	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	// Track whether we still own staging for cleanup on early abort.
	stagingOwned := true
	defer func() {
		if stagingOwned {
			os.RemoveAll(stagingDir) // best-effort cleanup
		}
	}()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	hasHead := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}

		// Strip "data/" prefix to extract into stagingDir.
		name := header.Name
		switch {
		case strings.HasPrefix(name, "data/"):
			name = strings.TrimPrefix(name, "data/")
		case name == "config.yaml":
			// Skip config: backup carries the SHIPPING config, not the
			// environment-specific live one. Restoring it would clobber
			// the user's API keys / endpoints.
			continue
		default:
			continue
		}

		if name == "" || name == "." {
			continue
		}
		if name == "HEAD" {
			hasHead = true
		}

		target := filepath.Join(stagingDir, name)
		// Path traversal protection. Append separator to prevent
		// prefix false positives (/foo/bar matching /foo/barbaz).
		if !strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), filepath.Clean(stagingDir)+string(filepath.Separator)) {
			return fmt.Errorf("path traversal in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := writeRestoredFile(target, tr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported file type in archive: %s (type flag %d)", header.Name, header.Typeflag)
		}
	}

	if !hasHead {
		return fmt.Errorf("invalid backup: no HEAD file found")
	}

	// Stage phase complete. fsync the staging directory so the
	// extracted content is durable before the swap.
	if err := fsyncDir(stagingDir); err != nil {
		return fmt.Errorf("fsync staging: %w", err)
	}

	// Swap phase. Both renames are within parentDir on the same
	// filesystem -> atomic. The window between them is tiny but
	// non-zero; on failure we attempt to roll back the first rename.
	dataDirExists := true
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		dataDirExists = false
	} else if err != nil {
		return fmt.Errorf("stat data dir: %w", err)
	}

	if dataDirExists {
		if err := os.Rename(dataDir, replacedDir); err != nil {
			return fmt.Errorf("rename data dir aside: %w", err)
		}
	}
	if err := os.Rename(stagingDir, dataDir); err != nil {
		// Try to put the original back. Best effort: if this also
		// fails the user has lost data dir under its original name
		// but the data itself survives in replacedDir.
		if dataDirExists {
			if rbErr := os.Rename(replacedDir, dataDir); rbErr != nil {
				return fmt.Errorf("rename staging into place failed (%w); rollback also failed (%v); original data is at %s",
					err, rbErr, replacedDir)
			}
		}
		return fmt.Errorf("rename staging into place: %w", err)
	}
	stagingOwned = false // staging IS the new dataDir now

	// Make the swap durable.
	if err := fsyncDir(parentDir); err != nil {
		// Data is in place; sync failure is logged but not fatal
		// (a subsequent fsync from the engine will catch up).
		fmt.Fprintf(os.Stderr, "warning: fsync parent dir after restore: %v\n", err)
	}

	// Remove the replaced directory. Best effort: failure leaves
	// disk usage but the restore is complete.
	if dataDirExists {
		if err := os.RemoveAll(replacedDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: remove replaced data dir %s: %v\n", replacedDir, err)
		}
	}

	return nil
}

// writeRestoredFile creates a regular file at target, copies the
// archive entry into it, fsyncs the file, and closes it. Caller
// arranges parent directory creation.
func writeRestoredFile(target string, tr *tar.Reader) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir for %s: %w", target, err)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", target, err)
	}
	if _, err := io.Copy(out, tr); err != nil {
		out.Close()
		return fmt.Errorf("copy %s: %w", target, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("fsync %s: %w", target, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", target, err)
	}
	return nil
}

// fsyncDir opens the given directory and fsyncs it. Required for
// durability of the rename(2) operations performed during Restore;
// POSIX fsync on a regular file does not guarantee that the parent
// directory entry is durable.
func fsyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
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

// StripAPIKeys removes sensitive and infrastructure-leaking fields
// from config YAML before the config is written into a backup
// archive. (P1-03 fix: was a blacklist of 4 fields; now a
// whitelist of known-safe ones.)
//
// Stripped fields and rationale:
//   - api_key, api_key_env, api_key_file: literal keys, env-var
//     names that may themselves contain hints, and filesystem
//     paths that leak the backup origin's directory layout.
//   - base_url: leaks internal proxies / private endpoints.
//   - aws_profile: leaks credential profile names.
//   - aws_access_key_id_env, aws_secret_access_key_env: env-var
//     names hint at the user's AWS setup.
//   - region: kept (region names like "us-east-1" are not sensitive).
//
// Implementation: inside the llm/embedding sections, only fields
// in the whitelist below are preserved. Everything else (including
// fields added by future config changes) is stripped. New "safe"
// fields must be added explicitly to the whitelist. This is
// safer than a blacklist because forgetting to update the strip
// list when adding a new sensitive field would silently leak.
func StripAPIKeys(data []byte) ([]byte, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	stripToWhitelist(raw, "llm", llmSafeFields)
	stripToWhitelist(raw, "embedding", embeddingSafeFields)

	return yaml.Marshal(raw)
}

// llmSafeFields enumerates every llm.* config field safe to ship
// in a backup. Anything not listed is stripped.
var llmSafeFields = map[string]bool{
	"provider": true,
	"model":    true,
	"models":   true,
	"region":   true, // region names like "us-east-1" are not sensitive
}

// embeddingSafeFields enumerates every embedding.* config field
// safe to ship in a backup.
var embeddingSafeFields = map[string]bool{
	"provider":  true,
	"model":     true,
	"dimension": true,
	"region":    true,
}

// stripToWhitelist replaces the named section's contents with only
// those keys present in the safe map. If the section is missing or
// not a map, nothing happens.
func stripToWhitelist(m map[string]any, section string, safe map[string]bool) {
	sub, ok := m[section]
	if !ok {
		return
	}
	subMap, ok := sub.(map[string]any)
	if !ok {
		return
	}
	for key := range subMap {
		if !safe[key] {
			delete(subMap, key)
		}
	}
}
