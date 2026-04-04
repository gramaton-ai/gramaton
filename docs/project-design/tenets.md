# Tenets

Principles that guide design and implementation decisions. When in doubt, refer to these.

## 1. Do it once, the right way.

Don't build a throwaway prototype and then rebuild it properly. Don't start with SQLite because it's easier and plan to migrate later. If the design calls for content-addressed storage, build content-addressed storage. The "temporary" solution has a habit of becoming permanent, and migration costs are always higher than expected.

This doesn't mean over-engineer. It means when we've already decided the right architecture, implement that architecture. Don't shortcut to something simpler with a "we'll replace it later" caveat.

## 2. Code is cheap.

In the agentic AI era, writing code is no longer the bottleneck. Design decisions, architecture, and clear thinking are the hard parts. Once those are right, implementation is fast. Don't let "that's a lot of code to write" be a reason to compromise the design.

## 3. Don't limit ourselves to traditional timelines.

Human-driven software projects plan in quarters and years because writing, reviewing, and debugging code is slow. AI-assisted development compresses this dramatically. A "multi-year project" assumption from 2023 doesn't apply. Make design decisions based on what's right, not on what seems achievable in a traditional sprint cycle.

## 4. Challenge everything.

Every decision is a hypothesis. AI collaborators should question choices, push back, and flag potential mistakes. Do not default to agreement. Nothing is settled until code proves it works.

## 5. The agent is the user.

The primary consumer is an LLM agent, not a human. Design for how agents think, retrieve, and reason. Humans interact through the agent, not directly with Gramaton (except for debugging and inspection). When in tension, optimize for agent ergonomics.

## 6. Transparent by default.

The system should be inspectable at every level. If you can't inspect it, you can't trust, debug, or improve it. Commit history, metadata, curation changes, retrieval scoring — all queryable. No black boxes.

## 7. Domain-neutral by default.

Gramaton is a general-purpose knowledge store. The user might be an engineer tracking architecture decisions, a researcher analyzing politics, a patient managing health records, or a writer organizing a novel. Every design choice — field names, context envelope structure, relationship types, examples in documentation — must work across all domains. When you catch yourself assuming tickets, PRs, sprints, or teams, step back and ask: does this work for a novelist? A lawyer? A doctor? If not, generalize.

## 8. Never delete, always supersede.

As a knowledge management practice, knowledge is never destroyed. Records are marked as superseded, refuted, or deprecated — and linked to what replaced them. The old knowledge stays in the graph because it may be contextually necessary (understanding why a decision was made, what it replaced, why a belief was held). The audit trail is sacred. Rollback reverts commits, it doesn't erase history.

Delete and purge exist as **repair tools** — for corrupt data, bad ingests, and compliance/PII requirements. They are not part of normal knowledge management. They require explicit reason strings and are logged.

## 9. Capture is a one-way door, curation is a two-way door.

Capture should be fast, autonomous, and low-friction. The agent stores without asking. If a bad capture happens, rollback handles it — so the cost of capturing too much is low. Curation modifies existing knowledge — confidence changes, concept merges, contradiction resolution. That's higher risk. Curation runs on branches, gets diffed, gets reviewed before merging. Easy to discard. Put guardrails where the risk is, not where the speed is.

## 10. Architecture is the product, code is the expression.

The internal structure of the code matters as much as the features it delivers. Clean interfaces between packages, clear dependency direction, small surface areas between components. Every package should have an obvious reason to exist and a clear boundary with its neighbors. When adding a feature, the first question is "where does this live and what does it depend on?" not "how do I make this work?"

Concretely:
- **Depend on interfaces, not implementations.** StorageBackend, VectorIndex, EmbeddingProvider — components talk through contracts, not concrete types. This is what makes the system evolvable.
- **Dependency direction flows inward.** The protocol layer depends on the tool layer. The tool layer depends on the graph engine. The graph engine depends on the storage interface. Never the reverse. An inner layer never imports an outer layer.
- **Packages should be replaceable.** If you can't swap the HNSW implementation without touching the query engine, the boundary is wrong. If you can't change the serialization format without touching the graph logic, the storage interface is leaking.
- **No god packages.** If a package is touching everything, it's doing too much. Split it. A 2000-line package with a clear purpose is better than a 500-line package that imports half the codebase.
- **Name things for what they do, not how they work.** `store.Backend` not `store.ChunkFileSystem`. `index.Vector` not `index.HNSW`. The interface name survives implementation changes.

This discipline pays dividends as the project evolves in ways we can't predict. Clean boundaries mean new features slot in without rewiring existing code. Bad boundaries compound — every new feature touches more files, breaks more tests, and makes the next feature harder.

## 11. Automate the obvious, surface the judgment calls.

If a fix is deterministic and non-destructive, do it automatically. Don't make the user notice a problem and run a command. Embedding model changed? Re-embed in the background. Records pending classification? Piggyback curation picks them up. Ephemeral records expired? Server handles it on startup. The CLI commands and config knobs exist for power users, manual overrides, and edge cases — not as the default path. The average user should never have to think about maintenance. If it requires judgment or could lose data, surface it to the user or agent — but don't force manual action for things the system can handle itself.

## 12. Tool output is a prompt.

The primary consumer of Gramaton's CLI output is an LLM. Every field name, every metadata summary, every error message, every hint — the LLM reads it and makes decisions based on what it sees. A poorly named field or an ambiguous summary degrades agent behavior just as much as a bug in the scoring model. Design tool responses as if you're writing instructions, because you are. `metadata_summary: "Current. Durable, high-confidence (0.85), well-established."` is a prompt that tells the agent how much to trust this record. Get it wrong and the agent reasons poorly, no matter how good the underlying data is.

## 13. No magic values.

Every tunable constant lives in a config file with a sensible default. Decay rates, activation factors, HNSW parameters, chunk sizes, emergence thresholds. Users aren't expected to touch the config, but it must exist for testing and tuning.
