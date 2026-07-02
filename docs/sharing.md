# Sharing read-only stores

A curated store can be frozen into a read-only artifact and handed to
someone else. Their agent can search and read it; nothing can change
it -- not tool calls, not background curation, not session capture.

## Freezing a store

```
gramaton store freeze [name]     # mark a store read-only
gramaton store thaw [name]       # make it writable again
gramaton store create <name> --read-only   # born frozen
```

With no name, the commands act on the active store (`--store` flag or
`GRAMATON_STORE`). Both refuse to run while that store's server is
up -- stop it first with `gramaton stop`.

Freezing writes a `STORE` manifest into the store's data directory:

```json
{"readonly": true, "owner": "Ada Lovelace <ada@example.com>", "published_at": "2026-07-02T18:00:00Z"}
```

The owner is the configured author identity at freeze time (see the
Author section in [configuration.md](configuration.md)). Because the
manifest lives in the data directory, it travels with every copy,
tar, and backup of the store -- a receiver cannot accidentally open
the store writable.

Thawing preserves the original `owner` and `published_at` so the
provenance of the publication survives; a later re-freeze overwrites
them. Thaw is honest, not cryptographic: anyone with the files can
thaw a store. It is a lid, not a lock.

## What read-only means

- Every logical write is rejected with a `forbidden` error: saves,
  updates, links, collection changes, session capture, curation
  triggers, branch operations, restores, imports.
- Background writers never start: the curation runner, startup
  self-heal, the access flusher, and the jobs sweeper are all gated.
- Search and inspect stop updating access counts and activation, so
  reads on a frozen store never touch the write lock at all.
- Reads work in full: search, inspect, explore, stats, history,
  collection listing, and export all behave normally. Export is how
  a frozen store is shared onward.
- Derived local caches (`indexes.db`, the vector index) remain
  writable: they are rebuilt from the graph at startup by design and
  are not part of the store's knowledge.
- Every HTTP response from a frozen store carries
  `store_readonly: true` in its envelope. MCP clients learn the
  frozen state three ways: the server instructions open with a
  read-only notice, no write tools are registered at all, and the
  `gramaton_status` tool reports `store_readonly: true`.

## Receiving a shared store

Two ways to consume a store someone sent you:

**Alongside your own store.** If you already use Gramaton (or want
to), attach the shared store as an additional named store:

```
gramaton store attach <path> [--name <name>]
```

This copies the store into your named-store directory, freezes the
local copy regardless of the artifact's own state (your copy is
read-only, ever; the original is untouched), and prints how to reach
it: `gramaton --store <name> ...`, `GRAMATON_STORE=<name>`, and the
`gramaton --store <name> mcp` form for wiring a dedicated MCP entry
into an agent harness.

**Read-only-only machine.** If the machine should have no writable
Gramaton at all, run `gramaton init` and pick the "attach a shared
read-only store" route -- the wizard's first question. That route
skips the author identity, LLM provider, and hooks steps entirely
(nothing on the machine will ever write), registers MCP entries
pointed at the attached store, and installs a read-only variant of
the agent guidance. The wizard states plainly that this is a
read-only-only integration before doing anything.

## Status and visibility

`gramaton store list` marks frozen stores; `gramaton status` reports
`store_readonly` whether or not the server is running. The read-only
badge is always derived live from the `STORE` manifest -- freezing
and thawing update every surface automatically.
