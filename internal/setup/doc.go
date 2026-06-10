// Package setup implements the interactive first-run wizard for Gramaton
// and the non-interactive bootstrap fallback that both entry points
// (`gramaton init` in a terminal, `gramaton init --non-interactive` in
// scripts/CI) share.
//
// # Why a wizard at all
//
// The pre-OSS target audience is "tech-capable users" — developers who
// can run `go install`. Even for them, a complete Gramaton install wires
// together five moving parts: config file, data directory, embedding
// model, LLM provider + API key, MCP client registration, automatic-
// capture hooks. A bare `gramaton init` that drops out after creating
// the data dir leaves four of those five steps undocumented in the
// quickstart and invisible in `--help`. New users end up with a
// half-wired install and assume Gramaton "doesn't really work."
//
// The wizard walks the user through each of those steps once, with
// a plain-English explanation at every decision point, auto-detection
// where it's possible (MCP clients, installed tools), and a validation
// pass at the end. It's the single highest-leverage UX investment we
// can make before the first public push.
//
// Decision captured in Memory record "Gramaton pre-OSS target audience
// and wave-prioritization decision" (2026-04-22). The wizard is an
// explicit Wave-1 requirement; no-code/low-code polish (pre-built
// binaries, install script, GUI) is Wave-2+ and explicitly deferred.
//
// # Why `internal/setup/` (not `cli/wizard/`)
//
// The wizard is a sequence of pure operations on config, filesystem,
// and the MCP-client's config files. None of it is CLI-flag-specific.
// Putting it in an internal package (a) keeps cli/ thin, (b) makes
// every step unit-testable with mock stdin/stdout, (c) leaves the door
// open for a future non-CLI driver (gramaton doctor --fix could
// reuse the MCP injection logic; a future desktop app, if one ever
// ships, could drive the same setup package).
//
// The trade-off: an extra package boundary. Worth it for the testability
// alone. No shared state with cli/; imports go one way (cli -> setup).
//
// # Wizard shape
//
// The public entry point is Run. It expects a Prompter and a Writer
// injected from the caller; cli/ passes terminal-backed implementations,
// tests pass scripted mocks.
//
//	wiz := setup.New(prompter, writer, cfg, cfgPath, configDir)
//	if err := wiz.Run(ctx); err != nil { ... }
//
// Steps (see wizard.go Run):
//   0. Welcome + branch fresh-vs-import
//   1. Knowledge-store bootstrap (config, data dir, embedding provider, model download)
//   2. LLM provider (optional but strongly recommended) + API key + test call + cost caps
//   3. MCP client auto-detect (harness registry: Claude Code, kiro-cli, Codex) + config injection
//   4. Hooks installer (auto-capture) for detected clients
//   5. Verification + concrete next-steps block
//
// Each step is idempotent and safe to re-run: re-running the wizard
// against an existing install offers a menu to reconfigure individual
// parts rather than clobbering state.
//
// # Non-interactive mode
//
// When the caller detects no TTY (or the user passes --non-interactive),
// the wizard runs in a defaults-only path that completes Step 1 only
// (bootstrap + BERT model download) and prints instructions for
// completing Steps 2-4 manually. This preserves backward compatibility
// with existing `gramaton init` invocations in scripts/CI and keeps
// the --non-interactive exit behavior predictable.
package setup
