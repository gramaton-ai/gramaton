## Knowledge Store (Gramaton — read-only)

A read-only Gramaton store is attached to this machine. It was
shared as a frozen artifact: everything in it can be searched and
read, and nothing is ever saved to it. No session capture, no
memory saves, no curation, no edits of any kind.

**What works (the read surface):**

- `gramaton_search` — ranked semantic + keyword retrieval across
  the store. Filters, sorting, and pagination all work normally.
- `gramaton_inspect` — one record plus its related edges in a
  single call. Use for ULIDs and ticket/decision codenames.
- `gramaton_explore` — graph traversal from a node; returns
  connected nodes and edges within a depth.
- `gramaton_collection_list` / `gramaton_collection_items` /
  `gramaton_collection_schema` — browse collections; items are
  returned exhaustively.
- `gramaton_history` — a record's version timeline (what changed,
  when, by whom); `gramaton_history_search` — lexical search over
  past versions; `gramaton_inspect` accepts `as_of` for
  point-in-time reads.
- `gramaton_stats`, `gramaton_status`, `gramaton_log`,
  `gramaton_diff`, `gramaton_duplicates`, `gramaton_pending`,
  `gramaton_session_get`, `gramaton_jobs_list` — store overview,
  commit log, and diagnostics.
- `gramaton_backup` — export the frozen store; sharing a copy is
  the one "write" a read-only store supports (the archive lands
  outside the store).
- `gramaton_guide` — the live reference for how Gramaton works.

**What is not available:** every write tool. `gramaton_save`, the
session tools (`gramaton_session_prepare`, `gramaton_session_save`),
collection writes, curation, link/update/resolve — none of them are
registered for this store, so they will not appear in your tool
list, and any write that reaches the server is rejected. Do not
queue content to "save later"; there is nowhere in this store to
save it. If the user asks to remember or store something here, say
that this store is read-only and cannot be written to.

Treat the store as reference material: search it before answering
questions about the decisions, context, and knowledge it covers.
Retrieval is the entire point of a shared store — an empty search
costs seconds, while a missed lookup rebuilds reasoning its
publisher already did.

Gramaton is accessed via MCP tools. If the tools appear
unavailable, tell the user the MCP server looks disconnected and
ask them to reconnect ({{mcp_reconnect_hint}}, or confirm the
store's `gramaton mcp` entry is configured). The CLI fallback is
read-only too:
`gramaton --store {{store_name}} search "<query>" --top 5`.
