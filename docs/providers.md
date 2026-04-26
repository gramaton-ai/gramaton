# Providers

Gramaton delegates embedding and LLM operations to pluggable providers. This guide covers setup for each. Embedding is on by default (pure-Go BERT, no external runtime); LLM is optional and enables autonomous curation when configured.

For the full configuration surface around these providers, see [configuration.md](configuration.md).

## Embedding providers

Embeddings power semantic search. Without any embedding provider, retrieval falls back to BM25 keyword search only — exact terms match, but semantic similarity disappears.

### Pure-Go BERT (default)

A pure-Go BERT encoder built into the Gramaton binary. No external process, no native dependencies. The default model is `bge-small-en-v1.5` (BAAI, 384-dim, ~33M parameters). Model weights download from HuggingFace on first use and cache at `~/.gramaton/models/<model-name>/`.

```yaml
embedding:
  provider: bert                      # default -- omit the embedding: section entirely to get this
  model: bge-small-en-v1.5             # default
  dimension: 384
```

**Setup:** pick `bert` at the embedding prompt in `gramaton init`'s wizard (it's the default), or leave the `embedding:` section off your config entirely. The model downloads automatically on first use; internet required for the first run, subsequent runs serve from cache. To re-run the wizard later (switch providers, reconfigure clients, install agent-usage instructions), use `gramaton init --force`; existing API keys in `~/.gramaton/config.yaml` are preserved unless explicitly overwritten.

**Custom BERT models:** set `model` to a HuggingFace repo path. The provider expects the repo to contain `config.json`, `tokenizer.json`, and `model.safetensors`. Context window is read from `config.json` (`max_position_embeddings`).

```yaml
embedding:
  provider: bert
  model: BAAI/bge-base-en-v1.5         # 768-dim version; update dimension to match
  dimension: 768
```

**Performance note:** both the arm64 (Apple Silicon) and amd64 builds use hand-written SIMD matmul kernels (NEON and AVX2+FMA3 respectively). The amd64 kernel requires Haswell-class silicon or newer; pre-Haswell hosts and Apple Rosetta 2 translation fall back to the pure-Go path automatically. A parity test (`embed/bert/math_test.go::TestMatMulKernelParity`) guards kernel-vs-generic correctness.

### Ollama (alternative local)

Run embedding models through a local Ollama instance. Use this if you want a model Gramaton's built-in BERT doesn't ship with (e.g., a larger or multilingual encoder), or if you already have Ollama running for other workloads.

```yaml
embedding:
  provider: ollama
  endpoint: http://localhost:11434     # default
  model: mxbai-embed-large             # 335M params, 1024d; update dimension accordingly
  dimension: 1024
```

**Setup:**
```bash
# Install Ollama (https://ollama.com)
# Pull the embedding model
ollama pull mxbai-embed-large
```

If `provider: ollama` is set in config (or selected at the wizard prompt), `gramaton init` will detect the Ollama binary, start it if it isn't already running, and pull the configured model. At server runtime Gramaton does **not** supervise Ollama — if Ollama crashes while the server is running, embedding calls will error and records land without vectors (still searchable by BM25). Run Ollama as a service if you need it always-on.

**Other local models:** any Ollama model that supports embeddings works. Set `model` to the model name (e.g., `nomic-embed-text`, `all-minilm`) and update `dimension` to match.

### OpenAI-compatible

Works with OpenAI, Azure OpenAI, vLLM, LiteLLM, Together, Fireworks, and anything else implementing `/v1/embeddings`.

```yaml
embedding:
  provider: openai
  model: text-embedding-3-small
  dimension: 1536
  api_key_env: OPENAI_API_KEY           # env var NAME (not the key itself)
```

**Custom endpoint** (Azure, vLLM, LiteLLM, local OpenAI-protocol server):
```yaml
embedding:
  provider: openai
  model: my-model
  base_url: http://localhost:8000       # your endpoint
  dimension: 768
  # api_key_env is optional for local servers
```

`api_key_env` is the name of an environment variable — Gramaton reads the value at startup. Don't put the raw key in `config.yaml`.

### AWS Bedrock

Supports Amazon Titan Embed and Cohere Embed model families. Uses the standard AWS SDK credential chain (profile, env vars, IMDS, SSO).

```yaml
embedding:
  provider: bedrock
  model: amazon.titan-embed-text-v2:0
  dimension: 1024
  region: us-west-2
  aws_profile: my-profile               # optional; default chain used if omitted
```

**Auth precedence:**
1. `aws_profile` — named profile from `~/.aws/credentials`
2. `aws_access_key_id_env` / `aws_secret_access_key_env` — env var names for explicit credentials
3. Default chain — environment variables, IMDS (EC2), SSO, etc.

