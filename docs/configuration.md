# Configuration

Gramaton reads config from `~/.gramaton/config.yaml`. All fields have sensible defaults — an empty or missing config file is valid. For the authoritative list of fields, see `config/config.go` (Go struct definitions with inline comments on semantics).

Config fields split into two tiers:

- **User-facing** — operational settings, cost/quality dials, provider selection. Safe to change to fit your environment.
- **Internal tuning** — algorithmic parameters calibrated for correctness and performance. Don't adjust casually; use the defaults unless you have a specific reason.

This page covers the user-facing tier in detail and summarizes the internal-tuning tier. For a running store's current effective values, check your local `~/.gramaton/config.yaml`.

## Named stores

Gramaton supports multiple isolated stores per binary — a personal store, a benchmark store, a per-project store. Each store has its own data directory and can override the global config.

**Layout:**
```
~/.gramaton/
  config.yaml          # global config
  data/                # default (unnamed) store data
  stores/
    <name>/
      config.yaml      # per-store config (optional)
      data/            # per-store state
```

A per-store `config.yaml` is loaded via `LoadWithFallback` (see `config/config.go`) with **deep-merge** semantics: defaults are applied first, the global config is overlaid, then the per-store config is overlaid on top. Keys absent from a layer inherit from the layer beneath, so a minimal per-store override (e.g. only `server.port`) keeps the global's `llm:`, `embedding:`, and `logging:` sections intact. Explicit empty values (e.g. `foo: []`, `foo: {}`) replace — use them to intentionally disable an inherited list or map.

**Running against a named store:**
```bash
gramaton --store <name> serve
gramaton --store <name> search "..."
```

## Server

```yaml
server:
  port: 0                # 0 = auto-assign; set a fixed port for stable URLs
  auto_start: true       # CLI auto-starts the server on first use
  idle_timeout: 4h       # server shuts down after this idle period
```

Long idle timeouts (hours) match async usage patterns where the agent returns later. Disable `auto_start` if you run the server as a managed service (systemd, launchd, supervisor).

## Embedding

See [providers.md](providers.md) for provider-specific setup.

```yaml
embedding:
  provider: bert                  # default; also: ollama, openai, bedrock, or "" (disabled)
  model: bge-small-en-v1.5        # default; see providers.md for each provider's options
  dimension: 384                  # embedding vector dimension (must match the model)
  endpoint: http://localhost:11434  # Ollama endpoint
  base_url: ""                    # OpenAI-compatible endpoint
  api_key_env: ""                 # env var name for API key (not the key itself)
  region: ""                      # AWS region (Bedrock)
  aws_profile: ""                 # AWS named profile (Bedrock)
```

Pure-Go BERT is the default — no external runtime; the model (~130MB) downloads to `~/.gramaton/models/<model>/` on first run. Long content is automatically chunked before embedding; if chunks exceed the model's context window, the chunk size is reduced and the content is re-split until it fits. No per-model configuration needed.

## LLM

Used for autonomous curation (classification, contradiction detection, manifest summary, LLM reranking). Optional — omit the `llm:` section to disable.

```yaml
llm:
  provider: ""                    # anthropic, openai, bedrock, or "" (disabled)
  model: claude-sonnet-4-6        # default model for general calls
  api_key_env: ""                 # env var name for API key
  base_url: ""                    # custom endpoint (OpenAI-compatible)
  region: ""                      # AWS region (Bedrock)
  aws_profile: ""                 # AWS named profile (Bedrock)

  # Per-call output ceiling. Anthropic clamps responses to this many
  # tokens; raise if concept synthesis on large stores truncates.
  max_response_tokens: 4096

  # Safety caps. 0 = disabled.
  max_calls_per_day: 0            # hard cap on LLM calls per calendar day
  max_calls_per_session: 0        # hard cap per server lifetime
  max_cost_usd_per_day: 0         # USD cap per calendar day (see below)

  # Tiered models — used by llm_curation effort dials below.
  models:
    low:    claude-haiku-4-5
    medium: claude-sonnet-4-6
    high:   claude-opus-4-7
```

The tiered `models` block lets `llm_curation` route different curation tasks at different cost/quality points (see `llm_curation` below). If you omit the `models` block, all tasks use `model`.

### Cost and call caps

Two independent safety nets, both checked before every LLM call:

