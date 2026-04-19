package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/backup"
)

// BackupInfo describes one archive found in the backup directory.
type BackupInfo struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Created   string `json:"created"`
}

type BackupStatusResponse struct {
	BackupDir string       `json:"backup_dir"`
	Backups   []BackupInfo `json:"backups"`
	Count     int          `json:"count"`
}

type BackupCreateResponse struct {
	Path       string   `json:"path"`
	SizeBytes  int64    `json:"size_bytes"`
	DeletedOld []string `json:"deleted_old,omitempty"`
}

type RestoreRequest struct {
	Path  string `json:"path" jsonschema:"absolute path to .tar.gz archive"`
	Force bool   `json:"force" jsonschema:"must be true -- restore overwrites the current store"`
}

type RestoreResponse struct {
	Restored bool   `json:"restored"`
	Path     string `json:"path"`
}

type ExportRequest struct {
	Format string `json:"format,omitempty" jsonschema:"json|csv|markdown (default: json)"`
}

type ImportRequest struct {
	Records []backup.ExportRecord `json:"records"`
}

type ImportResponse struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
	Errors   int `json:"errors"`
}

const (
	BackupStatusDescription  = "List existing backups with size and creation time."
	BackupCreateDescription  = "Create a snapshot-consistent tar.gz backup. Snapshots HEAD/refs under a brief read lock, then runs compression off-lock so concurrent writes are not blocked."
	BackupRestoreDescription = "Restore the store from a backup archive. Overwrites current data -- requires force=true."
	BackupExportDescription  = "Export the store as json, csv, or markdown. Streams the export bytes to the caller."
	BackupImportDescription  = "Import records from a prior export (JSON lines)."
)

