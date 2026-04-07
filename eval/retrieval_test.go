package eval

import (
	"fmt"
	"os"
	"testing"

	"github.com/brandonlattin/gramaton/config"
)

func loadTestDataset(t *testing.T) *EvalDataset {
	t.Helper()
	dir := DataDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("no eval data at %s -- generate with gramaton-bench", dir)
	}
	ds, err := LoadDataset(dir)
	if err != nil {
		t.Fatalf("load dataset: %v", err)
	}
	return ds
}

func loadTestSubDataset(t *testing.T, name string) *EvalDataset {
	t.Helper()
	dir := DataDir()
	ds, err := LoadSubDataset(dir, name)
	if err != nil {
		t.Skipf("no %s dataset: %v", name, err)
	}
	if len(ds.Records) == 0 {
		t.Skipf("no records in %s dataset", name)
	}
	return ds
}

func hasEmbeddings(ds *EvalDataset) bool {
	for _, r := range ds.Records {
		if len(r.Embedding) > 0 {
			return true
		}
	}
	return false
}

// TestRetrievalCombined runs the eval against ALL datasets merged.
func TestRetrievalCombined(t *testing.T) {
	ds := loadTestDataset(t)
	if !hasEmbeddings(ds) {
		t.Skip("no embeddings -- regenerate with gramaton-bench --embed")
	}

	t.Logf("Combined dataset: %d records, %d queries", len(ds.Records), len(ds.Queries))
	eng, nameToID := BuildEvalEngine(t, ds.Records)
	report := EvalRetrieval(t, eng, nameToID, ds.Queries, nil)

	t.Logf("=== Combined Retrieval Report ===")
	t.Logf("Mean NDCG@5:      %.3f", report.MeanNDCG5)
	t.Logf("Mean Precision@5: %.3f", report.MeanP5)
	t.Logf("MAP:              %.3f", report.MAP)

	for _, q := range report.Queries {
		t.Logf("  %-30s NDCG@5=%.3f  P@5=%.3f  top=%v",
			q.QueryName, q.NDCG5, q.Precision5, q.TopResults)
	}
}

// TestRetrievalByDataset runs the eval against each sub-dataset independently.
func TestRetrievalByDataset(t *testing.T) {
	dir := DataDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("no eval data at %s", dir)
	}

	// Root-level (personal) dataset.
	rootDS := &EvalDataset{}
	loadDatasetFiles(dir, rootDS)
	datasets := map[string]*EvalDataset{}
	if len(rootDS.Records) > 0 {
		datasets["personal"] = rootDS
	}

	for _, name := range AvailableDatasets(dir) {
		ds, err := LoadSubDataset(dir, name)
		if err != nil || len(ds.Records) == 0 {
			continue
		}
		datasets[name] = ds
	}

	t.Logf("=== Per-Dataset Retrieval Report ===")
	t.Logf("%-20s  %6s  %6s  %7s  %5s  %5s",
		"Dataset", "Recs", "Qrys", "NDCG@5", "P@5", "MAP")
	t.Logf("%-20s  %6s  %6s  %7s  %5s  %5s",
		"--------------------", "------", "------", "-------", "-----", "-----")

	for name, ds := range datasets {
		if !hasEmbeddings(ds) {
			t.Logf("%-20s  %6d  %6d  (no embeddings)", name, len(ds.Records), len(ds.Queries))
			continue
		}
		if len(ds.Queries) == 0 {
			t.Logf("%-20s  %6d  %6d  (no queries)", name, len(ds.Records), len(ds.Queries))
			continue
		}
		eng, nameToID := BuildEvalEngine(t, ds.Records)
		report := EvalRetrieval(t, eng, nameToID, ds.Queries, nil)
		t.Logf("%-20s  %6d  %6d  %7.3f  %5.3f  %5.3f",
			name, len(ds.Records), len(ds.Queries), report.MeanNDCG5, report.MeanP5, report.MAP)
	}
}

