package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultDataDir returns the default eval data directory.
func DefaultDataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gramaton", "eval")
}

// DataDir returns the eval data directory, checking GRAMATON_EVAL_DIR
// env var first, then falling back to DefaultDataDir.
func DataDir() string {
	if dir := os.Getenv("GRAMATON_EVAL_DIR"); dir != "" {
		return dir
	}
	return DefaultDataDir()
}

// EvalRecord is a record in the eval dataset with content, metadata,
// and an optional pre-computed embedding.
type EvalRecord struct {
	Name            string    `json:"name"`
	Content         string    `json:"content"`
	Temporality     string    `json:"temporality,omitempty"`
	Confidence      *float64  `json:"confidence,omitempty"`
	KnowledgeType   string    `json:"knowledge_type,omitempty"`
	EpistemicStatus string    `json:"epistemic_status,omitempty"`
	Importance      *float64  `json:"importance,omitempty"`
	Keywords        []string  `json:"keywords,omitempty"`
	SummaryShort    string    `json:"summary_short,omitempty"`
	CreatedDaysAgo  int       `json:"created_days_ago,omitempty"`
	AccessCount     int64     `json:"access_count,omitempty"`
	AccessedDaysAgo int       `json:"accessed_days_ago,omitempty"`
	ValidUntilDaysAgo int    `json:"valid_until_days_ago,omitempty"` // negative = future
	Resolution      string         `json:"resolution,omitempty"`
	Pending         bool           `json:"pending,omitempty"`
	Embedding       []float32      `json:"embedding,omitempty"`
	Meta            map[string]any `json:"meta,omitempty"`
}

// EvalQuery is a retrieval evaluation query with relevance judgments.
type EvalQuery struct {
	Name      string                    `json:"name"`
	Text      string                    `json:"text"`
	Embedding []float32                 `json:"embedding,omitempty"`
	Judgments map[string]RelevanceGrade `json:"judgments"`
	// Filter fields (optional).
	KnowledgeType   string   `json:"knowledge_type,omitempty"`
	EpistemicStatus string   `json:"epistemic_status,omitempty"`
	Temporality     string   `json:"temporality,omitempty"`
	Resolution      string   `json:"resolution,omitempty"`
	ConfidenceMin   *float64          `json:"confidence_min,omitempty"`
	SinceDaysAgo    int               `json:"since_days_ago,omitempty"`
	Meta            map[string]string `json:"meta,omitempty"`
}

// EvalDataset is the complete eval dataset loaded from disk.
type EvalDataset struct {
	Records []EvalRecord         `json:"records"`
	Queries []EvalQuery          `json:"queries"`
	Capture []CaptureGroundTruth `json:"capture"`
}

// LoadDataset loads the eval dataset from the data directory.
// Loads the root-level records.json, queries.json, capture.json,
// then scans subdirectories for additional dataset files and merges
// them all together.
func LoadDataset(dir string) (*EvalDataset, error) {
	var ds EvalDataset

	// Load root-level files (personal dataset).
	if err := loadDatasetFiles(dir, &ds); err != nil {
		return nil, err
	}

	// Scan subdirectories for additional datasets.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subDir := filepath.Join(dir, e.Name())
		if err := loadDatasetFiles(subDir, &ds); err != nil {
			// Non-fatal: log and continue.
			fmt.Fprintf(os.Stderr, "eval: skip %s: %v\n", e.Name(), err)
		}
	}

	return &ds, nil
}

// LoadSubDataset loads a single named sub-dataset (e.g. "sep", "news").
func LoadSubDataset(dir string, name string) (*EvalDataset, error) {
	var ds EvalDataset
	subDir := filepath.Join(dir, name)
	if err := loadDatasetFiles(subDir, &ds); err != nil {
		return nil, fmt.Errorf("load %s: %w", name, err)
	}
	return &ds, nil
}

// AvailableDatasets returns the names of sub-datasets in the data dir.
func AvailableDatasets(dir string) []string {
	var names []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		recPath := filepath.Join(dir, e.Name(), "records.json")
		if _, err := os.Stat(recPath); err == nil {
			names = append(names, e.Name())
		}
	}
	return names
}

func loadDatasetFiles(dir string, ds *EvalDataset) error {
	recPath := filepath.Join(dir, "records.json")
	if _, err := os.Stat(recPath); err == nil {
		records, err := loadJSON[[]EvalRecord](recPath)
		if err != nil {
			return fmt.Errorf("records: %w", err)
		}
		ds.Records = append(ds.Records, records...)
	}

	queryPath := filepath.Join(dir, "queries.json")
	if _, err := os.Stat(queryPath); err == nil {
		queries, err := loadJSON[[]EvalQuery](queryPath)
		if err != nil {
			return fmt.Errorf("queries: %w", err)
		}
		ds.Queries = append(ds.Queries, queries...)
	}

	capturePath := filepath.Join(dir, "capture.json")
	if _, err := os.Stat(capturePath); err == nil {
		capture, err := loadJSON[[]CaptureGroundTruth](capturePath)
		if err != nil {
			return fmt.Errorf("capture: %w", err)
		}
		ds.Capture = append(ds.Capture, capture...)
	}

	return nil
}

func loadJSON[T any](path string) (T, error) {
	var zero T
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return v, nil
}