// BackupStatus lists archives in the backup directory. No engine
// lock needed -- the backup directory is independent of the live
// store.
func (a *API) BackupStatus(ctx context.Context) (BackupStatusResponse, *APIError) {
	backupDir := a.engine.Config().Backup.Dir
	if backupDir == "" {
		backupDir = backup.DefaultBackupDir()
	}

	files, _ := filepath.Glob(filepath.Join(backupDir, "gramaton-backup-*.tar.gz"))
	resp := BackupStatusResponse{
		BackupDir: backupDir,
		Backups:   []BackupInfo{},
	}
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		resp.Backups = append(resp.Backups, BackupInfo{
			Path:      f,
			SizeBytes: info.Size(),
			Created:   info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	resp.Count = len(resp.Backups)
	return resp, nil
}

// BackupCreate snapshots HEAD/refs/FORMAT under RLock, releases,
// and then runs the compression pass off-lock. Concurrent writes
// can land during the compression; they are not included in the
// archive (which is what "snapshot at time T" means).
func (a *API) BackupCreate(ctx context.Context) (BackupCreateResponse, *APIError) {
	start := time.Now()
	cfg := a.engine.Config()
	backupDir := cfg.Backup.Dir
	if backupDir == "" {
		backupDir = backup.DefaultBackupDir()
	}
	cfgPath := filepath.Join(a.configDir, "config.yaml")

	// Phase 1: snapshot mutable pointers under RLock.
	a.engine.RLock()
	snap, snapErr := backup.ReadSnapshot(cfg.DataDir)
	a.engine.RUnlock()
	if snapErr != nil {
		a.log.Warn("backup: read snapshot failed", "component", "backup", "err", snapErr)
		return BackupCreateResponse{}, ErrInternal("failed to snapshot store state")
	}

	// Phase 2: compress off-lock.
	archivePath, err := backup.CreateSnapshot(snap, cfg.DataDir, cfgPath, backupDir, a.storeName)
	if err != nil {
		a.log.Error("backup failed", "component", "backup", "err", err)
		return BackupCreateResponse{}, ErrInternal("backup failed")
	}

	deleted, _ := backup.ApplyRetention(backupDir, cfg.Backup.Retain)
	info, _ := os.Stat(archivePath)
	var sizeBytes int64
	if info != nil {
		sizeBytes = info.Size()
	}
	a.log.Info("backup created",
		"component", "backup",
		"path", archivePath,
		"size_bytes", sizeBytes,
		"duration_ms", time.Since(start).Milliseconds())
	return BackupCreateResponse{
		Path:       archivePath,
		SizeBytes:  sizeBytes,
		DeletedOld: deleted,
	}, nil
}

// BackupRestore overwrites the current data directory with the
// archive's contents. Necessarily holds the engine write lock for
// the duration of extraction + index rebuild; this is a rare
// destructive operation and splitting it further would add
// complexity without real value.
func (a *API) BackupRestore(ctx context.Context, req RestoreRequest) (RestoreResponse, *APIError) {
	start := time.Now()
	if req.Path == "" {
		return RestoreResponse{}, ErrMissing("path is required")
	}
	if !filepath.IsAbs(req.Path) {
		return RestoreResponse{}, ErrInvalid("path must be absolute")
	}
	if filepath.Ext(req.Path) != ".gz" {
		return RestoreResponse{}, ErrInvalid("path must be a .tar.gz file")
	}
	if !req.Force {
		return RestoreResponse{}, ErrInvalid("restore overwrites the current store. Set force: true to confirm.")
	}

	dataDir := a.engine.Config().DataDir

	a.engine.Lock()
	defer a.engine.Unlock()

	if err := backup.Restore(req.Path, dataDir); err != nil {
		a.log.Error("restore failed", "component", "backup", "err", err)
		return RestoreResponse{}, ErrInternal("restore failed")
	}

	// Reload graph from restored HEAD.
	headData, err := os.ReadFile(filepath.Join(dataDir, "HEAD"))
	if err != nil {
		a.log.Error("restore: no HEAD after extraction", "component", "backup", "err", err)
		return RestoreResponse{}, ErrInternal("no HEAD after restore")
	}
	headHash := strings.TrimSpace(string(headData))
	if headHash != "" {
		if _, err := a.engine.Graph().Load(a.engine.Store(), headHash); err != nil {
			a.log.Error("restore: graph load failed", "component", "backup", "err", err)
			return RestoreResponse{}, ErrInternal("failed to load graph after restore")
		}
		a.engine.RebuildAllIndexes()
	}

	a.log.Info("restore complete",
		"component", "backup",
		"path", req.Path,
		"duration_ms", time.Since(start).Milliseconds())
	return RestoreResponse{Restored: true, Path: req.Path}, nil
}

// BackupExport streams the store in the requested format. The
// export runs under RLock into a buffer, the lock is released, and
// then the buffer is handed to the caller's writer. Buffer avoids
// holding the lock across slow network writes.
//
// Returns the content-type for the format on success.
func (a *API) BackupExport(ctx context.Context, req ExportRequest, w io.Writer) (string, *APIError) {
	format := req.Format
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" && format != "markdown" {
		return "", ErrInvalid("format must be json, csv, or markdown")
	}

	var buf bytes.Buffer

	a.engine.RLock()
	var err error
	switch format {
	case "json":
		err = backup.ExportJSON(&buf, a.engine)
	case "csv":
		err = backup.ExportCSV(&buf, a.engine)
	case "markdown":
		err = backup.ExportMarkdown(&buf, a.engine)
	}
	a.engine.RUnlock()
	if err != nil {
		a.log.Warn("export failed", "component", "backup", "format", format, "err", err)
		return "", ErrInternal("export failed")
	}

	if _, err := w.Write(buf.Bytes()); err != nil {
		return "", ErrInternal(fmt.Sprintf("write response: %v", err))
	}

	switch format {
	case "json":
		return "application/x-ndjson", nil
	case "csv":
		return "text/csv", nil
	default:
		return "text/markdown", nil
	}
}

// BackupImport takes a batch of ExportRecord values and imports
// them. Current behavior matches pre-migration -- backup.ImportJSON
// handles its own concurrency, and we rebuild indexes after.
func (a *API) BackupImport(ctx context.Context, req ImportRequest) (ImportResponse, *APIError) {
	start := time.Now()
	if len(req.Records) == 0 {
		return ImportResponse{}, ErrMissing("records array is required")
	}

	// Re-serialize to JSON Lines for the existing import path.
	var buf bytes.Buffer
	for _, rec := range req.Records {
		line, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	maxContent := a.engine.Config().Limits.MaxContentLength
	result, err := backup.ImportJSON(&buf, a.engine, maxContent)
	if err != nil {
		a.log.Warn("import failed", "component", "backup", "err", err)
		return ImportResponse{}, ErrInternal("import failed")
	}

	a.engine.Lock()
	a.engine.RebuildAllIndexes()
	a.engine.Unlock()

	a.log.Info("import complete",
		"component", "backup",
		"imported", result.Imported,
		"skipped", result.Skipped,
		"errors", result.Errors,
		"duration_ms", time.Since(start).Milliseconds())
	return ImportResponse{
		Imported: result.Imported,
		Skipped:  result.Skipped,
		Errors:   result.Errors,
	}, nil
}

