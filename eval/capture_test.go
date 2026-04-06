package eval

import (
	"os"
	"testing"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/llm"
)

func TestCaptureEvalMock(t *testing.T) {
	ds := loadTestDataset(t)
	if len(ds.Capture) == 0 {
		t.Skip("no capture samples in dataset")
	}

	classifier := &MockClassifier{}
	report := EvalCapture(t, classifier, ds.Capture)

	t.Logf("=== Capture Evaluation Report (Mock) ===")
	t.Logf("Samples evaluated:      %d", len(report.Samples))
	t.Logf("Temporality accuracy:   %.1f%%", report.TemporalityAccuracy*100)
	t.Logf("KnowledgeType accuracy: %.1f%%", report.KnowledgeTypeAccuracy*100)
	t.Logf("Epistemic accuracy:     %.1f%%", report.EpistemicAccuracy*100)
	t.Logf("Confidence MAE:         %.3f", report.ConfidenceMAE)
	t.Logf("Mean keyword Jaccard:   %.3f", report.MeanKeywordJaccard)

	// Framework validation: metrics in valid ranges.
	if report.TemporalityAccuracy < 0 || report.TemporalityAccuracy > 1 {
		t.Errorf("temporality accuracy out of range: %.3f", report.TemporalityAccuracy)
	}
}

func TestCaptureEvalWithLLM(t *testing.T) {
	if os.Getenv("GRAMATON_EVAL_LLM") == "" {
		t.Skip("set GRAMATON_EVAL_LLM=1 to run LLM capture evaluation")
	}

	ds := loadTestDataset(t)
	if len(ds.Capture) == 0 {
		t.Skip("no capture samples in dataset")
	}

	cfg := config.Defaults()
	cfg.LLM.Provider = "anthropic"
	llmProv, err := llm.New(cfg.LLM)
	if err != nil {
		t.Fatalf("create LLM provider: %v", err)
	}
	if llmProv == nil {
		t.Skip("no LLM provider configured")
	}

	classifier := &LLMClassifier{LLM: llmProv}
	report := EvalCapture(t, classifier, ds.Capture)

	t.Logf("=== Capture Evaluation Report (LLM) ===")
	t.Logf("Samples evaluated:      %d", len(report.Samples))
	t.Logf("Temporality accuracy:   %.1f%%", report.TemporalityAccuracy*100)
	t.Logf("KnowledgeType accuracy: %.1f%%", report.KnowledgeTypeAccuracy*100)
	t.Logf("Epistemic accuracy:     %.1f%%", report.EpistemicAccuracy*100)
	t.Logf("Confidence MAE:         %.3f", report.ConfidenceMAE)
	t.Logf("Mean keyword Jaccard:   %.3f", report.MeanKeywordJaccard)

	for _, s := range report.Samples {
		t.Logf("  %-25s temp=%-5v kt=%-5v ep=%-5v conf_err=%.2f kw_j=%.2f",
			s.Name,
			boolMark(s.TemporalityCorrect),
			boolMark(s.KnowledgeTypeCorrect),
			boolMark(s.EpistemicCorrect),
			s.ConfidenceError,
			s.KeywordJaccard,
		)
	}

	if report.TemporalityAccuracy < 0.7 {
		t.Errorf("LLM temporality accuracy = %.1f%%, want >= 70%%", report.TemporalityAccuracy*100)
	}
	if report.KnowledgeTypeAccuracy < 0.6 {
		t.Errorf("LLM knowledge_type accuracy = %.1f%%, want >= 60%%", report.KnowledgeTypeAccuracy*100)
	}
	if report.ConfidenceMAE > 0.2 {
		t.Errorf("LLM confidence MAE = %.3f, want <= 0.2", report.ConfidenceMAE)
	}
}

func boolMark(b bool) string {
	if b {
		return "ok"
	}
	return "MISS"
}
