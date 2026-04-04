# Embedding

## What Embeddings Are (in Gramaton's Context)

An embedding is a list of numbers (a vector) that represents the "meaning" of a piece of text. Texts with similar meanings produce similar vectors. This is how Gramaton finds records that are semantically related to a query, even without exact keyword matches.

A query for "how to handle transient failures" will find a record about "retry strategies with exponential backoff" because their embeddings are close in vector space — even though they share almost no words.

Embedding generation is distinct from LLM inference. Embedding models are small, fast, and run locally via Ollama or remotely via API. Gramaton does not bundle or run an embedding model itself — it delegates to an external provider via a clean interface.

## Embedding Provider Interface

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

Three implementations, all pure Go (no CGo, no native dependencies):

### Ollama (Default)

Ollama runs embedding models locally with optimized native inference. Gramaton makes HTTP calls to Ollama's local API. The user controls which model to use.

```yaml
embedding:
  provider: ollama
  endpoint: http://localhost:11434    # default
  model: nomic-embed-text            # user's choice
```

**Why Ollama as default:**
- Best performance of any local option (~5-15ms) — Ollama runs optimized native code, we just call the API
- Best search quality — user can pick any model Ollama supports, including the best available
- Zero native dependencies in Gramaton's Go binary — pure Go HTTP client
- Zero build complexity — no CGo, no cross-compilation issues, no bundled model files
- Zero maintenance burden on us — Ollama maintains inference, we maintain an HTTP client
- The ecosystem is moving toward Ollama — increasingly common among AI-forward developers

**User setup:** Install Ollama, pull an embedding model. Guided by `gramaton init`.

**Recommended models via Ollama:**

| Model | MTEB Retrieval Score | Notes |
|-------|---------------------|-------|
| nomic-embed-text | ~55-57 | Best balance of quality and size. Recommended default. |
| snowflake-arctic-embed-s | ~53-55 | Smaller, slightly lower quality |
| mxbai-embed-large | ~57-59 | Larger, best quality |
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

```
$ gramaton init

Checking for embedding providers...

  [✓] Ollama detected at localhost:11434
      Models with embedding support: nomic-embed-text

  → Use Ollama with nomic-embed-text? (Y/n)
  
  ✓ Configuration saved. Gramaton is ready — just use it.
```

Or if Ollama isn't detected:

```
$ gramaton init

Checking for embedding providers...

  [✗] No local providers detected.

Options:
  [1] Install Ollama (recommended — free, local, private)
      macOS: brew install ollama
      Linux: curl -fsSL https://ollama.com/install.sh | sh
      Windows: download from ollama.com
      Then: ollama pull nomic-embed-text
      
  [2] Use an API key (OpenAI-compatible endpoint)
  [3] Use AWS Bedrock
  [4] Skip for now (no semantic search — keyword and graph search still work)

Choice:
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
