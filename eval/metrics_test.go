package eval

import (
	"math"
	"testing"
)

func approx(a, b float64) bool {
	return math.Abs(a-b) < 0.001
}

func TestNDCG_PerfectRanking(t *testing.T) {
	// Perfect ranking: grades already in ideal order.
	relevances := []int{3, 2, 1, 0}
	ideal := []int{3, 2, 1, 0}
	got := NDCG(relevances, ideal, 4)
	if !approx(got, 1.0) {
		t.Errorf("perfect ranking: NDCG = %.3f, want 1.0", got)
	}
}

func TestNDCG_ReversedRanking(t *testing.T) {
	relevances := []int{0, 1, 2, 3}
	ideal := []int{3, 2, 1, 0}
	got := NDCG(relevances, ideal, 4)
	if got >= 1.0 {
		t.Errorf("reversed ranking: NDCG = %.3f, want < 1.0", got)
	}
	if got <= 0 {
		t.Errorf("reversed ranking: NDCG = %.3f, want > 0", got)
	}
}

func TestNDCG_AllIrrelevant(t *testing.T) {
	relevances := []int{0, 0, 0}
	ideal := []int{0, 0, 0}
	got := NDCG(relevances, ideal, 3)
	if got != 0 {
		t.Errorf("all irrelevant: NDCG = %.3f, want 0", got)
	}
}

func TestNDCG_AtK(t *testing.T) {
	// Only look at top 2: both highly relevant.
	relevances := []int{3, 3, 0, 0}
	ideal := []int{3, 3, 0, 0}
	got := NDCG(relevances, ideal, 2)
	if !approx(got, 1.0) {
		t.Errorf("NDCG@2 = %.3f, want 1.0", got)
	}
}

func TestPrecisionAtK(t *testing.T) {
	relevances := []int{3, 0, 2, 0, 1}

	// Threshold 2: positions 0 and 2 are relevant.
	p5 := PrecisionAtK(relevances, 5, 2)
	if !approx(p5, 0.4) {
		t.Errorf("P@5(threshold=2) = %.3f, want 0.4", p5)
	}

	p1 := PrecisionAtK(relevances, 1, 2)
	if !approx(p1, 1.0) {
		t.Errorf("P@1(threshold=2) = %.3f, want 1.0", p1)
	}

	// Empty.
	p0 := PrecisionAtK(relevances, 0, 2)
	if p0 != 0 {
		t.Errorf("P@0 = %.3f, want 0", p0)
	}
}

func TestAveragePrecision(t *testing.T) {
	// Relevant at positions 0, 2 (threshold=2).
	relevances := []int{3, 0, 2, 0}
	ap := AveragePrecision(relevances, 2)
	// AP = (1/1 + 2/3) / 2 = 0.833...
	if !approx(ap, 0.833) {
		t.Errorf("AP = %.3f, want 0.833", ap)
	}
}

func TestAveragePrecision_AllRelevant(t *testing.T) {
	relevances := []int{3, 3, 3}
	ap := AveragePrecision(relevances, 2)
	if !approx(ap, 1.0) {
		t.Errorf("AP (all relevant) = %.3f, want 1.0", ap)
	}
}

func TestAveragePrecision_NoneRelevant(t *testing.T) {
	relevances := []int{0, 1, 0}
	ap := AveragePrecision(relevances, 2)
	if ap != 0 {
		t.Errorf("AP (none relevant) = %.3f, want 0", ap)
	}
}

func TestMeanAveragePrecision(t *testing.T) {
	queries := [][]int{
		{3, 0, 2, 0}, // AP = 0.833
		{3, 3, 0, 0}, // AP = 1.0
	}
	m := MeanAveragePrecision(queries, 2)
	if !approx(m, 0.917) {
		t.Errorf("MAP = %.3f, want 0.917", m)
	}
}

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		expected []string
		actual   []string
		want     float64
	}{
		{"identical", []string{"a", "b", "c"}, []string{"a", "b", "c"}, 1.0},
		{"disjoint", []string{"a", "b"}, []string{"c", "d"}, 0.0},
		{"partial", []string{"a", "b", "c"}, []string{"b", "c", "d"}, 0.5},
		{"case_insensitive", []string{"Auth", "API"}, []string{"auth", "api"}, 1.0},
		{"both_empty", nil, nil, 1.0},
		{"one_empty", []string{"a"}, nil, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := JaccardSimilarity(tt.expected, tt.actual)
			if !approx(got, tt.want) {
				t.Errorf("Jaccard = %.3f, want %.3f", got, tt.want)
			}
		})
	}
}

func TestEnumAccuracy(t *testing.T) {
	expected := []string{"durable", "immutable", "temporal"}
	actual := []string{"durable", "durable", "temporal"}
	m, n, acc := EnumAccuracy(expected, actual)
	if m != 2 || n != 3 {
		t.Errorf("matches=%d, total=%d, want 2, 3", m, n)
	}
	if !approx(acc, 0.667) {
		t.Errorf("accuracy = %.3f, want 0.667", acc)
	}
}

func TestEnumAccuracyWithAlts(t *testing.T) {
	expected := []string{"immutable", "temporal"}
	actual := []string{"durable", "ephemeral"}
	alts := [][]string{
		{"durable"},    // durable is acceptable alt for immutable
		{"temporal"},   // temporal is acceptable alt for ephemeral -- but actual is ephemeral, not temporal
	}
	m, n, _ := EnumAccuracyWithAlts(expected, actual, alts)
	if m != 1 || n != 2 {
		t.Errorf("matches=%d, total=%d, want 1, 2", m, n)
	}
}

func TestConfidenceMAE(t *testing.T) {
	expected := []float64{0.9, 0.5, 0.8}
	actual := []float64{0.9, 0.6, 0.7}
	mae := ConfidenceMAE(expected, actual)
	// (0 + 0.1 + 0.1) / 3 = 0.0667
	if !approx(mae, 0.067) {
		t.Errorf("MAE = %.3f, want 0.067", mae)
	}
}

func TestConfidenceMAE_Perfect(t *testing.T) {
	expected := []float64{0.5, 0.5}
	mae := ConfidenceMAE(expected, expected)
	if mae != 0 {
		t.Errorf("perfect MAE = %.3f, want 0", mae)
	}
}

func TestConfidenceBucketCalibration(t *testing.T) {
	expected := []float64{0.1, 0.3, 0.5, 0.7, 0.95}
	actual := []float64{0.2, 0.3, 0.6, 0.8, 0.9}
	buckets := ConfidenceBucketCalibration(expected, actual)
	if len(buckets) != 5 {
		t.Fatalf("got %d buckets, want 5", len(buckets))
	}
	// Bucket 0 (0.0-0.2): 0.1 -> error 0.1
	if buckets[0].Count != 1 {
		t.Errorf("bucket[0] count = %d, want 1", buckets[0].Count)
	}
	if !approx(buckets[0].MeanErr, 0.1) {
		t.Errorf("bucket[0] err = %.3f, want 0.1", buckets[0].MeanErr)
	}
}
