package eval

import (
	"context"
	"testing"
)

// CaptureResult holds per-sample evaluation results.
type CaptureResult struct {
	Name                 string
	TemporalityCorrect   bool
	KnowledgeTypeCorrect bool
	EpistemicCorrect     bool
	ConfidenceError      float64
	KeywordJaccard       float64
	Actual               *Classification
}

// CaptureReport holds aggregate capture evaluation metrics.
type CaptureReport struct {
	Samples               []CaptureResult
	TemporalityAccuracy   float64
	KnowledgeTypeAccuracy float64
	EpistemicAccuracy     float64
	ConfidenceMAE         float64
	MeanKeywordJaccard    float64
	ConfCalibration       []BucketResult
}

// EvalCapture runs capture ground truth samples through the given
// classifier and computes accuracy metrics.
func EvalCapture(t *testing.T, classifier Classifier, samples []CaptureGroundTruth) *CaptureReport {
	t.Helper()

	ctx := context.Background()

	var report CaptureReport
	var expectedConf, actualConf []float64
	var expectedTemp, actualTemp []string
	var expectedKT, actualKT []string
	var expectedES, actualES []string
	var tempAlts, ktAlts, esAlts [][]string

	for _, s := range samples {
		cls, err := classifier.Classify(ctx, s.Content)
		if err != nil {
			t.Logf("  SKIP %s: classify error: %v", s.Name, err)
			continue
		}

		tempMatch := s.Temporality == cls.Temporality || contains(s.AltTemporality, cls.Temporality)
		ktMatch := s.KnowledgeType == cls.KnowledgeType || contains(s.AltKnowledgeType, cls.KnowledgeType)
		esMatch := s.EpistemicStatus == cls.EpistemicStatus || contains(s.AltEpistemicStatus, cls.EpistemicStatus)
		confErr := abs(s.Confidence - cls.Confidence)
		kwJaccard := JaccardSimilarity(s.Keywords, cls.Keywords)

		result := CaptureResult{
			Name:                 s.Name,
			TemporalityCorrect:   tempMatch,
			KnowledgeTypeCorrect: ktMatch,
			EpistemicCorrect:     esMatch,
			ConfidenceError:      confErr,
			KeywordJaccard:       kwJaccard,
			Actual:               cls,
		}
		report.Samples = append(report.Samples, result)

		expectedConf = append(expectedConf, s.Confidence)
		actualConf = append(actualConf, cls.Confidence)
		expectedTemp = append(expectedTemp, s.Temporality)
		actualTemp = append(actualTemp, cls.Temporality)
		expectedKT = append(expectedKT, s.KnowledgeType)
		actualKT = append(actualKT, cls.KnowledgeType)
		expectedES = append(expectedES, s.EpistemicStatus)
		actualES = append(actualES, cls.EpistemicStatus)
		tempAlts = append(tempAlts, s.AltTemporality)
		ktAlts = append(ktAlts, s.AltKnowledgeType)
		esAlts = append(esAlts, s.AltEpistemicStatus)
	}

	if len(report.Samples) == 0 {
		return &report
	}

	_, _, report.TemporalityAccuracy = EnumAccuracyWithAlts(expectedTemp, actualTemp, tempAlts)
	_, _, report.KnowledgeTypeAccuracy = EnumAccuracyWithAlts(expectedKT, actualKT, ktAlts)
	_, _, report.EpistemicAccuracy = EnumAccuracyWithAlts(expectedES, actualES, esAlts)
	report.ConfidenceMAE = ConfidenceMAE(expectedConf, actualConf)
	report.ConfCalibration = ConfidenceBucketCalibration(expectedConf, actualConf)

	var sumJaccard float64
	for _, r := range report.Samples {
		sumJaccard += r.KeywordJaccard
	}
	report.MeanKeywordJaccard = sumJaccard / float64(len(report.Samples))

	return &report
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
