package setup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/awscfg"
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
//
// On re-run (`gramaton init --force`) where the key file already
// exists, offers a keep-existing shortcut so users don't have to
// re-paste.
func (w *Wizard) llmAnthropic(ctx context.Context) error {
	keyPath := filepath.Join(w.configDir, "anthropic.key")

	// If a key was saved on a prior run, offer to reuse it rather
	// than force the user to fish it out of 1Password again. Still
	// runs the test call so a rotated/revoked key surfaces cleanly.
	if _, err := os.Stat(keyPath); err == nil {
		w.writer.Blank()
		w.writer.Paragraph(
			fmt.Sprintf("Anthropic key detected at %s.", keyPath),
		)
		w.writer.Raw("    [Y] Keep existing key")
		w.writer.Raw("    [n] Replace with a new key")
		w.writer.Prompt(">")

		keep, err := w.prompter.YesNo(true)
		if err != nil {
			if errors.Is(err, ErrAborted) {
				return err
			}
			w.writer.ErrorLine(err.Error())
			return nil
		}
		if keep {
			w.cfg.LLM.Provider = "anthropic"
			w.cfg.LLM.APIKeyFile = keyPath
			w.writer.Check(fmt.Sprintf("Using existing key: %s", keyPath))
			return w.validateAnthropicKey(ctx, keyPath)
		}
		// Fall through to paste-new-key path. Intentionally don't
		// delete the old key first — if the user Ctrl+Cs before
		// the new write, the old one stays usable.
	}

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

	// keyPath already resolved at top of function.
	// writeWithRollback: if the wizard is interrupted (Ctrl+C, error
	// in a later step) before Step 5 commits, restores the pre-
	// existing key file or removes the new one if none existed. On
	// --force re-run where a key file was already present, this
	// preserves the user's previous key rather than leaving them
	// with nothing.
	if err := w.writeWithRollback(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("write key file: %w", err)
	}
	w.writer.Check(fmt.Sprintf("Saved to %s (0600 perms -- only you can read it)", keyPath))

	// Update config in-memory; we save to disk later (either in
	// stepVerify or at wizard end). Intermediate saves would leave
	// half-configured files around if the user Ctrl+C'd mid-wizard.
	w.cfg.LLM.Provider = "anthropic"
	w.cfg.LLM.APIKeyFile = keyPath
	// Leave Models triple at the Defaults() values (low=haiku,
	// medium=sonnet, high=opus). All call sites resolve the model via
	// cfg.ModelForTask, which keys on the Tasks map (also at default).

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
		APIKeyFile: keyPath,
		Models: config.LLMModels{
			Medium: "claude-haiku-4-5", // cheapest model for the test call
		},
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
//
// On re-run where the key file already exists, offers the same
// keep-existing shortcut as llmAnthropic.
func (w *Wizard) llmOpenAI(ctx context.Context) error {
	keyPath := filepath.Join(w.configDir, "openai.key")

	if _, err := os.Stat(keyPath); err == nil {
		w.writer.Blank()
		w.writer.Paragraph(
			fmt.Sprintf("OpenAI key detected at %s.", keyPath),
		)
		w.writer.Raw("    [Y] Keep existing key")
		w.writer.Raw("    [n] Replace with a new key")
		w.writer.Prompt(">")

		keep, err := w.prompter.YesNo(true)
		if err != nil {
			if errors.Is(err, ErrAborted) {
				return err
			}
			w.writer.ErrorLine(err.Error())
			return nil
		}
		if keep {
			w.cfg.LLM.Provider = "openai"
			w.cfg.LLM.APIKeyFile = keyPath
			w.cfg.LLM.Models.Low = "gpt-4o-mini"
			w.cfg.LLM.Models.Medium = "gpt-4o-mini"
			w.cfg.LLM.Models.High = "gpt-4o"
			w.writer.Check(fmt.Sprintf("Using existing key: %s", keyPath))
			return w.cfgCapsPrompt(ctx)
		}
	}

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

	// keyPath already resolved at top of function. writeWithRollback
	// preserves an existing key file across Ctrl+C on --force re-runs
	// (restores the pre-existing content; removes the new file only
	// when none existed before).
	if err := w.writeWithRollback(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("write key file: %w", err)
	}
	w.writer.Check(fmt.Sprintf("Saved to %s (0600 perms)", keyPath))

	w.cfg.LLM.Provider = "openai"
	w.cfg.LLM.APIKeyFile = keyPath
	// Replace the Anthropic-named tier defaults with OpenAI ones so
	// every call site (rerank, decompose, curation tasks) resolves to
	// a model the OpenAI API recognizes.
	w.cfg.LLM.Models.Low = "gpt-4o-mini"
	w.cfg.LLM.Models.Medium = "gpt-4o-mini"
	w.cfg.LLM.Models.High = "gpt-4o"
	return w.cfgCapsPrompt(ctx)
}

// awsVerifier is the contract verifyAWSProfile satisfies. Hoisted
// to a function-typed field on Wizard so tests can stub it without
// touching real AWS state. Returns (resolvedIdentity, nil) on
// success or (zero, err) on any failure during config load or the
// STS round-trip.
type awsVerifier func(ctx context.Context, region, profile string) (callerIdentity, error)

// llmBedrock handles AWS Bedrock with Anthropic models. Asks for
// profile and region, then verifies the credentials via
// sts.GetCallerIdentity so the user sees exactly which AWS principal
// Gramaton will use before the config gets persisted. The verify-and-
// display step is the load-bearing trust signal: SSO sessions
// expire, profiles get renamed, regions get typo'd. Catching those
// at wizard time (with a clear retry path) is much better than
// curation cycles later silently logging credential errors.
func (w *Wizard) llmBedrock(ctx context.Context) error {
	var profile, region string
	for {
		w.writer.Blank()
		w.writer.Paragraph(
			"Which AWS profile should Gramaton use?",
			"(Leave blank to use the default credential chain -- env vars, IMDS, SSO, etc.)",
		)
		w.writer.Prompt(">")
		p, err := w.prompter.Text("")
		if err != nil {
			return err
		}
		profile = p

		w.writer.Paragraph(
			"Which AWS region? Gramaton reaches Anthropic models through",
			"Bedrock cross-region inference profiles, so any commercial",
			"region works (e.g. us-east-1, us-west-2, eu-west-1,",
			"eu-central-1, ap-northeast-1, ap-southeast-2).",
		)
		w.writer.Prompt(">")
		r, err := w.prompter.Text("us-west-2")
		if err != nil {
			return err
		}
		region = r

		// Verify credentials via STS GetCallerIdentity. Every IAM
		// principal has sts:GetCallerIdentity by default, so this
		// works even when the caller's role has zero attached
		// policies beyond its assume-role trust.
		w.writer.Blank()
		w.writer.Raw("    Verifying AWS credentials...")
		slog.Debug("wizard: verifying bedrock credentials",
			"component", "setup",
			"profile_set", profile != "",
			"region", region)
		identity, vErr := w.awsVerifier(ctx, region, profile)
		if vErr != nil {
			slog.Warn("wizard: bedrock credential verification failed",
				"component", "setup",
				"profile_set", profile != "",
				"region", region,
				"err", vErr)
			w.writer.Blank()
			w.writer.Warn(fmt.Sprintf("Could not verify AWS credentials: %v", vErr))
			w.writer.Paragraph(
				"Common fixes:",
				"  - SSO session expired: run `aws sso login --profile <profile>`",
				"  - Profile not found: check `~/.aws/config` and `~/.aws/credentials`",
				"  - Region mismatch: confirm the profile has access in this region",
			)
			retry, err := w.askRetryAWS()
			if err != nil {
				return err
			}
			if retry {
				continue
			}
			// User chose not to retry; fall through and persist the
			// config with what they entered. Curation cycles will
			// log credential errors when they hit them.
			break
		}
		slog.Info("wizard: bedrock credentials verified",
			"component", "setup",
			"account", identity.account,
			"principal", identity.arn,
			"region", region,
			"profile_set", profile != "")
		w.writer.Blank()
		w.writer.Check("AWS credentials verified.")
		w.writer.Raw(fmt.Sprintf("    Account:    %s", identity.account))
		w.writer.Raw(fmt.Sprintf("    Principal:  %s", identity.arn))
		w.writer.Raw(fmt.Sprintf("    Region:     %s", region))
		break
	}

	w.cfg.LLM.Provider = "bedrock"
	w.cfg.LLM.Region = region
	w.cfg.LLM.AWSProfile = profile
	// Newer Claude models on Bedrock reject the base foundation-model
	// ID with ResourceNotFoundException in most regions; invocation
	// goes through a cross-region inference profile whose ID is the
	// base model ID with a geography prefix (us. / eu. / jp. / au. /
	// global.). Derive the prefix from the region the user just gave
	// us so the persisted config works on the first LLM call. Users
	// who hand-edit config.yaml keep whatever ID they enter verbatim;
	// this only shapes the wizard's defaults.
	prefix := bedrockGeoPrefix(region)
	w.cfg.LLM.Models.Low = prefix + bedrockModelLow
	w.cfg.LLM.Models.Medium = prefix + bedrockModelMedium
	w.cfg.LLM.Models.High = prefix + bedrockModelHigh

	w.writer.Blank()
	w.writer.Check("Bedrock with Anthropic models configured.")
	if profile != "" {
		w.writer.Check(fmt.Sprintf("  AWS profile: %s", profile))
	}
	w.writer.Check(fmt.Sprintf("  Region: %s", region))
	return w.cfgCapsPrompt(ctx)
}

// Base bedrock-runtime model IDs for the wizard's Low/Medium/High
// Bedrock defaults. Verified 2026-07-02 against the per-model pages
// under the AWS "models at a glance" catalog:
//
//	https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-haiku-4-5.html
//	https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-4-6.html
//	https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-8.html
//
// These are never written to config as-is: llmBedrock prepends the
// geography prefix from bedrockGeoPrefix, because these models are
// invocable in most regions only through cross-region inference
// profiles (base ID rejected with ResourceNotFoundException).
//
// All three models offer the same profile family (us., eu., jp.,
// au., global.), but their documented SOURCE-REGION lists differ per
// model, so bedrockGeoPrefix only maps a region to a geography when
// that region is a documented source for ALL THREE tiers (the
// asymmetric regions -- ca-west-1, ap-southeast-6 -- fall back to
// global., see below). Claude Sonnet 5 was considered for Medium and
// rejected: as of the verification date its only documented geo
// profile is us. (plus global.), so a region-derived eu./jp./au.
// prefix would fabricate an ID that does not exist.
const (
	bedrockModelLow    = "anthropic.claude-haiku-4-5-20251001-v1:0"
	bedrockModelMedium = "anthropic.claude-sonnet-4-6"
	bedrockModelHigh   = "anthropic.claude-opus-4-8"
)

// bedrockGeoPrefix maps an AWS region to the cross-region inference
// profile prefix to prepend to the wizard's default Claude model IDs.
// The mapping mirrors the documented source regions of each geography
// profile on the model pages cited above (verified 2026-07-02):
//
//	us. -- us-east-1, us-east-2, us-west-1, us-west-2, ca-central-1
//	eu. -- eu-central-1, eu-central-2, eu-north-1, eu-south-1,
//	       eu-south-2, eu-west-1, eu-west-2, eu-west-3
//	jp. -- ap-northeast-1 (Tokyo), ap-northeast-3 (Osaka)
//	au. -- ap-southeast-2 (Sydney), ap-southeast-4 (Melbourne)
//
// There is no blanket Asia-Pacific ("apac.") profile for these
// models; Japan and Australia geographies are separate, and the
// remaining ap-* regions (Singapore, Seoul, Mumbai, ...) have no geo
// profile at all. Everything unmatched falls back to the global
// profile: the AWS docs mark Global as available from every
// commercial region listed on those pages, so global. is the
// only documented answer for regions outside a geography (and the
// least-wrong guess for regions we don't recognize). Known
// limitation: partition regions absent from the commercial catalog
// (us-gov-*, cn-*) are not served by these models per the same
// pages, so no prefix would help there; the wizard's credential
// verification and the bedrock client's error hints surface that
// case.
func bedrockGeoPrefix(region string) string {
	switch {
	case strings.HasPrefix(region, "us-gov-"):
		// Not part of the commercial catalog; fall back to the
		// documented default rather than claiming a us. mapping the
		// docs don't support.
		return "global."
	case strings.HasPrefix(region, "us-"):
		return "us."
	case region == "ca-central-1":
		// Documented source region of the US geo profile for all
		// three default models. ca-west-1 is deliberately absent: it
		// is not a documented US-geo source for Claude Haiku 4.5
		// (the Low tier), so it takes the global fallback instead.
		return "us."
	case strings.HasPrefix(region, "eu-"):
		return "eu."
	case region == "ap-northeast-1", region == "ap-northeast-3":
		return "jp."
	case region == "ap-southeast-2", region == "ap-southeast-4":
		// ap-southeast-6 (New Zealand) is deliberately absent: it is
		// a documented AU-geo source for Claude Haiku 4.5 and
		// Sonnet 4.6 but NOT for Opus 4.8 (the High tier), so it
		// takes the global fallback instead -- same tier asymmetry as
		// ca-west-1 above.
		return "au."
	default:
		return "global."
	}
}

// callerIdentity is the small subset of sts.GetCallerIdentityOutput
// the wizard cares about. Pulling it out keeps verifyAWSProfile
// independent of the SDK type system at the call site.
type callerIdentity struct {
	account string
	arn     string
}

// verifyAWSProfile loads the AWS config for (region, profile) and
// calls sts:GetCallerIdentity. Returns the resolved account + ARN
// on success. The default awsVerifier on every Wizard; tests
// replace it via the awsVerifier field on Wizard.
func verifyAWSProfile(ctx context.Context, region, profile string) (callerIdentity, error) {
	slog.Debug("aws: loading config for verification",
		"component", "awscfg",
		"region", region,
		"profile_set", profile != "")
	cfg, err := awscfg.Load(ctx, region, profile, "", "")
	if err != nil {
		return callerIdentity{}, fmt.Errorf("load AWS config: %w", err)
	}
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return callerIdentity{}, err
	}
	id := callerIdentity{}
	if out.Account != nil {
		id.account = *out.Account
	}
	if out.Arn != nil {
		id.arn = *out.Arn
	}
	return id, nil
}

