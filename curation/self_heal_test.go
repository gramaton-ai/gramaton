package curation

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

// setupSelfHealTest builds a minimal engine suitable for exercising
// self-heal logic. No LLM, no embedder — self-heal is pure
// deterministic work.
func setupSelfHealTest(t *testing.T) *core.Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatal(err)
	}
	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithVectorIndex(index.NewFlatIndex()),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

// seedContaminated adds a record with the exact observed pattern
// from the 3 real contaminated records observed in production.
// Returns the new node ID.
func seedContaminated(t *testing.T, eng *core.Engine, cleanPrefix, contentFull string) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	tail := `</summary_short>
<parameter name="keywords">["leaked", "fragments"]`
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty(contentFull),
		"content_short":     graph.StringProperty(cleanPrefix + tail),
		"embedding_model":   graph.StringProperty("bge-small-en-v1.5"),
		"processing_status": graph.StringProperty("processed"),
	})
	if _, err := eng.Save("seed"); err != nil {
		t.Fatalf("save: %v", err)
	}
	return n.ID
}

// --- DetectAndRepairSummary happy path ---

func TestDetectAndRepairSummaryStripTier(t *testing.T) {
	eng := setupSelfHealTest(t)
	longClean := "Setup-wizard language principles for Gramaton: lead with user benefit; one-sentence concept explanations; skip-for-now option on every step."
	id := seedContaminated(t, eng, longClean, "Full content here unused by this test.")

	eng.Lock()
	outcome := DetectAndRepairSummary(eng, id, slog.Default())
	eng.Unlock()

	if outcome != outcomeStripped {
		t.Errorf("outcome = %v, want %v", outcome, outcomeStripped)
	}
	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	stored, _ := n.Properties.GetString("content_short")
	if strings.Contains(stored, "</summary_short>") || strings.Contains(stored, "<parameter name=") {
		t.Errorf("stored content retained contamination: %q", stored)
	}
	if stored != longClean {
		t.Errorf("stored = %q, want %q", stored, longClean)
	}
	// Embedding-model should be cleared so reembed picks it up.
	if em, _ := n.Properties.GetString("embedding_model"); em != "" {
		t.Errorf("embedding_model = %q after repair, want cleared for reembed", em)
	}
	// Audit metadata written.
	if _, ok := n.Properties.GetTimestamp("repaired_at"); !ok {
		t.Error("repaired_at not set")
	}
	if m, _ := n.Properties.GetString("repair_method"); m != "stripped" {
		t.Errorf("repair_method = %q, want 'stripped'", m)
	}
}

func TestDetectAndRepairSummaryFallbackTier(t *testing.T) {
	eng := setupSelfHealTest(t)
	// cleanPrefix below minSummaryAfterStrip → strip tier skipped.
	shortClean := "Tiny."
	contentFull := "The full record content has several sentences. It explains the topic thoroughly. Extra context follows the initial claim."
	id := seedContaminated(t, eng, shortClean, contentFull)

	eng.Lock()
	outcome := DetectAndRepairSummary(eng, id, slog.Default())
	eng.Unlock()

	if outcome != outcomeFallback {
		t.Errorf("outcome = %v, want %v", outcome, outcomeFallback)
	}
	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	stored, _ := n.Properties.GetString("content_short")
	if strings.Contains(stored, "<parameter name=") {
		t.Errorf("stored content retained contamination: %q", stored)
	}
	if !strings.HasPrefix(stored, "The full record content") {
		t.Errorf("stored = %q, want content_full prefix", stored)
	}
	if m, _ := n.Properties.GetString("repair_method"); m != "fallback" {
		t.Errorf("repair_method = %q, want 'fallback'", m)
	}
}

func TestDetectAndRepairSummaryFlagForLLM(t *testing.T) {
	eng := setupSelfHealTest(t)
	// cleanPrefix below minSummaryAfterStrip AND content_full has no
	// sentence punctuation — both salvage paths fail.
	id := seedContaminated(t, eng, "Tiny.", "no punctuation here just words")

	eng.Lock()
	outcome := DetectAndRepairSummary(eng, id, slog.Default())
	eng.Unlock()

	if outcome != outcomeFlagged {
		t.Errorf("outcome = %v, want %v", outcome, outcomeFlagged)
	}
	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(id)
	if flag, _ := n.Properties.GetBool("repair_needed_llm"); !flag {
		t.Error("repair_needed_llm not set")
	}
	if m, _ := n.Properties.GetString("repair_method"); m != "flagged" {
		t.Errorf("repair_method = %q, want 'flagged'", m)
	}
}

