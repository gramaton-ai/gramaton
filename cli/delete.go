package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var deleteReason string

var deleteCmd = &cobra.Command{
	Use:   "delete <record-id> [id2 ...]",
	Short: "Delete records (repair tool)",
	Long: `Soft delete: removes nodes and their edges from the current graph
state. Creates a commit recording the deletion. Recoverable via
gramaton revert.

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

type deleteOutput struct {
	Deleted []string `json:"deleted"`
	Reason  string   `json:"reason"`
}

func runDelete(cmd *cobra.Command, args []string) error {
	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	var deleted []string
	for _, id := range args {
		n, ok := eng.graph.GetNode(id)
		if !ok {
			return writeError("not_found", fmt.Sprintf("record %s not found", id), false)
		}

		// Remove from property index.
		eng.propIdx.RemoveNode(id, n.Properties)

		// Remove from vector index.
		eng.vecIdx.Remove(id)

		// Delete from graph (cascades edges).
		if err := eng.graph.DeleteNode(id); err != nil {
			return writeError("delete_error", err.Error(), false)
		}

		deleted = append(deleted, id)
	}

	if _, err := eng.save(fmt.Sprintf("delete: %s", deleteReason)); err != nil {
		return writeError("save_error", err.Error(), false)
	}

	return printJSON(deleteOutput{
		Deleted: deleted,
		Reason:  deleteReason,
	})
}
