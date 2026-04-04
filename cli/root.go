package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var cfgDir string

var rootCmd = &cobra.Command{
	Use:   "gramaton",
	Short: "A knowledge store for AI agents",
	Long: `Gramaton stores knowledge in a versioned property graph with vector
search and epistemic metadata. Agents interact through CLI commands
that filter and rank by metadata before returning results.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgDir, "config-dir", "", "configuration directory (default: ~/.gramaton)")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// configDir returns the effective configuration directory.
func configDir() string {
	if cfgDir != "" {
		return cfgDir
	}
	return defaultConfigDir()
}

func defaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".gramaton"
	}
	return home + "/.gramaton"
}
