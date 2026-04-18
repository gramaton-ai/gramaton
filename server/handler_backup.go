package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/backup"
)

type restoreRequest struct {
	Path  string `json:"path"`
	Force bool   `json:"force"`
}

type exportRequest struct {
	Format string `json:"format"` // json, csv, markdown
}

type importRequest struct {
	Records []backup.ExportRecord `json:"records"`
}

func (s *Server) handleBackupStatus(w http.ResponseWriter, _ *http.Request) {
	cfg := s.engine.Config()
	backupDir := cfg.Backup.Dir
	if backupDir == "" {
		backupDir = backup.DefaultBackupDir()
	}

	files, _ := filepath.Glob(filepath.Join(backupDir, "gramaton-backup-*.tar.gz"))
	var backups []map[string]any
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		backups = append(backups, map[string]any{
			"path":       f,
			"size_bytes": info.Size(),
			"created":    info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	if backups == nil {
		backups = []map[string]any{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"backup_dir": backupDir,
		"backups":    backups,
		"count":      len(backups),
	})
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !isLoopback(r) {
		s.writeError(w, http.StatusForbidden, "forbidden",
			"backup is restricted to loopback connections", false)
		return
	}

	cfg := s.engine.Config()
	backupDir := cfg.Backup.Dir
	if backupDir == "" {
		backupDir = backup.DefaultBackupDir()
	}

	cfgPath := filepath.Join(s.cfg.ConfigDir, "config.yaml")
	dataDir := cfg.DataDir

	s.engine.RLock()
	archivePath, err := backup.Create(dataDir, cfgPath, backupDir, s.cfg.StoreName)
	s.engine.RUnlock()

	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "backup_error", "backup failed", false)
		s.log.Error("backup failed", "component", "backup", "err", err)
		return
	}

	// Apply retention.
	deleted, _ := backup.ApplyRetention(backupDir, cfg.Backup.Retain)

	info, _ := os.Stat(archivePath)
	var sizeBytes int64
	if info != nil {
		sizeBytes = info.Size()
	}

	s.log.Info("backup created", "component", "backup", "path", archivePath, "size_bytes", sizeBytes, "duration_ms", time.Since(start).Milliseconds())

	s.writeJSON(w, http.StatusOK, map[string]any{
		"path":        archivePath,
		"size_bytes":  sizeBytes,
		"deleted_old": deleted,
	})
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !isLoopback(r) {
		s.writeError(w, http.StatusForbidden, "forbidden",
			"restore is restricted to loopback connections", false)
		return
	}

	var req restoreRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if req.Path == "" {
		s.writeError(w, http.StatusBadRequest, "missing_field", "path is required", true)
		return
	}
	// Validate restore path: must be absolute and end with .tar.gz.
	if !filepath.IsAbs(req.Path) {
		s.writeError(w, http.StatusBadRequest, "input_error", "path must be absolute", true)
		return
	}
	if filepath.Ext(req.Path) != ".gz" {
		s.writeError(w, http.StatusBadRequest, "input_error", "path must be a .tar.gz file", true)
		return
	}
	if !req.Force {
		s.writeError(w, http.StatusBadRequest, "confirmation_required",
			"restore overwrites the current store. Set force: true to confirm.", true)
		return
	}

	dataDir := s.engine.Config().DataDir

	s.engine.Lock()
	defer s.engine.Unlock()

	if err := backup.Restore(req.Path, dataDir); err != nil {
		s.writeError(w, http.StatusInternalServerError, "restore_error", "restore failed", false)
		s.log.Error("restore failed", "component", "backup", "err", err)
		return
	}

	// Reload graph from restored HEAD.
	headData, err := os.ReadFile(filepath.Join(dataDir, "HEAD"))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "restore_error", "no HEAD after restore", false)
		return
	}
	headHash := strings.TrimSpace(string(headData))
	if headHash != "" {
		if _, err := s.engine.Graph().Load(s.engine.Store(), headHash); err != nil {
			s.writeError(w, http.StatusInternalServerError, "restore_error",
				fmt.Sprintf("failed to load graph after restore: %v", err), false)
			s.log.Error("graph load failed after restore", "component", "backup", "err", err)
			return
		}
		s.engine.RebuildAllIndexes()
	}

	s.log.Info("restore complete", "component", "backup", "path", req.Path, "duration_ms", time.Since(start).Milliseconds())

	s.writeJSONLocked(w, http.StatusOK, map[string]any{
		"restored": true,
		"path":     req.Path,
	})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	var req exportRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	format := req.Format
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" && format != "markdown" {
		s.writeError(w, http.StatusBadRequest, "input_error", "format must be json, csv, or markdown", true)
		return
	}

	// Buffer the export to release the lock before sending.
	var buf bytes.Buffer

	s.engine.RLock()
	var err error
	switch format {
	case "json":
		err = backup.ExportJSON(&buf, s.engine)
	case "csv":
		err = backup.ExportCSV(&buf, s.engine)
	case "markdown":
		err = backup.ExportMarkdown(&buf, s.engine)
	}
	s.engine.RUnlock()

	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "export_error", "export failed", false)
		return
	}

	// Set content type based on format.
	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/x-ndjson")
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
	case "markdown":
		w.Header().Set("Content-Type", "text/markdown")
	}

	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req importRequest
	if err := parseJSON(r, &req, maxIngestBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if len(req.Records) == 0 {
		s.writeError(w, http.StatusBadRequest, "missing_field", "records array is required", true)
		return
	}

	// Convert to JSON Lines for the import function.
	var buf bytes.Buffer
	for _, rec := range req.Records {
		line, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	maxContent := s.engine.Config().Limits.MaxContentLength
	result, err := backup.ImportJSON(&buf, s.engine, maxContent)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "import_error", "import failed", false)
		return
	}

	// Rebuild all indexes after bulk import. ImportJSON only populates
	// PropIdx; BM25 and VecIdx are rebuilt from graph state.
	s.engine.RebuildAllIndexes()

	s.log.Info("import complete",
		"component", "backup",
		"imported", result.Imported,
		"skipped", result.Skipped,
		"errors", result.Errors,
		"duration_ms", time.Since(start).Milliseconds())

	s.writeJSON(w, http.StatusOK, result)
}