- **`max_calls_per_day`** / **`max_calls_per_session`** — simple count caps. Fire on exact hit. Use for "never exceed N calls" guarantees.
- **`max_cost_usd_per_day`** — USD cap computed from reported token counts × pricing table (`llm/pricing.go`). Fires when accumulated cost exceeds the threshold.

**Both live in config deliberately.** The USD cap is the primary signal for cost control, but it only works for models in the pricing table. Keep `max_calls_per_day` set as a backstop: if a new or custom model has no pricing entry, `EstimateCost` returns 0 and the USD cap will never trip. CLI providers (claude-cli, kiro-cli) don't report tokens yet, so they always read 0 — the count cap is the only safety net for those.

When any cap trips, curation pauses and subsequent LLM calls return `llm.ErrCapped` until the daily boundary rolls over (automatic) or an operator manually unpauses.

## Logging

```yaml
logging:
  level: info                     # debug, info, warn, error
  max_size_mb: 512                # total disk budget for all log files
  rotate_size_mb: 50              # rotate when a file reaches this size
```

## Backup

```yaml
backup:
  enabled: true
  dir: ""                         # backup directory (default: ~/.gramaton/backups)
  retain: 2                       # number of backups to keep
  schedule: 24h                   # backup interval
```

## GC

```yaml
gc:
  enabled: false                  # garbage collection of unreferenced chunks
  dry_run: true                   # preview without deleting
  min_age_days: 30                # only GC chunks older than this
```

GC is disabled by default — the "never delete" posture means commit history is cheap to keep. Enable only if storage size becomes a concern.

## Curation

Controls the deterministic + autonomous background maintenance pipeline.

```yaml
curation:
  enabled: true
  interval: 1m                      # how often curation runs
  orphan_similarity_min: 0.6        # min similarity to link an orphan to its nearest neighbor
  stale_ephemeral_score: 0.95       # freshness-decay threshold for expiring ephemeral records
  stale_temporal_score: 0.99        # freshness-decay threshold for expiring temporal records
  max_orphans_per_run: 20
  max_dedup_per_run: 20
  section_link_min: 0.75            # min similarity for cross-section linking
  max_section_links_per_run: 30
  observation_batch_size: 0          # 0 = auto (500 for local providers, 20 for external)
  observation_min_content_length: 1500
  task_timeout: 90s                  # wall-clock cap on a single curation task; 0 disables
```

A hung LLM call (or stalled embedding) on a single task can otherwise starve a whole curation cycle. `task_timeout` cancels the task's context after the deadline; the cycle moves on and retries on the next interval.

## LLM curation

Controls the autonomous (LLM-requiring) phase of curation. Only runs if `llm:` is configured.

```yaml
llm_curation:
  batch_size: 10                        # records per LLM classification batch
  max_calls_per_run: 20                 # max LLM calls per curation cycle (count cap)
  max_cost_usd_per_run: 0               # USD cap per curation cycle (0 = disabled)
  max_classify_attempts: 3              # mark a record stuck after N consecutive classify failures (0 = legacy infinite-retry)

  # Contradiction detection.
  max_contradiction_checks: 5
  contradiction_min_similarity: 0.5
  contradiction_max_similarity: 0.85
  contradiction_batch_size: 5           # pairs per LLM call (~5x call reduction at saturation)

  # Concept synthesis.
  max_concepts_per_run: 5
  synthesis_batch_size: 5
  synthesis_max_input_tokens: 8000
  concept_coherence_min: 0.6            # skip synthesis when member cluster has mean cosine < this

  # Classification length split — short vs long classification prompts.
  long_classification_threshold: 2000

  # Cost-reduction optimizations (on by default).
  prompt_caching_enabled: true
  manifest_cache_enabled: true
  contradiction_check_reverse_edges: true
  classify_short_prompt_compressed: true

  # Effort dials — pick the model tier from llm.models per task.
  # Low = haiku-grade; medium = sonnet-grade; high = opus-grade.
  classification_short_effort: low
  classification_long_effort:  medium
  summarization_effort:        low
  contradiction_effort:        medium
  concept_effort:              medium
  manifest_effort:             low
```

The effort dials are the primary cost/quality knob. Short classification, summarization, and manifest rollup are Haiku-grade (clear-signal work, enum picks, distilled summaries). Contradiction detection, concept synthesis, and long-content classification benefit from Sonnet-grade reasoning. Opus is rarely needed — reserved for particularly nuanced tasks if you want to set one.

