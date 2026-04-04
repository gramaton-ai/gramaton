package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var deleteReason string

var deleteCmd = &cobra.Command{
	Use:   "delete <record-id> [id2 ...]",
	Short: "Delete records (repair tool)",
	Long: `Soft delete: marks records as deleted with an optional reason.
Recoverable via gramaton revert.

This is a repair tool, not a knowledge management tool. Normal
practice is to supersede, not delete (tenet 8).`,
	Args: cobra.MinimumNArgs(1),
	RunE: runDelete,
}

func init() {
	deleteCmd.Flags().StringVar(&deleteReason, "reason", "", "reason for deletion (required)")
	deleteCmd.MarkFlagRequired("reason")
	rootCmd.AddCommand(deleteCmd)
}

func runDelete(cmd *cobra.Command, args []string) error {
	var deleted []string
	for _, id := range args {
		path := fmt.Sprintf("/v1/records/%s", id)
		if deleteReason != "" {
			path += "?reason=" + url.QueryEscape(deleteReason)
		}

		_, err := serverDelete(path)
		if err != nil {
			return writeError("delete_error", fmt.Sprintf("failed to delete %s: %s", id, err), false)
		}
		deleted = append(deleted, id)
	}

	return printJSON(map[string]any{
		"deleted": deleted,
		"reason":  deleteReason,
	})
}
