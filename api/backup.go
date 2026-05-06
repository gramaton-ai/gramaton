package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/backup"
	"github.com/gramaton-ai/gramaton/embed"
	"github.com/gramaton-ai/gramaton/search"
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

// ExportRequest controls a streaming record export. Filter fields
// mirror SearchRequest's most useful subset; when none are set the
// request behaves like a full-store dump (legacy compatibility).
// When any filter is set, only matching records are exported.
//
// The export is exhaustive over the matched set -- there is no
// candidate_cap or pagination on this path. The CLI is the
// "give me everything" escape valve from gramaton_search's MCP-side
// pagination.
type ExportRequest struct {
	Format string `json:"format,omitempty" jsonschema:"jsonl|json|csv|markdown (default: jsonl). jsonl streams one JSON object per line; json buffers a parseable array."`

	// Filter fields. Additive; absent fields mean "no filter on
	// this dimension."
	Text             string         `json:"text,omitempty" jsonschema:"vector-similarity query; ranks results by relevance (otherwise sorted by created_at desc)"`
	Match            string         `json:"match,omitempty" jsonschema:"literal substring match across content fields (case-insensitive)"`
	Store            string         `json:"store,omitempty" jsonschema:"memory|sessions|all (default: all)"`
	Keywords         []string       `json:"keywords,omitempty" jsonschema:"keywords that must all be present"`
	Temporality      string         `json:"temporality,omitempty"`
	KnowledgeType    string         `json:"knowledge_type,omitempty"`
	EpistemicStatus  string         `json:"epistemic_status,omitempty"`
	Resolution       string         `json:"resolution,omitempty"`
	ProcessingStatus string         `json:"processing_status,omitempty"`
	Since            string         `json:"since,omitempty" jsonschema:"YYYY-MM-DD or RFC3339 lower bound on created_at"`
	Meta             map[string]string `json:"meta,omitempty"`
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
	// Populate the live JobStore handle so the walker can take a
	// bbolt-native snapshot of jobs.db (instead of a torn os.ReadFile).
	// JobStore is read-safe outside the engine lock — bbolt has its
	// own concurrency.
	if js := a.engine.JobStore(); js != nil {
		snap.JobsDB = js.DB()
	}
	a.fireBackupSnapshotHook()

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
//
// Path confinement: req.Path must live under the configured
// backup directory. Without this gate any caller could restore
// from an arbitrary tarball on the host filesystem -- a malicious
// archive could exploit tar-extraction edge cases or simply
// replace the live store with attacker-controlled state.
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

	cfg := a.engine.Config()
	backupDir := cfg.Backup.Dir
	if backupDir == "" {
		backupDir = backup.DefaultBackupDir()
	}
	cleanedBackupDir, err := filepath.Abs(filepath.Clean(backupDir))
	if err != nil {
		a.log.Warn("restore: resolve backup dir failed", "component", "backup", "err", err)
		return RestoreResponse{}, ErrInternal("failed to resolve backup directory")
	}
	cleanedReq := filepath.Clean(req.Path)
	prefix := cleanedBackupDir + string(filepath.Separator)
	if !strings.HasPrefix(cleanedReq, prefix) {
		return RestoreResponse{}, ErrInvalid("path must live under the configured backup directory")
	}

	dataDir := cfg.DataDir

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

// BackupExport streams matching records in the requested format.
//
// Three-phase pattern (lock-discipline preserving):
//
//   - Phase 1 (RLock): apply filters + collect candidate IDs.
//     Filter args (Text, Match, Filter, etc.) reuse the search
//     machinery; an empty filter set means "every record."
//   - Phase 2 (no lock): the response writer.
//   - Phase 3 (per-record RLock + write): backup.StreamRecords
//     reacquires the lock briefly per record, fetches it, releases,
//     then writes to w. Concurrent writers aren't blocked across
//     the entire export.
//
// Returns the content-type for the format on success. The export
// is exhaustive over the matched set -- no candidate_cap, no
// pagination -- since the loopback gate already restricts callers.
func (a *API) BackupExport(ctx context.Context, req ExportRequest, w io.Writer) (string, *APIError) {
	format := req.Format
	if format == "" {
		format = "jsonl"
	}
	switch format {
	case "jsonl", "json", "csv", "markdown":
		// supported
	case "":
		format = "jsonl"
	default:
		return "", ErrInvalid("format must be jsonl, json, csv, or markdown")
	}

	// Phase 1: collect candidate IDs.
	ids, apiErr := a.collectExportIDs(ctx, req)
	if apiErr != nil {
		return "", apiErr
	}

	// Phase 2 + 3: stream records to w. backup.StreamRecords
	// handles per-record RLock + write + format dispatch.
	if err := backup.StreamRecords(w, a.engine, format, ids); err != nil {
		a.log.Warn("export: stream failed", "component", "backup", "format", format, "err", err)
		return "", ErrInternal("export failed")
	}

	switch format {
	case "jsonl":
		return "application/x-ndjson", nil
	case "json":
		return "application/json", nil
	case "csv":
		return "text/csv", nil
	default:
		return "text/markdown", nil
	}
}

// collectExportIDs builds the candidate ID list for an export.
// When the request has no filter fields set, returns every node ID
// in the graph (legacy "full dump" behavior). Otherwise builds a
// search.Query from the filter fields and runs it with no Top cap,
// returning the matched IDs in ranked order (Text-mode) or
// created_at desc (filter-only mode).
func (a *API) collectExportIDs(ctx context.Context, req ExportRequest) ([]string, *APIError) {
	if !exportHasFilters(req) {
		// Legacy full-store dump: every node ID, in iterator order.
		a.engine.RLock()
		defer a.engine.RUnlock()
		return a.engine.Graph().AllNodeIDs(), nil
	}

	q := search.Query{
		Text:             req.Text,
		Match:            req.Match,
		Store:            req.Store,
		Keywords:         req.Keywords,
		Temporality:      req.Temporality,
		KnowledgeType:    req.KnowledgeType,
		EpistemicStatus:  req.EpistemicStatus,
		Resolution:       req.Resolution,
		ProcessingStatus: req.ProcessingStatus,
		Meta:             req.Meta,
		// No Top cap. The internal default top=10 fires when Top is
		// unset, so set explicitly to a value that exceeds any
		// realistic match count. ExecuteWithVector slices the top-K;
		// passing math.MaxInt32 effectively disables the cap.
		Top: math.MaxInt32,
	}
	if req.Since != "" {
		t, err := parseDateArg(req.Since)
		if err != nil {
			return nil, ErrInvalid(fmt.Sprintf("invalid since date: %s", err))
		}
		q.Since = &t
	}

	var queryVec []float32
	if q.Text != "" && a.engine.Embedder() != nil {
		vec, err := embed.EmbedForQuery(ctx, a.engine.Embedder(), q.Text)
		if err == nil {
			queryVec = vec
		}
		// On embed failure, search degrades to BM25-only -- same
		// behavior as gramaton_search's pre-embed path.
	}

	a.engine.RLock()
	results, err := a.engine.Searcher().ExecuteWithVector(ctx, q, queryVec)
	a.engine.RUnlock()
	if err != nil {
		a.log.Warn("export: filter pass failed", "component", "backup", "err", err)
		return nil, ErrInternal("export filter failed")
	}

	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids, nil
}

// exportHasFilters reports whether the request carries any filter
// argument that would narrow the export from the legacy full-dump
// behavior.
func exportHasFilters(req ExportRequest) bool {
	return req.Text != "" ||
		req.Match != "" ||
		req.Store != "" ||
		req.Temporality != "" ||
		req.KnowledgeType != "" ||
		req.EpistemicStatus != "" ||
		req.Resolution != "" ||
		req.ProcessingStatus != "" ||
		req.Since != "" ||
		len(req.Keywords) > 0 ||
		len(req.Meta) > 0
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

