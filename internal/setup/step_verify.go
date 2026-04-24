package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gramaton-ai/gramaton/config"
)

// stepVerify is Step 5: persist the config, then run a set of health
// checks that confirm the wizard's work reached the target state on
// disk. Each check reports ✓ / ⚠ with a specific remediation hint so
// users see what's right and what needs attention.
//
// Philosophy: this step is the "did it actually work" answer the
// wizard owes the user. We lean toward over-reporting (listing each
// check individually) rather than a single "all green" summary
// because users read exactly one wizard run per install and deserve
// concrete feedback, not abstractions. A future `gramaton doctor`
// command will reuse much of this logic; the shared-logic extraction
// is left as a post-OSS cleanup.
//
// Checks performed (each is graceful — a failed check is a ⚠ line,
// not a stepVerify return error):
//   - Config file saves successfully. If this fails everything else
//     is moot, so we abort after it.
//   - Config file permissions are 0600 (key file paths live in it).
//   - Data directory exists + is writable.
//   - Embedding provider configured + materialized (BERT model file
//     present, Ollama reachable, or "skipped by design").
//   - LLM provider configured + API key file exists with 0600 perms.
//   - MCP registration survey: if `claude` is on PATH, run `claude
//     mcp list` and check the output contains a gramaton entry.
//   - Hook scripts present + executable for each detected client.
//
// Deferred to a dedicated `gramaton doctor` command (post-OSS
// backlog item): actual API test-calls for LLM/embedding providers,
// recent error scan of ~/.gramaton/gramaton.log, config-validation
// against code's Validate() rules. The wizard is short and
// optimistic; `doctor` is the long and skeptical counterpart.
func (w *Wizard) stepVerify(ctx context.Context) {
	w.writer.Section("Verification")

	// Config save: blocking. If we can't persist, the whole wizard
	// was noise.
	if err := config.Save(*w.cfg, w.cfgPath); err != nil {
		w.writer.ErrorLine(fmt.Sprintf("Failed to save config: %v", err))
		w.writer.Paragraph(
			"This is unusual; check that the config directory is writable",
			"and try re-running `gramaton init`.",
		)
		return
	}
	w.writer.Check(fmt.Sprintf("Config saved: %s", w.cfgPath))

	// Config file perms: should be 0600 because LLM.APIKeyFile paths
	// live in it. Skipped on Windows: os.Stat returns 0o666 or 0o444
	// for writable/read-only files on NTFS — the Unix mode bits are
	// a fiction maintained by the Go runtime and don't reflect the
	// real ACL-based access control.
	if runtime.GOOS == "windows" {
		w.writer.Check("Config file permissions: skipped on Windows (NTFS ACL model)")
	} else if info, err := os.Stat(w.cfgPath); err == nil {
		if mode := info.Mode().Perm(); mode != 0o600 {
			w.writer.Warn(fmt.Sprintf("Config file perms are %o, expected 0600. Fix: chmod 600 %s", mode, w.cfgPath))
		} else {
			w.writer.Check("Config file permissions: 0600")
		}
	}

	// Data directory: exists + writable. A missing data dir at this
	// point would be shocking (Step 1 created it) but worth a check.
	if info, err := os.Stat(w.cfg.DataDir); err != nil || !info.IsDir() {
		w.writer.Warn(fmt.Sprintf("Data directory missing or not a directory: %s", w.cfg.DataDir))
	} else {
		// Writability: try creating a probe file, then remove.
		probe := filepath.Join(w.cfg.DataDir, ".gramaton-writability-probe")
		if err := os.WriteFile(probe, []byte{}, 0o600); err != nil {
			w.writer.Warn(fmt.Sprintf("Data directory not writable: %v", err))
		} else {
			_ = os.Remove(probe)
			w.writer.Check(fmt.Sprintf("Data directory writable: %s", w.cfg.DataDir))
		}
	}

	// Embedding provider summary with per-provider spot check.
	w.verifyEmbedding()

	// LLM provider summary + key-file spot check.
	w.verifyLLM()

	// MCP registration survey via `claude mcp list`. Skipped if
	// claude isn't on PATH (not detected in Step 3 -> no survey to
	// do). Kiro-cli's list format isn't verified; we don't survey it.
	w.verifyMCPRegistration(ctx)

	// Hooks materialized + executable per client.
	w.verifyHooks()
}

// verifyEmbedding inspects the configured embedding provider and
// reports a matching health check. For BERT we just confirm the
// provider is set; the actual model file presence would require
// knowing bert.DefaultModel's on-disk name, which couples this
// function to embed/bert/. Left for `gramaton doctor` to own.
func (w *Wizard) verifyEmbedding() {
	switch w.cfg.Embedding.Provider {
	case "bert":
		w.writer.Check("Embedding: pure-Go BERT (local, no external cost)")
	case "ollama":
		w.writer.Check("Embedding: Ollama (local)")
	case "openai":
		if w.cfg.Embedding.APIKeyFile == "" {
			w.writer.Warn("Embedding: OpenAI configured but api_key_file is empty")
		} else if _, err := os.Stat(w.cfg.Embedding.APIKeyFile); err != nil {
			w.writer.Warn(fmt.Sprintf("Embedding: OpenAI key file missing: %s", w.cfg.Embedding.APIKeyFile))
		} else {
			w.writer.Check("Embedding: OpenAI (cloud)")
		}
	case "bedrock":
		w.writer.Check("Embedding: AWS Bedrock (cloud)")
	case "":
		w.writer.Warn("Embedding: disabled (semantic search unavailable; BM25 keyword search still works)")
	default:
		w.writer.Warn(fmt.Sprintf("Embedding: unknown provider %q", w.cfg.Embedding.Provider))
	}
}

