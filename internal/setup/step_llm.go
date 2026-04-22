package setup

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/llm/anthropic"
)

// stepLLM is Step 2: the optional (but strongly nudged) LLM provider.
// Without an LLM, Gramaton captures and retrieves but doesn't curate:
// records stay unclassified, summaries stay verbose, contradictions
// accumulate undetected. With an LLM, curation runs in the background
// and keeps the store organized as it grows.
//
// Provider branches:
//  1. Anthropic direct API (recommended, simplest)
//  2. OpenAI-compatible API
//  3. AWS Bedrock with Anthropic models
//  4. Help path (print guidance + signup URL, re-prompt)
//  5. Skip (warned-about-but-allowed)
//
// After a successful key entry, we set search.rerank_enabled = true:
// if the user is paying for an LLM, they should get the search
// quality benefit. This was a deliberate default-flip decision vs the
// pre-wizard behavior where rerank was always off. Default stays off
// for users who skip LLM (rerank is useless without one).
func (w *Wizard) stepLLM(ctx context.Context) error {
	w.writer.StepHeader(2, totalSteps, "Autonomous curation (strongly recommended)")

	// The feature-map is the core of the "how good is Gramaton without
	// an LLM" answer. We include it inline rather than link to docs
	// because the user is making the decision right now and shouldn't
	// have to context-switch to read docs/providers.md.
	w.printLLMFeatureMap()
	w.writer.Blank()

	for {
		w.writer.Raw("    [1] Anthropic API key  (recommended, simplest)")
		w.writer.Raw("    [2] OpenAI API key")
		w.writer.Raw("    [3] AWS Bedrock with Anthropic models  (for AWS shops)")
		w.writer.Raw("    [4] I need help getting an API key")
		w.writer.Raw("    [5] Skip for now  (not recommended -- you'll get a passive store)")
		w.writer.Blank()
		w.writer.Prompt(">")

		idx, err := w.prompter.Choice(5, -1)
		if err != nil {
			// ErrAborted = stdin closed (EOF or user Ctrl+D).
			// Don't re-prompt forever -- propagate so the wizard
			// exits cleanly. Any other error is a validation
			// message we want to show and retry.
			if errors.Is(err, ErrAborted) {
				return err
			}
			w.writer.ErrorLine(err.Error())
			continue
		}

		switch idx {
		case 0:
			return w.llmAnthropic(ctx)
		case 1:
			return w.llmOpenAI(ctx)
		case 2:
			return w.llmBedrock(ctx)
		case 3:
			// Help path: print guidance, then re-enter the menu. The
			// user who picked "help" will pick [1]/[2]/[3] on the
			// second pass once they have a key.
			w.printGettingAKey()
			continue
		case 4:
			return w.llmSkip(ctx)
		}
	}
}

// printLLMFeatureMap prints the comparison table + plain-English
// framing the user needs to make an informed choice. The table is
// ASCII-formatted for terminal portability; it renders cleanly in
// any terminal that can handle the wizard's ✓/⚠ prefixes.
//
// Wording discipline: no sales-y adjectives ("amazing", "powerful").
// Every row is a concrete capability that either works or doesn't.
func (w *Wizard) printLLMFeatureMap() {
	w.writer.Paragraph(
		"Here's what an AI service adds to Gramaton:",
	)
	w.writer.Blank()
	w.writer.Raw("    FEATURE                         Without AI    With AI")
	w.writer.Raw("    ----------------------------------------------------")
	w.writer.Raw("    Capture records                    ✓             ✓")
	w.writer.Raw("    Search (keyword + vector)          ✓             ✓")
	w.writer.Raw("    Collections (task lists)           ✓             ✓")
	w.writer.Raw("    Auto-classification of records     -             ✓")
	w.writer.Raw("       (type, confidence, timeframe)")
	w.writer.Raw("    Semantic summaries for search      -             ✓")
	w.writer.Raw("       (better retrieval quality)")
	w.writer.Raw("    Contradiction linking              -             ✓")
	w.writer.Raw("       (connects conflicting records)")
	w.writer.Raw("    Auto-supersession                  -             ✓")
	w.writer.Raw("       (older records retire when")
	w.writer.Raw("        clearly replaced)")
	w.writer.Raw("    Concept synthesis                  -             ✓")
	w.writer.Raw("       (recurring themes grow into")
	w.writer.Raw("        richer concept nodes)")
	w.writer.Raw("    LLM-reranked search results        -             ✓")
	w.writer.Raw("       (intent-aware top-N ordering)")
	w.writer.Raw("    Store self-maintenance             -             ✓")
	w.writer.Raw("       (quality holds as graph grows)")
	w.writer.Blank()
	w.writer.Paragraph(
		"Plain English: without AI, Gramaton is a searchable filing",
		"cabinet that you or your agent have to organize yourselves.",
		"With AI, it organizes itself in the background.",
		"",
		"Cost: most users spend $1-10/month. Haiku (cheap and fast)",
		"handles the frequent work; larger models run only on the ~5%",
		"of tasks that benefit. We'll set hard spending caps next.",
	)
}

