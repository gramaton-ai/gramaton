package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/storage"
)

type revertRequest struct {
	Hash string `json:"hash"`
}

type ingestRequest struct {
	Path  string       `json:"path,omitempty"`
	Files []ingestFile `json:"files,omitempty"`
	// AllowSimilar disables the similar-record holds for the whole
	// ingest -- the bulk-import escape.
	AllowSimilar bool `json:"allow_similar,omitempty"`
}

type ingestFile struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	// Store-level read-only guard (this handler predates the api/
	// migration, so it carries its own): revert loads an old commit
	// into the live graph and re-commits -- a write.
	if s.engine.ReadOnly() {
		s.writeError(w, http.StatusForbidden, "forbidden",
			"store is read-only: revert is not permitted", false)
		return
	}

	var req revertRequest
	if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if req.Hash == "" {
		s.writeError(w, http.StatusBadRequest, "missing_field", "hash is required", true)
		return
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	store := s.engine.Store()

	// Resolve short hash.
	fullHash, err := resolveHash(store, req.Hash)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not_found", err.Error(), false)
		return
	}

	// Load the specified commit's state into a staged graph and adopt
	// it. An in-place Load on the live graph would see the populated
	// bbolt edge store and skip edge reload entirely -- reverted-away
	// nodes would resurrect with their edges missing and since-added
	// edges would survive the revert.
	staged, _, err := graph.LoadStaged(store, fullHash)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "load_error",
			"failed to load commit", false)
		return
	}
	s.engine.AdoptGraph(staged)

	s.engine.RebuildAllIndexes()
	preRevert := s.engine.HeadHashLocked()

	s.engine.ArmAdoptedCommit()
	commit, err := s.engine.Save(fmt.Sprintf("revert to %s", core.TruncHash(fullHash)), graph.CommitAction{
		Kind: graph.ActionRevert,
	})
	if err != nil {
		s.engine.DisarmAdoptedCommit()
		s.writeError(w, http.StatusInternalServerError, "save_error", "failed to save", false)
		return
	}
	// The adopted graph carries no dirty nodes, so Save's changelog
	// append saw an empty mutation set; derive the revert commit's
	// logical versions from its tree diff instead.
	s.engine.IndexCommitDiffByHash(preRevert, commit.Hash)

	s.writeJSON(w, http.StatusOK, map[string]any{
		"reverted_to": core.TruncHash(fullHash),
		"new_hash":    core.TruncHash(commit.Hash),
	})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	// Store-level read-only guard, before any parse or pre-embed
	// work (this handler predates the api/ migration, so it carries
	// its own).
	if s.engine.ReadOnly() {
		s.writeError(w, http.StatusForbidden, "forbidden",
			"store is read-only: ingest is not permitted", false)
		return
	}

	var req ingestRequest
	if err := parseJSON(r, &req, maxIngestBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	// Local path mode reads a caller-supplied host path (SSRF / host
	// read surface), so it stays loopback-only unless
	// server.remote.admin_ops is set.
	if req.Path != "" {
		if !s.adminAllowed(r) {
			s.writeAdminForbidden(w, "local path ingestion")
			return
		}
		s.handleIngestLocalPath(w, req)
		return
	}

	// Upload mode: accept file content in the request body.
	if len(req.Files) == 0 {
		s.writeError(w, http.StatusBadRequest, "missing_field",
			"either path or files is required", true)
		return
	}

	s.handleIngestFiles(r.Context(), w, req.Files, req.AllowSimilar)
}

func (s *Server) handleIngestLocalPath(w http.ResponseWriter, req ingestRequest) {
	// TODO: implement local path ingestion with allowed-paths validation.
	// For now, return a clear error until the full ingest logic is ported.
	s.writeError(w, http.StatusNotImplemented, "not_implemented",
		"local path ingestion via API is not yet implemented", false)
}

