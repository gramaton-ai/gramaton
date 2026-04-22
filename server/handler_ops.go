package server

import (
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
}

type ingestFile struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
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

	// Load the specified commit's state.
	if _, err := s.engine.Graph().Load(store, fullHash); err != nil {
		s.writeError(w, http.StatusInternalServerError, "load_error",
			"failed to load commit", false)
		return
	}

	s.engine.RebuildAllIndexes()

	commit, err := s.engine.Save(fmt.Sprintf("revert to %s", core.TruncHash(fullHash)))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "save_error", "failed to save", false)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"reverted_to": core.TruncHash(fullHash),
		"new_hash":    core.TruncHash(commit.Hash),
	})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := parseJSON(r, &req, maxIngestBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	// Local path mode: restricted to loopback only (SSRF protection).
	if req.Path != "" {
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"local path ingestion is restricted to loopback connections", false)
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

	s.handleIngestFiles(w, req.Files)
}

func (s *Server) handleIngestLocalPath(w http.ResponseWriter, req ingestRequest) {
	// TODO: implement local path ingestion with allowed-paths validation.
	// For now, return a clear error until the full ingest logic is ported.
	s.writeError(w, http.StatusNotImplemented, "not_implemented",
		"local path ingestion via API is not yet implemented", false)
}

func (s *Server) handleIngestFiles(w http.ResponseWriter, files []ingestFile) {
	// Pre-embed all files outside the lock. Observation extraction
	// happens asynchronously in the curation cycle (D18/D23).
	type precomputed struct {
		file     ingestFile
		embedded *preEmbeddedVectors
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
		capReq := &captureRequest{Content: f.Content}
		prepared = append(prepared, precomputed{
			file:     f,
			embedded: s.preEmbedContent(capReq),
		})
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	now := time.Now().UTC()
	ingested := 0
	for _, p := range prepared {
		props := graph.Properties{
			"content_full":      graph.StringProperty(p.file.Content),
			"source_ref":        graph.StringProperty(p.file.Filename),
			"created_at":        graph.TimestampProperty(now),
			"processing_status": graph.StringProperty("captured"),
			"access_count":      graph.Int64Property(0),
		}

		n := s.engine.Graph().AddNode(props)
		s.engine.IndexNode(n.ID, p.file.Content, nil)

		if err := s.applyPreEmbedded(n.ID, p.embedded); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: embedding failed: %s", p.file.Filename, err))
		}

		// Dedup check: auto-supersede if similar record exists.
		if dupID, sim := s.engine.CheckDedup(n.ID); dupID != "" {
			oldNode, _ := s.engine.Graph().GetNode(dupID)
			if oldNode != nil {
				if _, alreadyHistorical := oldNode.Properties.GetTimestamp("valid_until"); !alreadyHistorical {
					s.engine.SetProp(dupID, "valid_until", graph.TimestampProperty(now))
					s.engine.SetProp(dupID, "resolution", graph.StringProperty("superseded"))
					s.engine.SetProp(dupID, "resolved_at", graph.TimestampProperty(now))
					if _, err := s.engine.Graph().AddEdge(n.ID, dupID, "supersedes", sim, nil); err != nil {
						s.log.Error("failed to add supersedes edge",
							"component", "ingest", "newer", n.ID, "older", dupID, "err", err)
					}
				}
			}
			warnings = append(warnings, fmt.Sprintf("%s: superseded existing record %s (similarity %.3f)", p.file.Filename, dupID, sim))
		}

		ingested++
	}

	if ingested > 0 {
		if _, err := s.engine.Save("ingest"); err != nil {
			s.log.Error("ingest save failed", "component", "ingest", "err", err, "ingested", ingested)
			s.writeError(w, http.StatusInternalServerError, "save_failed",
				"failed to persist ingested records", false)
			return
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"ingested": ingested,
		"skipped":  len(files) - ingested,
		"warnings": warnings,
	})
}

// resolveHash resolves a short hash prefix to a full hash.
func resolveHash(store *storage.Store, prefix string) (string, error) {
	// Try the prefix as a full hash first.
	if store.Has(prefix) {
		return prefix, nil
	}
	return "", fmt.Errorf("commit %q not found", prefix)
}
