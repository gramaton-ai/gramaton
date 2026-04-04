package cli

import (
	"fmt"
	"os"
	"path/filepath"

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
	HeadCommit  string `json:"head_commit,omitempty"`
	Embedding   string `json:"embedding_provider"`
}

func runStatus(cmd *cobra.Command, args []string) error {
	dir := configDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Check initialization.
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		return printJSON(StatusOutput{
			Initialized: false,
			ConfigPath:  cfgPath,
		})
	}

	eng, err := loadEngine()
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	chunks, _ := eng.store.List()

	return printJSON(StatusOutput{
		Initialized: true,
		ConfigPath:  cfgPath,
		DataDir:     eng.cfg.DataDir,
		NodeCount:   eng.graph.NodeCount(),
		EdgeCount:   eng.graph.EdgeCount(),
		ChunkCount:  len(chunks),
		HeadCommit:  eng.headHash,
		Embedding:   eng.cfg.Embedding.Provider,
	})
}
