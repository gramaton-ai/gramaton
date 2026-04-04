package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/embed/ollama"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Gramaton configuration",
	Long: `Creates the configuration directory, default config file, and data
directory. Detects and configures an embedding provider automatically.`,
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

	fmt.Printf("Initialized Gramaton at %s\n", dir)
	fmt.Printf("  Config: %s\n", cfgPath)
	fmt.Printf("  Data:   %s\n", cfg.DataDir)
	fmt.Println()

	// Detect and configure embedding provider.
	configured := setupEmbedding(&cfg, cfgPath)

	// Save config.
	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if configured {
		fmt.Println()
		fmt.Println("Gramaton is ready. Start capturing knowledge.")
	}

	return nil
}

// setupEmbedding detects Ollama, starts it if needed, pulls the default
// model, and configures the embedding provider. Returns true if embedding
// was configured.
func setupEmbedding(cfg *config.Config, cfgPath string) bool {
	fmt.Println("Checking for embedding providers...")
	fmt.Println()

	endpoint := cfg.Embedding.Endpoint
	model := cfg.Embedding.Model

	// Check if Ollama binary exists.
	bin := ollama.FindBinary()
	if bin == "" {
		printNoOllama()
		return false
	}

	fmt.Printf("  [found] Ollama binary: %s\n", bin)

	// Ensure Ollama is running.
	if !ollama.IsReachable(endpoint) {
		fmt.Println("  [....] Starting Ollama...")
		if err := ollama.EnsureRunning(endpoint); err != nil {
			fmt.Printf("  [fail] Could not start Ollama: %s\n", err)
			printManualSetup(cfgPath)
			return false
		}
	}
	fmt.Printf("  [ok]    Ollama responding at %s\n", endpoint)

	// Check for embedding model.
	if !ollama.HasModel(endpoint, model) {
		fmt.Printf("  [....] Pulling %s...\n", model)
		err := ollama.PullModel(context.Background(), endpoint, model, func(msg string) {
			fmt.Printf("  [info] %s\n", msg)
		})
		if err != nil {
			fmt.Printf("  [fail] Could not pull model: %s\n", err)
			printManualSetup(cfgPath)
			return false
		}
	}
	fmt.Printf("  [ok]    Model %s available\n", model)

	// Configure.
	cfg.Embedding.Provider = "ollama"
	fmt.Println()
	fmt.Println("  Embedding configured: Ollama with " + model)

	return true
}

func printNoOllama() {
	fmt.Println("  Ollama not found.")
	fmt.Println()
	fmt.Println("  Ollama provides local, private embedding (recommended).")
	fmt.Println("  Install it:")
	switch runtime.GOOS {
	case "darwin":
		fmt.Println("    Download from https://ollama.com/download/mac")
		fmt.Println("    Or if you have Homebrew: brew install ollama")
	case "linux":
		fmt.Println("    curl -fsSL https://ollama.com/install.sh | sh")
	default:
		fmt.Println("    Download from https://ollama.com")
	}
	fmt.Println()
	fmt.Println("  Then re-run: gramaton init")
	fmt.Println()
	fmt.Println("  Gramaton works without embeddings (keyword and graph search")
	fmt.Println("  still work), but semantic similarity search requires them.")
}

func printManualSetup(cfgPath string) {
	fmt.Println()
	fmt.Printf("  To configure manually, edit %s:\n", cfgPath)
	fmt.Println("    embedding:")
	fmt.Println("      provider: ollama")
}
