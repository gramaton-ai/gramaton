# Project Design

Design documents for Gramaton. These cover the research, rationale, and detailed specifications behind the system. For developer-facing documentation, see [docs/](../). For an overview of what Gramaton is, see the [project README](../../README.md).

These documents were written during design and refined as the system was built. They serve as both specification and design rationale -- explaining not just what the system does, but why.

## Reading Order

Start with tenets and foundations for context, then read the system design docs in order. The remaining docs are reference material -- read as needed.

### Context

| Document | What It Covers |
|----------|---------------|
| [Tenets](tenets.md) | Guiding principles for design and implementation decisions |
| [Foundations](foundations.md) | Research influences -- philosophy, neuroscience, and technical KR |

### System Design

| Document | What It Covers |
|----------|---------------|
| [Architecture](architecture.md) | Two-part system, component responsibilities, package structure |
| [Data Model](data-model.md) | Graph structure, property types, metadata schema, summary pyramid |
| [Capture and Processing](capture-and-processing.md) | How knowledge enters the system, context envelope, classification, chunking |
| [Retrieval](retrieval.md) | Query pattern, CLI interface, versioning tools, scoring model |
| [Embedding](embedding.md) | Providers, vector indexing, chunking, model migration |
| [Curation](curation.md) | Deterministic and autonomous curation, decay, consolidation, manifest |
| [Observe Pipeline](observe-pipeline.md) | Passive knowledge extraction from agent conversations |
| [Agent Integration](agent-integration.md) | System prompts, subagent templates, capture modes, transparent operation |
| [Server Design](server-design.md) | Daemon architecture, HTTP API, MCP tools, concurrency |

### Reference

| Document | What It Covers |
|----------|---------------|
| [Case Studies](case-studies.md) | Real-world examples testing the design across domains |
| [Data Integrity](data-integrity.md) | Threat model, agent misuse, failure modes, defenses |
| [Validation](validation.md) | How we measure whether metadata-filtered retrieval actually helps |
| [Design Decisions](design-decisions.md) | Decision log with rationale |
| [Open Questions](open-questions.md) | Resolved questions and remaining items |
| [Glossary](glossary.md) | Terminology reference |