// TestDetectAndRepairSummarySkipsRedundantFlag pins the redundant-flag
// short-circuit: once a record is Tier-4 flagged, a re-run of the
// cascade against the SAME content_short must short-circuit and not
// re-write repair_needed_llm / repaired_at / repair_method. Pre-fix
// the cascade ran every server boot, churning three properties per
// Tier-4 record. Stored hash detects the already-flagged state.
func TestDetectAndRepairSummarySkipsRedundantFlag(t *testing.T) {
	eng := setupSelfHealTest(t)
	id := seedContaminated(t, eng, "Tiny.", "no punctuation here just words")

	// First pass: flag it.
	eng.Lock()
	first := DetectAndRepairSummary(eng, id, slog.Default())
	eng.Unlock()
	if first != outcomeFlagged {
		t.Fatalf("first pass outcome = %v, want %v", first, outcomeFlagged)
	}

	eng.RLock()
	n, _ := eng.Graph().GetNode(id)
	flaggedTimestamp, hasTS := n.Properties.GetTimestamp("repaired_at")
	storedHash, _ := n.Properties.GetString(repairInputHashKey)
	eng.RUnlock()
	if !hasTS {
		t.Fatal("repaired_at not set after first flag")
	}
	if storedHash == "" {
		t.Fatal("repair_input_hash not set after first flag")
	}

	// Second pass against unchanged content_short.
	time.Sleep(2 * time.Millisecond) // ensure any clock-driven write would shift the timestamp
	eng.Lock()
	second := DetectAndRepairSummary(eng, id, slog.Default())
	eng.Unlock()
	if second != outcomeClean {
		t.Errorf("second pass outcome = %v, want %v (already-flagged record should short-circuit)", second, outcomeClean)
	}

	eng.RLock()
	defer eng.RUnlock()
	n2, _ := eng.Graph().GetNode(id)
	gotTS, _ := n2.Properties.GetTimestamp("repaired_at")
	if !gotTS.Equal(flaggedTimestamp) {
		t.Errorf("repaired_at advanced on second pass; first=%v second=%v (the re-flag wasn't skipped)", flaggedTimestamp, gotTS)
	}
}

func TestDetectAndRepairSummaryCleanIsNoop(t *testing.T) {
	eng := setupSelfHealTest(t)
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":  graph.StringProperty("Body."),
		"content_short": graph.StringProperty("Already-clean summary with no contamination."),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	eng.Lock()
	outcome := DetectAndRepairSummary(eng, n.ID, slog.Default())
	eng.Unlock()

	if outcome != outcomeClean {
		t.Errorf("outcome = %v, want %v (clean records must be left alone)", outcome, outcomeClean)
	}
	eng.RLock()
	defer eng.RUnlock()
	node, _ := eng.Graph().GetNode(n.ID)
	if _, ok := node.Properties.GetTimestamp("repaired_at"); ok {
		t.Error("repaired_at written on a clean record (should have been no-op)")
	}
}

// TestDetectAndRepairSummaryClearsStaleFlagOnTier1Clean pins the
// stale-flag-clearing fix: when a record was previously
// Tier-4 flagged but content_short has since been rewritten to a
// clean value (manual edit, supersession, external repair), the
// stale repair_needed_llm flag must be cleared so a future
// LLM-escalation pass doesn't pick up records that no longer need
// repair.
func TestDetectAndRepairSummaryClearsStaleFlagOnTier1Clean(t *testing.T) {
	eng := setupSelfHealTest(t)
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Body."),
		"content_short":     graph.StringProperty("Already-clean summary with no contamination."),
		"repair_needed_llm": graph.BoolProperty(true),
		repairInputHashKey:  graph.StringProperty("0123456789abcdef"),
		"repair_method":     graph.StringProperty("flagged"),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	eng.Lock()
	outcome := DetectAndRepairSummary(eng, n.ID, slog.Default())
	eng.Unlock()

	if outcome != outcomeClean {
		t.Errorf("outcome = %v, want %v", outcome, outcomeClean)
	}
	eng.RLock()
	defer eng.RUnlock()
	node, _ := eng.Graph().GetNode(n.ID)
	if flag, _ := node.Properties.GetBool("repair_needed_llm"); flag {
		t.Error("repair_needed_llm should be false after Tier-1 clean on a previously-flagged record")
	}
	if h, _ := node.Properties.GetString(repairInputHashKey); h != "" {
		t.Errorf("repair_input_hash should be cleared on Tier-1 clean; got %q", h)
	}
}

