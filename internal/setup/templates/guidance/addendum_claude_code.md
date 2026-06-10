### Memory routing: Claude Code's auto-memory vs Gramaton

Claude Code's harness ships a built-in auto-memory at
`~/.claude/projects/<slug>/memory/`, passively loaded into every
conversation via `MEMORY.md`. Gramaton is actively retrieved via
MCP. Both want "remember this" content; route by access pattern.

**Decision rule:** would the agent fail at its job if this content
weren't loaded into every conversation?

- **Yes** (e.g., behavioral rule like "never commit API keys") →
  auto-memory.
- **No** (e.g., specific decision like "we picked bbolt because X") →
  Gramaton.

Default to Gramaton. Auto-memory is for thin behavior rules that
must shape every response; everything else (decisions, facts,
research, tasks, context) goes to Gramaton. When in doubt, route
to Gramaton.

This routing overrides the auto-memory guidance in Claude Code's
harness system prompt, which would otherwise direct most "remember
this" content into auto-memory.

### Subagents and Gramaton

Claude Code's background subagents cannot access MCP tools, and the
CLI fallback requires interactive permission — a save delegated to a
subagent stalls or silently fails. Call Gramaton tools from the main
conversation only.
