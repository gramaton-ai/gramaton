### Subagents and Gramaton

Cursor subagents inherit all tools from the parent, including
Gramaton's MCP tools, so a delegated task is able to write to the
store. Keep saves and session extraction in the main conversation,
and tell delegated tasks not to write to Gramaton: a subagent sees
only its task brief, and partial-context saves produce fragmentary
records.
