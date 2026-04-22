package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var updateFile string

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update a record or create an edge",
	Long: `Reads a JSON object from stdin or --file. Two modes:

Property update: set id and any metadata fields to change.
  {"id": "...", "confidence": 0.4, "epistemic_status": "contested"}

Edge creation: set id, link_to, edge_type, and optionally edge_weight.
  {"id": "...", "link_to": "...", "edge_type": "justifies", "edge_weight": 0.9}`,
	RunE: runUpdate,
}

func init() {
	updateCmd.Flags().StringVarP(&updateFile, "file", "f", "", "read JSON input from file instead of stdin (deleted after read if in gramaton temp dir)")
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	var input map[string]any
	if updateFile != "" {
		if err := readInputJSON(updateFile, &input, defaultLimits()); err != nil {
			return writeError("input_error", err.Error(), true)
		}
	} else {
		if err := readStdinJSON(&input, defaultLimits()); err != nil {
			return writeError("input_error", err.Error(), true)
		}
	}

	id, _ := input["id"].(string)
	if id == "" {
		return writeError("missing_field", "id is required", true)
	}

	// Check if this is an edge creation.
	if linkTo, ok := input["link_to"].(string); ok && linkTo != "" {
		edgeBody := map[string]any{
			"target_id": linkTo,
			"edge_type": input["edge_type"],
		}
		if w, ok := input["edge_weight"]; ok {
			edgeBody["edge_weight"] = w
		}

		resp, err := serverPost(fmt.Sprintf("/v1/records/%s/edges", url.PathEscape(id)), edgeBody)
		if err != nil {
			return fmt.Errorf("update: %w", err)
		}
		return printEnvelope(resp)
	}

	// Property update. Forward every field the server's updateRequest
	// accepts -- previously only 5 of 11 made it through, so users
	// piping JSON with summary_short/keywords/valid_until/meta/etc.
	// got a successful response with no change.
	updateBody := make(map[string]any)
	for _, key := range []string{
		"confidence",
		"temporality",
		"knowledge_type",
		"epistemic_status",
		"importance",
		"keywords",
		"summary_short",
		"valid_until",
		"asserted_as_of",
		"meta",
	} {
		if v, ok := input[key]; ok {
			updateBody[key] = v
		}
	}

	resp, err := serverPatch(fmt.Sprintf("/v1/records/%s", url.PathEscape(id)), updateBody)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}

	return printEnvelope(resp)
}
