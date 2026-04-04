package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/storage"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show knowledge store status",
	Long:  `Displays node and edge counts, storage health, and configuration state.`,
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

// StatusOutput is the structured output for the status command.
type StatusOutput struct {
	Initialized bool   `json:"initialized"`
	ConfigPath  string `json:"config_path"`
	DataDir     string `json:"data_dir"`
	NodeCount   int    `json:"node_count"`
	EdgeCount   int    `json:"edge_count"`
	ChunkCount  int    `json:"chunk_count"`
	Embedding   string `json:"embedding_provider"`
}

func runStatus(cmd *cobra.Command, args []string) error {
	dir := configDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Check initialization.
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		out := StatusOutput{
			Initialized: false,
			ConfigPath:  cfgPath,
		}
		return printJSON(out)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	out := StatusOutput{
		Initialized: true,
		ConfigPath:  cfgPath,
		DataDir:     cfg.DataDir,
		Embedding:   cfg.Embedding.Provider,
	}

	// Count storage chunks if data dir exists.
	if _, err := os.Stat(cfg.DataDir); err == nil {
		store, err := storage.New(cfg.DataDir)
		if err == nil {
			chunks, err := store.List()
			if err == nil {
				out.ChunkCount = len(chunks)
			}
		}
	}

	// Load graph and count nodes/edges.
	// For now, the graph is in-memory only, so we report 0.
	// Once persistence is wired, this will load from storage.
	g := graph.New()
	out.NodeCount = g.NodeCount()
	out.EdgeCount = g.EdgeCount()

	return printJSON(out)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
