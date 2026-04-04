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

var logLast int

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "Show commit history",
	Long:  `Displays the chain of commits from HEAD, showing what changed and when.`,
	RunE:  runLog,
}

func init() {
	logCmd.Flags().IntVar(&logLast, "last", 10, "number of commits to show")
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

	var entries []logEntry
	currentHash := headHash

	for i := 0; i < logLast && currentHash != ""; i++ {
		data, err := s.Read(currentHash)
		if err != nil {
			break
		}

		var commit graph.Commit
		if err := json.Unmarshal(data, &commit); err != nil {
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

func truncHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