// TestWeightMatrix runs weight configs against the combined dataset.
func TestWeightMatrix(t *testing.T) {
	ds := loadTestDataset(t)
	if !hasEmbeddings(ds) {
		t.Skip("no embeddings")
	}

	eng, nameToID := BuildEvalEngine(t, ds.Records)

	defaults := config.Defaults()
	configs := []struct {
		name string
		cfg  config.ScoringConfig
	}{
		{"Current", defaults.Scoring},
		{"A_Similarity-led", config.ScoringConfig{
			WeightSimilarity: 0.50, WeightConfidence: 0.15, WeightRecency: 0.10,
			WeightFreshness: 0.10, WeightFrequency: 0.05, WeightActivation: 0.10,
			ImportanceThreshold: defaults.Scoring.ImportanceThreshold,
			ImportanceFloor:     defaults.Scoring.ImportanceFloor,
			HistoricalPenalty:   defaults.Scoring.HistoricalPenalty,
		}},
		{"B_Sim-dominant", config.ScoringConfig{
			WeightSimilarity: 0.60, WeightConfidence: 0.12, WeightRecency: 0.08,
			WeightFreshness: 0.08, WeightFrequency: 0.04, WeightActivation: 0.08,
			ImportanceThreshold: defaults.Scoring.ImportanceThreshold,
			ImportanceFloor:     defaults.Scoring.ImportanceFloor,
			HistoricalPenalty:   defaults.Scoring.HistoricalPenalty,
		}},
		{"C_Confidence-cut", config.ScoringConfig{
			WeightSimilarity: 0.45, WeightConfidence: 0.10, WeightRecency: 0.15,
			WeightFreshness: 0.15, WeightFrequency: 0.10, WeightActivation: 0.05,
			ImportanceThreshold: defaults.Scoring.ImportanceThreshold,
			ImportanceFloor:     defaults.Scoring.ImportanceFloor,
			HistoricalPenalty:   defaults.Scoring.HistoricalPenalty,
		}},
		{"D_No-frequency", config.ScoringConfig{
			WeightSimilarity: 0.40, WeightConfidence: 0.20, WeightRecency: 0.15,
			WeightFreshness: 0.15, WeightFrequency: 0.00, WeightActivation: 0.10,
			ImportanceThreshold: defaults.Scoring.ImportanceThreshold,
			ImportanceFloor:     defaults.Scoring.ImportanceFloor,
			HistoricalPenalty:   defaults.Scoring.HistoricalPenalty,
		}},
		{"E_Near-pure", config.ScoringConfig{
			WeightSimilarity: 0.70, WeightConfidence: 0.10, WeightRecency: 0.05,
			WeightFreshness: 0.05, WeightFrequency: 0.05, WeightActivation: 0.05,
			ImportanceThreshold: defaults.Scoring.ImportanceThreshold,
			ImportanceFloor:     defaults.Scoring.ImportanceFloor,
			HistoricalPenalty:   defaults.Scoring.HistoricalPenalty,
		}},
	}

	t.Logf("=== Weight Matrix (combined: %d records, %d queries) ===", len(ds.Records), len(ds.Queries))
	t.Log("")
	t.Logf("%-20s  %5s  %5s  %5s  %5s  %5s  %5s  |  %7s  %5s  %5s",
		"Config", "Sim", "Conf", "Rec", "Frsh", "Freq", "Act", "NDCG@5", "P@5", "MAP")
	t.Logf("%-20s  %5s  %5s  %5s  %5s  %5s  %5s  |  %7s  %5s  %5s",
		"--------------------", "-----", "-----", "-----", "-----", "-----", "-----", "-------", "-----", "-----")

	for _, c := range configs {
		report := EvalRetrieval(t, eng, nameToID, ds.Queries, &c.cfg)
		t.Logf("%-20s  %5.2f  %5.2f  %5.2f  %5.2f  %5.2f  %5.2f  |  %7.3f  %5.3f  %5.3f",
			c.name,
			c.cfg.WeightSimilarity, c.cfg.WeightConfidence, c.cfg.WeightRecency,
			c.cfg.WeightFreshness, c.cfg.WeightFrequency, c.cfg.WeightActivation,
			report.MeanNDCG5, report.MeanP5, report.MAP,
		)
	}

	// Per-query detail for best config (E).
	t.Log("")
	t.Log("--- E_Near-pure per-query ---")
	report := EvalRetrieval(t, eng, nameToID, ds.Queries, &configs[len(configs)-1].cfg)
	for _, q := range report.Queries {
		t.Logf("  %-30s NDCG@5=%5.3f  P@5=%5.3f  top=%v",
			q.QueryName, q.NDCG5, q.Precision5,
			fmt.Sprintf("%v", q.TopResults))
	}
}

// TestMultiConceptQueries isolates multi-concept queries to measure
// the impact of BM25 hybrid search on cross-concept retrieval.
func TestMultiConceptQueries(t *testing.T) {
	dir := DataDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("no eval data at %s", dir)
	}

	// Collect multi-concept queries from all datasets.
	var allRecords []EvalRecord
	var mcQueries []EvalQuery

	// Personal dataset.
	rootDS := &EvalDataset{}
	loadDatasetFiles(dir, rootDS)
	allRecords = append(allRecords, rootDS.Records...)
	for _, q := range rootDS.Queries {
		if len(q.Name) > 2 && q.Name[:2] == "mc" {
			mcQueries = append(mcQueries, q)
		}
	}

	// SEP dataset.
	sepDS, err := LoadSubDataset(dir, "sep")
	if err == nil && len(sepDS.Records) > 0 {
		allRecords = append(allRecords, sepDS.Records...)
		for _, q := range sepDS.Queries {
			if len(q.Name) > 2 && q.Name[:2] == "mc" {
				mcQueries = append(mcQueries, q)
			}
		}
	}

	if len(mcQueries) == 0 {
		t.Skip("no multi-concept queries found")
	}

	// Check for embeddings.
	hasEmbed := false
	for _, r := range allRecords {
		if len(r.Embedding) > 0 {
			hasEmbed = true
			break
		}
	}
	if !hasEmbed {
		t.Skip("no embeddings")
	}

	eng, nameToID := BuildEvalEngine(t, allRecords)

	t.Logf("=== Multi-Concept Query Evaluation ===")
	t.Logf("Records: %d, Multi-concept queries: %d", len(allRecords), len(mcQueries))
	t.Log("")

	report := EvalRetrieval(t, eng, nameToID, mcQueries, nil)

	t.Logf("Overall:  NDCG@5=%.3f  P@5=%.3f  MAP=%.3f",
		report.MeanNDCG5, report.MeanP5, report.MAP)
	t.Log("")
	t.Logf("%-35s  %7s  %5s  %s",
		"Query", "NDCG@5", "P@5", "Top results")
	t.Logf("%-35s  %7s  %5s  %s",
		"-----------------------------------", "-------", "-----", "---")
	for _, q := range report.Queries {
		t.Logf("%-35s  %7.3f  %5.3f  %v",
			q.QueryName, q.NDCG5, q.Precision5, q.TopResults)
	}
}

