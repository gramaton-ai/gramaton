package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/storage"
	"github.com/spf13/cobra"
)

var (
	logLast   int
	logRecord string
)

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "Show commit history",
	Long: `Displays the chain of commits from HEAD.

With --record, shows only commits that affected a specific node,
including what changed at each step.`,
	RunE: runLog,
}

func init() {
	logCmd.Flags().IntVar(&logLast, "last", 20, "number of commits to walk")
	logCmd.Flags().StringVar(&logRecord, "record", "", "show history for a specific record ID")
	rootCmd.AddCommand(logCmd)
}

type logEntry struct {
	Hash      string `json:"hash"`
	Parent    string `json:"parent,omitempty"`
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
	Nodes     int    `json:"nodes"`
	Edges     int    `json:"edges"`
}

type recordLogEntry struct {
	Hash      string         `json:"hash"`
	Timestamp string         `json:"timestamp"`
	Action    string         `json:"action"`
	Message   string         `json:"message"`
	Changes   map[string]any `json:"changes,omitempty"`
}

func runLog(cmd *cobra.Command, args []string) error {
	dir := configDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(dir, "data")
	}

	headPath := filepath.Join(cfg.DataDir, "HEAD")
	headData, err := os.ReadFile(headPath)
	if err != nil {
		return printJSON([]logEntry{})
	}
	headHash := strings.TrimSpace(string(headData))
	if headHash == "" {
		return printJSON([]logEntry{})
	}

	s, err := storage.New(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}

	if logRecord != "" {
		return runRecordLog(s, headHash)
	}

	return runFullLog(s, headHash)
}

func runFullLog(s *storage.Store, headHash string) error {
	var entries []logEntry
	currentHash := headHash

	for i := 0; i < logLast && currentHash != ""; i++ {
		commit, err := loadCommit(s, currentHash)
		if err != nil {
			break
		}

		entries = append(entries, logEntry{
			Hash:      currentHash[:12],
			Parent:    truncHash(commit.Parent),
			Timestamp: commit.Timestamp.Format("2006-01-02T15:04:05Z"),
			Message:   commit.Message,
			Nodes:     len(commit.NodeHashes),
			Edges:     len(commit.EdgeHashes),
		})

		currentHash = commit.Parent
	}

	return printJSON(entries)
}

func runRecordLog(s *storage.Store, headHash string) error {
	var entries []recordLogEntry
	currentHash := headHash

	for i := 0; i < logLast && currentHash != ""; i++ {
		commit, err := loadCommit(s, currentHash)
		if err != nil {
			break
		}

		// Find the node in this commit's node hashes.
		nodeInCurrent := findNodeInCommit(s, commit, logRecord)

		// Find the node in the parent commit.
		var nodeInParent *graph.Node
		if commit.Parent != "" {
			parentCommit, err := loadCommit(s, commit.Parent)
			if err == nil {
				nodeInParent = findNodeInCommit(s, parentCommit, logRecord)
			}
		}

		// Determine what happened to the node at this commit.
		entry := recordLogEntry{
			Hash:      currentHash[:12],
			Timestamp: commit.Timestamp.Format("2006-01-02T15:04:05Z"),
			Message:   commit.Message,
		}

		if nodeInCurrent != nil && nodeInParent == nil {
			entry.Action = "created"
			entries = append(entries, entry)
		} else if nodeInCurrent == nil && nodeInParent != nil {
			entry.Action = "deleted"
			entries = append(entries, entry)
		} else if nodeInCurrent != nil && nodeInParent != nil {
			changes := diffProperties(nodeInParent.Properties, nodeInCurrent.Properties)
			if len(changes) > 0 {
				entry.Action = "modified"
				entry.Changes = changes
				entries = append(entries, entry)
			}
		}

		currentHash = commit.Parent
	}

	return printJSON(entries)
}

// findNodeInCommit looks up a specific node by ID in a commit.
// Uses prolly tree lookup for v1 commits (O(log N)).
func findNodeInCommit(s *storage.Store, commit *graph.Commit, nodeID string) *graph.Node {
	hash, found, err := graph.NodeHashInCommit(s, commit.Hash, nodeID)
	if err != nil || !found {
		return nil
	}
	data, err := s.Read(hash)
	if err != nil {
		return nil
	}
	n, err := graph.UnmarshalNode(data)
	if err != nil {
		return nil
	}
	return n
}

// diffProperties compares two property maps and returns the changes.
func diffProperties(old, new graph.Properties) map[string]any {
	changes := make(map[string]any)

	for k, newVal := range new {
		oldVal, existed := old[k]
		if !existed {
			changes[k] = map[string]any{"added": newVal.FormatValue()}
		} else if !oldVal.Equal(newVal) {
			changes[k] = map[string]any{
				"from": oldVal.FormatValue(),
				"to":   newVal.FormatValue(),
			}
		}
	}
	for k := range old {
		if _, exists := new[k]; !exists {
			changes[k] = map[string]any{"removed": old[k].FormatValue()}
		}
	}

	return changes
}

func truncHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
