package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var curationTrigger bool

var curationCmd = &cobra.Command{
	Use:   "curation",
	Short: "View curation status or trigger a curation cycle",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if curationTrigger {
			resp, err := serverPost("/v1/curation/trigger", map[string]any{})
			if err != nil {
				return fmt.Errorf("curation trigger: %w", err)
			}
			return printEnvelope(resp)
		}

		resp, err := serverGet("/v1/curation")
		if err != nil {
			return fmt.Errorf("curation: %w", err)
		}
		return printEnvelope(resp)
	},
}

func init() {
	curationCmd.Flags().BoolVar(&curationTrigger, "trigger", false, "trigger a curation cycle immediately")
	rootCmd.AddCommand(curationCmd)
}
