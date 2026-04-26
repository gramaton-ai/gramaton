package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// addSearchFlags registers search filter flags on a command.
// Used by both search and export commands.
func addSearchFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.Float64("confidence-min", 0, "minimum confidence (0-1)")
	f.Float64("confidence-max", 0, "maximum confidence (0-1)")
	f.Float64("importance-min", 0, "minimum importance (0-1)")
	f.Float64("importance-max", 0, "maximum importance (0-1)")
	f.String("temporality", "", "filter: immutable|durable|temporal|ephemeral")
	f.String("knowledge-type", "", "filter: episodic|semantic|procedural|conceptual|reference")
	f.String("epistemic-status", "", "filter: well_established|probable|speculative|contested|refuted")
	f.String("resolution", "", "filter: completed|superseded|abandoned|obsolete|unresolved")
	f.String("processing-status", "", "filter: captured|processed|stuck|deleted")
	f.Int("top", 10, "number of results")
	f.Bool("include-historical", false, "include records past valid_until")
	f.String("since", "", "created after date (YYYY-MM-DD)")
	f.String("sort", "", "sort field")
	f.String("order", "", "asc or desc")
	f.String("match", "", "literal substring match")
	f.String("similar-to", "", "record ID to find similar records")
	f.String("keywords", "", "comma-separated keywords")
	f.String("missing", "", "comma-separated field names that must be unset")
	f.Bool("random", false, "return random results")
	f.Int64("access-count-min", 0, "minimum access count")
	f.Int64("access-count-max", 0, "maximum access count")
	f.String("last-accessed-after", "", "accessed after date")
	f.String("last-accessed-before", "", "accessed before date")
	f.String("valid-after", "", "valid_from after date")
	f.String("valid-before", "", "valid_from before date")
	f.String("expires-after", "", "valid_until after date")
	f.String("expires-before", "", "valid_until before date")
	f.Int("min-edges", -1, "minimum edge count")
	f.Int("max-edges", -1, "maximum edge count (0 = orphans)")
}

// buildSearchBody reads search flags from a command and builds the
// request body. If args has a query string, it's set as "text".
func buildSearchBody(cmd *cobra.Command, args []string) map[string]any {
	body := map[string]any{}

	top, _ := cmd.Flags().GetInt("top")
	body["top"] = top
	hist, _ := cmd.Flags().GetBool("include-historical")
	body["include_historical"] = hist

	if len(args) > 0 {
		body["text"] = args[0]
	}

	setStringFlag(cmd, body, "temporality", "temporality")
	setStringFlag(cmd, body, "knowledge-type", "knowledge_type")
	setStringFlag(cmd, body, "epistemic-status", "epistemic_status")
	setStringFlag(cmd, body, "resolution", "resolution")
	setStringFlag(cmd, body, "processing-status", "processing_status")
	setStringFlag(cmd, body, "since", "since")
	setStringFlag(cmd, body, "sort", "sort")
	setStringFlag(cmd, body, "order", "order")
	setStringFlag(cmd, body, "match", "match")
	setStringFlag(cmd, body, "similar-to", "similar_to")
	setStringFlag(cmd, body, "last-accessed-after", "last_accessed_after")
	setStringFlag(cmd, body, "last-accessed-before", "last_accessed_before")
	setStringFlag(cmd, body, "valid-after", "valid_after")
	setStringFlag(cmd, body, "valid-before", "valid_before")
	setStringFlag(cmd, body, "expires-after", "expires_after")
	setStringFlag(cmd, body, "expires-before", "expires_before")

	if cmd.Flags().Changed("confidence-min") {
		v, _ := cmd.Flags().GetFloat64("confidence-min")
		body["confidence_min"] = v
	}
	if cmd.Flags().Changed("confidence-max") {
		v, _ := cmd.Flags().GetFloat64("confidence-max")
		body["confidence_max"] = v
	}
	if cmd.Flags().Changed("importance-min") {
		v, _ := cmd.Flags().GetFloat64("importance-min")
		body["importance_min"] = v
	}
	if cmd.Flags().Changed("importance-max") {
		v, _ := cmd.Flags().GetFloat64("importance-max")
		body["importance_max"] = v
	}
	if cmd.Flags().Changed("access-count-min") {
		v, _ := cmd.Flags().GetInt64("access-count-min")
		body["access_count_min"] = v
	}
	if cmd.Flags().Changed("access-count-max") {
		v, _ := cmd.Flags().GetInt64("access-count-max")
		body["access_count_max"] = v
	}

	if kw, _ := cmd.Flags().GetString("keywords"); kw != "" {
		body["keywords"] = strings.Split(kw, ",")
	}
	if m, _ := cmd.Flags().GetString("missing"); m != "" {
		body["missing"] = strings.Split(m, ",")
	}

	if r, _ := cmd.Flags().GetBool("random"); r {
		body["random"] = true
	}

	if me, _ := cmd.Flags().GetInt("min-edges"); me >= 0 {
		body["min_edges"] = me
	}
	if me, _ := cmd.Flags().GetInt("max-edges"); me >= 0 {
		body["max_edges"] = me
	}

	return body
}

func setStringFlag(cmd *cobra.Command, body map[string]any, flagName, bodyKey string) {
	v, _ := cmd.Flags().GetString(flagName)
	if v != "" {
		body[bodyKey] = v
	}
}
