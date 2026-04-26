# Embedding

## What Embeddings Are (in Gramaton's Context)

An embedding is a list of numbers (a vector) that represents the "meaning" of a piece of text. Texts with similar meanings produce similar vectors. This is how Gramaton finds records that are semantically related to a query, even without exact keyword matches.

A query for "how to handle transient failures" will find a record about "retry strategies with exponential backoff" because their embeddings are close in vector space — even though they share almost no words.

Embedding generation is distinct from LLM inference. Embedding models are small and fast. Gramaton ships a pure-Go BERT encoder as its default provider (no external runtime needed) and can also delegate to external providers (Ollama, OpenAI-compatible, AWS Bedrock) via a clean interface.

## Embedding Provider Interface

```go
type Provider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    ModelID() string
    ContextWindow() int
}
```

Four shipped implementations:

### Pure-Go BERT (Default)

`embed/bert/` contains a pure-Go BERT encoder that runs inside the Gramaton process — no external runtime, no native dependencies. Default model is `bge-small-en-v1.5` (BAAI, 384-dim, ~33M params). Weights download from HuggingFace on first use and cache at `~/.gramaton/models/<model>/`.

```yaml
embedding:
  provider: bert                       # default -- omit the embedding: block to get this
  model: bge-small-en-v1.5
  dimension: 384
```