// printGettingAKey walks the "I need help" user through Anthropic
// signup. We lead with Anthropic because the project's defaults are
// tuned for Claude models and the signup path is the simplest of the
// three providers.
func (w *Wizard) printGettingAKey() {
	w.writer.Blank()
	w.writer.Paragraph(
		"Here's how to get a Claude API key (Anthropic):",
		"",
		"  1. Open https://console.anthropic.com in your browser",
		"  2. Sign in or create an account",
		"  3. Go to Settings -> API Keys -> Create Key",
		"  4. Copy the key (it starts with \"sk-ant-...\")",
		"",
		"Come back and pick option [1] when you have the key. Or",
		"pick [5] to skip for now and add it later.",
	)
	w.writer.Blank()
}

// llmAnthropic handles the Anthropic direct-API branch. Reads the key
// (hidden), writes it to ~/.gramaton/anthropic.key at 0600, tests it
// with a minimal completion, and wires config.LLM accordingly.
func (w *Wizard) llmAnthropic(ctx context.Context) error {
	w.writer.Blank()
	w.writer.Paragraph(
		"Paste your Anthropic API key (it will be hidden as you type).",
		"Press Enter without typing to go back.",
	)
	w.writer.Prompt(">")
	key, err := w.prompter.Secret()
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		w.writer.Warn("No key entered. Skipping LLM setup.")
		return w.llmSkip(ctx)
	}

	// Soft format validation: Anthropic keys start with "sk-ant-".
	// We warn but don't reject -- the user might be pasting a valid
	// custom-prefix proxy key, or Anthropic might change the prefix
	// format in the future. Reject-on-prefix would be a future-proof
	// landmine.
	if !strings.HasPrefix(key, "sk-ant-") {
		w.writer.Warn("Key doesn't start with 'sk-ant-'. Continuing anyway; the test call will catch it if invalid.")
	}

	keyPath := filepath.Join(w.configDir, "anthropic.key")
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("write key file: %w", err)
	}
	w.writer.Check(fmt.Sprintf("Saved to %s (0600 perms -- only you can read it)", keyPath))

	// Update config in-memory; we save to disk later (either in
	// stepVerify or at wizard end). Intermediate saves would leave
	// half-configured files around if the user Ctrl+C'd mid-wizard.
	w.cfg.LLM.Provider = "anthropic"
	w.cfg.LLM.APIKeyFile = keyPath
	// Leave LLM.Model at the default ("claude-sonnet-4-6") so code
	// paths that call Complete() without a tier (search rerank,
	// decompose, observe) use a solid default. Curation uses the
	// tier-based Models map, which already defaults to Haiku for
	// the frequent-task tier. See config.Defaults().

	// Test the key with a minimal completion. 5-second deadline
	// keeps the wizard from hanging on a slow network; a failed
	// test is a warning, not an error -- we already saved the key,
	// and the user can proceed (maybe offline, maybe rate-limited).
	return w.validateAnthropicKey(ctx, keyPath)
}

