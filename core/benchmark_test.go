package core

import (
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// TestScaleMeasurement creates a synthetic dataset and measures key
// metrics: disk usage, memory (RSS proxy via HeapInuse), search
// latency, capture latency, and startup time.
//
// Default: 100 records (fast enough for CI on every platform under
// race detector). The default is sized to catch structural
// regressions (engine load, reload count match, no errors) -- the
// `t.Logf` perf numbers it prints are noisy at this scale, so use
// GRAMATON_SCALE=10000 or GRAMATON_SCALE=100000 for real
// measurements (the documented "is the system fast enough" runs).
func TestScaleMeasurement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scale measurement in -short mode")
	}

	scale := 100
	if s := os.Getenv("GRAMATON_SCALE"); s != "" {
		fmt.Sscanf(s, "%d", &scale)
	}

	t.Logf("=== Scale measurement: %d records ===", scale)

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}

	// --- Measure startup time ---
	startupStart := time.Now()
	eng, err := LoadEngineWithOptions(dir, nil, []EngineOption{
		WithVectorIndex(index.NewFlatIndex()),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	startupDur := time.Since(startupStart)
	t.Logf("Startup time (empty): %v", startupDur)

	// --- Measure capture latency ---
	words := []string{
		"knowledge", "engineering", "distributed", "architecture",
		"migration", "database", "protocol", "consensus", "streaming",
		"pipeline", "deployment", "monitoring", "observability",
		"resilience", "scalability", "throughput", "latency",
		"configuration", "automation", "infrastructure",
	}

	captureStart := time.Now()
	eng.Lock()
	for i := 0; i < scale; i++ {
		// Generate realistic content (~200-500 chars).
		content := fmt.Sprintf("Record %d: ", i)
		for j := 0; j < 20+rand.Intn(30); j++ {
			content += words[rand.Intn(len(words))] + " "
		}

		props := graph.Properties{
			"content_full":      graph.StringProperty(content),
			"processing_status": graph.StringProperty("processed"),
			"temporality":       graph.StringProperty([]string{"durable", "temporal", "ephemeral", "immutable"}[rand.Intn(4)]),
			"confidence":        graph.Float64Property(0.5 + rand.Float64()*0.5),
			"knowledge_type":    graph.StringProperty([]string{"semantic", "episodic", "procedural", "conceptual"}[rand.Intn(4)]),
			"epistemic_status":  graph.StringProperty("well_established"),
			"created_at":        graph.TimestampProperty(time.Now().UTC().Add(-time.Duration(rand.Intn(365*24)) * time.Hour)),
			"access_count":      graph.Int64Property(int64(rand.Intn(20))),
			"content_keywords":  graph.StringListProperty([]string{words[rand.Intn(len(words))], words[rand.Intn(len(words))]}),
		}

		n := eng.Graph().AddNode(props)
		for k, v := range n.Properties {
			eng.PropIdx().Add(n.ID, k, v)
		}
		eng.BM25Full().Add(n.ID, content)

		// Add a small float32 vector (4-dim for FlatIndex).
		vec := []float32{rand.Float32(), rand.Float32(), rand.Float32(), rand.Float32()}
		eng.VecIdx().Add(n.ID, vec)

		// Add some edges (~20% of nodes get an edge to a random prior node).
		if i > 0 && rand.Float32() < 0.2 {
			allIDs := eng.Graph().AllNodeIDs()
			target := allIDs[rand.Intn(len(allIDs))]
			if target != n.ID {
				eng.Graph().AddEdge(n.ID, target, "related_to", 0.5+rand.Float64()*0.5, nil)
			}
		}

		// Save periodically to simulate real usage.
		if (i+1)%500 == 0 {
			eng.Save(fmt.Sprintf("batch-%d", i+1))
		}
	}
	eng.Save("final")
	eng.Unlock()
	captureDur := time.Since(captureStart)
	perCapture := captureDur / time.Duration(scale)
	t.Logf("Capture: %d records in %v (%.2f ms/record)", scale, captureDur, float64(perCapture.Microseconds())/1000)

	// --- Measure memory ---
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("Heap in use: %.1f MB", float64(mem.HeapInuse)/(1024*1024))
	t.Logf("Heap alloc: %.1f MB", float64(mem.HeapAlloc)/(1024*1024))
	t.Logf("Sys: %.1f MB", float64(mem.Sys)/(1024*1024))

	// --- Measure disk ---
	diskUsage := dirSize(t, dir)
	t.Logf("Disk usage: %.1f MB", float64(diskUsage)/(1024*1024))
	t.Logf("Per-node disk: %.0f bytes", float64(diskUsage)/float64(scale))

	// --- Measure search latency ---
	eng.RLock()
	searchStart := time.Now()
	iterations := 100
	for i := 0; i < iterations; i++ {
		query := words[rand.Intn(len(words))]
		tokens := index.Tokenize(query)
		eng.BM25Full().Search(tokens, 10, nil)
	}
	eng.RUnlock()
	searchDur := time.Since(searchStart)
	perSearch := searchDur / time.Duration(iterations)
	t.Logf("BM25 search: %d queries in %v (%.2f ms/query)", iterations, searchDur, float64(perSearch.Microseconds())/1000)

	// --- Measure vector search ---
	eng.RLock()
	vecStart := time.Now()
	for i := 0; i < iterations; i++ {
		q := []float32{rand.Float32(), rand.Float32(), rand.Float32(), rand.Float32()}
		eng.VecIdx().Search(q, 10, nil)
	}
	eng.RUnlock()
	vecDur := time.Since(vecStart)
	perVec := vecDur / time.Duration(iterations)
	t.Logf("Vector search: %d queries in %v (%.2f ms/query)", iterations, vecDur, float64(perVec.Microseconds())/1000)

	// --- Measure reload (startup with data) ---
	eng.Close()
	reloadStart := time.Now()
	eng2, err := LoadEngine(dir)
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	reloadDur := time.Since(reloadStart)
	t.Logf("Startup time (with %d records): %v", scale, reloadDur)

	eng2.RLock()
	nodeCount := eng2.Graph().NodeCount()
	eng2.RUnlock()
	if nodeCount != scale {
		t.Fatalf("expected %d nodes after reload, got %d", scale, nodeCount)
	}
	eng2.Close()

	// --- Validate stats ---
	t.Logf("=== Summary ===")
	t.Logf("Records: %d", scale)
	t.Logf("Capture: %.2f ms/record", float64(perCapture.Microseconds())/1000)
	t.Logf("BM25 search: %.2f ms/query (p50 estimate)", float64(perSearch.Microseconds())/1000)
	t.Logf("Vector search: %.2f ms/query (p50 estimate)", float64(perVec.Microseconds())/1000)
	t.Logf("Disk: %.1f MB (%.0f bytes/node)", float64(diskUsage)/(1024*1024), float64(diskUsage)/float64(scale))
	t.Logf("Heap: %.1f MB", float64(mem.HeapInuse)/(1024*1024))
	t.Logf("Startup: %v (empty), %v (loaded)", startupDur, reloadDur)
}

func dirSize(t *testing.T, path string) int64 {
	t.Helper()
	var size int64
	err := walkDir(path, func(path string, info os.FileInfo) {
		size += info.Size()
	})
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}
	return size
}

func walkDir(path string, fn func(string, os.FileInfo)) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := path + "/" + e.Name()
		info, err := e.Info()
		if err != nil {
			continue
		}
		if e.IsDir() {
			walkDir(full, fn)
		} else {
			fn(full, info)
		}
	}
	return nil
}