`max_cost_usd_per_run` (per cycle) and `llm.max_cost_usd_per_day` (across the day) are independent — the per-cycle cap bounds a single cycle's damage, the per-day cap bounds the aggregate across many cycles. Both complement `max_calls_per_run` / `max_calls_per_day` rather than replacing them; see "Cost and call caps" under the LLM section above for why keeping the count caps set is important.

`max_classify_attempts` bounds the worst case for a *single* pathological record. Without it, a record whose content the LLM consistently can't classify (oversized content, content-policy refusal, persistent malformed-output, mid-call timeouts) sits at the front of the FIFO pending queue and re-attempts every cycle forever — billing input tokens on each retry. After `max_classify_attempts` consecutive failures, the record's `processing_status` flips to `"stuck"`, which excludes it from future cycles. The last failure reason is captured in `last_classify_error` (truncated to 200 runes) for triage. Surface stuck records via `gramaton_search(processing_status="stuck")`, inspect, then either fix the underlying record (`gramaton_update`) and let curation pick it up again, or `gramaton_classify` it manually (which clears the stuck state and the attempts counter). Setting `max_classify_attempts: 0` reverts to the legacy infinite-retry behavior.

`last_classify_error` may include provider-side error fragments (HTTP status messages, request IDs, occasional echoed prompt snippets, transport URLs). It's stored on the record as a normal property — surfaced through `gramaton_inspect` and any property-filtered `gramaton_search`. If you share an export or backup, redact stuck records' `last_classify_error` first if any of them sit on sensitive content.

## Observe

Controls the deterministic TF-IDF observation extractor that runs in every curation cycle (`curation/observe.go`). It finds records over `curation.observation_min_content_length` (default 1500 chars) that don't yet have observation children, picks key sentences via TF-IDF, and creates sub-fact nodes that inherit the parent's metadata. Provides narrow-target semantic recall for long documents.

Unrelated to automatic conversation capture — that lives in the Sessions flow (`gramaton_session_prepare` / `_commit`). The original `/v1/observe` LLM-driven endpoint was replaced by sessions and removed; its knobs (`enabled`, `default_confidence`, `default_temporality`, `substance_min_length`, feedback-loop and retrieval-tracking dials) were removed in the 2026-04-21 config-drift sweep.

```yaml
observe:
  max_facts_per_call: 20             # TF-IDF sentences kept per parent record
```

## Limits

Request and content size limits enforced at the boundary.

```yaml
limits:
  max_json_size: 2097152             # 2 MB max request body (HTTP + stdin file loads)
  max_content_length: 1048576        # 1 MB cap on record content_full
  max_keywords: 100                  # per-record keyword cap
  max_summary_short: 1000            # characters (~750 target; 500 pre-2026-04)
  stdin_timeout: 30s                 # how long CLI commands wait for stdin input
```

`max_summary_short` is the cap on `summary_short` per record. The session-extract flow targets ~750 chars; the limit is set higher to give segment extractors room to write complete semantic anchors.

Only the limits with active enforcement points are listed. Previous versions advertised `max_nesting_depth` and `max_writes_per_second` knobs that had no enforcement code; both were removed in the 2026-04-21 config-drift sweep.

## Search

```yaml
search:
  # User-facing dials
  retrieval_candidates: 200          # candidates pulled from vector + BM25 before reranking
  rerank_enabled: false              # LLM reranking of candidates
  rerank_candidates: 50              # how many candidates sent to LLM when rerank_enabled
  session_dedup_enabled: true        # suppress Session segments when their extracted Memory record is in results

  # Internal scoring / index parameters (rarely adjust)
  bm25_k1: 1.2                       # BM25 term frequency saturation
  bm25_b: 0.75                       # BM25 length normalization
  rrf_k: 60                          # reciprocal rank fusion constant
  suggestion_threshold: 0.75         # score below which faceted suggestions are returned
  hnsw_threshold: 5000               # vector count above which HNSW replaces flat scan
  hnsw_m: 16                         # HNSW max connections per layer
  hnsw_ef_construction: 200          # HNSW build quality
  hnsw_ef_search: 100                # HNSW search width
```

Below `hnsw_threshold`, vector search uses exact brute-force (FlatIndex). Above it, HNSW provides O(log N) approximate nearest-neighbor search. The HNSW parameters rarely need tuning — defaults follow the original paper.

