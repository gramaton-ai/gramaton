<p align="center">
  <img src=".github/gramaton-logo.jpg" alt="Gramaton" width="340">
</p>
<p align="center"><b>gram·a·ton</b> <i>/ˈɡramətɒn/</i> — from Greek <i>gramma</i> (writing) + <i>automaton</i> (self-acting). A thing that writes and remembers by itself.</p>

<p align="center">
  <a href="https://github.com/gramaton-ai/gramaton/actions/workflows/ci.yml"><img src="https://github.com/gramaton-ai/gramaton/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://go.dev/doc/install"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
</p>

**Gramaton is a versioned, epistemic memory for AI agents.** Every record
carries metadata about the knowledge it holds: how confident it is, how it
ages, whether something newer supersedes it, who wrote it, and what it
connects to. Agents read and write it over MCP: save now, retrieve months
later, and know how much to trust what comes back.

It ships as a single Go binary for Linux, macOS, and Windows. Your data is a
directory on your filesystem, and the default setup runs entirely on your
machine with no external runtime. A cloud LLM is optional but recommended:
it enables autonomous curation, which is designed to spend frugally, with
routine work on a small model and spending caps in the config. Gramaton is
alpha software: the on-disk format and tool surface are still evolving, and
breaking changes are flagged in the [CHANGELOG](CHANGELOG.md).

## What using it looks like

**Software work**

> **You, in June:** Remember that we switched the importer to streaming parse;
> the DOM parser blew past 2 GB on the big export files.
>
> **You, in August:** Why did we stop using the DOM parser?
>
> **Your agent,** after checking Gramaton: You switched to streaming parse on
> June 12 because the DOM parser exceeded 2 GB on large exports. The decision
> is durable, and "benchmark the streaming importer" is still open in your
> backlog.

**Research**

> **You, in March:** Remember that we're excluding the Hoffmann 2024 trial
> from the meta-analysis. The control arm was unblinded, and the effect size
> is an outlier.
>
> **You, in June:** Why isn't Hoffmann 2024 in our analysis set?
>
> **Your agent,** after checking Gramaton: You excluded it on March 9: the
> control arm was unblinded and its effect size was an outlier. The exclusion
> is marked well-established, and it links to the screening notes you saved
> the same week.

**Business records**

> **You, in January:** Remember that the Meridian contract auto-renews on
> May 1 with 60 days' cancellation notice, and that we decided to renegotiate
> rather than renew. Their support SLA slipped twice this year.
>
> **You, in February:** Where do we stand on Meridian?
>
> **Your agent,** after checking Gramaton: In January you decided to
> renegotiate rather than let the contract renew; the trigger was two SLA
> misses. It auto-renews May 1 with 60 days' notice, so the cancellation
> window closes March 2. Nothing supersedes the decision, and your backlog
> still has "draft renegotiation terms" open.

Each answer comes back with its provenance. Because everything is versioned,
updating a memory preserves its history rather than overwriting it. And
supersession extends this further: had any of these decisions later been
reversed, search would have returned the replacement, and the old record
would remain, marked historical and linked to its successor.

## Why Gramaton

AI tools ship with built-in memory, and general-purpose vector stores are
everywhere. Both store text and rank it by similarity, so retrieval cannot
tell whether knowledge is current, superseded, or refuted. Ask what was
decided three months ago and you get a confident answer assembled from
whatever scored highest: sometimes the decision, sometimes the draft it
replaced. And when the question is "what's still open?", ranked retrieval is
the wrong guarantee entirely; missing one item out of nine is a failure, not
a relevance tradeoff.

Gramaton is built for exactly these gaps:

- **Epistemic metadata on every record.** Confidence, temporality, epistemic
  status, knowledge type, author. A superseded decision is marked historical
  and stops competing with its replacement in search.
