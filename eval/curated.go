package eval

import (
	"testing"

	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/curation"
)

// BuildCuratedEvalEngine creates an eval engine and runs deterministic
// curation on it. This produces concept nodes, cross-section links,
// orphan linking, and quality repairs -- matching what the production
// store looks like after curation has run.
func BuildCuratedEvalEngine(t *testing.T, records []EvalRecord) (*core.Engine, map[string]string) {
	t.Helper()

	eng, nameToID := BuildEvalEngine(t, records)

	// Run deterministic curation to build concept nodes, link orphans,
	// detect duplicates, and apply quality repairs.
	cfg := eng.Config()
	cfg.Concepts.EmergenceThreshold = 3 // lower threshold for eval dataset size
	result := curation.RunDeterministic(eng, cfg, nil)

	t.Logf("Curation: orphans_linked=%d, quality_repairs=%d, concept_candidates=%d",
		result.OrphansLinked, result.QualityRepairs, len(result.ConceptCandidates))

	return eng, nameToID
}
