package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/server"
)

// preflightCmd answers "is my Gramaton install healthy?". Pairs with
// `gramaton init`: init sets up first-time, preflight verifies. Scope
// is the install/environment (config files, providers, MCP
// registration, hook scripts, recent log errors). Store-data
// integrity is covered by `gramaton validate` / `repair` / `reembed`
// — different layer.
//
// Output:
//
//   ✓  passed
//   ⚠  warning (won't break Gramaton, but worth noting)
//   ✗  error  (something's broken; remediation follows)
//   ○  skipped (irrelevant to this install)
//
// Exits non-zero if any check returned ✗ so CI can gate on a clean run.
var preflightCmd = &cobra.Command{
	Use:   "preflight",
	Short: "Verify the Gramaton install is healthy",
	Long: `Walks a series of environment checks and reports the state of each
in plain English. Complements gramaton init (which sets things up):
preflight tells you whether what got set up is still working.

Checks include:
  - Config file: present, parseable, owner-only perms (Unix)
  - Data directory: exists and writable
  - Server: running and PID alive (or cleanly not running)
  - Embedding provider: configured + key file present (if cloud)
  - LLM provider: configured + key file present + perms (if local)
  - MCP registration: gramaton entry in Claude Code's config
  - Hooks: installed scripts and exec bits per client
  - Recent errors: scan tail of ~/.gramaton/gramaton.log

Each warning or error includes a one-line remediation. Exits non-zero
on any error so you can wire this into pre-flight scripts or CI.`,
	RunE: runPreflight,
}

func init() {
	rootCmd.AddCommand(preflightCmd)
}

// preflightStatus categorizes a single check result. Maps to a glyph
// in printResult; also gates the exit code.
type preflightStatus int

const (
	statusOK preflightStatus = iota
	statusWarn
	statusError
	statusSkip
)

// preflightResult is what each check returns. name + message render
// regardless; remediation only shows on warn/error.
type preflightResult struct {
	name        string
	status      preflightStatus
	message     string
	remediation string
}

func runPreflight(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	cfgDir := configDir()
	cfgPath := filepath.Join(cfgDir, "config.yaml")

	// Try to load config; checks downstream branch on whether it loaded.
	var cfg config.Config
	cfgErr := error(nil)
	if _, err := os.Stat(cfgPath); err == nil {
		cfg, cfgErr = config.Load(cfgPath)
	} else {
		cfgErr = err
	}

	// Run checks in a stable order. Config check runs first so users
	// see "config not found / unparseable" before downstream checks
	// noise the output up.
	var results []preflightResult
	results = append(results, checkPreflightConfig(cfgPath, cfgErr))
	if cfgErr == nil {
		results = append(results, checkPreflightDataDir(cfg))
		results = append(results, checkPreflightEmbedding(cfg))
		results = append(results, checkPreflightLLM(cfg))
	}
	results = append(results, checkPreflightServer(cfgDir))
	results = append(results, checkPreflightMCP(ctx)...)
	if cfgErr == nil {
		results = append(results, checkPreflightHooks(cfgDir)...)
	}
	results = append(results, checkPreflightRecentErrors(cfgDir))

	fmt.Fprintln(cmd.OutOrStdout(), "Gramaton preflight")
	fmt.Fprintln(cmd.OutOrStdout())
	var warnCount, errCount int
	for _, r := range results {
		printPreflightResult(cmd.OutOrStdout(), r)
		switch r.status {
		case statusWarn:
			warnCount++
		case statusError:
			errCount++
		}
	}
	fmt.Fprintln(cmd.OutOrStdout())

	switch {
	case errCount > 0:
		fmt.Fprintf(cmd.OutOrStdout(), "Result: %d error(s), %d warning(s)\n", errCount, warnCount)
		// Returning a generic error makes cobra non-zero-exit. We
		// don't want cobra to print "Error: ..." after our pretty
		// output, so SilenceErrors is set on rootCmd; the exit code
		// is what matters.
		return fmt.Errorf("preflight failed")
	case warnCount > 0:
		fmt.Fprintf(cmd.OutOrStdout(), "Result: ok with %d warning(s)\n", warnCount)
	default:
		fmt.Fprintln(cmd.OutOrStdout(), "Result: all checks passed")
	}
	return nil
}

