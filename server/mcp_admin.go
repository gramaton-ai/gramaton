package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/backup"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
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

	type diffInput struct {
		Since string `json:"since,omitempty" jsonschema:"show changes after date (YYYY-MM-DD)"`
		Topic string `json:"topic,omitempty" jsonschema:"filter by topic keyword"`
		Limit int    `json:"limit,omitempty" jsonschema:"max changes to return (default 50)"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_diff",
		Description: "Show what changed since a date: added, modified, and removed records with summaries. Use to audit curation, catch up after time away, or review what other agents captured.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args diffInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_diff")
		defer done(nil)
		if len(args.Topic) > maxTopicLength {
			return mcpErr(fmt.Sprintf("topic exceeds maximum length of %d", maxTopicLength))
		}

		s.engine.RLock()
		defer s.engine.RUnlock()

		store := s.engine.Store()
		headHash := s.engine.HeadHashLocked()

		var sinceHash string
		if args.Since != "" {
			sinceTime, err := parseDateArg(args.Since)
			if err != nil {
				return mcpErr("invalid since date")
			}
			hash := headHash
			for hash != "" {
				commit, err := loadCommit(store, hash)
				if err != nil {
					break
				}
				if commit.Timestamp.Before(sinceTime) {
					sinceHash = hash
					break
				}
				hash = commit.Parent
			}
		}

		if sinceHash == "" && args.Since != "" {
			return mcpJSONResult(map[string]any{"added": []any{}, "removed": []any{}})
		}

		headCommit, err := loadCommit(store, headHash)
		if err != nil {
			return mcpErr("failed to load HEAD")
		}

		var sinceCommit *graph.Commit
		if sinceHash != "" {
			c, err := loadCommit(store, sinceHash)
			if err != nil {
				return mcpErr("failed to load since commit")
			}
			sinceCommit = c
		}

		diff, err := graph.DiffCommits(store, sinceCommit, headCommit)
		if err != nil {
			return mcpErr("failed to compute diff")
		}

		limit := args.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 1000 {
			limit = 1000
		}

		addedSet := make(map[string]struct{}, len(diff.Added))
		for _, e := range diff.Added {
			addedSet[e.Key] = struct{}{}
		}
		removedSet := make(map[string]struct{}, len(diff.Removed))
		for _, e := range diff.Removed {
			removedSet[e.Key] = struct{}{}
		}

		var added, modified, removed []map[string]any
		total := 0
		for _, entry := range diff.Added {
			if total >= limit {
				break
			}
			if args.Topic != "" && !matchesTopic(s, entry.Key, args.Topic) {
				continue
			}
			rec := map[string]any{"id": entry.Key}
			if n, ok := s.engine.Graph().GetNode(entry.Key); ok {
				if v, ok := n.Properties.GetString("content_short"); ok {
					rec["summary_short"] = v
				}
			}
			if _, wasRemoved := removedSet[entry.Key]; wasRemoved {
				modified = append(modified, rec)
			} else {
				added = append(added, rec)
			}
			total++
		}
		for _, entry := range diff.Removed {
			if total >= limit {
				break
			}
			if _, wasAdded := addedSet[entry.Key]; wasAdded {
				continue
			}
			if args.Topic != "" && !matchesTopic(s, entry.Key, args.Topic) {
				continue
			}
			removed = append(removed, map[string]any{"id": entry.Key})
			total++
		}
		if added == nil {
			added = []map[string]any{}
		}
		if modified == nil {
			modified = []map[string]any{}
		}
		if removed == nil {
			removed = []map[string]any{}
		}
		resp := map[string]any{"added": added, "modified": modified, "removed": removed}
		if total >= limit {
			resp["truncated"] = true
			resp["limit"] = limit
		}
		return mcpJSONResult(resp)
	})

	type logInput struct {
		Limit  int    `json:"limit,omitempty" jsonschema:"max entries (default 20)"`
		Record string `json:"record,omitempty" jsonschema:"record ID for per-record history"`
	}
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "gramaton_log",
		Description: "View commit history. Use to see what operations modified the store and when. For per-record history, use gramaton_history instead.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args logInput) (*mcp.CallToolResult, any, error) {
		done := s.mcpToolStart("gramaton_log")
		defer done(nil)
		limit := args.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > maxLogLimit {
			limit = maxLogLimit
		}

		s.engine.RLock()
		defer s.engine.RUnlock()

		if args.Record != "" {
			store := s.engine.Store()
			hash := s.engine.HeadHashLocked()
			var changes []map[string]any
			var prevHash string
			depth := 0

			for hash != "" && len(changes) < limit && depth < maxLogTraversal {
				depth++
				commit, err := loadCommit(store, hash)
				if err != nil {
					break
				}
				nodeHash, found, _ := graph.NodeHashInCommit(store, hash, args.Record)
				if found {
					if prevHash != "" && nodeHash != prevHash {
						changes = append(changes, map[string]any{
							"commit":    hash[:12],
							"timestamp": commit.Timestamp.Format("2006-01-02T15:04:05Z"),
							"action":    commit.Message,
						})
					} else if prevHash == "" {
						changes = append(changes, map[string]any{
							"commit":    hash[:12],
							"timestamp": commit.Timestamp.Format("2006-01-02T15:04:05Z"),
							"action":    commit.Message,
						})
					}
					prevHash = nodeHash
				}
				hash = commit.Parent
			}
			if changes == nil {
				changes = []map[string]any{}
			}
			return mcpJSONResult(map[string]any{"id": args.Record, "changes": changes})
		}

		var commits []map[string]any
		hash := s.engine.HeadHashLocked()
		store := s.engine.Store()

		for hash != "" && len(commits) < limit {
			commit, err := loadCommit(store, hash)
			if err != nil {
				break
			}
			commits = append(commits, map[string]any{
				"hash":      hash[:12],
				"timestamp": commit.Timestamp.Format("2006-01-02T15:04:05Z"),
				"action":    commit.Message,
			})
			hash = commit.Parent
		}
		if commits == nil {
			commits = []map[string]any{}
		}
		return mcpJSONResult(map[string]any{"commits": commits})
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
