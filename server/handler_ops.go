package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/storage"
)

type revertRequest struct {
	Hash string `json:"hash"`
}

type reembedRequest struct {
	Batch int `json:"batch,omitempty"`
}

type ingestRequest struct {
	Path      string        `json:"path,omitempty"`
	Recursive bool          `json:"recursive,omitempty"`
	Files     []ingestFile  `json:"files,omitempty"`
}

type ingestFile struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	s.engine.RLock()
	defer s.engine.RUnlock()

	captured := s.engine.PropIdx().Lookup("processing_status",
		graph.StringProperty("captured"))

	var records []map[string]any
	for _, id := range captured {
		entry := map[string]any{"id": id}
		if n, ok := s.engine.Graph().GetNode(id); ok {
			if v, ok := n.Properties.GetString("content_short"); ok {
				entry["summary_short"] = v
			}
			if v, ok := n.Properties.GetTimestamp("created_at"); ok {
				entry["created_at"] = v.Format("2006-01-02T15:04:05Z")
			}
		}
		records = append(records, entry)
	}

	if records == nil {
		records = []map[string]any{}
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{"records": records})
}

func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	var req revertRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
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

	s.writeJSONLocked(w, http.StatusOK, map[string]any{
		"reverted_to": core.TruncHash(fullHash),
		"new_hash":    core.TruncHash(commit.Hash),
	})
}

func (s *Server) handleReembed(w http.ResponseWriter, r *http.Request) {
	var req reembedRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		// Allow empty body -- no required fields.
		req = reembedRequest{}
	}

	if req.Batch <= 0 {
		req.Batch = 50
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	if s.engine.Embedder() == nil {
		s.writeError(w, http.StatusServiceUnavailable, "no_embedder",
			"no embedding provider configured", false)
		return
	}

	currentModel := s.engine.Embedder().ModelID()
	var staleIDs []string

	for _, id := range s.engine.Graph().AllNodeIDs() {
		n, ok := s.engine.Graph().GetNode(id)
		if !ok {
			continue
		}
		model, ok := n.Properties.GetString("embedding_model")
		if !ok {
			// No embedding at all -- needs embedding.
			if _, hasContent := n.Properties.GetString("content_full"); hasContent {
				staleIDs = append(staleIDs, id)
			}
			continue
		}
		if model != currentModel {
			staleIDs = append(staleIDs, id)
		}
	}

	if len(staleIDs) > req.Batch {
		staleIDs = staleIDs[:req.Batch]
	}

	reembedded := 0
	errors := 0
	for _, id := range staleIDs {
		if err := s.engine.GenerateEmbeddings(context.Background(), id); err != nil {
			errors++
			continue
		}
		reembedded++
	}

	if reembedded > 0 {
		s.engine.Save("reembed")
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{
		"reembedded": reembedded,
		"skipped":    len(staleIDs) - reembedded - errors,
		"errors":     errors,
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
	s.engine.Lock()
	defer s.engine.Unlock()

	ingested := 0
	var warnings []string

	for _, f := range files {
		if f.Content == "" {
			warnings = append(warnings, fmt.Sprintf("skipped %s: empty content", f.Filename))
			continue
		}

		props := graph.Properties{
			"content_full":      graph.StringProperty(f.Content),
			"source_ref":        graph.StringProperty(f.Filename),
			"processing_status": graph.StringProperty("captured"),
			"access_count":      graph.Int64Property(0),
		}

		n := s.engine.Graph().AddNode(props)
		for k, v := range n.Properties {
			s.engine.PropIdx().Add(n.ID, k, v)
		}

		if err := s.engine.GenerateEmbeddings(context.Background(), n.ID); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: embedding failed: %s", f.Filename, err))
		}

		if numChunks, err := s.engine.ChunkIfNeeded(context.Background(), n.ID); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: chunking failed: %s", f.Filename, err))
		} else if numChunks > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: chunked into %d segments", f.Filename, numChunks))
		}

		ingested++
	}

	if ingested > 0 {
		s.engine.Save("ingest")
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{
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
