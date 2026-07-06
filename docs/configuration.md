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

## Author

```yaml
author:
  name: Ada Lovelace              # optional
  email: ada@example.com          # optional
```

The identity that records created in the store are attributed to. Works like git's `user.name` / `user.email`: the two fields compose into a single string at use time — `Ada Lovelace <ada@example.com>` when both are set, the name alone when only `name` is set, the bare email (no angle brackets) when only `email` is set. The composed identity is stamped as a set-once `author` property on records created in the store. If both fields are blank, records get no author property.

`gramaton init` collects the identity (interactively, or from the `--author` flag / the OS account name in non-interactive mode). Edit the fields here any time; changes apply to records created afterwards — existing records keep the author they were stamped with.

## Server

```yaml
server:
  port: 0                # 0 = auto-assign; set a fixed port for stable URLs
  auto_start: true       # CLI auto-starts the server on first use
  idle_timeout: 4h       # server shuts down after this idle period
  tls:                   # bring-your-own certificate for the remote TLS listener
    cert_file: ""        # PEM paths; set both together, or leave empty to use
    key_file: ""         # the generated self-signed certificate
```

Long idle timeouts (hours) match async usage patterns where the agent returns later. Disable `auto_start` if you run the server as a managed service (systemd, launchd, supervisor).

