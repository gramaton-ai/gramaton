<p align="center">
  <img src=".github/gramaton-logo.jpg" alt="Gramaton — Knowledge, written and remembered." width="340">
</p>
<p align="center"><b>gram·a·ton</b> <i>/ˈɡramətɒn/</i> — from Greek <i>gramma</i> (writing) + <i>automaton</i> (self-acting). A thing that writes and remembers by itself.</p>

<p align="center">
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
</p>

**Gramaton is a knowledge store for AI agents.** It stores facts, decisions, and context in a versioned property graph with semantic search, epistemic metadata, and automatic curation. Agents capture knowledge during their sessions and retrieve it later -- across conversations, across tools, across time.

## Quick Start

```bash
# Install
go install github.com/gramaton-ai/gramaton@latest

# Install Ollama for local embeddings (https://ollama.com)
curl -fsSL https://ollama.com/install.sh | sh

# Initialize (pulls embedding model, starts server)
gramaton init
```

Then add Gramaton to your MCP client (Claude Code, Kiro, etc.):

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

Your agent now has tools like `gramaton_capture`, `gramaton_search`, `gramaton_inspect`, and `gramaton_explore`. A CLI is also available for inspection and debugging.

## Who This Is For

Gramaton is for anyone who works with AI tools and wants them to remember things properly.

A researcher tracking findings across hundreds of papers. A project manager whose AI assistant should know what was decided last quarter and why. A developer whose coding agent keeps forgetting architecture decisions. A writer building a novel with an AI collaborator that needs to keep characters and plot points straight. Anyone who has ever wished their AI tools had a better memory.

Gramaton gives AI agents a structured, searchable, versioned place to store and retrieve knowledge -- so context survives across sessions, across tools, and across time.

**What it is:** A knowledge graph with versioning, metadata-aware search, and background curation. A single binary with no infrastructure to manage.

## How It Works

Records are nodes in a property graph. Each carries metadata: confidence, temporality (ephemeral, temporal, durable, immutable), epistemic status, knowledge type, and provenance. Relationships are typed, weighted edges. Every mutation is a versioned commit with full history.

Search combines vector similarity and BM25 keywords (RRF fusion), then scores results using four signals: similarity, freshness, ACT-R activation (usage-based), and confidence. Metadata filters narrow results before ranking -- a superseded decision doesn't compete with its replacement.

Background curation runs automatically: expiring stale records, linking orphans, detecting duplicates, and (with an LLM provider configured) classifying unprocessed records, generating summaries, and finding contradictions.

```
Agent ──► CLI / MCP / HTTP ──► Server ──► Graph Engine
                                            ├── Vector Index
                                            ├── BM25 Index
                                            ├── Property Index
                                            └── Prolly Tree (storage)
```

## Features

- **Epistemic metadata** -- confidence, temporality, knowledge type, epistemic status on every record
- **Hybrid search** -- vector similarity + BM25 keyword search, fused with RRF
- **Versioned graph** -- branch, diff, merge, revert. Full commit history.
- **Concept emergence** -- recurring keywords graduate to concept nodes that connect related knowledge
- **Automatic curation** -- lifecycle management, orphan linking, dedup, optional LLM classification
- **Multiple providers** -- Ollama (local), OpenAI-compatible, AWS Bedrock for embeddings and LLM

## MCP Tools

Gramaton's primary interface is MCP. Agents interact through structured tools.

| Tool | What it does |
|------|-------------|
| `gramaton_search` | Hybrid vector + keyword search with metadata filtering |
| `gramaton_capture` | Store a knowledge record |
| `gramaton_inspect` | Full content, metadata, related edges |
| `gramaton_explore` | Graph traversal from a node |
| `gramaton_observe` | Extract knowledge from conversation context |
| `gramaton_update` | Modify record properties |
| `gramaton_classify` | Classify a pending record with metadata |
| `gramaton_resolve` | Mark as resolved (completed, superseded, abandoned) |
| `gramaton_link` / `gramaton_unlink` | Manage edges between records |
| `gramaton_pending` | List records awaiting classification |
| `gramaton_duplicates` | Find near-duplicate records |
| `gramaton_stats` | Aggregate statistics |
| `gramaton_curation` | View curation status, trigger, or dry-run |
| `gramaton_branch` | Branch management (create, checkout, merge) |
| `gramaton_diff` / `gramaton_log` | Version history and diffs |
| `gramaton_reembed` | Re-embed records after model change |
| `gramaton_backup` | Create a backup archive |

Gramaton also ships prompt templates and agent instructions for [Claude Code](integration/claude-code/), [Kiro](integration/kiro/), and [custom agent frameworks](integration/docs/custom-agents.md).

<details>
<summary><strong>CLI Reference</strong></summary>

A CLI mirrors every MCP tool for inspection, debugging, and scripting.

| Command | Description |
|---------|-------------|
| `gramaton search <query> [flags]` | Search with metadata filtering |
| `gramaton inspect <id>` | Full record details |
| `gramaton explore <id> [--depth N]` | Graph traversal |
| `gramaton capture` | Store a record (JSON on stdin) |
| `gramaton classify <id>` | Classify a pending record |
| `gramaton update <id>` | Modify properties or create edges |
| `gramaton resolve <id>` | Mark as resolved |
| `gramaton delete <id> --reason "..."` | Soft delete |
| `gramaton ingest <files>` | Bulk-load text files |
| `gramaton log [--last N]` | Commit history |
| `gramaton diff [ref1..ref2]` | Structural diff between commits |
| `gramaton branch create/list/checkout/merge/discard` | Branch management |
| `gramaton revert <commit>` | Rollback to a prior commit |
| `gramaton backup` / `gramaton restore` | Backup and restore |
| `gramaton export` / `gramaton import` | Export and import records |
| `gramaton reembed [--batch N]` | Re-embed after model change |

</details>

<details>
<summary><strong>Configuration</strong></summary>

Config lives at `~/.gramaton/config.yaml`. All fields have sensible defaults.

```yaml
# Minimal config -- local embeddings
embedding:
  provider: ollama
  model: mxbai-embed-large

# Optional -- enables autonomous curation
llm:
  provider: anthropic
  api_key_env: ANTHROPIC_API_KEY
```

Embedding and LLM providers: Ollama (local), OpenAI-compatible, AWS Bedrock, Anthropic (LLM only).

See [docs/configuration.md](docs/configuration.md) for all fields and [docs/providers.md](docs/providers.md) for provider setup.

</details>

## Documentation

| | |
|---|---|
| [Integrator Guide](docs/integrator-guide.md) | How to build agents and tools on Gramaton |
| [Configuration](docs/configuration.md) | All config fields, defaults, and examples |
| [Providers](docs/providers.md) | Embedding and LLM provider setup |
| [Architecture](docs/architecture.md) | Package structure, data flow, concurrency model |
| [Tenets](docs/tenets.md) | Design principles |
| [Design Documents](docs/project-design/) | Research foundations, data model, scoring, curation, threat model |

## License

[Apache 2.0](LICENSE)
