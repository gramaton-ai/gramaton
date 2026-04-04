# Project Design

Design documents for Gramaton. See the [project README](../../README.md) for an overview of what Gramaton is.

Everything here is pre-implementation. All decisions are hypotheses subject to revision once code meets reality.

## Reading Order

Start with tenets and foundations for context, then read the system design docs in order. The remaining docs are reference material — read as needed.

### Context

| Document | What It Covers |
|----------|---------------|
| [Tenets](tenets.md) | Guiding principles for design and implementation decisions |
| [Foundations](foundations.md) | Research influences — philosophy, neuroscience, and technical KR |

### System Design

| Document | What It Covers |
|----------|---------------|
| [Architecture](architecture.md) | Two-part system, component responsibilities, package structure |
| [Data Model](data-model.md) | Graph structure, property types, metadata schema, summary pyramid |
| [Capture and Processing](capture-and-processing.md) | How knowledge enters the system, context envelope, classification, chunking |
| [Retrieval](retrieval.md) | Query pattern, CLI interface, versioning tools, scoring model |
| [Embedding](embedding.md) | Providers (Ollama/API/Bedrock), vector indexing, chunking, model migration |
| [Curation](curation.md) | Deterministic and piggyback curation, decay, consolidation, manifest |
| [Agent Integration](agent-integration.md) | System prompts, subagent templates, capture modes, transparent operation |

### Reference

| Document | What It Covers |
|----------|---------------|
| [Case Studies](case-studies.md) | Real-world examples testing the design across domains |
| [Data Integrity](data-integrity.md) | Threat model, agent misuse, failure modes, defenses |
| [Validation](validation.md) | How we measure whether metadata-filtered retrieval actually helps |
| [Design Decisions](design-decisions.md) | All 29 decisions with rationale |
| [Open Questions](open-questions.md) | Resolved questions and remaining items |
| [Glossary](glossary.md) | Terminology reference |
