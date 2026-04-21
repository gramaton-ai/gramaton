# Project Design

Design documents for Gramaton. These cover the research foundations, data model, and reference material behind the system. For developer-facing documentation, see [docs/](../). For an overview of what Gramaton is, see the [project README](../../README.md).

These documents were written during design and reflect the reasoning behind the system as-built. They are **historical rationale**, not live API references. When a document here conflicts with the current code or the top-level `docs/` surface, the code and top-level docs win.

A larger sunset pass on 2026-04-20 removed nine documents that described superseded pipelines (pre-T-02 server design, the `gramaton_observe` flow replaced by sessions, the early subagent-classification capture model, a validation methodology replaced by the LongMemEval benchmark) or were straight duplicates of top-level `docs/` content. Git history preserves them at commits prior to that date if needed.

## Reading Order

Start with foundations for context, then read the design docs in order. The remaining docs are reference material — read as needed.

### Context

| Document | What It Covers |
|----------|---------------|
| [Foundations](foundations.md) | Research influences -- philosophy, neuroscience, and technical KR |

### Data & Retrieval

| Document | What It Covers |
|----------|---------------|
| [Data Model](data-model.md) | Graph structure, property types, metadata schema, summary pyramid |
| [Retrieval](retrieval.md) | Query pattern, CLI interface, versioning tools, scoring model |
| [Embedding](embedding.md) | Providers, vector indexing, chunking, model migration |
| [Collections](collections.md) | Structured containers with schema enforcement and exhaustive retrieval |

### Reference

| Document | What It Covers |
|----------|---------------|
| [Case Studies](case-studies.md) | Real-world examples testing the design across domains |
| [Data Integrity](data-integrity.md) | Threat model, agent misuse, failure modes, defenses |
| [Design Decisions](design-decisions.md) | Decision log with rationale |
| [Glossary](glossary.md) | Terminology reference |