- **Three storage paths with different guarantees.** Ranked semantic search
  for knowledge, automatic extraction from conversations, and exhaustive
  collections for tasks. See [Three ways to store knowledge](#three-ways-to-store-knowledge).
- **Versioned by design.** Every mutation is a commit. Branch, diff, log,
  revert. You can ask what changed as easily as what is.
- **A property graph.** Typed, weighted edges connect records, and traversal
  surfaces related knowledge the query text never mentioned.
- **Automatic curation.** A background process expires stale short-lived
  records, links orphans, and consolidates duplicates. With an LLM
  configured, it also classifies new records and detects contradictions.
- **Self-hosted and portable.** One store works with every MCP-aware tool you
  use, and your other machines can reach it over your network.

## What Gramaton doesn't do

- It searches distilled knowledge, not raw transcripts. Sessions extract the
  load-bearing pieces of a conversation; full transcripts can be archived
  alongside, readable on demand but not indexed.
- It is single-user. There is no tenancy and no hosted service. The server is
  local-only by default; you can opt into reaching your own server from your
  other machines over your network. See [Remote access](#remote-access).

## What this is, and what we don't know yet

Gramaton is an experiment in whether structured epistemic metadata actually
helps agents remember well. The mechanics work: agents save, retrieve,
traverse, branch, and supersede, and curation integrates new knowledge in the
background. What remains unproven is whether agents consistently use the
structure well enough to justify its surface area.

Real usage is the only way to find out. If Gramaton works for you, that's
signal. If it falls short, [open an issue](https://github.com/gramaton-ai/gramaton/issues)
and tell us what you expected and what happened; that's louder signal.
Gramaton is a hypothesis to test, not a product to defend.

## Quick start

Requires Go 1.26+ ([install](https://go.dev/doc/install)).

```bash
go install github.com/gramaton-ai/gramaton@latest
gramaton init
```

The wizard first asks how you want to start: fresh, restore from a backup,
or attach a shared read-only store. A fresh install then sets up your author
identity, an embedding provider, an optional LLM for autonomous curation,
MCP registration for each AI tool it detects, agent usage guidance, and
automatic session capture. The default embedder is pure-Go BERT; its 130 MB
model downloads on first use. Re-running the wizard is safe; nothing gets
double-registered.

There is no step two. The server starts on demand the first time an agent or
CLI command needs it. To check the install:

```bash
gramaton preflight
```

Then restart your AI tool and try: *"Remember that we moved the team retro to
Thursdays; Monday attendance kept slipping."* In a later session, ask what
was decided about the retro.

Prefer manual setup? Add Gramaton to any MCP client:

```json
{
  "mcpServers": {
    "gramaton": {
      "command": "gramaton",
      "args": ["mcp"]
    }
  }
}
```

> **Note:** Some managed machines run endpoint security software that can
> interfere with newly built, unsigned binaries. If `gramaton` is blocked or
> slow to start on such a machine, clearing its extended attributes may help:
> `xattr -c "$(which gramaton)"` (macOS). Follow the guidance of your IT and
> security teams.

## Supported AI tools

`gramaton init` detects and configures these end to end:

| Tool | MCP registration | Usage guidance | Automatic capture |
|------|-----------|----------------|-------------------|
| Claude Code | registered via the `claude` CLI | managed block in `~/.claude/CLAUDE.md` | session lifecycle hooks |
| Codex | registered via the `codex` CLI | managed block in `~/.codex/AGENTS.md` | session lifecycle hooks |
| Cursor | written to `~/.cursor/mcp.json` | a `gramaton` skill | session lifecycle hooks |

Anything else that speaks MCP works with the manual setup above; see
[custom agent frameworks](integration/docs/custom-agents.md).

## Uninstall

`gramaton uninstall` removes everything `gramaton init` wired into your AI
tools: MCP registrations, hooks, and usage guidance. It shows what it found
and asks for confirmation before touching anything; `--dry-run` previews the
removal without applying it.

It never deletes your data. Stores, config, and API keys stay on disk, and
the final output prints where they live in case you want to remove them
yourself.

## Three ways to store knowledge

Gramaton offers three storage paths with different retrieval guarantees.
Matching the path to the question is the most important integration decision.

### Memory

For knowledge that benefits from best-match retrieval: decisions, design
rationale, research findings, preferences, domain context. Records arrive
from explicit `gramaton_save` calls, from session extraction (below), and
from bulk ingest (`gramaton ingest`). Retrieval is `gramaton_search`, which
ranks current, confident, relevant records first; superseded ones drop out
unless asked for.

### Sessions

For knowledge that emerges during conversation without anyone saying "save
this". The flow is two-phase: `gramaton_session_prepare` returns extraction
instructions and session state, then `gramaton_session_save` commits the
extracted segments. Each segment is kept as part of the conversation record
and, by default, also promoted to a Memory record for semantic search.
Exploration and dead ends can stay session-only (`promote_to_memory: false`).
With the capture hooks installed, sessions bind to your working directory
automatically and the agent is nudged to extract at natural boundaries,
including before context compaction.

### Collections

For items where missing one is a failure: tasks, action items, backlogs,
checklists, reading lists. `gramaton_collection_items` returns every item,
every time; there is no ranking and no top-N cutoff.

A collection can start from a built-in template (`backlog`, `todo`,
`reading-list`, `shopping-list`, `packing-list`, `journal`, `references`) or
from a custom schema that validates fields on every write:

```
# Ready-made shape
gramaton_collection_create(name="launch-backlog", template="backlog")

# Custom schema
gramaton_collection_create(name="trial-screening", schema={
  "fields": [
    {"name": "paper",  "type": "string", "required": true},
    {"name": "status", "type": "enum", "required": true,
     "values": ["screening", "included", "excluded"]},
    {"name": "reason", "type": "string"}
  ]})

gramaton_collection_add(collection_id="01...", fields={
  "paper": "Hoffmann 2024", "status": "excluded",
  "reason": "control arm unblinded"})

# Exhaustive: every match, no cutoff
gramaton_collection_items(collection_id="01...", filter={"status": "excluded"})
```

Field types are `string`, `number`, `boolean`, `date`, `enum`, and `enum[]`.
Items are graph nodes, so a task can be linked to the decision that spawned
it. Schemas can evolve after creation; the
[Integrator Guide](docs/integrator-guide.md) covers migration and the full
tool reference.

### Decision rule

> *Will missing one item be a failure?*
> **Yes**: Collection. **No**: Memory, saved directly or extracted from the
> session.

## Sharing a store

A store is a directory, and a curated subset of one is something you can
hand to a teammate. Suppose your agent has spent weeks building up memories
about a codebase: the decisions, the constraints, the sharp edges. That
graph is useful to more than just you.

```bash
# Preview what a carve would select, without writing anything
gramaton store create billing-notes --query "billing service" --dry-run

# Carve matching memories into a new read-only store
gramaton store create billing-notes --query "billing service" --read-only

# Top it up later with the same selection (idempotent)
gramaton store add billing-notes --query "billing service"

# Recipient: copy the directory in as a frozen named store
gramaton store attach /path/to/billing-notes
```

Carving copies records with their embeddings, edges, and identities intact,
so the new store is searchable immediately, with no re-embedding. Selections
are seeds plus closure: pick records by id, query, or collection, and
structural dependents follow automatically. Sessions never travel. Each run
reports what it copied and which edges were dropped at the selection
boundary.

Read-only is enforced by a `STORE` manifest that travels with every copy of
the directory: a frozen store rejects every knowledge write, and its MCP
surface registers only the read tools. `gramaton store freeze` and `thaw`
flip the flag on stores you own. Saves carry a set-once `author` stamp from
the `author:` config, so shared knowledge stays attributed;
`gramaton backfill author` stamps stores created before attribution existed.

See [docs/sharing.md](docs/sharing.md) for the full flow.

## Remote access

Sharing hands over a frozen copy. Remote access is the live version: one store
stays on a host, and your other machines reach it over your network. Point a
laptop at the workstation that holds your memory, or run a small always-on
server and connect every machine to it.

```bash
# Host: turn on remote access and print a one-line credentials bundle
gramaton remote enable --host workstation.local

# Client: connect as a named store alongside your local one
gramaton --store home remote add   # paste the bundle when prompted
```

The host opens a separate TLS-only listener (the loopback listener is
unchanged) with a bearer token and a self-signed certificate. The bundle
carries the address, the certificate's pin, and the token. Trust is pinned to
that certificate, so there is no first-connection window to intercept and a
changed address never breaks verification. `remote add` writes the client
config and registers the store's MCP entry, so an agent can reach the remote
store right away.

Remote stores are named, so a remote store sits alongside your local default
instead of replacing it. Remote access is off until you enable it, and
pointing a store that already holds local data at a remote is refused, so
nothing is stranded. Freeze the store on the host if you want to read it from
your other machines but never write it.

See [docs/remote.md](docs/remote.md) for running the host as a managed
service, the read-only setup, and troubleshooting.

## How it works

Records are nodes in a property graph, connected by typed, weighted edges.
Every mutation is a commit: the store has branches, diffs, a log, and
reverts, and each record's change history is queryable.

Search fuses vector similarity with keyword matching, then ranks candidates
by a composite score of similarity, freshness (decayed by each record's
temporality), usage, and confidence. Metadata filters run before ranking, so
a superseded record is excluded rather than merely outscored. Graph traversal
then fans out from the top results to related knowledge the query text never
mentioned.

Curation runs on a timer inside the server. Without an LLM, it expires stale
short-lived records, links orphans, consolidates duplicates, and detects
concept candidates. With an LLM provider configured, it also classifies
pending records, generates missing summaries, detects contradictions between
similar records, and promotes recurring themes to concept nodes.

```
   Agent
     │
     │  MCP  /  HTTP  /  CLI
     ▼
┌────────────────────────┐
│  Server transports     │   bindings + MCP handlers + curation runner
└───────────┬────────────┘
            │
┌───────────┴────────────┐
│   api (canonical ops)  │   one definition per operation; locking discipline
└───────────┬────────────┘
            │
┌───────────┴────────────┐
│ Engine (composition)   │   graph + indexes + embedder + LLM
└───────────┬────────────┘
            │
  ┌─────────┼─────────┐
  ▼         ▼         ▼
┌─────┐  ┌──────────┐  ┌────────┐
│Graph│  │ Indexes  │  │Storage │
│     │  │ BM25     │  │ prolly │
│     │  │ HNSW/Flat│  │ tree   │
└─────┘  └──────────┘  └────────┘
```

For the layered package map, lock discipline, and data flow, see
[Architecture](docs/architecture.md).

## MCP tools

MCP is Gramaton's primary interface. The tools are grouped to match the three
storage paths, plus versioning and admin. A read-only store registers only
the read tools. For live descriptions and schemas, call
`gramaton_guide(topic=...)` from any MCP-aware agent.

### Records (Memory)

| Tool | What it does |
|------|-------------|
| `gramaton_save` | Store a knowledge record with epistemic metadata |
| `gramaton_save_batch` | Store many records in one call, sync or async, with `_status`, `_result`, and `_cancel` companions for polling |
| `gramaton_inspect` | Full content, metadata, and one-hop related edges for a record |
| `gramaton_update` | Modify properties on an existing record |
| `gramaton_classify` | Assign or update classification metadata on a pending record |
| `gramaton_resolve` | Mark a record resolved (completed / superseded / abandoned / obsolete) |
| `gramaton_link` / `gramaton_unlink` | Manage typed, weighted edges between records |
| `gramaton_history` | Per-record change history |

### Search and discovery

| Tool | What it does |
|------|-------------|
| `gramaton_search` | Hybrid vector and keyword search with metadata filtering |
| `gramaton_explore` | Graph traversal from a node with edge-type and weight filters |
| `gramaton_pending` | List records awaiting classification |
| `gramaton_duplicates` | Find near-duplicate records by vector similarity |
| `gramaton_stats` | Aggregate statistics over the store |
| `gramaton_status` | Server health, curation state, embedding status, read-only flag |

### Sessions

| Tool | What it does |
|------|-------------|
| `gramaton_session_start` | Begin a conversation session bound to a client |
| `gramaton_session_get` | Fetch current session state |
| `gramaton_session_prepare` | Get extraction instructions; must be called before save |
| `gramaton_session_save` | Submit extracted segments as Session records and, by default, linked Memory records |

### Collections

Twelve `gramaton_collection_*` tools cover the lifecycle: `create`, `list`,
`items`, `add`, `add_batch`, `update`, `move`, `remove`, `rename`, `delete`,
`schema`, `migrate`. Schemas and templates are covered in
[Collections](#collections) above; the
[Integrator Guide](docs/integrator-guide.md) has the full reference.

### History and admin

| Tool | What it does |
|------|-------------|
| `gramaton_log` | Commit log or per-record change log |
| `gramaton_diff` | Structural diff between two commits or branches |
| `gramaton_branch` | Create, list, checkout, merge, and discard branches |
| `gramaton_backup` | Create a backup archive of the store |
| `gramaton_curation` | View curation status, trigger a sweep, or dry-run |
| `gramaton_reembed` | Re-embed records after an embedding model change |
| `gramaton_jobs_list` | List active async jobs |
| `gramaton_guide` | Live topic-addressable reference (save, search, sessions, collections, metadata, curation, temporal-queries) |

Gramaton also ships canonical agent guidance for
[Claude Code](integration/claude-code/), [Codex](integration/codex/),
[Cursor](integration/cursor/), and
[custom agent frameworks](integration/docs/custom-agents.md), rendered from
one template set and drift-tested against what `gramaton init` installs.

<details>
<summary><strong>CLI reference</strong></summary>

The CLI mirrors the MCP surface for inspection, scripting, and debugging. A
curated subset:

| Command | Description |
|---------|-------------|
| `gramaton init [--force]` | Interactive setup wizard; `--non-interactive` for scripted defaults |
| `gramaton preflight` | Verify the install end to end; non-zero exit on blocking issues |
| `gramaton serve` | Start the server (background by default, `--fg` for foreground); if you never run it, the server starts on demand |
| `gramaton stop` | Stop the server and its MCP proxies (`--keep-mcp` leaves proxies running) |
| `gramaton status` | Server and store health |
| `gramaton uninstall` | Remove AI-tool integrations; never deletes data (`--harness`, `--dry-run`) |
| `gramaton store <subcmd>` | Named stores: `list`, `create`, `add`, `delete`, `rename`, `freeze`, `thaw`, `attach` |
| `gramaton search <query>` | Search with metadata filtering |
| `gramaton inspect <id>` | Full record details |
| `gramaton explore <id>` | Graph traversal |
| `gramaton save` | Store a record (JSON on stdin) |
| `gramaton classify / resolve / update` | Record maintenance |
| `gramaton delete <id> --reason "..."` | Soft delete |
| `gramaton session <subcmd>` | Sessions: `start`, `get`, `prepare`, `commit`, `archive`, `current` |
| `gramaton ingest <files>` | Bulk-load text files |
| `gramaton log / diff / history` | Commit log, structural diff, per-record history |
| `gramaton branch / revert` | Branch management and rollback |
| `gramaton export / import` | Export and import records |
| `gramaton backup / restore` | Create a store archive; restore from one |
| `gramaton reembed` | Re-embed after an embedding model change |
| `gramaton repair` | Operator-driven self-heal |
| `gramaton backfill author` | Stamp author on records created before attribution |
| `gramaton validate / migrate` | Store integrity check; on-disk format upgrade |
| `gramaton mcp` | Run as an MCP stdio proxy to the HTTP server |

Run `gramaton <command> --help` for flags.

</details>

<details>
<summary><strong>Configuration</strong></summary>

Config lives at `~/.gramaton/config.yaml`. All fields have defaults; an empty
or missing config file is valid.

```yaml
# Identity stamped on every record you save (the wizard sets this).
author:
  name: Ada Lovelace
  email: ada@example.com

# Optional: enables autonomous curation.
llm:
  provider: anthropic
  api_key_env: ANTHROPIC_API_KEY

# Optional: override the default embedding provider.
# embedding:
#   provider: ollama
#   model: mxbai-embed-large
```

Embedding providers: pure-Go BERT (`bge-small-en-v1.5`, the default; no
external runtime), Ollama, OpenAI-compatible, AWS Bedrock. LLM providers for
autonomous curation: Anthropic, OpenAI-compatible, AWS Bedrock.

Per-store config can override the global config by placing a `config.yaml`
inside the store directory. See [docs/configuration.md](docs/configuration.md)
for all fields and [docs/providers.md](docs/providers.md) for provider setup.

</details>

## Documentation

| | |
|---|---|
| [How to use Gramaton](HOW_TO_USE_GRAMATON.md) | Practical tips for driving Gramaton through an agent: what to say, what to expect, common pitfalls |
| [Integrator Guide](docs/integrator-guide.md) | Building agents and tools on Gramaton: the three storage paths, retrieval patterns, tool reference |
| [Sharing stores](docs/sharing.md) | Freezing, attaching, and carving read-only stores |
| [Remote access](docs/remote.md) | Hosting a store for your other machines: enable, connect, read-only topology, troubleshooting |
| [Architecture](docs/architecture.md) | Package layers, data flow, concurrency model, lock discipline |
| [Configuration](docs/configuration.md) | All config fields, defaults, examples, named-store model |
| [Providers](docs/providers.md) | Embedding and LLM provider setup |
| [Benchmarks](docs/benchmarks.md) | Benchmark store setup (LongMemEval and similar) |
| [Tenets](docs/tenets.md) | Design principles |
| [Project Design](docs/project-design/) | Data model, retrieval, collections, threat model, research foundations, design decisions |
| [Contributing](CONTRIBUTING.md) | Conventions for contributors: the operation recipe, lock discipline, tests, CHANGELOG etiquette |
| [Windows](docs/windows.md) | Windows-specific notes: install, supported features, current limitations |

## Community

- [Code of Conduct](CODE_OF_CONDUCT.md) — Contributor Covenant v2.1.
- [Security](SECURITY.md) — vulnerability disclosure process. Do not file
  public issues for security bugs.

## Acknowledgments

Gramaton draws from many projects in this space, with special
acknowledgment to:

- **[Dolt](https://github.com/dolthub/dolt)** — git-for-data: a SQL
  database with branches, commits, diff, and merge. Gramaton's
  versioned storage follows the same philosophy at smaller scale.

- **[Beads](https://github.com/gastownhall/beads)** — a graph issue
  tracker for AI agents, built on Dolt. The agent-as-primary-user
  framing, typed graph edges, and persistent-memory primitives all
  echo through Gramaton's design.

We're excited by the continuing evolution of AI memory across
projects and ideas.

## License

[Apache 2.0](LICENSE)
