package eval

import (
	"context"
	"testing"
	"time"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/core"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/search"
)

// RetrievalResult holds metrics for a single query.
type RetrievalResult struct {
	QueryName  string
	NDCG5      float64
	NDCG10     float64
	Precision5 float64
	AP         float64
	TopResults []string // record names of top results
}

// RetrievalReport holds aggregate metrics across all queries.
type RetrievalReport struct {
	Queries   []RetrievalResult
	MeanNDCG5 float64
	MeanP5    float64
	MAP       float64
}

// BuildEvalEngine creates an engine from eval records. Returns a map
// from record name to record ID for result interpretation.
func BuildEvalEngine(t *testing.T, records []EvalRecord) (*core.Engine, map[string]string) {
	t.Helper()

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir + "/data"
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	config.Save(cfg, dir+"/config.yaml")

	eng, err := core.LoadEngine(dir)
	if err != nil {
		t.Fatalf("load engine: %v", err)
	}

	now := time.Now().UTC()
	nameToID := make(map[string]string)

	eng.Lock()
	for _, rec := range records {
		props := graph.Properties{
			"content_full":     graph.StringProperty(rec.Content),
			"created_at":       graph.TimestampProperty(now.Add(-time.Duration(rec.CreatedDaysAgo) * 24 * time.Hour)),
			"access_count":     graph.Int64Property(rec.AccessCount),
		}

		if rec.Pending {
			props["processing_status"] = graph.StringProperty("captured")
		} else {
			props["processing_status"] = graph.StringProperty("processed")
		}

		if rec.Temporality != "" {
			props["temporality"] = graph.StringProperty(rec.Temporality)
		}
		if rec.Confidence != nil {
			props["confidence"] = graph.Float64Property(*rec.Confidence)
		}
		if rec.KnowledgeType != "" {
			props["knowledge_type"] = graph.StringProperty(rec.KnowledgeType)
		}
		if rec.EpistemicStatus != "" {
			props["epistemic_status"] = graph.StringProperty(rec.EpistemicStatus)
		}
		if rec.Importance != nil {
			props["importance"] = graph.Float64Property(*rec.Importance)
		}
		if len(rec.Keywords) > 0 {
			props["content_keywords"] = graph.StringListProperty(rec.Keywords)
		}
		if rec.SummaryShort != "" {
			props["content_short"] = graph.StringProperty(rec.SummaryShort)
		}
		if rec.AccessedDaysAgo > 0 {
			props["last_accessed"] = graph.TimestampProperty(now.Add(-time.Duration(rec.AccessedDaysAgo) * 24 * time.Hour))
		}
		if rec.ValidUntilDaysAgo != 0 {
			props["valid_until"] = graph.TimestampProperty(now.Add(-time.Duration(rec.ValidUntilDaysAgo) * 24 * time.Hour))
		}
		if rec.Resolution != "" {
			props["resolution"] = graph.StringProperty(rec.Resolution)
			props["resolved_at"] = graph.TimestampProperty(now.Add(-time.Duration(rec.ValidUntilDaysAgo) * 24 * time.Hour))
		}

		n := eng.Graph().AddNode(props)
		var vec []float32
		if len(rec.Embedding) > 0 {
			vec = rec.Embedding
		}
		eng.IndexNode(n.ID, rec.Content, vec)

		nameToID[rec.Name] = n.ID
	}
	eng.Save("eval: load records")
	eng.Unlock()

	return eng, nameToID
}

// EvalRetrieval runs queries against an engine and computes ranking
// metrics. If cfgOverride is non-nil, uses custom scoring weights.
func EvalRetrieval(t *testing.T, eng *core.Engine, nameToID map[string]string, queries []EvalQuery, cfgOverride *config.ScoringConfig) *RetrievalReport {
	t.Helper()

	// Build reverse map: ID -> record name.
	idToName := make(map[string]string, len(nameToID))
	for name, id := range nameToID {
		idToName[id] = name
	}

	// Build searcher.
	cfg := eng.Config()
	if cfgOverride != nil {
		cfg.Scoring = *cfgOverride
	}
	searcher := search.New(eng.Graph(), eng.PropIdx(), eng.VecIdx(), eng.BM25Idx(), nil, cfg)

	now := time.Now().UTC()
	var report RetrievalReport
	var allRelevances [][]int

	for _, q := range queries {
		if q.Embedding == nil && q.Text != "" {
			continue // skip queries without embeddings
		}

		sq := search.Query{
			Text:            q.Text,
			Top:             10,
			KnowledgeType:   q.KnowledgeType,
			EpistemicStatus: q.EpistemicStatus,
			Temporality:     q.Temporality,
			Resolution:      q.Resolution,
			ConfidenceMin:   q.ConfidenceMin,
		}
		if q.SinceDaysAgo > 0 {
			since := now.Add(-time.Duration(q.SinceDaysAgo) * 24 * time.Hour)
			sq.Since = &since
		}

		eng.RLock()
		results, err := searcher.ExecuteWithVector(context.Background(), sq, q.Embedding)
		eng.RUnlock()

		if err != nil {
			t.Logf("  SKIP %s: %v", q.Name, err)
			continue
		}

		relevances := make([]int, len(results))
		topNames := make([]string, 0, min(5, len(results)))
		for i, r := range results {
			name := idToName[r.ID]
			relevances[i] = int(q.Judgments[name])
			if i < 5 {
				topNames = append(topNames, name)
			}
		}

		var idealGrades []int
		for _, g := range q.Judgments {
			idealGrades = append(idealGrades, int(g))
		}

		result := RetrievalResult{
			QueryName:  q.Name,
			NDCG5:      NDCG(relevances, idealGrades, 5),
			NDCG10:     NDCG(relevances, idealGrades, 10),
			Precision5: PrecisionAtK(relevances, 5, int(Relevant)),
			AP:         AveragePrecision(relevances, int(Relevant)),
			TopResults: topNames,
		}
		report.Queries = append(report.Queries, result)
		allRelevances = append(allRelevances, relevances)
	}

	if len(report.Queries) > 0 {
		var sumNDCG5, sumP5 float64
		for _, r := range report.Queries {
			sumNDCG5 += r.NDCG5
			sumP5 += r.Precision5
		}
		report.MeanNDCG5 = sumNDCG5 / float64(len(report.Queries))
		report.MeanP5 = sumP5 / float64(len(report.Queries))
		report.MAP = MeanAveragePrecision(allRelevances, int(Relevant))
	}

	return &report
}
