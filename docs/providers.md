# Providers

Gramaton delegates embedding and LLM operations to external providers. This guide covers setup for each.

## Embedding Providers

Embeddings power semantic search. Without an embedding provider, Gramaton falls back to BM25 keyword search only.

### Ollama (local)

Run embedding models locally. No API keys, no network calls, no cost.

```yaml
embedding:
  provider: ollama
  endpoint: http://localhost:11434   # default
  model: mxbai-embed-large           # default, 335M params, 1024d
```

**Setup:**
```bash
# Install Ollama (https://ollama.com)
# Pull the embedding model
ollama pull mxbai-embed-large

# Or let gramaton init handle it
gramaton init
```

`gramaton init` auto-detects Ollama, starts it if needed, and pulls the model.

**Other local models:** Any Ollama model with embedding support works. Set `model` to the model name (e.g., `nomic-embed-text`, `all-minilm`).

### OpenAI-compatible

Works with OpenAI, Azure OpenAI, vLLM, LiteLLM, Together, Fireworks, and any API implementing `/v1/embeddings`.

```yaml
embedding:
  provider: openai
  model: text-embedding-3-small
  api_key_env: OPENAI_API_KEY        # env var name containing the key
```

**Custom endpoint** (Azure, vLLM, etc.):
```yaml
embedding:
  provider: openai
  model: my-model
  base_url: http://localhost:8000     # your endpoint
  # api_key_env is optional for local servers
```

The `api_key_env` field is the name of an environment variable, not the key itself. If the value starts with `sk-`, it's treated as a direct key (not recommended for config files).

### AWS Bedrock

Supports Titan Embed V2 and Cohere Embed model families.

```yaml
embedding:
  provider: bedrock
  model: amazon.titan-embed-text-v2:0
  region: us-west-2
  aws_profile: my-profile            # optional, uses default chain if omitted
```

**Auth:** Uses the AWS SDK default credential chain:
1. `aws_profile` -- selects a named profile from `~/.aws/credentials`
2. `aws_access_key_id_env` / `aws_secret_access_key_env` -- env var names for explicit credentials
3. Default chain -- environment variables, IMDS, SSO, etc.

**Supported models:**

| Model | Dimensions | Batching | Notes |
|-------|-----------|----------|-------|
| `amazon.titan-embed-text-v2:0` | 1024 (configurable) | No (one text per call) | Amazon-native |
| `cohere.embed-english-v3` | 1024 | Yes (up to 96/call) | English only |
| `cohere.embed-multilingual-v3` | 1024 | Yes (up to 96/call) | Multilingual |

## LLM Providers

The LLM provider powers autonomous curation: classification of pending records, summary generation, contradiction detection, and concept synthesis. It is optional -- without it, curation runs in deterministic-only mode and agents handle classification via piggyback curation.

### Anthropic (direct API)

```yaml
llm:
  provider: anthropic
  model: claude-sonnet-4-6            # default
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

Works with any `/v1/chat/completions` endpoint.

```yaml
llm:
  provider: openai
  model: gpt-4o
  api_key_env: OPENAI_API_KEY
```

Also works with local servers (vLLM, Ollama with chat models, LiteLLM):
```yaml
llm:
  provider: openai
  model: llama3
  base_url: http://localhost:11434/v1
```

### AWS Bedrock

Uses the Converse API -- works with any Bedrock model (Claude, Titan, Llama, Mistral).

```yaml
llm:
  provider: bedrock
  model: anthropic.claude-sonnet-4-6-20250514-v1:0
  region: us-west-2
  aws_profile: my-profile
```

Auth works the same as Bedrock embeddings (profile, env vars, or default chain).

## Using Different Providers for Embedding and LLM

Each provider is configured independently. Mix and match:

```yaml
# Local embeddings, cloud LLM
embedding:
  provider: ollama
  model: mxbai-embed-large

llm:
  provider: anthropic
  api_key_env: ANTHROPIC_API_KEY
```

```yaml
# All Bedrock, same profile
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

```yaml
# All Bedrock, different accounts
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

## Changing Embedding Models

When you switch embedding models, existing records have stale embeddings. Re-embed them:

```bash
gramaton reembed --batch 50
```

This re-embeds all records whose `embedding_model` property doesn't match the current config. Progress is committed in batches. The operation is idempotent -- safe to interrupt and resume.
