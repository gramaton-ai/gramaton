package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/backup"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerMCPAdminTools(mcpServer *mcp.Server) {
	type branchInput struct {
		Action string `json:"action" jsonschema:"create|list|checkout|merge|discard"`
		Name   string `json:"name,omitempty" jsonschema:"branch name (required for create/checkout/merge/discard)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_branch",
		Description: "Manage branches: create, list, checkout, merge, or discard. Use for safe experimentation, bulk imports, or testing curation changes before merging.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args branchInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_branch")
		defer done(nil)
		switch args.Action {
		case "list":
			s.engine.RLock()
			defer s.engine.RUnlock()

			dataDir := s.engine.Config().DataDir
			active := core.ActiveBranch(dataDir)
			dir := core.RefsDir(dataDir)
			entries, _ := os.ReadDir(dir)
			var branches []map[string]any
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				hash, _ := core.ReadRef(dataDir, e.Name())
				b := map[string]any{"name": e.Name(), "commit": core.TruncHash(hash)}
				if e.Name() == active {
					b["active"] = true
				}
				branches = append(branches, b)
			}
			if branches == nil {
				branches = []map[string]any{}
			}
			return mcpJSONResult(map[string]any{"branches": branches, "current": active})

		case "create":
			if args.Name == "" {
				return mcpErr("name is required for create")
			}
			if err := core.ValidBranchName(args.Name); err != nil {
				return mcpErr(err.Error())
			}

			s.engine.Lock()
			defer s.engine.Unlock()

			dataDir := s.engine.Config().DataDir
			if _, err := core.ReadRef(dataDir, args.Name); err == nil {
				return mcpErr(fmt.Sprintf("branch %q already exists", args.Name))
			}
			headHash := s.engine.HeadHashLocked()
			if err := core.WriteRef(dataDir, args.Name, headHash); err != nil {
				return mcpErr("failed to create branch")
			}
			return mcpJSONResult(map[string]any{"name": args.Name, "commit": core.TruncHash(headHash), "created": true})

		case "checkout":
			if args.Name == "" {
				return mcpErr("name is required for checkout")
			}
			if err := core.ValidBranchName(args.Name); err != nil {
				return mcpErr(err.Error())
			}

			s.engine.Lock()
			defer s.engine.Unlock()

			dataDir := s.engine.Config().DataDir
			hash, err := core.ReadRef(dataDir, args.Name)
			if err != nil {
				return mcpErr(fmt.Sprintf("branch %q not found", args.Name))
			}
			headPath := filepath.Join(dataDir, "HEAD")
			if err := core.AtomicWriteFile(headPath, []byte(hash), 0o600); err != nil {
				return mcpErr("failed to update HEAD")
			}
			if err := core.SetActiveBranch(dataDir, args.Name); err != nil {
				return mcpErr("failed to set active branch")
			}
			if _, err := s.engine.Graph().Load(s.engine.Store(), hash); err != nil {
				return mcpErr(fmt.Sprintf("failed to load branch state: %v", err))
			}
			s.engine.RebuildAllIndexes()
			return mcpJSONResult(map[string]any{"name": args.Name, "commit": core.TruncHash(hash), "checked_out": true})

		case "merge":
			if args.Name == "" {
				return mcpErr("name is required for merge")
			}
			if args.Name == "main" {
				return mcpErr("cannot merge main into itself")
			}

			s.engine.Lock()
			defer s.engine.Unlock()

			dataDir := s.engine.Config().DataDir
			if err := core.SetActiveBranch(dataDir, "main"); err != nil {
				return mcpErr(fmt.Sprintf("failed to switch to main: %v", err))
			}
			branchHash, err := core.ReadRef(dataDir, args.Name)
			if err != nil {
				return mcpErr(fmt.Sprintf("branch %q not found", args.Name))
			}
			if _, err := s.engine.Graph().Load(s.engine.Store(), branchHash); err != nil {
				return mcpErr(fmt.Sprintf("failed to load branch state: %v", err))
			}
			s.engine.RebuildAllIndexes()
			commit, err := s.engine.Save(fmt.Sprintf("merge branch %q", args.Name))
			if err != nil {
				return mcpErr("failed to save merge")
			}
			if err := core.WriteRef(dataDir, "main", commit.Hash); err != nil {
				return mcpErr(fmt.Sprintf("failed to update main ref: %v", err))
			}
			if err := core.DeleteRef(dataDir, args.Name); err != nil {
				s.log.Warn("branch ref cleanup failed after merge", "component", "branch", "branch", args.Name, "err", err)
			}
			s.log.Info("branch merged", "component", "branch", "branch", args.Name, "commit", core.TruncHash(commit.Hash))
			return mcpJSONResult(map[string]any{"merged": args.Name, "new_commit": core.TruncHash(commit.Hash)})

		case "discard":
			if args.Name == "" {
				return mcpErr("name is required for discard")
			}
			if args.Name == "main" {
				return mcpErr("cannot discard main")
			}

			s.engine.Lock()
			defer s.engine.Unlock()

			dataDir := s.engine.Config().DataDir
			if _, err := core.ReadRef(dataDir, args.Name); err != nil {
				return mcpErr(fmt.Sprintf("branch %q not found", args.Name))
			}
			if core.ActiveBranch(dataDir) == args.Name {
				mainHash, err := core.ReadRef(dataDir, "main")
				if err == nil {
					headPath := filepath.Join(dataDir, "HEAD")
					if err := core.AtomicWriteFile(headPath, []byte(mainHash), 0o600); err != nil {
						s.log.Error("failed to write HEAD during branch discard", "component", "branch", "branch", args.Name, "err", err)
					}
				}
				if err := core.SetActiveBranch(dataDir, "main"); err != nil {
					s.log.Error("failed to switch to main during branch discard", "component", "branch", "branch", args.Name, "err", err)
				}
			}
			if err := core.DeleteRef(dataDir, args.Name); err != nil {
				s.log.Warn("branch ref cleanup failed during discard", "component", "branch", "branch", args.Name, "err", err)
			}
			s.log.Info("branch discarded", "component", "branch", "branch", args.Name)
			return mcpJSONResult(map[string]any{"discarded": args.Name})

		default:
			return mcpErr("action must be one of: create, list, checkout, merge, discard")
		}
	})

	type backupToolInput struct {
		Action string `json:"action" jsonschema:"backup|status"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_backup",
		Description: "Create a backup of the store or list existing backups.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args backupToolInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_backup")
		defer done(nil)
		cfg := s.engine.Config()
		backupDir := cfg.Backup.Dir
		if backupDir == "" {
			backupDir = backup.DefaultBackupDir()
		}

		switch args.Action {
		case "backup":
			cfgPath := filepath.Join(s.cfg.ConfigDir, "config.yaml")
			s.engine.RLock()
			archivePath, err := backup.Create(cfg.DataDir, cfgPath, backupDir, s.cfg.StoreName)
			s.engine.RUnlock()
			if err != nil {
				return mcpErr("backup failed")
			}
			deleted, _ := backup.ApplyRetention(backupDir, cfg.Backup.Retain)
			info, _ := os.Stat(archivePath)
			var sizeBytes int64
			if info != nil {
				sizeBytes = info.Size()
			}
			return mcpJSONResult(map[string]any{
				"path":        archivePath,
				"size_bytes":  sizeBytes,
				"deleted_old": deleted,
			})

		default:
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
			return mcpJSONResult(map[string]any{
				"backup_dir": backupDir,
				"backups":    backups,
				"count":      len(backups),
			})
		}
	})
}
