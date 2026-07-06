package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/gramaton-ai/gramaton/testutil"
	"github.com/spf13/pflag"
)

// Shared test state -- initialized once in TestMain.
var (
	testStore  *testutil.PopulatedStore
	testSrv    *server.Server
	testCfgDir string
)

type noopTestLLM struct{}

func (noopTestLLM) Complete(_ context.Context, _ string) (string, error)             { return "", nil }
func (noopTestLLM) CompleteWithModel(_ context.Context, _, _ string) (string, error) { return "", nil }
func (noopTestLLM) ModelID() string                                                  { return "test-noop" }
func (noopTestLLM) ProviderName() string                                             { return "noop" }
func (noopTestLLM) SupportsStructuredOutput() bool                                   { return false }
func (noopTestLLM) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}

func TestMain(m *testing.M) {
	// CLI integration suite: 80+ tests that share one HTTP server,
	// each doing an in-process command + roundtrip. Under -race on
	// Windows this routinely runs ~10 min and trips the package
	// timeout. -short skips the entire suite (race-detector CI uses
	// it); non-race CI on every platform still hits the full suite.
	// testing.Short() requires test flags to be parsed; testing.Init
	// + flag.Parse must run before the call (TestMain is the only
	// place where flags aren't auto-parsed).
	testing.Init()
	flag.Parse()
	if testing.Short() {
		fmt.Fprintln(os.Stderr, "cli integration: skipping in -short mode")
		os.Exit(0)
	}

	// Create engine without testutil.NewEngine (needs *testing.T).
	dir, err := os.MkdirTemp("", "gramaton-cli-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmpdir: %v\n", err)
		os.Exit(1)
	}

	cfg := config.Defaults()
	cfg.DataDir = dir + "/data"
	os.MkdirAll(cfg.DataDir, 0o755)
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	config.Save(cfg, dir+"/config.yaml")

	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithLLM(noopTestLLM{}),
		core.WithVolatileStorage(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "LoadEngine: %v\n", err)
		os.Exit(1)
	}

	// Populate the store using the testutil builders.
	testStore = populateStore(eng)

	// Start HTTP server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	srvCfg := server.DefaultConfig()
	srvCfg.Port = port
	srvCfg.ConfigDir = dir
	srvCfg.IdleTimeout = 0

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	testSrv, err = server.New(eng, srvCfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "New server: %v\n", err)
		os.Exit(1)
	}

	if err := testSrv.StartHTTP(); err != nil {
		fmt.Fprintf(os.Stderr, "StartHTTP: %v\n", err)
		os.Exit(1)
	}

	// Wait for server readiness.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/status", port))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Point CLI at our test server.
	cfgDir = dir
	testCfgDir = dir

	// Default the store-lifecycle harness backend to a no-op fake for
	// the whole suite: the production DefaultMCPBackend shells out to the
	// real claude/codex CLIs and edits ~/.cursor/mcp.json, so leaving it
	// live would let `store create`/`attach`/`delete`/`rename` tests
	// mutate the developer's actual harness config. Tests that assert
	// registration wiring swap in a fake with detected clients.
	storeHarnessBackend = &fakeStoreHarnessBackend{}

	code := m.Run()

	testSrv.Shutdown()
	os.RemoveAll(dir)
	os.Exit(code)
}