// askRetryAWS asks the user whether to re-enter the profile/region
// after a credential verification failure. Defaults to yes; on no,
// the wizard persists whatever the user entered and continues.
func (w *Wizard) askRetryAWS() (bool, error) {
	w.writer.Blank()
	w.writer.Paragraph("Re-enter profile and region?")
	w.writer.Prompt("[Y/n] >")
	return w.prompter.YesNo(true)
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
	w.cfg.LLM.Rerank.Enabled = true

	// Set the defaults. These values are chosen to be "safety net, not
	// budget" -- most users will spend far less at typical usage.
	// Anchors derived from: Haiku @ ~$0.005/call x 20 calls/cycle max
	// x 60 cycles/hour = ~$6/hour absolute worst case, which is well
	// below $5/day because most cycles are idle.
	w.cfg.LLM.CostLimits.MaxCostUSDPerDay = 5.00
	w.cfg.LLM.CostLimits.MaxCallsPerDay = 500
	w.cfg.LLM.CostLimits.MaxCostUSDPerRun = 1.00

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
	w.writer.Prompt(fmt.Sprintf("Max USD per day (default $%.2f):", w.cfg.LLM.CostLimits.MaxCostUSDPerDay))
	day, err := w.prompter.Text(fmt.Sprintf("%.2f", w.cfg.LLM.CostLimits.MaxCostUSDPerDay))
	if err != nil {
		return err
	}
	// Best-effort parse: on success apply, on failure print a
	// user-visible warn explaining why we kept the default. Silent
	// fallback would leave users thinking their value took effect.
	if v, parseErr := parseMoneyUSD(day); parseErr == nil && v > 0 {
		w.cfg.LLM.CostLimits.MaxCostUSDPerDay = v
	} else if day != fmt.Sprintf("%.2f", w.cfg.LLM.CostLimits.MaxCostUSDPerDay) && parseErr != nil {
		w.writer.Warn(fmt.Sprintf("Invalid USD/day value: %v. Keeping default $%.2f.", parseErr, w.cfg.LLM.CostLimits.MaxCostUSDPerDay))
	}

	w.writer.Prompt(fmt.Sprintf("Max API calls per day (default %d):", w.cfg.LLM.CostLimits.MaxCallsPerDay))
	calls, err := w.prompter.Text(fmt.Sprintf("%d", w.cfg.LLM.CostLimits.MaxCallsPerDay))
	if err != nil {
		return err
	}
	if v, parseErr := parseIntAtLeast(calls, 1); parseErr == nil {
		w.cfg.LLM.CostLimits.MaxCallsPerDay = v
	} else if calls != fmt.Sprintf("%d", w.cfg.LLM.CostLimits.MaxCallsPerDay) {
		w.writer.Warn(fmt.Sprintf("Invalid calls/day value: %v. Keeping default %d.", parseErr, w.cfg.LLM.CostLimits.MaxCallsPerDay))
	}

	w.writer.Prompt(fmt.Sprintf("Max USD per curation cycle (default $%.2f):", w.cfg.LLM.CostLimits.MaxCostUSDPerRun))
	run, err := w.prompter.Text(fmt.Sprintf("%.2f", w.cfg.LLM.CostLimits.MaxCostUSDPerRun))
	if err != nil {
		return err
	}
	if v, parseErr := parseMoneyUSD(run); parseErr == nil && v > 0 {
		w.cfg.LLM.CostLimits.MaxCostUSDPerRun = v
	} else if run != fmt.Sprintf("%.2f", w.cfg.LLM.CostLimits.MaxCostUSDPerRun) && parseErr != nil {
		w.writer.Warn(fmt.Sprintf("Invalid USD/cycle value: %v. Keeping default $%.2f.", parseErr, w.cfg.LLM.CostLimits.MaxCostUSDPerRun))
	}

	w.writer.Check(fmt.Sprintf(
		"Caps set: $%.2f/day, %d calls/day, $%.2f/cycle",
		w.cfg.LLM.CostLimits.MaxCostUSDPerDay,
		w.cfg.LLM.CostLimits.MaxCallsPerDay,
		w.cfg.LLM.CostLimits.MaxCostUSDPerRun,
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
