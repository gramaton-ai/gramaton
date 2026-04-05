package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/store"
	"github.com/spf13/cobra"
)

var (
	cfgDir    string
	storeName string
)

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
	rootCmd.PersistentFlags().StringVar(&storeName, "store", "", "named store to use (env: GRAMATON_STORE)")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// configDir returns the effective configuration directory, incorporating
// the --store flag or GRAMATON_STORE env var for named store resolution.
func configDir() string {
	base := baseConfigDir()
	name := activeStoreName()
	if name != "" {
		if err := store.ValidateName(name); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid store name: %s\n", err)
			os.Exit(1)
		}
	}
	return store.Resolve(base, name)
}

// baseConfigDir returns the root gramaton directory without store
// resolution. Used by store management commands.
func baseConfigDir() string {
	if cfgDir != "" {
		return cfgDir
	}
	return config.DefaultDir()
}

// activeStoreName returns the resolved store name from the --store
// flag or GRAMATON_STORE env var. Returns empty string for the
// unnamed default store.
func activeStoreName() string {
	if storeName != "" {
		return storeName
	}
	return os.Getenv("GRAMATON_STORE")
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
