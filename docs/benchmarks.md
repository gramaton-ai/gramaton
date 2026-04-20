# Benchmark setup

Gramaton benchmarks run against a dedicated named store to keep
benchmark data from polluting the developer's personal knowledge
graph. This document covers the one-time setup.

## Shape

- Personal store: `~/.gramaton/` (default), ephemeral port, MCP
  name `gramaton`.
- Benchmark store: `~/.gramaton/stores/longmemeval-bench/`, fixed
  port 7338, MCP name `gramaton-bench`.
- Both servers run simultaneously; Claude Code sees two named MCP
  toolsets (`mcp__gramaton__*` and `mcp__gramaton-bench__*`).

## One-time setup

```bash
# 1. Create the named store (data dir only; no config yet).
gramaton store create longmemeval-bench

# 2. Copy global config as the benchmark's override, then edit the
#    two fields that differ. LoadWithFallback does not deep-merge --
#    a partial override file leaves other sections at defaults and
#    the server refuses to start (LLM is required).
cp ~/.gramaton/config.yaml ~/.gramaton/stores/longmemeval-bench/config.yaml

# Edit ~/.gramaton/stores/longmemeval-bench/config.yaml:
#   data_dir: /Users/b/.gramaton/stores/longmemeval-bench/data
#   server:
#     port: 7338
#     auto_start: false
#     idle_timeout: 8h

# 3. Register a second MCP server for Claude Code (stdio, proxies to
#    the benchmark gramaton instance via --store).
claude mcp add --scope user gramaton-bench \
    /Users/b/bin/gramaton -- --store longmemeval-bench mcp

# 4. Start the benchmark server.
gramaton --store longmemeval-bench serve

# 5. Restart Claude Code to pick up the new MCP entry. After
#    restart, tools named mcp__gramaton-bench__* should be
#    available alongside mcp__gramaton__*.
```

Verify both are live:

```bash
claude mcp list | grep gramaton
lsof -iTCP:7338 -P
```

## Operational notes

- **Starting / stopping** the benchmark server:
  ```bash
  gramaton --store longmemeval-bench serve    # start in background
  gramaton --store longmemeval-bench serve --stop
  ```
- **Idle timeout**: set to 8h in the benchmark config so multi-phase
  runs that pause don't cost a cold restart. Re-launch on expiry.
- **Port**: 7338 is a deliberate choice to avoid collision with the
  personal store's ephemeral port (typically 40000+). If you run
  more than one benchmark store in the future, assign sequential
  fixed ports (7339, 7340, …).
- **Deleting**: `gramaton store delete longmemeval-bench` removes
  the data directory after confirmation. The Claude Code MCP entry
  is separate -- remove it with `claude mcp remove gramaton-bench
  -s user` if you want to drop the tool surface too.

## Why a separate store

Session segments committed with `promote_to_memory: true` (the
default used during benchmark extraction) become both session
segments *and* memory records. Loading 15-20k benchmark memories
into the personal store would wreck retrieval quality there and
bloat the vector index by GBs. Separate stores isolate the two
workloads end-to-end.

## Related

- Benchmark datasets, loaders, and run ledger live in a separate
  repo: `~/workspaces/gramaton-benchmarks/`.
- Extraction + evaluation skills (future work, tracked in
  `.claude/skills/benchmark-extract/` and
  `.claude/skills/benchmark-eval/`) drive the benchmark MCP
  toolset, not the personal one.