// TestDetectAndRepairSummaryClearsStaleFlagOnTier2Stripped pins
// that a successful strip-tier repair clears any prior Tier-4 flag.
// Pre-fix the strip would write content_short + repair_method=stripped
// but leave repair_needed_llm=true, producing contradictory state.
func TestDetectAndRepairSummaryClearsStaleFlagOnTier2Stripped(t *testing.T) {
	eng := setupSelfHealTest(t)
	tail := `</summary_short>
<parameter name="keywords">["leak"]`
	cleanPrefix := "Setup-wizard language principles for Gramaton: lead with user benefit; one-sentence concept explanations; skip-for-now option on every step."

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("Full content unused by this test."),
		"content_short":     graph.StringProperty(cleanPrefix + tail),
		"repair_needed_llm": graph.BoolProperty(true),
		repairInputHashKey:  graph.StringProperty("staleHashFromPriorCycle"),
		"repair_method":     graph.StringProperty("flagged"),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	eng.Lock()
	outcome := DetectAndRepairSummary(eng, n.ID, slog.Default())
	eng.Unlock()

	if outcome != outcomeStripped {
		t.Errorf("outcome = %v, want %v", outcome, outcomeStripped)
	}
	eng.RLock()
	defer eng.RUnlock()
	node, _ := eng.Graph().GetNode(n.ID)
	if flag, _ := node.Properties.GetBool("repair_needed_llm"); flag {
		t.Error("repair_needed_llm should be false after a Tier-2 strip succeeds on a previously-flagged record")
	}
	if h, _ := node.Properties.GetString(repairInputHashKey); h != "" {
		t.Errorf("repair_input_hash should be cleared on Tier-2 success; got %q", h)
	}
	if m, _ := node.Properties.GetString("repair_method"); m != string(outcomeStripped) {
		t.Errorf("repair_method = %q, want %q", m, outcomeStripped)
	}
}

// TestDetectAndRepairSummaryClearsStaleFlagOnTier3Fallback pins
// the same clearing behavior on the Tier-3 (firstSentences-from-
// content_full) success path. Pre-fix this path also left
// repair_needed_llm stale.
func TestDetectAndRepairSummaryClearsStaleFlagOnTier3Fallback(t *testing.T) {
	eng := setupSelfHealTest(t)
	tail := `</summary_short>
<parameter name="keywords">["leak"]`

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("First full sentence here. Second sentence provides context. Third one wraps it up."),
		"content_short":     graph.StringProperty("Tiny." + tail), // strip yields "Tiny." (5 chars) -> Tier-2 fails (< 50 chars)
		"repair_needed_llm": graph.BoolProperty(true),
		repairInputHashKey:  graph.StringProperty("staleHash"),
		"repair_method":     graph.StringProperty("flagged"),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	eng.Lock()
	outcome := DetectAndRepairSummary(eng, n.ID, slog.Default())
	eng.Unlock()

	if outcome != outcomeFallback {
		t.Errorf("outcome = %v, want %v (Tier-3 should have fired)", outcome, outcomeFallback)
	}
	eng.RLock()
	defer eng.RUnlock()
	node, _ := eng.Graph().GetNode(n.ID)
	if flag, _ := node.Properties.GetBool("repair_needed_llm"); flag {
		t.Error("repair_needed_llm should be false after a Tier-3 fallback succeeds on a previously-flagged record")
	}
	if h, _ := node.Properties.GetString(repairInputHashKey); h != "" {
		t.Errorf("repair_input_hash should be cleared on Tier-3 success; got %q", h)
	}
}

// TestDetectAndRepairSummaryCleanIsNoopWhenNotFlagged confirms
// the conditional clear: a clean record without a prior flag does
// NOT receive a spurious property write. Pre-fix concern was
// adding three SetProps to every clean record on every boot --
// the conditional gate prevents that.
func TestDetectAndRepairSummaryCleanIsNoopWhenNotFlagged(t *testing.T) {
	eng := setupSelfHealTest(t)
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":  graph.StringProperty("Body."),
		"content_short": graph.StringProperty("Already-clean summary with no contamination."),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	preHead := eng.HeadHash()

	eng.Lock()
	outcome := DetectAndRepairSummary(eng, n.ID, slog.Default())
	eng.Unlock()
	if outcome != outcomeClean {
		t.Fatalf("outcome = %v, want %v", outcome, outcomeClean)
	}

	eng.RLock()
	defer eng.RUnlock()
	if eng.HeadHash() != preHead {
		t.Error("clean+unflagged record advanced the commit chain; clearStaleRepairFlag should have been a no-op")
	}
	node, _ := eng.Graph().GetNode(n.ID)
	if _, has := node.Properties["repair_needed_llm"]; has {
		t.Error("repair_needed_llm property should NOT exist on a record that was never flagged")
	}
}

