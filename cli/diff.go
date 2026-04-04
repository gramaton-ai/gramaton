package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/storage"
	"github.com/spf13/cobra"
)

var diffCmd = &cobra.Command{
	Use:   "diff [commit1..commit2]",
	Short: "Show changes between commits",
	Long: `Compares two commits and shows what changed (nodes and edges added,
removed, or modified).

Usage:
  gramaton diff                     # compare HEAD~1 with HEAD
  gramaton diff abc123..def456      # compare two specific commits`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDiff,
}

func init() {
	rootCmd.AddCommand(diffCmd)
}

type diffOutput struct {
	OldCommit    string       `json:"old_commit"`
	NewCommit    string       `json:"new_commit"`
	Added        []diffRecord `json:"added,omitempty"`
	Removed      []diffRecord `json:"removed,omitempty"`
	AddedEdges   int          `json:"added_edges"`
	RemovedEdges int          `json:"removed_edges"`
}

type diffRecord struct {
	ID           string `json:"id"`
	SummaryShort string `json:"summary_short,omitempty"`
}

func runDiff(cmd *cobra.Command, args []string) error {
	dir := configDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(dir, "data")
	}

	s, err := storage.New(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}

	var oldHash, newHash string

	if len(args) == 1 && strings.Contains(args[0], "..") {
		parts := strings.SplitN(args[0], "..", 2)
		oldHash = parts[0]
		newHash = parts[1]
	} else {
		// Default: compare HEAD with its parent.
		headPath := filepath.Join(cfg.DataDir, "HEAD")
		headData, err := os.ReadFile(headPath)
		if err != nil {
			return writeError("no_commits", "No commits found", false)
		}
		newHash = strings.TrimSpace(string(headData))

		newCommit, err := loadCommit(s, newHash)
		if err != nil {
			return fmt.Errorf("load HEAD commit: %w", err)
		}
		oldHash = newCommit.Parent
		if oldHash == "" {
			return writeError("no_parent", "HEAD has no parent commit to diff against", false)
		}
	}

	// Resolve short hashes by scanning storage.
	oldHash, err = resolveHash(s, oldHash)
	if err != nil {
		return fmt.Errorf("resolve old commit: %w", err)
	}
	newHash, err = resolveHash(s, newHash)
	if err != nil {
		return fmt.Errorf("resolve new commit: %w", err)
	}

	oldCommit, err := loadCommit(s, oldHash)
	if err != nil {
		return fmt.Errorf("load old commit: %w", err)
	}
	newCommit, err := loadCommit(s, newHash)
	if err != nil {
		return fmt.Errorf("load new commit: %w", err)
	}

	diff := graph.DiffCommits(oldCommit, newCommit)

	out := diffOutput{
		OldCommit:    oldHash[:12],
		NewCommit:    newHash[:12],
		AddedEdges:   len(diff.AddedEdges),
		RemovedEdges: len(diff.RemovedEdges),
	}

	// Resolve added node hashes to record summaries.
	for _, hash := range diff.AddedNodes {
		rec := resolveNodeHash(s, hash)
		out.Added = append(out.Added, rec)
	}
	for _, hash := range diff.RemovedNodes {
		rec := resolveNodeHash(s, hash)
		out.Removed = append(out.Removed, rec)
	}

	return printJSON(out)
}

func loadCommit(s *storage.Store, hash string) (*graph.Commit, error) {
	data, err := s.Read(hash)
	if err != nil {
		return nil, err
	}
	var c graph.Commit
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	c.Hash = hash
	return &c, nil
}

func resolveNodeHash(s *storage.Store, hash string) diffRecord {
	data, err := s.Read(hash)
	if err != nil {
		return diffRecord{ID: hash[:12]}
	}
	n, err := graph.UnmarshalNode(data)
	if err != nil {
		return diffRecord{ID: hash[:12]}
	}
	rec := diffRecord{ID: n.ID}
	if v, ok := n.Properties["content_short"]; ok {
		rec.SummaryShort = v.String()
	}
	return rec
}

// resolveHash expands a short hash prefix to a full hash by scanning storage.
func resolveHash(s *storage.Store, hash string) (string, error) {
	if len(hash) == 64 {
		return hash, nil
	}
	// Try to find a matching hash in storage.
	chunks, err := s.List()
	if err != nil {
		return hash, nil
	}
	var matches []string
	for _, h := range chunks {
		if strings.HasPrefix(h, hash) {
			matches = append(matches, h)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous hash prefix %q matches %d chunks", hash, len(matches))
	}
	return hash, nil
}