func printPreflightResult(w io.Writer, r preflightResult) {
	var icon string
	switch r.status {
	case statusOK:
		icon = "✓"
	case statusWarn:
		icon = "⚠"
	case statusError:
		icon = "✗"
	case statusSkip:
		icon = "○"
	}
	fmt.Fprintf(w, "  %s %s: %s\n", icon, r.name, r.message)
	if r.remediation != "" && (r.status == statusWarn || r.status == statusError) {
		fmt.Fprintf(w, "      → %s\n", r.remediation)
	}
}

// --- check functions ---

func checkPreflightConfig(cfgPath string, loadErr error) preflightResult {
	if os.IsNotExist(loadErr) {
		return preflightResult{
			name:        "Config",
			status:      statusError,
			message:     fmt.Sprintf("not found at %s", cfgPath),
			remediation: "Run `gramaton init` to create one.",
		}
	}
	if loadErr != nil {
		return preflightResult{
			name:        "Config",
			status:      statusError,
			message:     fmt.Sprintf("failed to parse: %v", loadErr),
			remediation: "Fix YAML syntax, or reset with `gramaton init --force`.",
		}
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(cfgPath); err == nil {
			if mode := info.Mode().Perm(); mode != 0o600 {
				return preflightResult{
					name:        "Config",
					status:      statusWarn,
					message:     fmt.Sprintf("loaded, but perms are %o (expected 0600)", mode),
					remediation: fmt.Sprintf("chmod 600 %s", cfgPath),
				}
			}
		}
	}
	return preflightResult{name: "Config", status: statusOK, message: cfgPath}
}

func checkPreflightDataDir(cfg config.Config) preflightResult {
	info, err := os.Stat(cfg.DataDir)
	if err != nil || !info.IsDir() {
		return preflightResult{
			name:        "Data directory",
			status:      statusError,
			message:     fmt.Sprintf("missing or not a directory: %s", cfg.DataDir),
			remediation: fmt.Sprintf("mkdir -p %s, or re-run `gramaton init`.", cfg.DataDir),
		}
	}
	// Writability test: drop a tiny file and remove it. Real runtime
	// writes are far more elaborate, but if we can't even create one
	// byte, we'd fail anywhere downstream.
	testPath := filepath.Join(cfg.DataDir, ".preflight-write-test")
	if err := os.WriteFile(testPath, []byte{}, 0o600); err != nil {
		return preflightResult{
			name:        "Data directory",
			status:      statusError,
			message:     fmt.Sprintf("not writable: %v", err),
			remediation: fmt.Sprintf("chmod u+w %s, or check disk space.", cfg.DataDir),
		}
	}
	_ = os.Remove(testPath)
	return preflightResult{name: "Data directory", status: statusOK, message: cfg.DataDir}
}

func checkPreflightEmbedding(cfg config.Config) preflightResult {
	switch cfg.Embedding.Provider {
	case "bert":
		return preflightResult{name: "Embedding", status: statusOK, message: "pure-Go BERT (local, no external cost)"}
	case "ollama":
		return preflightResult{name: "Embedding", status: statusOK, message: "Ollama (local)"}
	case "openai":
		if cfg.Embedding.APIKeyFile == "" {
			return preflightResult{
				name:        "Embedding",
				status:      statusWarn,
				message:     "OpenAI configured but api_key_file is empty",
				remediation: "Re-run `gramaton init --force` to set the key file.",
			}
		}
		if _, err := os.Stat(cfg.Embedding.APIKeyFile); err != nil {
			return preflightResult{
				name:        "Embedding",
				status:      statusError,
				message:     fmt.Sprintf("OpenAI key file missing: %s", cfg.Embedding.APIKeyFile),
				remediation: "Re-run `gramaton init --force` to re-save the key.",
			}
		}
		return preflightResult{name: "Embedding", status: statusOK, message: "OpenAI (cloud, key file present)"}
	case "bedrock":
		return preflightResult{name: "Embedding", status: statusOK, message: "AWS Bedrock (cloud)"}
	case "":
		return preflightResult{
			name:        "Embedding",
			status:      statusWarn,
			message:     "disabled (BM25 keyword search still works; semantic search unavailable)",
			remediation: "Run `gramaton init --force` and pick a provider in Step 1.",
		}
	}
	return preflightResult{
		name:    "Embedding",
		status:  statusWarn,
		message: fmt.Sprintf("unknown provider %q", cfg.Embedding.Provider),
	}
}

