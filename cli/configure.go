package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var configureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Configure API keys and settings",
	Long: `Interactively configure Gramaton API keys. Keys are stored in files
with restricted permissions (0600) so the server can read them
regardless of how it was started.

Currently supports:
  - Anthropic API key (for LLM-powered curation)`,
	RunE: runConfigure,
}

func init() {
	rootCmd.AddCommand(configureCmd)
}

func runConfigure(cmd *cobra.Command, args []string) error {
	dir := configDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Load existing config.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w\nRun 'gramaton init' first.", err)
	}

	reader := bufio.NewReader(os.Stdin)

	// Anthropic API key.
	fmt.Println("Anthropic API key (for LLM-powered curation)")
	fmt.Println()
	if cfg.LLM.APIKeyFile != "" {
		fmt.Printf("  Currently configured via file: %s\n", cfg.LLM.APIKeyFile)
	} else if cfg.LLM.APIKeyEnv != "" {
		fmt.Printf("  Currently configured via env var: %s\n", cfg.LLM.APIKeyEnv)
	} else {
		fmt.Println("  Not configured.")
	}
	fmt.Println()
	fmt.Print("Enter API key (or press Enter to skip): ")

	key, err := readSecret()
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	key = strings.TrimSpace(key)

	if key == "" {
		fmt.Println("  Skipped.")
		return nil
	}

	// Validate key format.
	if !strings.HasPrefix(key, "sk-") {
		fmt.Print("  Key doesn't start with 'sk-'. Save anyway? [y/N]: ")
		confirm, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(confirm)) != "y" {
			fmt.Println("  Aborted.")
			return nil
		}
	}

	// Write key file.
	keyPath := filepath.Join(dir, "anthropic.key")
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("write key file: %w", err)
	}
	fmt.Printf("\n  Key saved to %s\n", keyPath)

	// Update config to point to the key file.
	cfg.LLM.APIKeyFile = keyPath
	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("update config: %w", err)
	}
	fmt.Printf("  Config updated: llm.api_key_file = %s\n", keyPath)

	fmt.Println()
	fmt.Println("Restart the server to pick up changes:")
	fmt.Println("  gramaton stop && gramaton serve")

	return nil
}

// readSecret reads a line from stdin, hiding input if connected to a
// terminal.
func readSecret() (string, error) {
	fd := int(syscall.Stdin)
	if term.IsTerminal(fd) {
		bytes, err := term.ReadPassword(fd)
		fmt.Println() // newline after hidden input
		return string(bytes), err
	}
	// Not a terminal (piped input).
	reader := bufio.NewReader(os.Stdin)
	return reader.ReadString('\n')
}