// validateAnthropicKey fires a tiny test call against the Anthropic
// API with the provided key. Success is reported as ✓; failure is
// reported as ⚠ with a retry/skip option, but the key stays saved
// because the failure might be transient.
func (w *Wizard) validateAnthropicKey(ctx context.Context, keyPath string) error {
	// Use a dedicated short-deadline context so a slow API doesn't
	// hang the wizard longer than the user would wait.
	testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Build a minimal anthropic.Client with just enough config to
	// make the call. We don't mutate w.cfg further here -- this is
	// purely a test of the key.
	testCfg := config.LLMConfig{
		Provider:   "anthropic",
		Model:      "claude-haiku-4-5", // cheapest model for the test call
		APIKeyFile: keyPath,
	}
	client, err := anthropic.New(testCfg)
	if err != nil {
		// Construction failed (e.g., unreadable key file). Treat as
		// non-fatal; user can re-run wizard. Path to diagnose is the
		// error message we're about to print.
		w.writer.Warn(fmt.Sprintf("Could not construct client: %v", err))
		return nil
	}

	// Minimal prompt; we only need a 200 back to confirm the key
	// authenticates. "Say 'ok'" is the de-facto minimum interaction.
	_, err = client.Complete(testCtx, "Say 'ok'.")
	if err == nil {
		w.writer.Check("Tested -- the key works.")
		return nil
	}

	// Non-fatal failure. Common cases:
	//   - 401 invalid key: user typed/pasted wrong. Saved anyway;
	//     the next server start will fail clearly.
	//   - Network timeout: transient. Saved; will validate on first
	//     real use.
	//   - 429 rate limit: the key is valid, we just got throttled.
	//     Saved; fine.
	//
	// We don't try to distinguish these cases inline because the
	// underlying error strings vary by provider and we don't want
	// the wizard guessing. Just report the raw error and move on.
	w.writer.Warn(fmt.Sprintf("Key saved but validation test failed: %v", err))
	w.writer.Paragraph(
		"This might be a network issue, a typo, or rate limiting.",
		"The key will be re-validated when curation next runs.",
	)
	return nil
}

// llmOpenAI writes an OpenAI key and configures the provider without
// a test call. Reason: the Anthropic test uses the anthropic package
// directly; doing the same for OpenAI would couple the wizard to the
// openai package. Worth the symmetry for v2; for v1 we trust the key
// format and let first use fail loudly if invalid.
func (w *Wizard) llmOpenAI(ctx context.Context) error {
	w.writer.Blank()
	w.writer.Paragraph("Paste your OpenAI API key (hidden):")
	w.writer.Prompt(">")
	key, err := w.prompter.Secret()
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return w.llmSkip(ctx)
	}

	keyPath := filepath.Join(w.configDir, "openai.key")
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("write key file: %w", err)
	}
	w.writer.Check(fmt.Sprintf("Saved to %s (0600 perms)", keyPath))

	w.cfg.LLM.Provider = "openai"
	w.cfg.LLM.APIKeyFile = keyPath
	w.cfg.LLM.Model = "gpt-4o-mini"
	// Note: Models tier map still holds Anthropic defaults, which are
	// Anthropic-specific names. Curation will fail on task-tier calls
	// against OpenAI unless Models is re-populated with OpenAI names.
	// TODO(post-OSS, for a follow-up): override Models.{Low, Medium,
	// High} with OpenAI equivalents when provider is openai. For now,
	// this path leaves a latent-but-surviveable misconfig: the
	// fallback LLM.Model works for rerank/decompose, but curation
	// tasks will error on tier-specific calls until Models is edited.
	// This is a known gap surfaced in the config.yaml comments.

	w.writer.Warn("Note: curation tier models still point at Anthropic names. Edit config.yaml's llm.models map to use OpenAI model names (e.g., gpt-4o-mini, gpt-4o).")
	return w.cfgCapsPrompt(ctx)
}

// llmBedrock handles AWS Bedrock with Anthropic models. Asks for
// profile and region; sets up the config to use Bedrock's
// Anthropic-on-Bedrock model IDs (different format from the direct
// API).
func (w *Wizard) llmBedrock(ctx context.Context) error {
	w.writer.Blank()
	w.writer.Paragraph(
		"Which AWS profile should Gramaton use?",
		"(Leave blank to use the default credential chain -- env vars, IMDS, SSO, etc.)",
	)
	w.writer.Prompt(">")
	profile, err := w.prompter.Text("")
	if err != nil {
		return err
	}

	w.writer.Paragraph(
		"Which AWS region? (Anthropic models are available in us-east-1,",
		"us-west-2, eu-central-1, ap-northeast-1, ap-south-1.)",
	)
	w.writer.Prompt(">")
	region, err := w.prompter.Text("us-west-2")
	if err != nil {
		return err
	}

	w.cfg.LLM.Provider = "bedrock"
	w.cfg.LLM.Region = region
	w.cfg.LLM.AWSProfile = profile
	// Bedrock model IDs use the anthropic.claude-<tier>-<version>-<date>-v<n>:0 format.
	// These values track current Bedrock catalog for Anthropic.
	w.cfg.LLM.Model = "anthropic.claude-sonnet-4-6-20250514-v1:0"
	w.cfg.LLM.Models.Low = "anthropic.claude-haiku-4-5-20250514-v1:0"
	w.cfg.LLM.Models.Medium = "anthropic.claude-sonnet-4-6-20250514-v1:0"
	w.cfg.LLM.Models.High = "anthropic.claude-opus-4-7-20250514-v1:0"

	w.writer.Check("Bedrock with Anthropic models configured.")
	if profile != "" {
		w.writer.Check(fmt.Sprintf("  AWS profile: %s", profile))
	}
	w.writer.Check(fmt.Sprintf("  Region: %s", region))
	return w.cfgCapsPrompt(ctx)
}