func checkPreflightLLM(cfg config.Config) preflightResult {
	if cfg.LLM.Provider == "" {
		return preflightResult{
			name:        "LLM",
			status:      statusWarn,
			message:     "not configured (curation runs in deterministic-only mode)",
			remediation: "Run `gramaton init --force` and configure an LLM in Step 2.",
		}
	}
	switch cfg.LLM.Provider {
	case "anthropic", "openai":
		if cfg.LLM.APIKeyFile != "" {
			info, err := os.Stat(cfg.LLM.APIKeyFile)
			if err != nil {
				return preflightResult{
					name:        "LLM",
					status:      statusError,
					message:     fmt.Sprintf("api_key_file missing: %s", cfg.LLM.APIKeyFile),
					remediation: "Re-run `gramaton init --force` and re-save the key.",
				}
			}
			if runtime.GOOS != "windows" {
				if mode := info.Mode().Perm(); mode != 0o600 {
					return preflightResult{
						name:        "LLM",
						status:      statusWarn,
						message:     fmt.Sprintf("%s key file perms are %o (expected 0600)", cfg.LLM.Provider, mode),
						remediation: fmt.Sprintf("chmod 600 %s", cfg.LLM.APIKeyFile),
					}
				}
			}
			return preflightResult{name: "LLM", status: statusOK, message: fmt.Sprintf("%s (key file present)", cfg.LLM.Provider)}
		}
		if cfg.LLM.APIKeyEnv != "" {
			if os.Getenv(cfg.LLM.APIKeyEnv) == "" {
				return preflightResult{
					name:        "LLM",
					status:      statusWarn,
					message:     fmt.Sprintf("%s configured but env var %s is not set", cfg.LLM.Provider, cfg.LLM.APIKeyEnv),
					remediation: fmt.Sprintf("export %s=...", cfg.LLM.APIKeyEnv),
				}
			}
			return preflightResult{name: "LLM", status: statusOK, message: fmt.Sprintf("%s (via env var %s)", cfg.LLM.Provider, cfg.LLM.APIKeyEnv)}
		}
		return preflightResult{
			name:        "LLM",
			status:      statusError,
			message:     fmt.Sprintf("%s configured but no key source (api_key_file / api_key_env)", cfg.LLM.Provider),
			remediation: "Re-run `gramaton init --force`.",
		}
	case "bedrock":
		if cfg.LLM.Region == "" {
			return preflightResult{
				name:        "LLM",
				status:      statusWarn,
				message:     "Bedrock configured but region is empty",
				remediation: "Set llm.region in config.yaml.",
			}
		}
		return preflightResult{name: "LLM", status: statusOK, message: fmt.Sprintf("Bedrock + Anthropic (%s)", cfg.LLM.Region)}
	}
	return preflightResult{name: "LLM", status: statusOK, message: fmt.Sprintf("%s (custom provider)", cfg.LLM.Provider)}
}

func checkPreflightServer(cfgDir string) preflightResult {
	info, err := server.ReadServerInfo(cfgDir)
	if err != nil {
		// server.json missing → server cleanly stopped or never started.
		// Not an error; just informational.
		return preflightResult{
			name:        "Server",
			status:      statusWarn,
			message:     "not running",
			remediation: "Run `gramaton serve` to start it.",
		}
	}
	if !server.IsProcessAlive(info.PID) {
		return preflightResult{
			name:        "Server",
			status:      statusWarn,
			message:     fmt.Sprintf("server.json points at PID %d but the process isn't alive", info.PID),
			remediation: fmt.Sprintf("Run `gramaton serve` to start, or `rm %s/server.json` to clear stale info.", cfgDir),
		}
	}
	return preflightResult{
		name:    "Server",
		status:  statusOK,
		message: fmt.Sprintf("running (PID %d, port %d)", info.PID, info.Port),
	}
}

