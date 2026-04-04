package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/spf13/cobra"
)

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Store a knowledge record",
	Long: `Reads a JSON object from stdin containing the record content and
optional metadata. Creates a node in the knowledge graph, generates
embeddings if a provider is configured, and commits the change.

Required fields:
  content    The knowledge to store (string)

Optional fields:
  temporality, confidence, knowledge_type, epistemic_status,
  importance, keywords, summary_short, summary_abstract,
  source_ref, source_credibility, testimony_hops,
  context_about, context_who, context_prompted,
  context_findable_by, context_related,
  valid_from, valid_until`,
	RunE: runCapture,
}

func init() {
	rootCmd.AddCommand(captureCmd)
}

// captureInput is the JSON schema for capture input.
type captureInput struct {
	Content           string   `json:"content"`
	Temporality       string   `json:"temporality,omitempty"`
	Confidence        *float64 `json:"confidence,omitempty"`
	KnowledgeType     string   `json:"knowledge_type,omitempty"`
	EpistemicStatus   string   `json:"epistemic_status,omitempty"`
	Importance        *float64 `json:"importance,omitempty"`
	Keywords          []string `json:"keywords,omitempty"`
	SummaryShort      string   `json:"summary_short,omitempty"`
	SummaryAbstract   string   `json:"summary_abstract,omitempty"`
	SourceRef         string   `json:"source_ref,omitempty"`
	SourceCredibility *float64 `json:"source_credibility,omitempty"`
	TestimonyHops     *int64   `json:"testimony_hops,omitempty"`
	ContextAbout      string   `json:"context_about,omitempty"`
	ContextWho        string   `json:"context_who,omitempty"`
	ContextPrompted   string   `json:"context_prompted,omitempty"`
	ContextFindable   string   `json:"context_findable_by,omitempty"`
	ContextRelated    string   `json:"context_related,omitempty"`
	ValidFrom         string   `json:"valid_from,omitempty"`
	ValidUntil        string   `json:"valid_until,omitempty"`
}

// captureOutput is the JSON response from capture.
type captureOutput struct {
	ID       string   `json:"id"`
	Warnings []string `json:"warnings,omitempty"`
}