// llmSkip sets the config to no-LLM mode. Curation will run in
// deterministic-only mode; records will stay unclassified until an
// agent manually classifies them via gramaton_classify.
func (w *Wizard) llmSkip(_ context.Context) error {
	w.writer.Blank()
	w.writer.Warn("Skipping LLM setup. Curation will run in deterministic-only mode.")
	w.writer.Paragraph(
		"",
		"Add an API key later with: gramaton set-key",
		"(or edit ~/.gramaton/config.yaml directly).",
	)
	// Explicit empty provider -- makes the intent clear when re-reading
	// the yaml later.
	w.cfg.LLM.Provider = ""
	return nil
}

// cfgCapsPrompt walks the user through spending caps after a
// successful LLM configuration. Defaults to $5/day + 500 calls/day
// with the option to customize.
//
// Per the earlier design discussion, we deliberately do NOT show a
// per-cycle dollar number by default -- "$X per 1-minute cycle" tends
// to make users multiply by 1440 and panic. Instead we lead with the
// daily cap (a single round number) and let the customize path expose
// the per-cycle detail for users who want it.
func (w *Wizard) cfgCapsPrompt(ctx context.Context) error {
	// Also flip rerank on here: the user has committed to an LLM, so
	// they should get the search-quality benefit. Rerank gracefully
	// no-ops if the provider becomes unreachable.
	w.cfg.Search.RerankEnabled = true

	// Set the defaults. These values are chosen to be "safety net, not
	// budget" -- most users will spend far less at typical usage.
	// Anchors derived from: Haiku @ ~$0.005/call x 20 calls/cycle max
	// x 60 cycles/hour = ~$6/hour absolute worst case, which is well
	// below $5/day because most cycles are idle.
	w.cfg.LLM.MaxCostUSDPerDay = 5.00
	w.cfg.LLM.MaxCallsPerDay = 500
	w.cfg.LLMCuration.MaxCostUSDPerRun = 1.00

	w.writer.Blank()
	w.writer.Paragraph(
		"Spending caps",
		"",
		"I'm setting safe defaults:",
		"",
		"  * $5 per day (hard stop for all AI features)",
		"  * 500 API calls per day (backstop for untracked-cost paths)",
		"",
		"At typical usage you'll spend far less than the daily cap --",
		"it's a safety net, not a budget. Rough monthly estimates:",
		"",
		"  Light use (a few captures/day)        ~$1-3/month",
		"  Regular use (agent auto-capture)      ~$3-10/month",
		"  Heavy use (bulk imports, many/day)    ~$15-40/month",
		"",
		"When a cap is hit, curation pauses until the next day.",
		"Records stay queued. Nothing is lost.",
		"",
		"Edit caps any time in ~/.gramaton/config.yaml.",
	)
	w.writer.Blank()
	w.writer.Raw("    [Y] Use these defaults")
	w.writer.Raw("    [n] Customize now")
	w.writer.Blank()
	w.writer.Prompt(">")

	useDefaults, err := w.prompter.YesNo(true)
	if err != nil {
		if errors.Is(err, ErrAborted) {
			return err
		}
		// Bad input: take the safe path (defaults) and move on.
		w.writer.Warn(err.Error())
		useDefaults = true
	}

	if useDefaults {
		w.writer.Check("Spending caps set.")
		return nil
	}

	// Customize path: prompt for each cap with the default as the
	// fallback. We keep this minimal -- three fields -- because
	// going deeper means exposing the per-cycle / per-run distinction
	// that confuses users. Editing config.yaml is the escape hatch
	// for advanced tuning.
	w.writer.Blank()
	w.writer.Prompt(fmt.Sprintf("Max USD per day (default $%.2f):", w.cfg.LLM.MaxCostUSDPerDay))
	day, err := w.prompter.Text(fmt.Sprintf("%.2f", w.cfg.LLM.MaxCostUSDPerDay))
	if err != nil {
		return err
	}
	// Best-effort parse: on success apply, on failure print a
	// user-visible warn explaining why we kept the default. Silent
	// fallback would leave users thinking their value took effect.
	if v, parseErr := parseMoneyUSD(day); parseErr == nil && v > 0 {
		w.cfg.LLM.MaxCostUSDPerDay = v
	} else if day != fmt.Sprintf("%.2f", w.cfg.LLM.MaxCostUSDPerDay) && parseErr != nil {
		w.writer.Warn(fmt.Sprintf("Invalid USD/day value: %v. Keeping default $%.2f.", parseErr, w.cfg.LLM.MaxCostUSDPerDay))
	}

	w.writer.Prompt(fmt.Sprintf("Max API calls per day (default %d):", w.cfg.LLM.MaxCallsPerDay))
	calls, err := w.prompter.Text(fmt.Sprintf("%d", w.cfg.LLM.MaxCallsPerDay))
	if err != nil {
		return err
	}
	if v, parseErr := parseIntAtLeast(calls, 1); parseErr == nil {
		w.cfg.LLM.MaxCallsPerDay = v
	} else if calls != fmt.Sprintf("%d", w.cfg.LLM.MaxCallsPerDay) {
		w.writer.Warn(fmt.Sprintf("Invalid calls/day value: %v. Keeping default %d.", parseErr, w.cfg.LLM.MaxCallsPerDay))
	}

	w.writer.Prompt(fmt.Sprintf("Max USD per curation cycle (default $%.2f):", w.cfg.LLMCuration.MaxCostUSDPerRun))
	run, err := w.prompter.Text(fmt.Sprintf("%.2f", w.cfg.LLMCuration.MaxCostUSDPerRun))
	if err != nil {
		return err
	}
	if v, parseErr := parseMoneyUSD(run); parseErr == nil && v > 0 {
		w.cfg.LLMCuration.MaxCostUSDPerRun = v
	} else if run != fmt.Sprintf("%.2f", w.cfg.LLMCuration.MaxCostUSDPerRun) && parseErr != nil {
		w.writer.Warn(fmt.Sprintf("Invalid USD/cycle value: %v. Keeping default $%.2f.", parseErr, w.cfg.LLMCuration.MaxCostUSDPerRun))
	}

	w.writer.Check(fmt.Sprintf(
		"Caps set: $%.2f/day, %d calls/day, $%.2f/cycle",
		w.cfg.LLM.MaxCostUSDPerDay,
		w.cfg.LLM.MaxCallsPerDay,
		w.cfg.LLMCuration.MaxCostUSDPerRun,
	))
	return nil
}