func checkPreflightMCP(ctx context.Context) []preflightResult {
	var results []preflightResult

	// Claude Code: shell out to `claude mcp list` and look for the
	// gramaton entry. Same approach used by step_verify; relying on
	// the user's actual config rather than guessing.
	claudeBin, err := exec.LookPath("claude")
	if err == nil {
		out, err := exec.CommandContext(ctx, claudeBin, "mcp", "list").CombinedOutput()
		if err != nil {
			results = append(results, preflightResult{
				name:        "MCP (Claude Code)",
				status:      statusWarn,
				message:     "couldn't run `claude mcp list` to verify",
				remediation: "Try the command manually. Re-run `gramaton init --force` to re-register if needed.",
			})
		} else {
			found := false
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "gramaton:") {
					found = true
					break
				}
			}
			if found {
				results = append(results, preflightResult{
					name:    "MCP (Claude Code)",
					status:  statusOK,
					message: "gramaton registered",
				})
			} else {
				results = append(results, preflightResult{
					name:        "MCP (Claude Code)",
					status:      statusWarn,
					message:     "gramaton not registered with Claude Code",
					remediation: "Run `claude mcp add --scope user gramaton gramaton -- mcp` or re-run `gramaton init --force`.",
				})
			}
		}
	}
	// Kiro CLI: would need a `kiro mcp list` equivalent; not surveyed
	// here because Kiro's list-output format isn't in our corpus.
	// Same omission as step_verify.

	return results
}

func checkPreflightHooks(cfgDir string) []preflightResult {
	var results []preflightResult
	for _, client := range []string{"claude-code", "kiro"} {
		dir := filepath.Join(cfgDir, "hooks", client)
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Not present is fine — user may not use that client.
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
			// Exec-bit check is Unix-only (Windows uses PATHEXT, not
			// mode bits). Same gate as step_verify.
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
				nonExec++
			}
		}
		if count == 0 {
			continue
		}
		if nonExec > 0 {
			results = append(results, preflightResult{
				name:        fmt.Sprintf("Hooks (%s)", client),
				status:      statusWarn,
				message:     fmt.Sprintf("%d script(s) installed, %d not executable", count, nonExec),
				remediation: fmt.Sprintf("chmod +x %s/*.sh", dir),
			})
			continue
		}
		results = append(results, preflightResult{
			name:    fmt.Sprintf("Hooks (%s)", client),
			status:  statusOK,
			message: fmt.Sprintf("%d script(s) installed", count),
		})
	}
	return results
}

// checkPreflightRecentErrors scans the tail of gramaton.log for ERROR
// lines. Cheap heuristic — we don't try to age them out by timestamp;
// any ERROR in the last ~64 KB of log is reported. Tightening to "last
// 24h" needs a parsed timestamp, which is a follow-up.
func checkPreflightRecentErrors(cfgDir string) preflightResult {
	logPath := filepath.Join(cfgDir, "gramaton.log")
	info, err := os.Stat(logPath)
	if err != nil {
		return preflightResult{name: "Recent log errors", status: statusOK, message: "no log file (server hasn't run yet)"}
	}
	if info.Size() == 0 {
		return preflightResult{name: "Recent log errors", status: statusOK, message: "log is empty"}
	}

	const tailBytes = 64 * 1024
	f, err := os.Open(logPath)
	if err != nil {
		return preflightResult{
			name:        "Recent log errors",
			status:      statusWarn,
			message:     fmt.Sprintf("couldn't open %s: %v", logPath, err),
			remediation: fmt.Sprintf("ls -la %s", logPath),
		}
	}
	defer f.Close()
	if info.Size() > tailBytes {
		_, _ = f.Seek(info.Size()-tailBytes, 0)
	}
	data, _ := io.ReadAll(f)

	errCount := 0
	for _, line := range strings.Split(string(data), "\n") {
		// Match Go's slog "level=ERROR" + JSON "level":"ERROR" + the
		// component=server "level":"ERROR" form, all of which contain
		// the literal "ERROR" (or `"ERROR"`). False-positive on log
		// lines with the literal word ERROR in their message is
		// possible; minor cost for the simplicity.
		if strings.Contains(line, `"level":"ERROR"`) || strings.Contains(line, "level=ERROR") {
			errCount++
		}
	}
	if errCount > 0 {
		return preflightResult{
			name:        "Recent log errors",
			status:      statusWarn,
			message:     fmt.Sprintf("%d ERROR line(s) in tail of %s", errCount, logPath),
			remediation: fmt.Sprintf("tail -200 %s | grep ERROR", logPath),
		}
	}
	return preflightResult{name: "Recent log errors", status: statusOK, message: "no errors in tail of log"}
}
