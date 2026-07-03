package server

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerAdminRoutes wires branch + backup HTTP endpoints to api.
// Loopback gates remain at the transport layer -- api/ has no
// concept of HTTP origin.
func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	// --- Branches ---
	mux.HandleFunc("GET /v1/branches", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.BranchList(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/branches", func(w http.ResponseWriter, r *http.Request) {
		var req api.BranchCreateRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.BranchCreate(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusCreated, result)
	})

	mux.HandleFunc("POST /v1/branches/{name}/checkout", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.BranchCheckout(r.Context(), r.PathValue("name"))
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/branches/{name}/merge", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.BranchMerge(r.Context(), r.PathValue("name"))
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("DELETE /v1/branches/{name}", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.BranchDiscard(r.Context(), r.PathValue("name"))
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	// --- Backup ---
	mux.HandleFunc("GET /v1/backup", func(w http.ResponseWriter, r *http.Request) {
		result, apiErr := s.api.BackupStatus(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/backup", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"backup is restricted to loopback connections", false)
			return
		}
		result, apiErr := s.api.BackupCreate(r.Context())
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/restore", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"restore is restricted to loopback connections", false)
			return
		}
		var req api.RestoreRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.BackupRestore(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /v1/export", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"export is restricted to loopback connections", false)
			return
		}
		var req api.ExportRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		// Buffer the export locally so we can set the content-type
		// header before any write. Export is not a hot path; a
		// per-request buffer is fine.
		var buf bytes.Buffer
		contentType, apiErr := s.api.BackupExport(r.Context(), req, &buf)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	})

	mux.HandleFunc("POST /v1/import", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"import is restricted to loopback connections", false)
			return
		}
		var req api.ImportRequest
		if err := parseJSON(r, &req, maxIngestBodySize); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.BackupImport(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	// --- Store carve-out ---
	// Server-mediated so the carve reads the LIVE source under the
	// engine RLock in this process. Loopback-gated as its FIRST act
	// (mirroring backup/restore/export/import): CarveOut materializes a
	// brand-new store at a caller-supplied absolute filesystem path, so a
	// remote caller reaching it would be an arbitrary host-path write.
	// The agent-facing description of this op is api.CarveOutDescription
	// (the single source of truth, shared with the CLI help); HTTP itself
	// exposes no description surface, and no MCP tool ships this pass.
	mux.HandleFunc("POST /v1/store/carve", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"store carve is restricted to loopback connections", false)
			return
		}
		var req api.CarveOutRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		result, apiErr := s.api.CarveOut(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})

	// --- Store carve top-up ---
	// Adds more of the LIVE source (read under the engine RLock in this
	// process) into an EXISTING destination store, idempotently. Loopback-
	// gated as its FIRST act (mirroring carve): CarveAdd opens and writes a
	// store at a caller-supplied absolute filesystem path, so a remote
	// caller reaching it would be an arbitrary host-path write. The
	// agent-facing description of this op is api.CarveAddDescription (the
	// single source of truth, shared with the CLI help); HTTP exposes no
	// description surface, and no MCP tool ships this pass.
	mux.HandleFunc("POST /v1/store/add", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			s.writeError(w, http.StatusForbidden, "forbidden",
				"store add is restricted to loopback connections", false)
			return
		}
		var req api.CarveAddRequest
		if err := parseJSON(r, &req, getMaxJSONSize()); err != nil {
			s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
			return
		}
		// Anti-hang guard: a top-up opens a SECOND engine on the
		// destination's data dir. If a live server is already serving that
		// store, bbolt's file lock is held and core.LoadEngine would block
		// FOREVER (it opens bbolt with no lock timeout). Refuse promptly
		// with a 409 -- BEFORE api.CarveAdd, so BOTH the commit and the
		// dry-run paths are covered. This also covers a self-add
		// (gramaton --store foo store add foo): the mediating server is
		// itself serving the destination, so it detects its own liveness
		// here. api/ cannot host this check -- it must not import server/ --
		// so the guard lives at the transport that already owns
		// server-liveness detection.
		if destServerAlive(filepath.Dir(req.DestDataDir)) {
			s.writeAPIError(w, api.ErrConflict(
				"destination store is served by a running server; stop it before adding (gramaton stop)"))
			return
		}
		result, apiErr := s.api.CarveAdd(r.Context(), req)
		if apiErr != nil {
			s.writeAPIError(w, apiErr)
			return
		}
		s.writeJSON(w, http.StatusOK, result)
	})
}

// destServerAlive reports whether a live gramaton server is currently
// serving the store whose config dir is cfgDir (the parent of the store's
// data dir, holding server.json). It reads server.json and checks the
// recorded PID is alive -- the same liveness probe cli/store.go's
// isServerRunning and `store freeze`/`store delete` use. Used to refuse a
// carve top-up into a store another process (or this one, for a self-add)
// holds open, which would otherwise block forever on bbolt's file lock.
func destServerAlive(cfgDir string) bool {
	info, err := ReadServerInfo(cfgDir)
	if err != nil {
		return false
	}
	return IsProcessAlive(info.PID)
}

// registerAdminMCPTools wires gramaton_branch + gramaton_backup.
// Both keep their single `action` argument and dispatch across the
// underlying api methods so external MCP clients see the same
// surface as pre-migration.
func (s *Server) registerAdminMCPTools(mcpServer *mcp.Server) {
	type branchArgs struct {
		Action string `json:"action" jsonschema:"create|list|checkout|merge|discard"`
		Name   string `json:"name,omitempty" jsonschema:"branch name (required for create|checkout|merge|discard)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_branch",
		Description: "Manage branches: list, create, checkout, merge, or discard. Use for safe experimentation, bulk imports, or testing curation changes before merging.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args branchArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_branch")
		defer done(nil)
		switch args.Action {
		case "list":
			result, apiErr := s.api.BranchList(ctx)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		case "create":
			result, apiErr := s.api.BranchCreate(ctx, api.BranchCreateRequest{Name: args.Name})
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		case "checkout":
			result, apiErr := s.api.BranchCheckout(ctx, args.Name)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		case "merge":
			result, apiErr := s.api.BranchMerge(ctx, args.Name)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		case "discard":
			result, apiErr := s.api.BranchDiscard(ctx, args.Name)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		default:
			return mcpAPIErr(api.ErrInvalid("action must be one of: list, create, checkout, merge, discard"))
		}
	})

	type backupArgs struct {
		Action string `json:"action,omitempty" jsonschema:"backup|status (default: status)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_backup",
		Description: "Create a snapshot-consistent backup or list existing backups. action=backup creates a new tar.gz archive; action=status (default) lists existing archives.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args backupArgs) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_backup")
		defer done(nil)
		switch args.Action {
		case "backup":
			result, apiErr := s.api.BackupCreate(ctx)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		case "", "status":
			result, apiErr := s.api.BackupStatus(ctx)
			if apiErr != nil {
				return mcpAPIErr(apiErr)
			}
			return mcpJSONResult(result)
		default:
			return mcpAPIErr(api.ErrInvalid("action must be one of: backup, status"))
		}
	})
}