// populateStore adds a subset of realistic records directly to the engine.
// Uses testutil.Record builder but calls AddDirect (no *testing.T needed).
func populateStore(eng *core.Engine) *testutil.PopulatedStore {
	s := &testutil.PopulatedStore{}
	now := time.Now().UTC()

	// We use a subset of the full PopulatedEngine -- enough for CLI tests.
	s.WorkReorg = testutil.Record("The engineering team was restructured into two squads: platform and product.").
		Temporality("durable").Confidence(0.95).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("work", "team", "engineering", "reorg").
		Summary("Engineering split into platform and product squads").
		CreatedAt(now.Add(-30 * 24 * time.Hour)).AccessCount(8).
		Embedding([]float32{0.9, 0.1, 0.05, 0.0}).
		AddDirect(eng)

	s.WorkNewManager = testutil.Record("New manager prefers async updates over daily standups.").
		Temporality("durable").Confidence(0.9).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("work", "team", "manager").
		Summary("New manager prefers async updates").
		CreatedAt(now.Add(-7 * 24 * time.Hour)).
		Embedding([]float32{0.85, 0.15, 0.1, 0.0}).
		AddDirect(eng)

	s.HealthAllergy = testutil.Record("Allergic to shellfish. Reaction: hives and throat swelling.").
		Temporality("immutable").Confidence(1.0).KnowledgeType("semantic").EpistemicStatus("well_established").
		Keywords("health", "allergy", "food", "medical").
		Summary("Shellfish allergy: hives + throat swelling").
		Importance(0.9).
		CreatedAt(now.Add(-180 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.88, 0.12, 0.0}).
		AddDirect(eng)

	s.HealthExercise = testutil.Record("Current routine: run 3x/week, strength training 2x/week, yoga on Sundays.").
		Temporality("durable").Confidence(0.95).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("health", "exercise", "fitness", "routine").
		Summary("Weekly exercise routine").
		CreatedAt(now.Add(-20 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.85, 0.15, 0.0}).
		AddDirect(eng)

	s.TravelSeat = testutil.Record("Always request a window seat. Aisle for flights over 6 hours.").
		Temporality("immutable").Confidence(1.0).KnowledgeType("semantic").EpistemicStatus("well_established").
		Keywords("travel", "flights", "preference").
		Summary("Window seat default, aisle for 6+ hour flights").
		CreatedAt(now.Add(-120 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.9, 0.1}).
		AddDirect(eng)

	// Cooking cluster (for keyword tests)
	s.CookingRecipe = testutil.Record("Lemon garlic pasta recipe: cook spaghetti, sautee garlic, add lemon.").
		Temporality("durable").Confidence(0.95).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("cooking", "food", "recipe", "pasta").
		Summary("Lemon garlic pasta").
		CreatedAt(now.Add(-40 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.9}).
		AddDirect(eng)

	s.CookingSubstitution = testutil.Record("Greek yogurt works as a substitute for sour cream.").
		Temporality("durable").Confidence(0.85).KnowledgeType("semantic").EpistemicStatus("well_established").
		Keywords("cooking", "food", "substitution").
		Summary("Greek yogurt subs for sour cream").
		CreatedAt(now.Add(-35 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.05, 0.88}).
		AddDirect(eng)

	s.CookingDietary = testutil.Record("No refined sugar. Using honey or maple syrup instead.").
		Temporality("durable").Confidence(0.9).KnowledgeType("semantic").EpistemicStatus("well_established").
		Keywords("cooking", "food", "health", "diet").
		Summary("No refined sugar policy").
		CreatedAt(now.Add(-60 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.1, 0.0, 0.85}).
		AddDirect(eng)

	// TODOs
	s.TodoOpen = testutil.Record("TODO: Clean out the garage this weekend.").
		Temporality("temporal").Confidence(1.0).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("todo", "home", "organizing").
		Summary("TODO: Clean garage").
		Importance(0.7).
		CreatedAt(now.Add(-3 * 24 * time.Hour)).
		AddDirect(eng)

	s.TodoCompleted = testutil.Record("TODO: Schedule annual dental cleaning").
		Temporality("temporal").Confidence(1.0).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("todo", "health", "dental").
		Summary("DONE: Dental cleaning scheduled").
		Resolution("completed").ResolvedAt(now.Add(-5 * 24 * time.Hour)).
		ValidUntil(now.Add(-5 * 24 * time.Hour)).
		CreatedAt(now.Add(-14 * 24 * time.Hour)).
		AddDirect(eng)

	// Pending records
	s.Pending1 = testutil.Record("Had a great conversation about sustainable architecture.").
		Pending().CreatedAt(now.Add(-2 * 24 * time.Hour)).AddDirect(eng)

	s.Pending2 = testutil.Record("Meeting notes from Thursday: discussed Q3 planning.").
		Pending().CreatedAt(now.Add(-1 * 24 * time.Hour)).AddDirect(eng)

	s.Pending3 = testutil.Record("Someone recommended a podcast about behavioral economics.").
		Pending().CreatedAt(now.Add(-4 * 24 * time.Hour)).AddDirect(eng)

	// Edges
	testutil.EdgeDirect(eng, s.WorkReorg, s.WorkNewManager, "relates_to", 0.8)
	testutil.EdgeDirect(eng, s.HealthAllergy, s.CookingDietary, "relates_to", 0.7)

	return s
}

// runCmd executes a CLI command and returns captured stdout.
//
// The reader (io.Copy in the goroutine) drains concurrently with the
// command's writes. Without that draining, any output exceeding the OS
// pipe buffer deadlocks: rootCmd.Execute writes synchronously into w,
// nobody reads from r until Execute returns, so a Write that fills the
// buffer blocks forever. POSIX anonymous pipes default to ~64KB and
// most CLI outputs fit, so it's silent there. Windows anonymous pipes
// default to ~4KB; any JSON output >4KB hangs the test for the full
// per-package timeout. Surfaced as #50: TestCLISearchSort and
// TestCLISearch both produced 4134-byte writes and hung on Windows CI.
func runCmd(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	rootCmd.SetArgs(args)
	// Reset all subcommand flags to avoid cross-test pollution.
	for _, sub := range rootCmd.Commands() {
		sub.Flags().VisitAll(func(f *pflag.Flag) {
			f.Changed = false
			f.Value.Set(f.DefValue)
		})
	}
	// Preserve the config-dir override on root's persistent flags.
	rootCmd.PersistentFlags().Set("config-dir", testCfgDir)

	// Drain the read end concurrently so writes never block on the
	// pipe-buffer limit. See docstring above for the Windows deadlock.
	var buf bytes.Buffer
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(copyDone)
	}()

	err := rootCmd.Execute()

	w.Close()
	os.Stdout = old

	<-copyDone
	r.Close()

	return buf.Bytes(), err
}