// maxUSDCap is the upper bound the wizard accepts for any spending cap.
// A user who types "10000" (or more) very likely meant something else
// or is inflating a typo -- accepting it silently would defeat the
// "safety net, not a budget" framing in the caps step. We reject
// anything above this and keep the default, with a warning.
const maxUSDCap = 10_000.0

// maxCallsCap similarly bounds MaxCallsPerDay. 1 million calls/day is
// already well past any realistic single-user workload.
const maxCallsCap = 1_000_000

// parseMoneyUSD accepts inputs like "5.00", "$5", "$5.00" and returns
// the numeric value. Rejects negatives, NaN, +/-Inf, and values above
// maxUSDCap (sanity guard against typos like "10000000" that would
// effectively disable the cost cap). Any rejection returns an error
// so callers print a warning and keep the default.
func parseMoneyUSD(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, err
	}
	// NaN and Inf both slip past "< 0" checks; guard explicitly.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("value must be a finite number")
	}
	if v < 0 {
		return 0, fmt.Errorf("negative values not allowed")
	}
	if v > maxUSDCap {
		return 0, fmt.Errorf("value too large (max $%.2f; edit config.yaml directly for higher)", maxUSDCap)
	}
	return v, nil
}

// parseIntAtLeast parses s as an integer and requires it to be between
// min and maxCallsCap inclusive.
func parseIntAtLeast(s string, min int) (int, error) {
	s = strings.TrimSpace(s)
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	if v < min {
		return 0, fmt.Errorf("value must be >= %d", min)
	}
	if v > maxCallsCap {
		return 0, fmt.Errorf("value too large (max %d; edit config.yaml directly for higher)", maxCallsCap)
	}
	return v, nil
}