// verifyLLM inspects the configured LLM provider and spot-checks the
// API key file if one is referenced. Does NOT make a test API call
// (that's the wizard's Step 2 job and future `gramaton doctor`
// domain); here we're just verifying the local config is coherent.
func (w *Wizard) verifyLLM() {
	switch w.cfg.LLM.Provider {
	case "":
		w.writer.Warn("LLM: not configured (curation runs in deterministic-only mode -- see Step 2 output)")
		return
	case "anthropic", "openai":
		if w.cfg.LLM.APIKeyFile != "" {
			info, err := os.Stat(w.cfg.LLM.APIKeyFile)
			if err != nil {
				w.writer.Warn(fmt.Sprintf("LLM: api_key_file missing: %s", w.cfg.LLM.APIKeyFile))
				return
			}
			if runtime.GOOS == "windows" {
				w.writer.Check(fmt.Sprintf("LLM: %s (key file present; perm check skipped on Windows, NTFS ACL model)", w.cfg.LLM.Provider))
			} else if mode := info.Mode().Perm(); mode != 0o600 {
				w.writer.Warn(fmt.Sprintf("LLM: api_key_file perms are %o, expected 0600. Fix: chmod 600 %s", mode, w.cfg.LLM.APIKeyFile))
				return
			} else {
				w.writer.Check(fmt.Sprintf("LLM: %s (key file present, 0600 perms)", w.cfg.LLM.Provider))
			}
		} else if w.cfg.LLM.APIKeyEnv != "" {
			if os.Getenv(w.cfg.LLM.APIKeyEnv) == "" {
				w.writer.Warn(fmt.Sprintf("LLM: %s configured but env var %s is not set", w.cfg.LLM.Provider, w.cfg.LLM.APIKeyEnv))
			} else {
				w.writer.Check(fmt.Sprintf("LLM: %s (via env var %s)", w.cfg.LLM.Provider, w.cfg.LLM.APIKeyEnv))
			}
		} else {
			w.writer.Warn(fmt.Sprintf("LLM: %s configured but no key source (api_key_file / api_key_env)", w.cfg.LLM.Provider))
		}
	case "bedrock":
		// Bedrock uses AWS credential chain, not a key file. Config
		// coherence here is just "region set."
		if w.cfg.LLM.Region == "" {
			w.writer.Warn("LLM: Bedrock configured but region is empty")
		} else {
			w.writer.Check(fmt.Sprintf("LLM: Bedrock + Anthropic models (%s)", w.cfg.LLM.Region))
		}
	default:
		w.writer.Check(fmt.Sprintf("LLM: %s (custom provider -- config review recommended)", w.cfg.LLM.Provider))
	}

	if w.cfg.Search.RerankEnabled {
		w.writer.Check("Search reranking: enabled (uses LLM for better top-N)")
	}
}

// verifyMCPRegistration runs `claude mcp list` and reports whether
// gramaton is registered. Skipped if `claude` isn't on PATH. Kiro-
// cli's list format is unverified, so we don't survey it.
func (w *Wizard) verifyMCPRegistration(ctx context.Context) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		// Not a warning -- user may not use Claude Code at all.
		return
	}
	out, err := exec.CommandContext(ctx, claudeBin, "mcp", "list").CombinedOutput()
	if err != nil {
		w.writer.Warn("MCP: couldn't run `claude mcp list` to verify registration")
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "gramaton:") {
			// State-neutral phrasing: this is a survey of what's
			// currently in Claude Code's config, not an action this
			// wizard just performed. An entry may be present because
			// Step 3 added it on this run, a prior wizard run added
			// it, or the user configured it manually -- all three
			// cases render the same ✓ because the user's end state
			// is the same.
			w.writer.Check("MCP: gramaton entry present in Claude Code's config")
			return
		}
	}
	w.writer.Warn("MCP: gramaton not found in Claude Code's config. If you declined Step 3 or it failed, register manually: claude mcp add --scope user gramaton gramaton -- mcp")
}

// verifyHooks walks <configDir>/hooks/<client>/ for each known
// client and confirms the expected scripts exist and are executable.
// Reports per-client ✓ or ⚠.
func (w *Wizard) verifyHooks() {
	for _, client := range []string{"claude-code", "kiro"} {
		dir := filepath.Join(w.configDir, "hooks", client)
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Not an error -- user may have skipped hook install.
			continue
		}
		count := 0
		nonExec := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".sh") && !strings.HasSuffix(name, ".cmd") {
				continue
			}
			count++
			info, err := e.Info()
			if err != nil {
				continue
			}
			// Execute-bit check is Unix-only: Windows NTFS ACLs
			// don't use the Unix mode bits, and os.Stat returns a
			// synthesized 0o666/0o444 regardless of runnability.
			// Windows associates .cmd with cmd.exe via PATHEXT,
			// not via an exec bit on the file.
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
				nonExec++
			}
		}
		if count == 0 {
			continue
		}
		if nonExec > 0 {
			w.writer.Warn(fmt.Sprintf("Hooks (%s): %d script(s) installed but %d not executable. Fix: chmod +x %s/*.sh", client, count, nonExec, dir))
		} else {
			w.writer.Check(fmt.Sprintf("Hooks (%s): %d script(s) installed and executable", client, count))
		}
	}
}
