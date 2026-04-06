// Package eval provides capture quality and retrieval quality evaluation
// for the Gramaton knowledge store.
package eval

import (
	"math"
	"strings"
)

// NDCG computes Normalized Discounted Cumulative Gain at position k.
// relevances contains the relevance grades in ranked order (as returned
// by the system). idealRelevances is the complete set of all relevance
// grades (will be sorted internally to compute the ideal ranking).
func NDCG(relevances []int, idealRelevances []int, k int) float64 {
	dcg := dcgAtK(relevances, k)
	// Build ideal ordering: sort descending.
	sorted := make([]int, len(idealRelevances))
	copy(sorted, idealRelevances)
	sortDescending(sorted)
	idcg := dcgAtK(sorted, k)
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

func dcgAtK(relevances []int, k int) float64 {
	var sum float64
	for i := 0; i < k && i < len(relevances); i++ {
		sum += float64(relevances[i]) / math.Log2(float64(i+2)) // i+2 because log2(1)=0
	}
	return sum
}

func sortDescending(s []int) {
	// Simple insertion sort -- slices are small.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] > s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// PrecisionAtK computes Precision@k. A result is "relevant" if its
// relevance grade >= threshold.
func PrecisionAtK(relevances []int, k int, threshold int) float64 {
	if k <= 0 {
		return 0
	}
	relevant := 0
	n := k
	if n > len(relevances) {
		n = len(relevances)
	}
	for i := 0; i < n; i++ {
		if relevances[i] >= threshold {
			relevant++
		}
	}
	return float64(relevant) / float64(k)
}

// AveragePrecision computes AP for a single ranked list. A result is
// "relevant" if its grade >= threshold.
func AveragePrecision(relevances []int, threshold int) float64 {
	var sum float64
	relevant := 0
	for i, r := range relevances {
		if r >= threshold {
			relevant++
			sum += float64(relevant) / float64(i+1)
		}
	}
	if relevant == 0 {
		return 0
	}
	return sum / float64(relevant)
}

// MeanAveragePrecision computes MAP across multiple queries.
func MeanAveragePrecision(queryRelevances [][]int, threshold int) float64 {
	if len(queryRelevances) == 0 {
		return 0
	}
	var sum float64
	for _, rels := range queryRelevances {
		sum += AveragePrecision(rels, threshold)
	}
	return sum / float64(len(queryRelevances))
}

// JaccardSimilarity computes |A intersect B| / |A union B| for two
// string sets. Case-insensitive comparison.
func JaccardSimilarity(expected, actual []string) float64 {
	if len(expected) == 0 && len(actual) == 0 {
		return 1.0
	}
	a := make(map[string]bool, len(expected))
	for _, s := range expected {
		a[strings.ToLower(s)] = true
	}
	b := make(map[string]bool, len(actual))
	for _, s := range actual {
		b[strings.ToLower(s)] = true
	}

	intersection := 0
	for k := range a {
		if b[k] {
			intersection++
		}
	}

	union := len(a)
	for k := range b {
		if !a[k] {
			union++
		}
	}

	if union == 0 {
		return 1.0
	}
	return float64(intersection) / float64(union)
}

// EnumAccuracy computes exact-match accuracy for enum field
// classifications. Returns (matches, total, accuracy).
func EnumAccuracy(expected, actual []string) (int, int, float64) {
	if len(expected) != len(actual) {
		n := len(expected)
		if len(actual) < n {
			n = len(actual)
		}
		matches := 0
		for i := 0; i < n; i++ {
			if expected[i] == actual[i] {
				matches++
			}
		}
		total := len(expected)
		if total == 0 {
			return 0, 0, 0
		}
		return matches, total, float64(matches) / float64(total)
	}
	matches := 0
	for i := range expected {
		if expected[i] == actual[i] {
			matches++
		}
	}
	if len(expected) == 0 {
		return 0, 0, 0
	}
	return matches, len(expected), float64(matches) / float64(len(expected))
}

// EnumAccuracyWithAlts is like EnumAccuracy but allows alternative
// acceptable values. alts[i] is a list of acceptable alternatives for
// position i (in addition to expected[i]).
func EnumAccuracyWithAlts(expected, actual []string, alts [][]string) (int, int, float64) {
	n := len(expected)
	if len(actual) < n {
		n = len(actual)
	}
	matches := 0
	for i := 0; i < n; i++ {
		if expected[i] == actual[i] {
			matches++
			continue
		}
		if i < len(alts) {
			for _, alt := range alts[i] {
				if alt == actual[i] {
					matches++
					break
				}
			}
		}
	}
	total := len(expected)
	if total == 0 {
		return 0, 0, 0
	}
	return matches, total, float64(matches) / float64(total)
}

// ConfidenceMAE computes the mean absolute error between expected and
// actual confidence values.
func ConfidenceMAE(expected, actual []float64) float64 {
	if len(expected) == 0 {
		return 0
	}
	n := len(expected)
	if len(actual) < n {
		n = len(actual)
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += math.Abs(expected[i] - actual[i])
	}
	return sum / float64(n)
}

// BucketResult holds calibration data for a confidence bucket.
type BucketResult struct {
	Bucket  string
	Count   int
	MeanErr float64
}

// ConfidenceBucketCalibration bins confidence values into 5 buckets
// (0-0.2, 0.2-0.4, 0.4-0.6, 0.6-0.8, 0.8-1.0) and computes per-bucket
// mean absolute error.
func ConfidenceBucketCalibration(expected, actual []float64) []BucketResult {
	type bucket struct {
		sum   float64
		count int
	}
	buckets := [5]bucket{}
	labels := [5]string{"0.0-0.2", "0.2-0.4", "0.4-0.6", "0.6-0.8", "0.8-1.0"}

	n := len(expected)
	if len(actual) < n {
		n = len(actual)
	}
	for i := 0; i < n; i++ {
		idx := int(expected[i] * 5)
		if idx >= 5 {
			idx = 4
		}
		if idx < 0 {
			idx = 0
		}
		buckets[idx].sum += math.Abs(expected[i] - actual[i])
		buckets[idx].count++
	}

	results := make([]BucketResult, 5)
	for i, b := range buckets {
		results[i] = BucketResult{Bucket: labels[i], Count: b.count}
		if b.count > 0 {
			results[i].MeanErr = b.sum / float64(b.count)
		}
	}
	return results
}