func parseOutput(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse output: %v\nraw: %s", err, string(data))
	}
	return result
}

// --- Integration tests ---

func TestCLISearch(t *testing.T) {
	out, err := runCmd(t, "search", "exercise routine", "--top", "5")
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	result := parseOutput(t, out)
	results, ok := result["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
}

func TestCLISearchWithFilters(t *testing.T) {
	out, err := runCmd(t, "search", "--temporality", "immutable", "--top", "10")
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	result := parseOutput(t, out)
	results := result["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected at least 1 immutable record")
	}
	for _, r := range results {
		rec := r.(map[string]any)
		if rec["temporality"] != "immutable" {
			t.Fatalf("expected immutable, got %v", rec["temporality"])
		}
	}
}

func TestCLISearchResolutionFilter(t *testing.T) {
	out, err := runCmd(t, "search", "--resolution", "unresolved", "--keywords", "todo", "--top", "10")
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	result := parseOutput(t, out)
	results := result["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected at least 1 unresolved TODO")
	}
	for _, r := range results {
		rec := r.(map[string]any)
		if v, ok := rec["resolution"]; ok && v != nil && v != "" {
			t.Fatalf("expected unresolved, got resolution=%v", v)
		}
	}
}

func TestCLIInspect(t *testing.T) {
	out, err := runCmd(t, "inspect", testStore.HealthAllergy)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	result := parseOutput(t, out)
	if result["id"] != testStore.HealthAllergy {
		t.Fatalf("expected id %s, got %v", testStore.HealthAllergy, result["id"])
	}
	props := result["properties"].(map[string]any)
	if props["temporality"] != "immutable" {
		t.Fatalf("expected immutable, got %v", props["temporality"])
	}
	related := result["related"].([]any)
	if len(related) == 0 {
		t.Fatal("expected related records for HealthAllergy")
	}
}

func TestCLIExplore(t *testing.T) {
	out, err := runCmd(t, "explore", testStore.WorkReorg, "--depth", "2")
	if err != nil {
		t.Fatalf("explore: %v", err)
	}

	result := parseOutput(t, out)
	nodes, ok := result["nodes"].([]any)
	if !ok {
		t.Fatalf("expected nodes array, got %T", result["nodes"])
	}
	if len(nodes) < 1 {
		t.Fatalf("expected at least 1 connected node, got %d", len(nodes))
	}
}

func TestCLIPending(t *testing.T) {
	out, err := runCmd(t, "pending")
	if err != nil {
		t.Fatalf("pending: %v", err)
	}

	result := parseOutput(t, out)
	records := result["records"].([]any)
	if len(records) < 3 {
		t.Fatalf("expected at least 3 pending records, got %d", len(records))
	}
}

func TestCLIStats(t *testing.T) {
	out, err := runCmd(t, "stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	result := parseOutput(t, out)
	total, ok := result["total_records"].(float64)
	if !ok {
		t.Fatalf("expected total_records, got %v", result["total_records"])
	}
	if total < 10 {
		t.Fatalf("expected at least 10 records, got %.0f", total)
	}
}

func TestCLIStatus(t *testing.T) {
	out, err := runCmd(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	result := parseOutput(t, out)
	store, ok := result["store"].(map[string]any)
	if !ok {
		t.Fatalf("expected store object: %s", string(out))
	}
	if store["nodes"] == nil {
		t.Fatal("expected nodes count")
	}
}

func TestCLISearchSort(t *testing.T) {
	out, err := runCmd(t, "search", "--sort", "created_at", "--order", "asc", "--top", "5")
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	result := parseOutput(t, out)
	results := result["results"].([]any)
	if len(results) < 2 {
		t.Fatal("expected at least 2 results")
	}
	first := results[0].(map[string]any)["created_at"].(string)
	last := results[len(results)-1].(map[string]any)["created_at"].(string)
	if first > last {
		t.Fatalf("expected ascending order, got first=%s last=%s", first, last)
	}
}

func TestCLISearchKeywords(t *testing.T) {
	out, err := runCmd(t, "search", "--keywords", "cooking,food", "--top", "10")
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	result := parseOutput(t, out)
	results := result["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected results for cooking+food keywords")
	}
}

func TestCLISearchMatch(t *testing.T) {
	out, err := runCmd(t, "search", "--match", "shellfish", "--top", "5")
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	result := parseOutput(t, out)
	results := result["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'shellfish', got %d", len(results))
	}
}

func TestCLISearchRandom(t *testing.T) {
	out, err := runCmd(t, "search", "--random", "--top", "3")
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	result := parseOutput(t, out)
	results := result["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("expected 3 random results, got %d", len(results))
	}
}