// --- Defensive paths ---

// TestDetectAndRepairSummaryMissingNode covers the defensive
// early-return when the node has been deleted between scan and
// repair (a theoretical race today since we scan + repair under the
// same engine, but the guard exists and should stay covered).
func TestDetectAndRepairSummaryMissingNode(t *testing.T) {
	eng := setupSelfHealTest(t)
	eng.Lock()
	outcome := DetectAndRepairSummary(eng, "01NONEXISTENTXXXXXXXXXXXXX", slog.Default())
	eng.Unlock()
	if outcome != outcomeClean {
		t.Errorf("outcome = %v, want %v (missing node must be treated as no-op clean)", outcome, outcomeClean)
	}
}

// TestDetectAndRepairSummaryEmptyField covers the defensive early-
// return when a node exists but has no content_short property.
// Common case: capture-only records that haven't been classified
// yet and won't have a summary until curation summarizes them.
func TestDetectAndRepairSummaryEmptyField(t *testing.T) {
	eng := setupSelfHealTest(t)
	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("Body only, no summary yet."),
	})
	if _, err := eng.Save("seed"); err != nil {
		eng.Unlock()
		t.Fatalf("save: %v", err)
	}
	eng.Unlock()

	eng.Lock()
	outcome := DetectAndRepairSummary(eng, n.ID, slog.Default())
	eng.Unlock()

	if outcome != outcomeClean {
		t.Errorf("outcome = %v, want %v (empty content_short must be no-op)", outcome, outcomeClean)
	}
}

// --- RunSelfHeal integration ---

func TestRunSelfHealScansAndRepairs(t *testing.T) {
	eng := setupSelfHealTest(t)
	// Three contaminated, one clean.
	seedContaminated(t, eng, strings.Repeat("Good prefix sentence with enough characters. ", 3), "Body full a. Body full b.")
	seedContaminated(t, eng, strings.Repeat("Another good prefix with enough characters. ", 3), "Body c. Body d.")
	seedContaminated(t, eng, "Tiny.", "Body of the record has enough prose to produce a real summary via the fallback tier. A second sentence adds additional substance.") // fallback tier

	eng.Lock()
	cleanNode := eng.Graph().AddNode(graph.Properties{
		"content_full":  graph.StringProperty("Clean body."),
		"content_short": graph.StringProperty("Clean summary, no contamination here."),
	})
	if _, err := eng.Save("clean"); err != nil {
		eng.Unlock()
		t.Fatalf("save clean: %v", err)
	}
	eng.Unlock()

	result := RunSelfHeal(eng, slog.Default())

	if result.Scanned < 4 {
		t.Errorf("Scanned = %d, want >= 4", result.Scanned)
	}
	if result.Repaired != 3 {
		t.Errorf("Repaired = %d, want 3", result.Repaired)
	}
	if result.FlaggedForLLM != 0 {
		t.Errorf("FlaggedForLLM = %d, want 0", result.FlaggedForLLM)
	}
	// Clean record must be untouched.
	eng.RLock()
	defer eng.RUnlock()
	n, _ := eng.Graph().GetNode(cleanNode.ID)
	if _, ok := n.Properties.GetTimestamp("repaired_at"); ok {
		t.Error("clean record got repaired_at property")
	}
}

// --- firstSentences helper ---

func TestFirstSentencesBasic(t *testing.T) {
	got := firstSentences("First sentence. Second sentence. Third sentence.", 2, 500)
	want := "First sentence. Second sentence."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFirstSentencesNoPunctuation(t *testing.T) {
	got := firstSentences("this is not a sentence", 2, 500)
	if got != "" {
		t.Errorf("got %q, want empty (no sentence terminator → no salvage)", got)
	}
}

func TestFirstSentencesCapsAtMaxChars(t *testing.T) {
	long := strings.Repeat("Sentence. ", 100) // 1000+ chars
	got := firstSentences(long, 10, 50)
	if len(got) > 50 {
		t.Errorf("got len %d, want <= 50", len(got))
	}
}
