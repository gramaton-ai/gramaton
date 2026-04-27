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

# 2. Write a minimal override with just the fields that differ from
#    the global config. LoadWithFallback deep-merges (defaults ->
#    global -> per-store), so unset keys inherit from the global.
cat > ~/.gramaton/stores/longmemeval-bench/config.yaml <<'EOF'
data_dir: /path/to/.gramaton/stores/longmemeval-bench/data
server:
  port: 7338
  auto_start: false
EOF

# 3. Register a second MCP server for Claude Code (stdio, proxies to
#    the benchmark gramaton instance via --store).
claude mcp add --scope user gramaton-bench \
    gramaton -- --store longmemeval-bench mcp

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

## Disable contradiction detection for benchmark stores

Benchmark corpora are test fixtures — records we measure retrieval against, not knowledge we need to reason about. The autonomous contradiction-detection task in `llm.curation` adds no value on a benchmark store and can burn real money on ambient LLM calls.

Set `llm.curation.contradiction.max_checks: 0` in the per-store `config.yaml` when creating a benchmark store:

```yaml
llm:
  curation:
    contradiction:
      max_checks: 0    # disabled for benchmark store
```

Apply the same rule to any future benchmark store you create, unless the benchmark specifically exercises contradiction-detection behavior. Other autonomous curation tasks (classification, summarization, concept synthesis, manifest) are fine to leave on — they cost less and produce useful signal for benchmark extraction flows.

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