`session_dedup_enabled: true` (the default) means when a Memory record and the Session segment it was extracted from both match a query, only the Memory record is returned — the segment is suppressed to avoid visible duplication across the two stores.

---

## Internal tuning (rarely adjust)

The following sections control algorithmic parameters that affect scoring, decay, and storage behavior. The defaults are calibrated; change them only if you know what you're doing and have a specific reason. See `config/config.go` for the full field comments and rationale.

### Scoring

Weights used to combine scoring signals into the composite result score. Should sum to ~1.0.

```yaml
scoring:
  weight_similarity: 0.55            # vector + BM25 similarity
  weight_freshness: 0.10             # time-decay by temporality
  weight_activation: 0.20            # ACT-R access-based signal
  weight_confidence: 0.15            # record confidence
  importance_threshold: 0.7          # records above this resist decay
  importance_floor_ratio: 0.5        # minimum score for high-importance records
  historical_penalty: 0.5            # multiplier for records with valid_until set
```

### Decay

Per-temporality access-decay rates (per-hour coefficients).

```yaml
decay:
  rates:
    ephemeral: 0.173                 # ~4 hour half-life
    temporal:  0.0096                # ~3 day half-life
    durable:   0.000321              # ~90 day half-life
    immutable: 0.0                   # no decay
```

### Freshness

Knowledge-freshness curve shape (distinct from access decay above — this scores age of the underlying claim, not age of the most recent access).

```yaml
freshness:
  scale: 8760                        # normalization scale in hours (8760 = 1 year)
  exponents:
    immutable: 0                     # freshness is always 1.0
    durable:   0.5                   # gentle power-law
    temporal:  1.0                   # steeper
    ephemeral: 1.0
```

### Activation

ACT-R spreading-activation parameters.

```yaml
activation:
  base_amount: 1.0                   # activation added per access
  attenuation_factor: 0.5            # decay per hop in spreading activation
```

### Chunking

Long-content splitting before embedding.

```yaml
chunking:
  threshold: 512                     # content longer than this gets chunked (characters)
  chunk_size: 512                    # target chunk size
  overlap: 128                       # overlap between adjacent chunks
  section_min: 500                   # min section size for section-aware chunking
  section_max: 5000                  # max section size
```

### Concepts

Controls when candidate keywords become concept nodes.

```yaml
concepts:
  emergence_threshold: 3             # min records sharing a keyword to become a candidate
  min_content_length_direct: 50      # min content length for direct concept creation
```

Concept candidate *detection* is deterministic and always-on. *Promotion* of candidates to concept nodes is LLM-gated (requires an `llm:` provider).

### Dedup

```yaml
dedup:
  similarity_threshold: 0.92         # cosine similarity threshold for duplicate detection
  action: supersede                  # supersede | reject (default: supersede)
```

`supersede` (the default) marks the older near-duplicate historical (sets `valid_until` + `resolution=superseded`) and adds a `supersedes` edge from the new record to the old. `reject` refuses the capture with `ErrConflict` and rolls back the new node.

**Legacy `action: flag`**: configs written before 2026-04 used `flag` as the default. The value is accepted and silently coerced to `supersede` at load time — the two values never had distinct behavior in any capture path. See `design-decisions.md` D37 for the full history. Any other value (typos, unsupported options) errors at config load rather than being silently ignored.

### Graph

```yaml
graph:
  edge_weight_traversal_threshold: 0.3  # min edge weight for graph_explore traversal
```

### Storage

Prolly tree tuning.

```yaml
storage:
  prolly_target_chunk_size: 64       # target entries per leaf chunk
  prolly_split_bits: 6               # bits for chunk boundary (2^6 = avg 64 entries)
```

### Merge

```yaml
merge:
  conflict_strategy: timestamp_wins  # how branch-merge conflicts resolve
```

---

## Provider credentials

Never put API keys directly in `config.yaml`. Use `api_key_env: ANTHROPIC_API_KEY` (etc.) to reference an environment variable by name. Gramaton reads the value at startup. This way, `config.yaml` can be committed or shared without exposing secrets.

For AWS Bedrock, prefer `aws_profile` (reads from `~/.aws/config`) or the standard AWS credential chain over embedded keys. See [providers.md](providers.md) for details.