**Why BERT as default (supersedes D21's Ollama-as-default):**
- Single-binary install. No external process to manage. Quick Start is `go install && gramaton init`.
- Offline-first after the first-run model download. No network required for capture or search once cached.
- Graceful degradation: no embedding provider is still a fallback, but the BERT default means every install gets semantic search out of the box.
- Cross-platform consistency: Apple Silicon uses a hand-written NEON matmul kernel (`matmul_arm64.s`); amd64 ships an AVX2+FMA3 kernel (`matmul_amd64.s`) with a pure-Go fallback for older CPUs.

**Alternative models:** set `model` to a different HuggingFace repo path (e.g. `BAAI/bge-base-en-v1.5` for a 768-dim version, or any BERT-encoder repo containing `config.json`, `tokenizer.json`, and `model.safetensors`). Update `dimension` to match.

### Ollama (Alternative)

Ollama runs embedding models in a separate local process with optimized native inference. Gramaton makes HTTP calls to its local API. Use this if you want a model BERT doesn't ship (larger encoders, multilingual, etc.) or if Ollama is already part of your workflow.

```yaml
embedding:
  provider: ollama
  endpoint: http://localhost:11434     # default
  model: mxbai-embed-large             # your choice
  dimension: 1024
```

**Setup:** Install Ollama, pull an embedding model (`ollama pull mxbai-embed-large`). `gramaton init` with `provider: ollama` configured will detect the binary, start Ollama if not running, and pull the configured model. At runtime Gramaton does not supervise Ollama — if it crashes, embed calls error and records land without vectors (still BM25-searchable).

**Recommended models via Ollama:**

| Model | MTEB Retrieval Score | Notes |
|-------|---------------------|-------|
| mxbai-embed-large | ~57-59 | Larger, strong quality |
| nomic-embed-text | ~55-57 | Balanced size / quality |
| snowflake-arctic-embed-s | ~53-55 | Smaller, slightly lower quality |
| all-minilm | ~41-43 | Smallest, noticeably weaker |

### OpenAI-Compatible API

Works with any service that speaks the OpenAI embeddings API format: OpenAI, Anthropic, Together, Groq, vLLM, llama.cpp server, and more.

```yaml
embedding:
  provider: openai-compatible
  base_url: https://api.openai.com/v1     # or any compatible endpoint
  api_key_env: OPENAI_API_KEY              # env var containing the key
  model: text-embedding-3-small
```

### AWS Bedrock

For enterprise/internal use. Supports multiple AWS authentication methods:

```yaml
embedding:
  provider: bedrock
  region: us-east-1
  model: amazon.titan-embed-text-v2

  # Authentication — one of:
  
  # IAM profile (assumes role from ~/.aws/config)
  aws_profile: default
  
  # Short-lived session credentials (from STS, SSO, etc.)
  # Resolved automatically via AWS SDK credential chain
  
  # Long-lived access keys (least preferred)
  aws_access_key_id_env: AWS_ACCESS_KEY_ID
  aws_secret_access_key_env: AWS_SECRET_ACCESS_KEY
```

Bedrock authentication uses the standard AWS SDK for Go credential chain — profiles, environment variables, IAM roles, SSO, instance metadata. All pure Go via the AWS SDK.

## Graceful Degradation Without Embeddings

Gramaton works without an embedding provider configured. `gramaton init` offers a "skip for now" option. Without embeddings:

- **Works:** Keyword filtering on `content_keywords`, property filtering (temporality, confidence, etc.), graph traversal, versioning (diff, log, branch), all CLI commands
- **Doesn't work:** Semantic similarity search (finding conceptually related records without exact keyword matches)

Users can add an embedding provider at any time. Existing records without embeddings get embedded when the provider is configured (via a backfill pass).

## Embedding Model Migration

If the user changes embedding models (switches from `nomic-embed-text` to `mxbai-embed-large`, or Ollama updates a model version), existing embeddings become incompatible — they're from a different vector space. Similarity between old and new embeddings is meaningless.

**How Gramaton handles this:**

1. **Every node stores `embedding_model`** — the identifier of the model that generated its embeddings. Set at embedding time.

2. **Model change detection.** When generating embeddings, the server compares the current provider's model to `embedding_model` on existing records. If they differ, a migration is in progress.

3. **Stale embedding handling.** Records with a different `embedding_model` than the current one are penalized in vector similarity scoring (not excluded — gradual transition, not a cliff). Their embeddings are not deleted.

4. **Automatic re-embedding.** On server startup (or first embedding call after a model change), the server detects the mismatch and begins background re-embedding automatically. Batched, non-blocking — the server serves queries normally during the process. Stale embeddings are gradually replaced. The user never needs to notice or act.

```
Server starts
  → current provider model: "nomic-embed-text:v1.5"
  → finds 2350 records with "nomic-embed-text:v1.0"
  → begins background re-embedding
  → queries served normally (stale embeddings penalized, not excluded)
  → progress visible via gramaton status
  → completes, all records on current model
```

The `gramaton reembed` CLI command exists for manual triggers, retries after interruption, or forced full re-embeds — but the default path is automatic (tenet 11: automate the obvious).

```bash
gramaton reembed                    # manual trigger for all stale records
gramaton reembed --batch 100        # process 100 at a time
gramaton status                     # shows embedding health + re-embed progress
```

Re-embedding runs against the configured provider (Ollama, API, Bedrock). At ~5-15ms per embedding via Ollama, 1000 records takes ~5-15 seconds.

**`gramaton status` reports embedding health:**
```json
{
  "embedding_model": "nomic-embed-text:v1.5",
  "total_records": 2403,
  "embedded": 2350,
  "stale_embeddings": 42,
  "unembedded": 11
}
```

## First-Run Experience

The default path — no user choice, no external installs:

```
$ gramaton init

Initialized Gramaton at /home/user/.gramaton
  Config: /home/user/.gramaton/config.yaml
  Data:   /home/user/.gramaton/data

Checking for embedding providers...

  Setting up built-in BERT embedder (bge-small-en-v1.5)...
  Downloading model.safetensors (130MB)... 100%
  Downloading tokenizer.json... 100%
  Model bge-small-en-v1.5 ready

  Embedding configured: bert with bge-small-en-v1.5

Gramaton is ready. Start capturing knowledge.
```

If the BERT download fails (no internet on first run, or HuggingFace is unreachable), `gramaton init` falls back to detecting Ollama. The fallback message names the BERT download as the likely failure and points at Ollama, OpenAI, or Bedrock as alternatives:

```
$ gramaton init
...
  Setting up built-in BERT embedder (bge-small-en-v1.5)...
  BERT setup failed: dial tcp: lookup huggingface.co: no such host
  Falling back to Ollama...
  Ollama not found

  Embedding setup failed.

  Gramaton's default embedding provider is a pure-Go BERT embedder
  that downloads the model (~130MB) from HuggingFace on first run.
  Setup probably failed because of a network issue.

  Options:
    1. Check your internet connection and re-run: gramaton init
    2. Use Ollama as an alternative local embedding provider:
         Download from https://ollama.com/download/mac
         Or: brew install ollama
       Then re-run: gramaton init
    3. Configure OpenAI or AWS Bedrock manually. See docs/providers.md.

  Gramaton also works without embeddings (keyword and graph search
  still work), but semantic similarity search requires them.
```

## Chunking Strategy

Embedding models have a maximum input length, typically 512 tokens (~400 words). Content longer than this gets truncated — the model only sees the beginning.

### How Gramaton Handles This

**Short content (≤ chunk threshold):** Embedded directly. One embedding per summary pyramid level.

**Long content (> chunk threshold):**

1. Summary pyramid levels are embedded directly (they're short by design)
2. Full content is split into overlapping chunks
3. Each chunk becomes a child node with its own embedding
4. Child nodes link to parent via `part_of` edge

```
Parent node (abc-123):
  content_keywords → embedding_keywords
  content_short → embedding_short
  content_abstract → embedding_abstract
  content_full = [full 8000-token document, stored but not embedded as one piece]

Chunk nodes:
  abc-123-c01: tokens 0-512     → embedding_full
  abc-123-c02: tokens 384-896   → embedding_full  (128 token overlap)
  abc-123-c03: tokens 768-1280  → embedding_full
  ...
  abc-123-c16: tokens 7680-8000 → embedding_full
```

### Configuration

```yaml
chunking:
  threshold: 512      # tokens; content above this gets chunked
  chunk_size: 512     # tokens per chunk
  overlap: 128        # tokens of overlap between adjacent chunks
```

All values have sensible defaults. Most users never change them.

## When Embeddings Are Generated

| Event | What Gets Embedded |
|-------|--------------------|
| `gramaton capture` with content | content_full (or chunks if long), plus any provided summary levels |
| `gramaton classify` adds summary | The new summary level gets embedded |
| Subagent provides keywords/summary at capture time | Each provided level gets embedded at capture |
| Curation regenerates a summary | The regenerated summary gets re-embedded |
| Embedding provider added to existing store | Backfill pass embeds all existing content |

Embeddings are generated synchronously during the CLI call. At ~5-15ms per embedding via Ollama, even a record with 4 summary levels + 16 chunks takes under a second.

## Vector Index

Two implementations behind a `VectorIndex` interface:

- **HNSW** — For large candidate sets or unfiltered queries. Approximate nearest neighbors, fast at scale.
- **Flat scan** — For small filtered sets. Exact results, no index overhead.

The query engine dynamically chooses based on candidate set size after metadata filtering. If a filter narrows candidates to 50 nodes, flat scan is faster and exact. If 10,000 nodes remain, HNSW is used.

HNSW parameters (ef_construction, M, ef_search) exposed in config.
