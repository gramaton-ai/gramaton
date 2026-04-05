package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/brandonlattin/gramaton/core"
)

type branchCreateRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleListBranches(w http.ResponseWriter, r *http.Request) {
	s.engine.RLock()
	defer s.engine.RUnlock()

	dataDir := s.engine.Config().DataDir
	dir := core.RefsDir(dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.writeJSONLocked(w, http.StatusOK, map[string]any{
			"branches": []any{},
			"current":  core.ActiveBranch(dataDir),
		})
		return
	}

	active := core.ActiveBranch(dataDir)
	var branches []map[string]any
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		hash, _ := core.ReadRef(dataDir, e.Name())
		b := map[string]any{
			"name":   e.Name(),
			"commit": core.TruncHash(hash),
		}
		if e.Name() == active {
			b["active"] = true
		}
		branches = append(branches, b)
	}
	if branches == nil {
		branches = []map[string]any{}
	}

	s.writeJSONLocked(w, http.StatusOK, map[string]any{
		"branches": branches,
		"current":  active,
	})
}

func (s *Server) handleCreateBranch(w http.ResponseWriter, r *http.Request) {
	var req branchCreateRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	if err := core.ValidBranchName(req.Name); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_name", err.Error(), true)
		return
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	dataDir := s.engine.Config().DataDir

	if _, err := core.ReadRef(dataDir, req.Name); err == nil {
		s.writeError(w, http.StatusConflict, "exists",
			fmt.Sprintf("branch %q already exists", req.Name), false)
		return
	}

	headHash := s.engine.HeadHashLocked()
	if err := core.WriteRef(dataDir, req.Name, headHash); err != nil {
		s.writeError(w, http.StatusInternalServerError, "write_error", "failed to create branch", false)
		return
	}

	s.writeJSONLocked(w, http.StatusCreated, map[string]any{
		"name":    req.Name,
		"commit":  core.TruncHash(headHash),
		"created": true,
	})
}

func (s *Server) handleCheckoutBranch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := core.ValidBranchName(name); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_name", err.Error(), true)
		return
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	dataDir := s.engine.Config().DataDir
	hash, err := core.ReadRef(dataDir, name)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not_found",
			fmt.Sprintf("branch %q not found", name), false)
		return
	}

	headPath := filepath.Join(dataDir, "HEAD")
	if err := core.AtomicWriteFile(headPath, []byte(hash), 0o600); err != nil {
		s.writeError(w, http.StatusInternalServerError, "write_error", "failed to update HEAD", false)
		return
	}

	if err := core.SetActiveBranch(dataDir, name); err != nil {
		s.writeError(w, http.StatusInternalServerError, "write_error", "failed to set active branch", false)
		return
	}

	// Reload graph from the branch's commit.
	s.engine.Graph().Load(s.engine.Store(), hash)
	s.engine.RebuildAllIndexes()

	s.writeJSONLocked(w, http.StatusOK, map[string]any{
		"name":         name,
		"commit":       core.TruncHash(hash),
		"checked_out":  true,
	})
}

func (s *Server) handleMergeBranch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := core.ValidBranchName(name); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_name", err.Error(), true)
		return
	}
	if name == "main" {
		s.writeError(w, http.StatusBadRequest, "invalid", "cannot merge main into itself", false)
		return
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	dataDir := s.engine.Config().DataDir

	// Ensure we're on main.
	core.SetActiveBranch(dataDir, "main")

	branchHash, err := core.ReadRef(dataDir, name)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not_found",
			fmt.Sprintf("branch %q not found", name), false)
		return
	}

	// Fast-forward merge: adopt the branch's state.
	s.engine.Graph().Load(s.engine.Store(), branchHash)
	s.engine.RebuildAllIndexes()

	commit, err := s.engine.Save(fmt.Sprintf("merge branch %q", name))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "save_error", "failed to save merge", false)
		return
	}

	core.WriteRef(dataDir, "main", commit.Hash)
	core.DeleteRef(dataDir, name)

	s.writeJSONLocked(w, http.StatusOK, map[string]any{
		"merged":     name,
		"new_commit": core.TruncHash(commit.Hash),
	})
}

func (s *Server) handleDiscardBranch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := core.ValidBranchName(name); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_name", err.Error(), true)
		return
	}
	if name == "main" {
		s.writeError(w, http.StatusBadRequest, "invalid", "cannot discard main", false)
		return
	}

	s.engine.Lock()
	defer s.engine.Unlock()

	dataDir := s.engine.Config().DataDir

	if _, err := core.ReadRef(dataDir, name); err != nil {
		s.writeError(w, http.StatusNotFound, "not_found",
			fmt.Sprintf("branch %q not found", name), false)
		return
	}

	// If on the discarded branch, switch back to main.
	if core.ActiveBranch(dataDir) == name {
		mainHash, err := core.ReadRef(dataDir, "main")
		if err == nil {
			headPath := filepath.Join(dataDir, "HEAD")
			core.AtomicWriteFile(headPath, []byte(mainHash), 0o600)
		}
		core.SetActiveBranch(dataDir, "main")
	}

	core.DeleteRef(dataDir, name)

	s.writeJSONLocked(w, http.StatusOK, map[string]any{"discarded": name})
}
