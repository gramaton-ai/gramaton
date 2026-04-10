package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	curationTrigger bool
	curationBatch   bool
)

var curationCmd = &cobra.Command{
	Use:   "curation",
	Short: "View curation status or trigger a curation cycle",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if curationBatch {
			fmt.Println("Starting batch classification...")
			resp, err := serverPost("/v1/curation/batch", map[string]any{})
			if err != nil {
				return fmt.Errorf("curation batch: %w", err)
			}
			return printEnvelope(resp)
		}

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
	curationCmd.Flags().BoolVar(&curationBatch, "batch", false, "submit all pending records as a batch (API providers: 50%% discount)")
	rootCmd.AddCommand(curationCmd)
}
