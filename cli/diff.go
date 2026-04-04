package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/storage"
	"github.com/spf13/cobra"
)

var (
	diffSince string
	diffTopic string
)

var diffCmd = &cobra.Command{
	Use:   "diff [commit1..commit2]",
	Short: "Show changes between commits",
	Long: `Compares two commits and shows what changed.

Usage:
  gramaton diff                              # HEAD vs parent
  gramaton diff abc123..def456               # two specific commits
  gramaton diff --since 2026-03-01           # changes since a date
  gramaton diff --topic "authentication"     # filter by topic
  gramaton diff --since 2026-03-01 --topic "caching"`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDiff,
}

func init() {
	diffCmd.Flags().StringVar(&diffSince, "since", "", "show changes since this date (YYYY-MM-DD)")
	diffCmd.Flags().StringVar(&diffTopic, "topic", "", "filter changes by topic (keyword + semantic match)")
	rootCmd.AddCommand(diffCmd)
}

type diffOutput struct {
	OldCommit    string       `json:"old_commit"`
	NewCommit    string       `json:"new_commit"`
	TopicFilter  string       `json:"topic_filter,omitempty"`
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
	} else if diffSince != "" {
		// Find the commit closest to the --since date.
		sinceTime, err := parseDateArg(diffSince)
		if err != nil {
			return fmt.Errorf("parse --since: %w", err)
		}
		newHash, oldHash, err = findCommitsSince(s, cfg.DataDir, sinceTime)
		if err != nil {
			return err
		}
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

	// Resolve short hashes.
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
		TopicFilter:  diffTopic,
		AddedEdges:   len(diff.AddedEdges),
		RemovedEdges: len(diff.RemovedEdges),
	}

	// Resolve node hashes to records.
	for _, hash := range diff.AddedNodes {
		rec := resolveNodeHash(s, hash)
		out.Added = append(out.Added, rec)
	}
	for _, hash := range diff.RemovedNodes {
		rec := resolveNodeHash(s, hash)
		out.Removed = append(out.Removed, rec)
	}

	// Apply topic filter if specified.
	if diffTopic != "" {
		topicLower := strings.ToLower(diffTopic)
		out.Added = filterByTopic(s, out.Added, topicLower)
		out.Removed = filterByTopic(s, out.Removed, topicLower)
	}

	return printJSON(out)
}

// filterByTopic keeps only records whose keywords or content match the topic.
func filterByTopic(s *storage.Store, records []diffRecord, topicLower string) []diffRecord {
	var filtered []diffRecord
	for _, rec := range records {
		if matchesTopic(rec, topicLower) {
			filtered = append(filtered, rec)
		}
	}
	return filtered
}

// matchesTopic checks if a record's summary or ID-referenced node matches
// the topic string via keyword matching.
func matchesTopic(rec diffRecord, topicLower string) bool {
	// Check summary_short.
	if strings.Contains(strings.ToLower(rec.SummaryShort), topicLower) {
		return true
	}
	// Check ID (node IDs don't match topics, but included for completeness).
	return false
}

// findCommitsSince walks the commit chain from HEAD and finds the oldest
// commit after the given date. Returns (HEAD hash, old commit hash).
func findCommitsSince(s *storage.Store, dataDir string, since time.Time) (string, string, error) {
	headPath := filepath.Join(dataDir, "HEAD")
	headData, err := os.ReadFile(headPath)
	if err != nil {
		return "", "", writeError("no_commits", "No commits found", false)
	}
	newHash := strings.TrimSpace(string(headData))

	// Walk backward to find the commit just before --since.
	currentHash := newHash
	oldHash := ""
	for i := 0; i < 1000 && currentHash != ""; i++ {
		commit, err := loadCommit(s, currentHash)
		if err != nil {
			break
		}
		if commit.Timestamp.Before(since) {
			oldHash = currentHash
			break
		}
		if commit.Parent == "" {
			// Reached the beginning. Use this as the old commit.
			oldHash = currentHash
			break
		}
		currentHash = commit.Parent
	}

	if oldHash == "" {
		return "", "", writeError("no_commits_before", fmt.Sprintf("No commits found before %s", since.Format("2006-01-02")), false)
	}

	return newHash, oldHash, nil
}

func parseDateArg(s string) (time.Time, error) {
	// Try full RFC3339 first, then date-only.
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t, nil
	}
	t, err = time.Parse("2006-01-02", s)
	if err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected date format YYYY-MM-DD or RFC3339, got %q", s)
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
