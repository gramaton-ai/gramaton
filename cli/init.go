package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/brandonlattin/gramaton/config"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Gramaton configuration",
	Long: `Creates the configuration directory and default config file.
Sets up the data directory for the knowledge store.`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	dir := configDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Check if already initialized.
	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Fprintf(os.Stderr, "Already initialized: %s\n", cfgPath)
		fmt.Fprintln(os.Stderr, "Edit the config file directly, or delete it and re-run init.")
		return nil
	}

	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")

	// Create data directory.
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	// Save config.
	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Initialized Gramaton at %s\n", dir)
	fmt.Printf("  Config: %s\n", cfgPath)
	fmt.Printf("  Data:   %s\n", cfg.DataDir)
	fmt.Println()
	fmt.Println("No embedding provider configured. Gramaton works without one")
	fmt.Println("(keyword and graph search still work), but semantic similarity")
	fmt.Println("search requires an embedding provider.")
	fmt.Println()
	fmt.Println("To configure Ollama (recommended):")
	fmt.Println("  1. Install Ollama: brew install ollama")
	fmt.Println("  2. Pull a model:   ollama pull nomic-embed-text")
	fmt.Printf("  3. Edit %s and set:\n", cfgPath)
	fmt.Println("     embedding:")
	fmt.Println("       provider: ollama")

	return nil
}