// TestCuratedRetrieval runs the same eval but with deterministic curation
// applied first. Compares curated vs uncurated to measure the impact of
// concept nodes, orphan linking, and quality repairs.
func TestCuratedRetrieval(t *testing.T) {
	ds := loadTestDataset(t)
	if !hasEmbeddings(ds) {
		t.Skip("no embeddings -- regenerate with gramaton-bench --embed")
	}

	t.Logf("=== Curated vs Uncurated Retrieval ===")
	t.Logf("Records: %d, Queries: %d", len(ds.Records), len(ds.Queries))

	// Uncurated baseline.
	engRaw, nameToIDRaw := BuildEvalEngine(t, ds.Records)
	rawReport := EvalRetrieval(t, engRaw, nameToIDRaw, ds.Queries, nil)

	// Curated.
	engCurated, nameToIDCurated := BuildCuratedEvalEngine(t, ds.Records)
	curatedReport := EvalRetrieval(t, engCurated, nameToIDCurated, ds.Queries, nil)

	t.Logf("")
	t.Logf("%-12s  %7s  %5s  %5s", "", "NDCG@5", "P@5", "MAP")
	t.Logf("%-12s  %7s  %5s  %5s", "----------", "------", "-----", "-----")
	t.Logf("%-12s  %7.3f  %5.3f  %5.3f", "Uncurated", rawReport.MeanNDCG5, rawReport.MeanP5, rawReport.MAP)
	t.Logf("%-12s  %7.3f  %5.3f  %5.3f", "Curated", curatedReport.MeanNDCG5, curatedReport.MeanP5, curatedReport.MAP)

	delta := curatedReport.MeanNDCG5 - rawReport.MeanNDCG5
	t.Logf("")
	t.Logf("Delta NDCG@5: %+.3f", delta)

	// Show queries where curated did better or worse.
	rawByName := make(map[string]RetrievalResult)
	for _, q := range rawReport.Queries {
		rawByName[q.QueryName] = q
	}

	var improved, degraded int
	for _, cq := range curatedReport.Queries {
		rq := rawByName[cq.QueryName]
		diff := cq.NDCG5 - rq.NDCG5
		if diff > 0.05 {
			improved++
			t.Logf("  IMPROVED %-30s  %.3f -> %.3f (+%.3f)", cq.QueryName, rq.NDCG5, cq.NDCG5, diff)
		} else if diff < -0.05 {
			degraded++
			t.Logf("  DEGRADED %-30s  %.3f -> %.3f (%.3f)", cq.QueryName, rq.NDCG5, cq.NDCG5, diff)
		}
	}
	t.Logf("")
	t.Logf("Improved: %d, Degraded: %d, Unchanged: %d",
		improved, degraded, len(curatedReport.Queries)-improved-degraded)
}

// TestTicketRetrieval evaluates the tickets dataset which tests
// structured/meta queries (assignee, status, priority, sprint).
func TestTicketRetrieval(t *testing.T) {
	ds := loadTestSubDataset(t, "tickets")
	if len(ds.Records) == 0 {
		t.Skip("no tickets dataset")
	}

	t.Logf("=== Ticket Retrieval Evaluation ===")
	t.Logf("Records: %d, Queries: %d", len(ds.Records), len(ds.Queries))

	eng, nameToID := BuildEvalEngine(t, ds.Records)
	report := EvalRetrieval(t, eng, nameToID, ds.Queries, nil)

	t.Logf("")
	t.Logf("Overall:  NDCG@5=%.3f  P@5=%.3f  MAP=%.3f",
		report.MeanNDCG5, report.MeanP5, report.MAP)
	t.Logf("")
	t.Logf("%-25s  %7s  %5s  %s", "Query", "NDCG@5", "P@5", "Top results")
	t.Logf("%-25s  %7s  %5s  %s", "-------------------------", "-------", "-----", "---")
	for _, q := range report.Queries {
		t.Logf("%-25s  %7.3f  %5.3f  %v",
			q.QueryName, q.NDCG5, q.Precision5, q.TopResults)
	}
}
