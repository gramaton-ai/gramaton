package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var reembedBatch int

var reembedCmd = &cobra.Command{
	Use:   "reembed",
	Short: "Re-embed records with stale embeddings",
	Long: `Finds records where embedding_model differs from the current
provider's model and regenerates their embeddings. Use after
changing embedding models or providers.`,
	RunE: runReembed,
}

func init() {
	reembedCmd.Flags().IntVar(&reembedBatch, "batch", 0, "maximum number of records to process (0 = all)")
	rootCmd.AddCommand(reembedCmd)
}

type reembedOutput struct {
	CurrentModel string `json:"current_model"`
	StaleCount   int    `json:"stale_found"`
	Processed    int    `json:"processed"`
	Skipped      int    `json:"skipped"`
}

func runReembed(cmd *cobra.Command, args []string) error {
	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	if eng.embedder == nil {
		return writeError("no_provider", "No embedding provider configured", false)
	}

	currentModel := eng.embedder.ModelID()

	// Find records with stale or missing embeddings.
	var staleIDs []string
	for _, id := range eng.graph.AllNodeIDs() {
		n, ok := eng.graph.GetNode(id)
		if !ok {
			continue
		}

		// Skip nodes that don't have content to embed.
		hasContent := false
		for _, key := range []string{"content_full", "content_short", "content_abstract", "content_keywords"} {
			if _, ok := n.Properties[key]; ok {
				hasContent = true
				break
			}
		}
		if !hasContent {
			continue
		}

		// Check if embedding model matches current.
		if v, ok := n.Properties["embedding_model"]; ok {
			if v.String() == currentModel {
				continue // up to date
			}
		}

		staleIDs = append(staleIDs, id)
	}

	out := reembedOutput{
		CurrentModel: currentModel,
		StaleCount:   len(staleIDs),
	}

	if len(staleIDs) == 0 {
		return printJSON(out)
	}

	limit := len(staleIDs)
	if reembedBatch > 0 && reembedBatch < limit {
		limit = reembedBatch
	}

	for i := 0; i < limit; i++ {
		id := staleIDs[i]
		n, _ := eng.graph.GetNode(id)

		// Remove old embedding properties.
		for _, key := range []string{"embedding_keywords", "embedding_short", "embedding_abstract", "embedding_full"} {
			if old, ok := n.Properties[key]; ok {
				eng.propIdx.Remove(id, key, old)
				eng.graph.RemoveNodeProperty(id, key)
			}
		}
		eng.vecIdx.Remove(id)

		// Remove old embedding_model.
		if old, ok := n.Properties["embedding_model"]; ok {
			eng.propIdx.Remove(id, "embedding_model", old)
		}

		// Regenerate.
		if err := eng.generateEmbeddings(context.Background(), id); err != nil {
			out.Skipped++
			continue
		}
		out.Processed++
	}

	if out.Processed > 0 {
		msg := fmt.Sprintf("reembed %d records to %s", out.Processed, currentModel)
		if _, err := eng.save(msg); err != nil {
			return writeError("save_error", err.Error(), false)
		}
	}

	return printJSON(out)
}