func runCapture(cmd *cobra.Command, args []string) error {
	// Load engine first so we have config limits for stdin reading.
	eng, err := loadEngine()
	if err != nil {
		return writeError("engine_error", err.Error(), false)
	}

	// Read and parse JSON from stdin with size limit and timeout.
	var input captureInput
	if err := readStdinJSON(&input, eng.cfg.Limits); err != nil {
		return writeError("input_error", err.Error(), true)
	}

	// Validate required fields.
	if input.Content == "" {
		return writeError("missing_field", "content is required", true)
	}

	// Validate field values.
	if err := validateCaptureInput(&input, eng.cfg.Limits); err != nil {
		return writeError("invalid_field", err.Error(), true)
	}

	// Build properties.
	props := graph.Properties{
		"content_full": graph.StringProperty(input.Content),
		"created_at":   graph.TimestampProperty(time.Now().UTC()),
	}

	hasClassification := input.Temporality != "" || input.Confidence != nil
	if hasClassification {
		props["processing_status"] = graph.StringProperty("processed")
	} else {
		props["processing_status"] = graph.StringProperty("captured")
	}

	if input.Temporality != "" {
		props["temporality"] = graph.StringProperty(input.Temporality)
	}
	if input.Confidence != nil {
		props["confidence"] = graph.Float64Property(*input.Confidence)
	}
	if input.KnowledgeType != "" {
		props["knowledge_type"] = graph.StringProperty(input.KnowledgeType)
	}
	if input.EpistemicStatus != "" {
		props["epistemic_status"] = graph.StringProperty(input.EpistemicStatus)
	}
	if input.Importance != nil {
		props["importance"] = graph.Float64Property(*input.Importance)
	}
	if len(input.Keywords) > 0 {
		props["content_keywords"] = graph.StringListProperty(input.Keywords)
	}
	if input.SummaryShort != "" {
		props["content_short"] = graph.StringProperty(input.SummaryShort)
	}
	if input.SummaryAbstract != "" {
		props["content_abstract"] = graph.StringProperty(input.SummaryAbstract)
	}
	if input.SourceRef != "" {
		props["source_ref"] = graph.StringProperty(input.SourceRef)
	}
	if input.SourceCredibility != nil {
		props["source_credibility"] = graph.Float64Property(*input.SourceCredibility)
	}
	if input.TestimonyHops != nil {
		props["testimony_hops"] = graph.Int64Property(*input.TestimonyHops)
	}
	if input.ContextAbout != "" {
		props["context_about"] = graph.StringProperty(input.ContextAbout)
	}
	if input.ContextWho != "" {
		props["context_who"] = graph.StringProperty(input.ContextWho)
	}
	if input.ContextPrompted != "" {
		props["context_prompted"] = graph.StringProperty(input.ContextPrompted)
	}
	if input.ContextFindable != "" {
		props["context_findable_by"] = graph.StringProperty(input.ContextFindable)
	}
	if input.ContextRelated != "" {
		props["context_related"] = graph.StringProperty(input.ContextRelated)
	}
	if input.ValidFrom != "" {
		if t, err := time.Parse(time.RFC3339, input.ValidFrom); err == nil {
			props["valid_from"] = graph.TimestampProperty(t)
		}
	}
	if input.ValidUntil != "" {
		if t, err := time.Parse(time.RFC3339, input.ValidUntil); err == nil {
			props["valid_until"] = graph.TimestampProperty(t)
		}
	}

	props["access_count"] = graph.Int64Property(0)

	n := eng.graph.AddNode(props)
	for k, v := range n.Properties {
		eng.propIdx.Add(n.ID, k, v)
	}

	var warnings []string
	if err := eng.generateEmbeddings(context.Background(), n.ID); err != nil {
		warnings = append(warnings, fmt.Sprintf("embedding generation failed: %s. Record stored without embeddings.", err))
	}

	if dupID, sim := eng.checkDedup(n.ID); dupID != "" {
		msg := fmt.Sprintf("potential duplicate of %s (similarity %.3f)", dupID, sim)
		if eng.cfg.Dedup.Action == "reject" {
			eng.propIdx.RemoveNode(n.ID, n.Properties)
			eng.vecIdx.Remove(n.ID)
			eng.graph.DeleteNode(n.ID)
			return writeError("duplicate", msg, false)
		}
		warnings = append(warnings, msg)
	}

	if numChunks, err := eng.chunkIfNeeded(context.Background(), n.ID); err != nil {
		warnings = append(warnings, fmt.Sprintf("chunking failed: %s", err))
	} else if numChunks > 0 {
		warnings = append(warnings, fmt.Sprintf("content chunked into %d segments", numChunks))
	}

	if _, err := eng.save("capture"); err != nil {
		return writeError("save_error", err.Error(), false)
	}

	return printJSON(captureOutput{ID: n.ID, Warnings: warnings})
}

func validateCaptureInput(input *captureInput, limits config.LimitsConfig) error {
	if err := validateStringLength("content", input.Content, limits.MaxContentLength); err != nil {
		return err
	}
	if err := validateFloat64Range("confidence", input.Confidence, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("importance", input.Importance, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateFloat64Range("source_credibility", input.SourceCredibility, 0.0, 1.0); err != nil {
		return err
	}
	if err := validateEnum("temporality", input.Temporality, validTemporalities); err != nil {
		return err
	}
	if err := validateEnum("knowledge_type", input.KnowledgeType, validKnowledgeTypes); err != nil {
		return err
	}
	if err := validateEnum("epistemic_status", input.EpistemicStatus, validEpistemicStatuses); err != nil {
		return err
	}
	if len(input.Keywords) > limits.MaxKeywords {
		return fmt.Errorf("keywords array exceeds maximum (%d items)", limits.MaxKeywords)
	}
	if err := validateStringLength("summary_short", input.SummaryShort, limits.MaxSummaryShort); err != nil {
		return err
	}
	if err := validateStringLength("summary_abstract", input.SummaryAbstract, limits.MaxSummaryAbstract); err != nil {
		return err
	}
	if err := validateStringFieldUTF8("content", input.Content); err != nil {
		return err
	}
	return nil
}

type errorOutput struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func writeError(code, message string, retryable bool) error {
	out := errorOutput{
		Error:     code,
		Message:   message,
		Retryable: retryable,
	}
	printJSON(out)
	return fmt.Errorf("%s: %s", code, message)
}