`server.tls` takes effect when remote access is enabled and is ignored while the server is loopback-only (remote access is in progress; see issue #96). `cert_file` and `key_file` must be set together — a config with only one of them fails to load. Note that the both-or-neither check runs after the global/per-store deep merge: a per-store config that sets only `cert_file` will inherit `key_file` from the global layer and pass validation with a mismatched pair, so per-store overrides should always set both fields together.

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

Used for autonomous curation (classification, contradiction detection, manifest summary), search-time LLM reranking, and query decomposition. Optional — omit the `llm:` section to disable. Every LLM-touching dial lives under this single block.

```yaml
llm:
  # Provider + auth
  provider: ""                    # anthropic, openai, bedrock, or "" (disabled).
                                  #   claude-cli / kiro-cli are also accepted but
                                  #   UNSUPPORTED (TOS risk; see docs/providers.md).
  api_key_file: ""                # path to a file holding the key (preferred)
  api_key_env: ""                 # env var name (used if api_key_file empty)
  base_url: ""                    # custom endpoint (OpenAI-compatible)
  region: ""                      # AWS region (Bedrock)
  aws_profile: ""                 # AWS named profile (Bedrock)

  # Search-time LLM reranking.
  rerank:
    enabled: false                # send retrieval candidates back through the LLM
    candidates: 50                # how many to feed the reranker

  # Tiered models + per-task tier assignments. Every code path that
  # calls an LLM picks a tier; each tier maps to a model name.
  models:
    low:    claude-haiku-4-5      # cheap, fast (high-volume / easy tasks)
    medium: claude-sonnet-4-6     # balanced (real reasoning)
    high:   claude-opus-4-7       # premium (use sparingly)
    tasks:                        # which tier each task uses
      classification_short: low
      classification_long:  medium
      summarization:        low
      contradiction:        medium
      concept:              medium
      manifest:             low
      rerank:               low
      decompose:            low

  # Caps that apply to ALL LLM calls. 0 = disabled.
  # The values below are what `gramaton init` writes on first
  # install (conservative defaults for cost safety). Operators
  # running production curation will hit these and need to bump
  # them — see "Cost and call caps" below for tuning guidance.
  cost_limits:
    max_calls_per_day: 500        # hard cap per calendar day
    max_calls_per_session: 0      # hard cap per server lifetime
    max_cost_usd_per_day: 5       # USD cap per calendar day (see below)
    max_cost_usd_per_run: 0       # USD cap per curation cycle
    rate_limit_interval: 0s       # min gap between successive calls
    max_response_tokens: 0        # 0 = provider default (Anthropic only)

  # Curation-cycle tuning -- these are NOT operator dials. See
  # "LLM curation" below for what each knob does and the warning
  # about touching them.
  curation:
    # ... see "LLM curation" section below
```

The `models.tasks` map is the primary cost/quality knob. Lower a task's tier to save cost; raise it for quality. Removing a key falls back to the baked-in default (`defaultEffortForTask` in `config/config.go`). Default assignments are Haiku-grade for short classification, summarization, manifest rollup, and search-time tasks; Sonnet-grade for long classification, contradiction detection, and concept synthesis.

### Provider asymmetry: per-task tiers on OpenAI / Bedrock

The `models.tasks` map is fully honored by the **anthropic** provider. The **openai** and **bedrock** providers fix the model at client-construction time (the SDKs need it baked into the client), so per-call `CompleteWithModel` overrides are ignored — both providers log a warning and use the construction-time model. In practice this means:

- On Anthropic: every task uses the model resolved through its tier (`ModelForTask(task)` -> `Models.{Low,Medium,High}`). Setting `tasks.rerank: low` and `tasks.contradiction: high` actually picks two different models.
- On OpenAI / Bedrock: every task uses `models.medium` (the construction-time default). The `tasks` map and `models.{low,high}` entries are dead weight — they're written, validated, and even resolved internally, but the provider call ignores them.

If you're on OpenAI or Bedrock and want per-task tiering, that's a provider-level limitation we'd have to fix by reconstructing the client per call (rejected today: ~10x latency overhead at LLM-call rates curation hits). For now, set `models.medium` to whatever model you want all tasks to use, and ignore the tier dials.

### Cost and call caps

Two independent safety nets under `cost_limits`, both checked before every LLM call:

- **`max_calls_per_day`** / **`max_calls_per_session`** — simple count caps. Fire on exact hit. Use for "never exceed N calls" guarantees.
- **`max_cost_usd_per_day`** — USD cap computed from reported token counts × pricing table (`llm/pricing.go`). Fires when accumulated cost exceeds the threshold.

**Both live in config deliberately.** The USD cap is the primary signal for cost control, but it only works for models in the pricing table. Keep `max_calls_per_day` set as a backstop: if a new or custom model has no pricing entry, `EstimateCost` returns 0 and the USD cap will never trip. CLI providers (claude-cli, kiro-cli) don't report tokens yet, so they always read 0 — the count cap is the only safety net for those.

`max_cost_usd_per_run` (per cycle) is independent of `max_cost_usd_per_day` (across all cycles). The per-cycle cap bounds a single cycle's damage; the per-day cap bounds the aggregate. Both complement the count caps rather than replacing them.

When any cap trips, curation pauses and subsequent LLM calls return `llm.ErrCapped` until the daily boundary rolls over (automatic) or an operator manually unpauses.

#### Tuning for real workloads

`gramaton init` writes conservative defaults (`max_calls_per_day: 500`, `max_cost_usd_per_day: 5`) — appropriate for trying Gramaton out, too tight for production curation against a meaningful backlog. A typical 500-record backlog with the default cycle interval (5 min) and per-cycle cap (`curation.max_calls_per_run: 20`) drains in ~125 minutes WITHIN the daily 500-call envelope; the moment you exceed roughly 500 records of work in 24 hours, you'll hit the cap and curation pauses until midnight UTC. Bump `max_calls_per_day` to `5000`-`10000`+ for sustained production work, or set it to `0` to disable entirely (relying on `max_cost_usd_per_day` as the sole guardrail).

`curation.max_calls_per_run` is the per-cycle cap (default 20). It bounds worst-case spend in a single cycle; raising it to 100+ drains backlogs faster but multiplies per-cycle exposure. For a tight cost envelope keep it low; for backlog catch-up after a long pause, bump temporarily.

When you hit a cap, look for a `llm call refused: cap reached` warning in the gramaton log — the warning's `reason` field names which cap fired.

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

Controls the deterministic + autonomous background maintenance pipeline. **Note**: this section governs the global *runner* — its cadence, retry caps, and per-task budgets. Whether a given record actually flows through curation depends on the per-collection eligibility knobs (`curation`, `supersession`, `contradictions`, `clear_mode`) set at collection-create time on the collection node, not in `config.yaml`. New ad-hoc collections default to `curation: none`; the standard templates opt their items into `curation: standard`. See [docs/integrator-guide.md](integrator-guide.md) for the per-collection knob shape and template defaults.

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
  max_observation_attempts: 5        # exclude a parent from extraction after N consecutive embed failures (0 = legacy infinite-retry; 5 because embed failures are usually transient)
  task_timeout: 90s                  # wall-clock cap on a single curation task; 0 disables
```

A hung LLM call (or stalled embedding) on a single task can otherwise starve a whole curation cycle. `task_timeout` cancels the task's context after the deadline; the cycle moves on and retries on the next interval.

## LLM curation

Controls the autonomous (LLM-requiring) phase of curation. Lives under `llm.curation` in the new shape; only runs if `llm.provider` is set. **WARNING:** these values control algorithmic behavior that has been carefully tuned. Do not edit unless you have a specific reason and have read the relevant code path. Wrong values silently degrade store quality (missed contradictions, generic concepts, runaway cost).

```yaml
llm:
  curation:
    batch_size: 10                          # records per LLM classification batch
    max_calls_per_run: 20                   # max LLM calls per curation cycle
    long_classification_threshold: 2000     # short vs long classification prompt cutoff
    task_timeout: 90s                       # wall-clock cap per task; 0 disables

    # Cost-reduction optimizations (on by default).
    prompt_caching_enabled: true
    manifest_cache_enabled: true
    classify_short_prompt_compressed: true

    contradiction:
      max_checks: 5
      min_similarity: 0.5
      max_similarity: 0.85
      batch_size: 5                         # pairs per LLM call (~5x call reduction at saturation)
      check_reverse_edges: true
      # Note: contradiction detection only runs against collections whose
      # `contradictions` knob is set to `on`. Memory records (no collection)
      # use a default that orphan-style records still get checked. The
      # config above tunes the runner; per-collection eligibility lives on
      # the collection node.

    concept:
      max_per_run: 5
      synthesis_batch_size: 5
      synthesis_max_input_tokens: 8000
      coherence_min: 0.6                    # skip synthesis when member cluster has mean cosine < this

    retries:
      max_classify_attempts: 3              # mark a record stuck after N consecutive classify failures (0 = legacy infinite-retry)
      max_summary_attempts: 3               # skip a record after N consecutive summary failures
      max_synthesis_attempts: 3             # mark a concept stuck after N consecutive synthesis failures
      max_manifest_attempts: 3              # negative-cache the manifest LLM call after N consecutive failures on the same store fingerprint
      max_contradiction_attempts: 3         # lock out a contradiction-check pair after N consecutive failures
      max_embed_attempts: 3                 # exclude a record from gramaton_reembed after N consecutive embed failures
```

Per-task model selection lives in the `llm.models.tasks` map (see "LLM" above), not under `llm.curation`. That keeps every model-selection dial in one place.

`retries.max_classify_attempts` bounds the worst case for a *single* pathological record. Without it, a record whose content the LLM consistently can't classify (oversized content, content-policy refusal, persistent malformed-output, mid-call timeouts) sits at the front of the FIFO pending queue and re-attempts every cycle forever — billing input tokens on each retry. After `max_classify_attempts` consecutive failures, the record's `processing_status` flips to `"stuck"`, which excludes it from future cycles. The last failure reason is captured in `last_classify_error` (truncated to 200 runes) for triage. Surface stuck records via `gramaton_search(processing_status="stuck")`, inspect, then either fix the underlying record (`gramaton_update`) and let curation pick it up again, or `gramaton_classify` it manually (which clears the stuck state and the attempts counter). Setting `max_classify_attempts: 0` reverts to the legacy infinite-retry behavior.

`last_classify_error` may include provider-side error fragments (HTTP status messages, request IDs, occasional echoed prompt snippets, transport URLs). It's stored on the record as a normal property — surfaced through `gramaton_inspect` and any property-filtered `gramaton_search`. If you share an export or backup, redact stuck records' `last_classify_error` first if any of them sit on sensitive content.

`retries.max_summary_attempts` does the equivalent for summary generation. After N consecutive failures (LLM error or empty-after-trim), the record is skipped at selection time on subsequent cycles; the failure reason is captured in `last_summary_error`. The summary phase doesn't flip `processing_status` to `"stuck"` (that's the classify phase's terminal state) — instead the selection guard skips records with `summary_attempts >= max`. Both counters reset to 0 on a successful classify or summary respectively, so an operator-fixed record passes cleanly.

`retries.max_synthesis_attempts` does the equivalent for concept synthesis. Concept syntheses bundle multiple records' member summaries per LLM call, so a single failure (LLM error, JSON parse error, short response, empty synthesis at a position) rebills the entire batch's input tokens. After N consecutive failures, the concept's `synthesis_status` flips to `"stuck"` — the existing selection guard (`synthesis_status="pending"`) auto-excludes stuck concepts from future cycles. Failure reason captured in `last_synthesis_error`. Operator triage: surface stuck concepts via `gramaton_search(processing_status="processed", missing=["content_full"])` with `synthesis_status="stuck"` (concepts have processing_status=processed regardless of synthesis state).

`retries.max_manifest_attempts` is shaped differently because the manifest summary is keyed by a content-derived store-state fingerprint, not by a record. After N consecutive failures on the same fingerprint, the in-memory `ManifestCache` flips into a "negative cache" mode and the LLM call is skipped on subsequent cycles. The negative cache clears automatically when the fingerprint changes (records added, removed, or modified) or when any later success lands. No on-disk state — a server restart with the same store gets the same fingerprint and may retry once before re-tripping the negative cache.

`retries.max_contradiction_attempts` covers the contradiction-detection pipeline, where the unit is a *pair* of records, not a single record. Per-pair failure state lives on a `contradiction_check_skipped` edge between the pair, which carries `attempts` (Int64), `last_error` (String, max 200 runes), and `checked_at` (Timestamp) properties. While `attempts < max`, the read-phase guard treats the edge as a *soft skip* — the pair stays in the candidate pool and gets retried next time the random shuffle surfaces it. At `attempts >= max`, the edge becomes a *hard skip* — the pair is locked out of future cycles. Operator triage: find stuck pairs via `gramaton_explore` from one of the records (looking for `contradiction_check_skipped` outbound edges), then `gramaton_unlink` to retry. Setting `max_contradiction_attempts: 0` reverts to legacy behavior: failed pairs never get a marker, just re-enter every cycle. Distinct from `no_contradiction` (which is a real LLM affirmation of no conflict) — the epistemic state is "we tried and couldn't determine," not "we determined no conflict."

`retries.max_embed_attempts` covers `gramaton_reembed`. The reembed candidate selection picks records whose `embedding_model` is missing or differs from the configured embedder. After N consecutive embed failures, the record is excluded from selection on subsequent invocations; failure reason captured in `last_embed_error`. A successful re-embed clears the counter. Reembed is manual-only (CLI / MCP tool / HTTP endpoint), so the per-call cost is bounded by operator cadence — but each invocation still re-paid for the same failures pre-fix. Operator triage: surface stuck records via `gramaton_search(missing=["embedding_model"])` filtered by `embed_attempts >= 3`, inspect `last_embed_error`, then either fix the underlying record or let the next reembed try after `gramaton_update` clears the counter.

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
  session_dedup_enabled: true        # suppress Session segments when their extracted Memory record is in results
  # Note: rerank_enabled / rerank_candidates moved to llm.rerank.{enabled,candidates}.

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

### Pagination

```yaml
search:
  pagination:
    snapshot_ttl: 20m              # how long a fresh search snapshot stays valid
    candidate_cap: 500             # max candidates materialized per fresh search (hard ceiling 1000)
    page_size_default: 20          # page size when the request omits page_size
    page_size_max: 100             # cap on requested page_size
```

`gramaton_search` materializes up to `candidate_cap` ranked candidates on a fresh query and pins them in an in-engine snapshot keyed by an opaque `query_id`. Subsequent calls with `cursor` slice into the same snapshot at the encoded boundaries, so paged retrieval stays consistent even as the underlying graph mutates. Snapshots evict after `snapshot_ttl`; an expired cursor returns `{error: "snapshot_expired"}` so the caller knows to re-run the original query.

The hard ceiling on `candidate_cap` is 1000 (`config.MaxCandidateCapHard`); the runtime clamps higher values down to that. The legacy hardcoded cap on the `top` request field — used when callers ask for more results than the system will return — remains 1000 (`api.MaxSearchTop`). For exhaustive retrieval beyond the candidate cap, use `gramaton_export` with the same filters: it skips pagination entirely and streams every match.

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
  max_keyword_pct: 0.2               # skip keywords on more than this fraction of records (corpus-wide vocabulary)
  member_overlap_threshold: 0.6      # Jaccard above which a candidate aliases an existing concept (0 disables)
```

Concept candidate *detection* is deterministic and always-on. *Promotion* of candidates to concept nodes is LLM-gated (requires an `llm:` provider).

`member_overlap_threshold` suppresses the duplicate-cluster pattern where each member record's 5-6 content_keywords each cross emergence_threshold on the same evidence set, spawning multiple concept nodes about the same idea. When a candidate's member-set Jaccard with an existing or peer-pending concept exceeds this threshold, the candidate's keyword is folded into that concept's `content_keywords` as an alias rather than emitting a new concept node. Setting 0 disables the gate (legacy behavior).

**Search default excludes concepts.** `gramaton_search` filters out `node_type=concept` from results by default — concept syntheses are derivative cross-record summaries that compete with their member records for top-N slots without earning the space. Pass `include_concepts=true` (or `--include-concepts` on the CLI) to opt back in. `gramaton_explore` and `gramaton_inspect` are unchanged; concepts remain walkable and inspectable when their IDs are known.

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
