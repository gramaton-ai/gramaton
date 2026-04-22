package setup

import "context"

// stepMCP is Step 3: detect installed MCP clients (Claude Code,
// kiro-cli) and inject Gramaton into their configs.
//
// NOT YET IMPLEMENTED. This is a deliberate stub for the first-pass
// wizard commit. The design is fully captured in the backlog item
// 01KPVD3YD0RHKJJKX847H6F95J and the implementation plan below;
// this function currently just prints a placeholder message so the
// wizard flow completes end-to-end.
//
// Implementation plan for the follow-up pass:
//
//  1. Detection helpers in a new detect.go:
//     - findClaudeCodeConfig() (path string, installed bool)
//     - findKiroCliConfig() (path string, installed bool)
//     Each returns the path to the client's config file (even if
//     the file doesn't exist yet -- MCP entries can be the first
//     thing written) plus a bool for "did we detect the binary."
//     Binary detection via exec.LookPath; config paths follow each
//     client's documented convention. For kiro-cli, verify against
//     integration/kiro/ and hooks/kiro/ before committing to a path.
//
//  2. Config injection helper in a new inject.go:
//     - injectMCPServer(configPath string, serverName string, serverCfg map[string]any) error
//     Reads existing JSON (create empty if missing), preserves any
//     other mcpServers entries, adds/updates just our key, writes
//     atomically (tmp + rename). Backs up to <path>.bak-<ts>
//     before touching.
//
//  3. The "gramaton" MCP entry contents: {"command": "gramaton",
//     "args": ["mcp"]}. Relies on gramaton being on PATH; verified
//     by stepVerify afterwards.
//
//  4. User interaction:
//     - Show detected list as a checkbox-style report.
//     - Single [Y/n] "Configure these?" prompt.
//     - On confirm, inject for all detected clients.
//     - If none detected, print "no clients detected; add manually
//       later" message and continue gracefully.
//
//  5. Report ✓ per client with the specific config file edited.
//     Warn about restarts.
//
// File refs: integration/claude-code/, integration/kiro/,
// hooks/claude-code/, hooks/kiro/ are the current-behavior source
// of truth for what paths and config shapes each client expects.
func (w *Wizard) stepMCP(ctx context.Context) error {
	w.writer.StepHeader(3, totalSteps, "Connecting to your AI tools")
	w.writer.Paragraph(
		"(Auto-detect + config injection is still being built. For now,",
		"add Gramaton to your MCP client's config manually. See the",
		"README's Quick Start section for the JSON snippet.)",
	)
	return nil
}