func (s *Server) handleIngestFiles(ctx context.Context, w http.ResponseWriter, files []ingestFile, allowSimilar bool) {
	// Pre-embed all files outside the lock. Observation extraction
	// happens asynchronously in the curation cycle (D18/D23).
	type precomputed struct {
		file     ingestFile
		embedded *preEmbeddedVectors
		preChunk *core.PreChunkResult
	}
	var prepared []precomputed
	var warnings []string

	for _, f := range files {
		// Sanitize filename: strip directory components to prevent
		// path traversal in stored source_ref.
		f.Filename = filepath.Base(f.Filename)

		if f.Content == "" {
			warnings = append(warnings, fmt.Sprintf("skipped %s: empty content", f.Filename))
			continue
		}
		if len(f.Content) > s.engine.Config().Limits.MaxContentLength {
			warnings = append(warnings, fmt.Sprintf("skipped %s: content exceeds maximum length", f.Filename))
			continue
		}
		capReq := &saveRequest{Content: f.Content}
		p := precomputed{
			file:     f,
			embedded: s.preEmbedContent(ctx, capReq),
		}
		// Long-document chunking, off-lock. Ingest is the primary
		// long-document entry; a file the guard later holds wastes
		// the prechunk (the scan runs under the lock on this path).
		if s.engine.ShouldChunk(len(f.Content)) {
			p.preChunk = s.engine.PreChunk(ctx, f.Content, "")
		}
		prepared = append(prepared, p)
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	now := time.Now().UTC()
	ingested := 0
	// Author attribution: composed once for the whole ingest (see
	// server/service_records.go for the stamping contract).
	author := s.engine.Config().Author.String()
	var ingestActions []graph.CommitAction
	var held []map[string]any
	for _, p := range prepared {
		// Save-guard hold, pre-insert. The scan runs under the write
		// lock -- parity with the post-insert dedup check it replaces
		// (ingest is a bulk CLI/REST surface, not a hot path), and it
		// naturally covers intra-ingest siblings: earlier files'
		// records are already indexed by the time later files scan.
		if !allowSimilar && p.embedded != nil && p.embedded.err == nil {
			if vec, ok := p.embedded.vectors["embedding_full"]; ok {
				if out := s.engine.ScanSimilarVec(vec, p.file.Content); out.Hold != nil {
					held = append(held, map[string]any{
						"filename":   p.file.Filename,
						"similar_to": out.Hold.NodeID,
						"similarity": out.Hold.Similarity,
					})
					warnings = append(warnings, fmt.Sprintf(
						"%s: held -- closely matches record %s (similarity %.3f); revise it via gramaton_update or re-run with --allow-similar",
						p.file.Filename, out.Hold.NodeID, out.Hold.Similarity))
					continue
				}
			}
		}
		props := graph.Properties{
			"content_full":      graph.StringProperty(p.file.Content),
			"source_ref":        graph.StringProperty(p.file.Filename),
			"created_at":        graph.TimestampProperty(now),
			"processing_status": graph.StringProperty("captured"),
			"access_count":      graph.Int64Property(0),
		}
		if author != "" {
			props["author"] = graph.StringProperty(author)
		}

		n := s.engine.Graph().AddNode(props)
		s.engine.IndexNode(n.ID, p.file.Content, nil)

		if err := s.applyPreEmbedded(n.ID, p.embedded); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: embedding failed: %s", p.file.Filename, err))
			// Never similarity-checked; the deferred re-scan at
			// reembed gates on this flag (parity with save/batch).
			s.engine.SetProp(n.ID, "similar_check_pending", graph.BoolProperty(true))
		}

		// Apply pre-chunked children (see api/save.go for the fresh-
		// props rationale). Children ride the same ingest commit.
		if p.preChunk != nil {
			if parent, ok := s.engine.Graph().GetNode(n.ID); ok {
				if created, err := s.engine.ApplyChunks(n.ID, p.preChunk, parent.Properties); err != nil {
					warnings = append(warnings, fmt.Sprintf("%s: chunking failed: %s", p.file.Filename, err))
				} else {
					if p.preChunk.ParentVec != nil {
						s.engine.NoteRecentWrite(n.ID, p.preChunk.ParentVec)
					}
					s.log.Info("long document chunked",
						"component", "ingest", "file", p.file.Filename,
						"node", n.ID, "children", created)
				}
			}
		}

		ingested++
		ingestActions = append(ingestActions, graph.CommitAction{
			Kind: graph.ActionIngest, RecordID: n.ID,
		})
	}

	if ingested > 0 {
		if _, err := s.engine.Save("ingest", ingestActions...); err != nil {
			s.log.Error("ingest save failed", "component", "ingest", "err", err, "ingested", ingested)
			s.writeError(w, http.StatusInternalServerError, "save_failed",
				"failed to persist ingested records", false)
			return
		}
	}

	resp := map[string]any{
		"ingested": ingested,
		"skipped":  len(files) - ingested,
		"warnings": warnings,
	}
	if len(held) > 0 {
		resp["held"] = held
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// resolveHash resolves a short hash prefix to a full hash.
func resolveHash(store *storage.Store, prefix string) (string, error) {
	// Try the prefix as a full hash first.
	if store.Has(prefix) {
		return prefix, nil
	}
	return "", fmt.Errorf("commit %q not found", prefix)
}
