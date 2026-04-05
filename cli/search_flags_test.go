package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestBuildSearchBodyEmpty(t *testing.T) {
	cmd := &cobra.Command{}
	addSearchFlags(cmd)
	body := buildSearchBody(cmd, nil)

	if body["top"] != 10 {
		t.Fatalf("expected top=10, got %v", body["top"])
	}
	if _, ok := body["text"]; ok {
		t.Fatal("text should not be set without args")
	}
}

func TestBuildSearchBodyWithText(t *testing.T) {
	cmd := &cobra.Command{}
	addSearchFlags(cmd)
	body := buildSearchBody(cmd, []string{"query text"})

	if body["text"] != "query text" {
		t.Fatalf("expected 'query text', got %v", body["text"])
	}
}

func TestBuildSearchBodyStringFlags(t *testing.T) {
	cmd := &cobra.Command{}
	addSearchFlags(cmd)
	cmd.Flags().Set("temporality", "durable")
	cmd.Flags().Set("sort", "created_at")
	cmd.Flags().Set("order", "asc")
	cmd.Flags().Set("match", "RWMutex")
	cmd.Flags().Set("similar-to", "abc123")

	body := buildSearchBody(cmd, nil)

	if body["temporality"] != "durable" {
		t.Fatalf("expected durable, got %v", body["temporality"])
	}
	if body["sort"] != "created_at" {
		t.Fatalf("expected created_at, got %v", body["sort"])
	}
	if body["order"] != "asc" {
		t.Fatalf("expected asc, got %v", body["order"])
	}
	if body["match"] != "RWMutex" {
		t.Fatalf("expected RWMutex, got %v", body["match"])
	}
	if body["similar_to"] != "abc123" {
		t.Fatalf("expected abc123, got %v", body["similar_to"])
	}
}

func TestBuildSearchBodyKeywords(t *testing.T) {
	cmd := &cobra.Command{}
	addSearchFlags(cmd)
	cmd.Flags().Set("keywords", "auth,security,oauth")

	body := buildSearchBody(cmd, nil)
	kw := body["keywords"].([]string)
	if len(kw) != 3 {
		t.Fatalf("expected 3 keywords, got %d", len(kw))
	}
	if kw[0] != "auth" || kw[2] != "oauth" {
		t.Fatalf("unexpected keywords: %v", kw)
	}
}

func TestBuildSearchBodyMissing(t *testing.T) {
	cmd := &cobra.Command{}
	addSearchFlags(cmd)
	cmd.Flags().Set("missing", "temporality,confidence")

	body := buildSearchBody(cmd, nil)
	m := body["missing"].([]string)
	if len(m) != 2 {
		t.Fatalf("expected 2 missing fields, got %d", len(m))
	}
}

func TestBuildSearchBodyEdges(t *testing.T) {
	cmd := &cobra.Command{}
	addSearchFlags(cmd)
	cmd.Flags().Set("min-edges", "1")
	cmd.Flags().Set("max-edges", "5")

	body := buildSearchBody(cmd, nil)
	if body["min_edges"] != 1 {
		t.Fatalf("expected min_edges=1, got %v", body["min_edges"])
	}
	if body["max_edges"] != 5 {
		t.Fatalf("expected max_edges=5, got %v", body["max_edges"])
	}
}

func TestBuildSearchBodyEdgesSentinel(t *testing.T) {
	cmd := &cobra.Command{}
	addSearchFlags(cmd)
	// Default -1 means "not set" -- should not appear in body.
	body := buildSearchBody(cmd, nil)
	if _, ok := body["min_edges"]; ok {
		t.Fatal("min_edges should not be set at default -1")
	}
	if _, ok := body["max_edges"]; ok {
		t.Fatal("max_edges should not be set at default -1")
	}
}

func TestBuildSearchBodyRandom(t *testing.T) {
	cmd := &cobra.Command{}
	addSearchFlags(cmd)
	cmd.Flags().Set("random", "true")

	body := buildSearchBody(cmd, nil)
	if body["random"] != true {
		t.Fatal("expected random=true")
	}
}

func TestAddSearchFlagsNoPanic(t *testing.T) {
	cmd := &cobra.Command{}
	addSearchFlags(cmd)
	// Just verify it doesn't panic.
	if cmd.Flags().Lookup("temporality") == nil {
		t.Fatal("temporality flag should be registered")
	}
	if cmd.Flags().Lookup("sort") == nil {
		t.Fatal("sort flag should be registered")
	}
}