**Supported embedding models:**

| Model | Dimensions | Batching | Notes |
|-------|-----------|----------|-------|
| `amazon.titan-embed-text-v2:0` | 1024 (configurable) | No (one text per call) | Amazon-native, English-focused |
| `cohere.embed-english-v3` | 1024 | Yes (up to 96/call) | English only |
| `cohere.embed-multilingual-v3` | 1024 | Yes (up to 96/call) | 100+ languages |

## LLM providers

The LLM provider powers autonomous curation: classification of pending records, summary generation, contradiction detection, concept synthesis, and optional search reranking. It is **optional** — without it, curation runs in deterministic-only mode (lifecycle transitions, orphan linking, duplicate consolidation, concept candidate detection) and records stay in `processing_status: captured` until classified by piggyback curation from an agent session.

### Anthropic (direct API)

```yaml
llm:
  provider: anthropic
  model: claude-sonnet-4-6              # default
  api_key_env: ANTHROPIC_API_KEY
```

**Custom endpoint** (proxy, compatible API):
```yaml
llm:
  provider: anthropic
  model: claude-sonnet-4-6
  base_url: https://my-proxy.example.com
  api_key_env: MY_API_KEY
```

### OpenAI-compatible

Any endpoint implementing `/v1/chat/completions`.

```yaml
llm:
  provider: openai
  model: gpt-4o
  api_key_env: OPENAI_API_KEY
```

Also works with local servers (vLLM, LiteLLM, Ollama with chat models):
```yaml
llm:
  provider: openai
  model: llama3
  base_url: http://localhost:11434/v1
```

### AWS Bedrock

Uses the Bedrock Converse API, which is model-agnostic — works with any Converse-capable model (Claude, Titan, Llama, Mistral, etc.).

```yaml
llm:
  provider: bedrock
  model: anthropic.claude-sonnet-4-6-20250514-v1:0
  region: us-west-2
  aws_profile: my-profile
```

Auth works the same as Bedrock embeddings (profile, env vars, or default credential chain).

### CLI shims (unsupported — use at your own risk)

Two additional providers exist in the codebase — `claude-cli` and `kiro-cli` —
which proxy LLM requests through the vendor's interactive command-line tools
rather than a first-party API.

**These are not officially supported.** Automating an interactive CLI with a
background process is outside the intended use of those tools and may violate
the vendor's terms of service. Using them could result in your vendor account
being suspended or banned. They are retained for local experimentation only.

For production or shared use, configure `anthropic`, `openai`, or `bedrock`
against a first-party API key.

## Mix and match

Each provider is configured independently. Common combinations:

**Pure local (default):**
```yaml
# BERT embedding, no LLM -- deterministic curation only
embedding:
  provider: bert
```

**Local embeddings + cloud LLM:**
```yaml
embedding:
  provider: bert

llm:
  provider: anthropic
  api_key_env: ANTHROPIC_API_KEY
```

**All Bedrock, same account:**
```yaml
embedding:
  provider: bedrock
  model: amazon.titan-embed-text-v2:0
  region: us-west-2
  aws_profile: work

llm:
  provider: bedrock
  model: anthropic.claude-sonnet-4-6-20250514-v1:0
  region: us-west-2
  aws_profile: work
```

**Bedrock across multiple profiles:**
```yaml
embedding:
  provider: bedrock
  model: cohere.embed-english-v3
  region: us-east-1
  aws_profile: ml-team

llm:
  provider: bedrock
  model: anthropic.claude-sonnet-4-6-20250514-v1:0
  region: us-west-2
  aws_profile: platform-team
```

**Disable embedding (BM25-only):**
```yaml
embedding:
  provider: ""        # explicit empty string; BM25 keyword search still works
```

## Changing embedding models

When you switch embedding models (or change their dimension), existing records have stale vectors. Re-embed them:

```bash
gramaton reembed --batch 50
```

This re-embeds every record whose stored `embedding_model` property doesn't match the currently-configured model. The operation is batched, idempotent, and safe to interrupt and resume. Progress shows via `gramaton status`.

A dimension change is a bigger deal — the vector index is dimension-typed, so a cross-dimension switch requires rebuilding the index. In practice, stop the server, delete the vector index file, update the config, restart, and let `reembed` rebuild. The graph and other indexes are untouched.

## Credential handling

Never put API keys directly in `config.yaml`:

- Use `api_key_env: ENVIRONMENT_VARIABLE_NAME` to reference the env var holding the key.
- For AWS, prefer `aws_profile` or the default credential chain over explicit key env vars.
- If `api_key_env` is set and the referenced env var is missing, Gramaton fails at startup with a clear error — it does not silently run unauthenticated.
