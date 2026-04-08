# Configuration

Gramaton reads config from `~/.gramaton/config.yaml`. All fields have defaults -- an empty or missing config file is valid. Per-store config can override the global config by placing a `config.yaml` inside the store directory.

## Server

```yaml
server:
  port: 0                # 0 = auto-assign; set a fixed port for stable URLs
  auto_start: true        # CLI auto-starts the server on first use
  idle_timeout: 30m       # server shuts down after this idle period
```

## Embedding

See [providers.md](providers.md) for detailed setup.

```yaml
embedding:
  provider: ""            # ollama, openai, bedrock, or "" (disabled)
  endpoint: http://localhost:11434  # Ollama endpoint
  model: mxbai-embed-large          # model name
  base_url: ""            # OpenAI-compatible endpoint
  api_key_env: ""         # env var name for API key
  region: ""              # AWS region (Bedrock)
  aws_profile: ""         # AWS named profile (Bedrock)
```

## LLM

Used for autonomous curation. Optional.

```yaml
llm:
  provider: ""            # anthropic, openai, bedrock, or "" (disabled)
  model: claude-sonnet-4-6
  base_url: ""            # custom endpoint
  api_key_env: ""         # env var name for API key
  region: ""              # AWS region (Bedrock)
  aws_profile: ""         # AWS named profile (Bedrock)
```

## Scoring

Controls how search results are ranked. The four weights should sum to 1.0.

```yaml
scoring:
  weight_similarity: 0.55    # vector + BM25 similarity
  weight_freshness: 0.10     # time decay
  weight_activation: 0.20    # ACT-R usage signal
  weight_confidence: 0.15    # record confidence
  importance_threshold: 0.7  # records above this resist decay
  importance_floor_ratio: 0.5  # minimum score for important records
  historical_penalty: 0.5    # multiplier for records with valid_until set
```

## Decay

Controls how fast records lose freshness by temporality class. Values are hourly decay rates.

```yaml
decay:
  rates:
    ephemeral: 0.173       # ~4 hour half-life
    temporal: 0.0096        # ~3 day half-life
    durable: 0.000321       # ~90 day half-life
    immutable: 0.0          # no decay
```

## Freshness

Controls the freshness scoring curve.

```yaml
freshness:
  scale: 8760              # normalization scale (hours; 8760 = 1 year)
  exponents:
    immutable: 0            # freshness is always 1.0
    durable: 0.5
    temporal: 1.0
    ephemeral: 1.0
```

## Activation

Controls the spreading activation behavior.

```yaml
activation:
  base_amount: 1.0          # activation added per access
  attenuation_factor: 0.5   # decay per hop in spreading activation
```

## Chunking

Controls how large content is split into chunks.

```yaml
chunking:
  threshold: 512           # content longer than this (chars) gets chunked
  chunk_size: 512          # target chunk size (chars)
  overlap: 128             # overlap between chunks (chars)
  section_min: 500         # min section size for section-aware chunking
  section_max: 5000        # max section size
```

## Search

Controls hybrid search behavior.

```yaml
search:
  bm25_k1: 1.2             # BM25 term frequency saturation
  bm25_b: 0.75             # BM25 length normalization
  rrf_k: 60                # reciprocal rank fusion constant
  suggestion_threshold: 0.75  # score below which faceted suggestions are returned
  hnsw_threshold: 5000     # vector count above which HNSW replaces flat scan
  hnsw_m: 16               # HNSW max connections per layer
  hnsw_ef_construction: 200  # HNSW build quality (higher = better recall, slower build)
  hnsw_ef_search: 100      # HNSW search width (higher = better recall, slower search)
```

Below `hnsw_threshold`, vector search uses exact brute-force (FlatIndex). Above it, HNSW provides O(log N) approximate search with high recall. The HNSW parameters rarely need tuning -- defaults are from the original paper and work well for most workloads.

## Concepts

Controls when keywords graduate to concept nodes.

```yaml
concepts:
  emergence_threshold: 3        # min records sharing a keyword for concept emergence
  min_content_length_direct: 50  # min content length for direct concept creation
```

## Dedup

Controls duplicate detection.

```yaml
dedup:
  similarity_threshold: 0.92   # cosine similarity threshold for duplicate detection
  action: flag                  # flag or merge
```

## Graph

```yaml
graph:
  edge_weight_traversal_threshold: 0.3  # min edge weight for traversal
```

## Storage

Controls the prolly tree storage layer.

```yaml
storage:
  prolly_target_chunk_size: 64  # target entries per leaf chunk
  prolly_split_bits: 6          # bits for chunk boundary (2^6 = avg 64 entries)
```

## Limits

```yaml
limits:
  max_json_size: 2097152        # 2 MB max request body
  max_nesting_depth: 10
  max_content_length: 1048576   # 1 MB max content field
  max_keywords: 100
  max_summary_short: 500
  max_summary_abstract: 5000
  stdin_timeout: 30s
  max_writes_per_second: 100
```

## Curation

Controls background maintenance.

```yaml
curation:
  enabled: true
  interval: 5m                   # how often curation runs
  orphan_similarity_min: 0.6     # min similarity for orphan linking
  stale_ephemeral_score: 0.95    # decay score threshold for ephemeral expiry
  stale_temporal_score: 0.99     # decay score threshold for temporal expiry
  max_orphans_per_run: 20
  max_dedup_per_run: 20
  section_link_min: 0.75         # min similarity for cross-section linking
  max_section_links_per_run: 30
```

## LLM Curation

Controls autonomous curation (requires LLM provider).

```yaml
llm_curation:
  batch_size: 10                 # records per LLM batch
  max_calls_per_run: 20          # max LLM calls per curation cycle (cap: 1000)
  max_contradiction_checks: 5
  contradiction_min_similarity: 0.5
  contradiction_max_similarity: 0.85
  max_concepts_per_run: 5
```

## Observe

Controls the passive observation pipeline.

```yaml
observe:
  enabled: true
  max_facts_per_call: 20
  default_confidence: 0.3
  default_temporality: ephemeral
  substance_min_length: 20       # min fact length to pass quality gate
  feedback_loop_hours: 4         # suppress near-duplicates within this window
  feedback_loop_similarity: 0.85
  retrieval_tracking: true       # track which records were retrieved
  retrieval_similarity: 0.7
```

## Logging

```yaml
logging:
  level: info                    # debug, info, warn, error
  max_size_mb: 512               # total disk budget for all log files
  rotate_size_mb: 50             # rotate when file reaches this size
```

## Backup

```yaml
backup:
  enabled: false
  dir: ""                        # backup directory (default: ~/.gramaton/backups)
  retain: 2                      # number of backups to keep
  schedule: 24h
```

## GC

```yaml
gc:
  enabled: false
  dry_run: true                  # preview without deleting
  min_age_days: 30               # only GC records older than this
```

## Merge

```yaml
merge:
  conflict_strategy: timestamp_wins  # how branch merge conflicts are resolved
```
