# Open Questions

Unresolved design questions, organized by priority.

## Should Address Before Building

22. **~~Activation/decay scoring model.~~ RESOLVED — see D22.** Single authoritative model in retrieval.md. Five-factor weighted `effective_score` with importance floor. Decay rates research-backed: ephemeral 4h, temporal 3d, durable 90d, immutable never. curation.md now references retrieval.md instead of defining its own formula.

23. **~~Spreading activation storage target.~~ RESOLVED — see D22.** New `activation_boost` Float64 property added to data model. Accumulated by spreading activation, decays independently. Distinct from `access_count` (direct access) — keeps direct and indirect signals separate.

24. **~~Chunk nodes vs sub-record nodes.~~ RESOLVED.** They coexist. Chunks are server-created retrieval infrastructure (`chunk_of` edge, no own metadata, surface parent in search). Sub-records are subagent-created knowledge structure (semantic edges, own metadata, independently searchable). Distinguished by edge type. A record can have both.

25. **~~Context envelope fields consumed and discarded.~~ RESOLVED — see D23.** Raw envelope stored as five String properties (`context_about`, `context_who`, `context_prompted`, `context_findable_by`, `context_related`). Not indexed — keywords handle search. Stored for inspectability and audit.

26. **~~Embedding model migration.~~ RESOLVED — see D24.** `embedding_model` property tracks which model generated each node's embeddings. Model change detected automatically. Stale embeddings excluded from similarity search. `gramaton reembed` re-processes stale records. `gramaton status` reports embedding health.

## Should Address During Implementation

27. **~~Reads-triggering-writes under concurrent load.~~ RESOLVED — see D22.** Documented in retrieval.md scoring model: activation writes can be batched (accumulated in memory, flushed periodically) without affecting correctness — activation is approximate by nature. The concurrency note is now explicit in the docs.

28. **~~`gramaton diff --topic` semantics.~~ RESOLVED.** Two-step: structural diff produces full change set, then `--topic` filters it via keyword match + embedding similarity. Cheap structural diff first, semantic filter only over the changed set. Documented in retrieval.md.

29. **~~Node ID format.~~ RESOLVED — see D25.** ULIDs. 26 chars, case-insensitive, no hyphens, time-sortable. Content hash is separate (changes on mutation, ID is stable). Go library: oklog/ulid.

30. **~~Merge conflict semantics.~~ RESOLVED — see D26.** Timestamp wins for property conflicts. Duplicate concepts auto-merged. Modified-vs-deleted keeps the node (tenet 8). All resolutions logged. Interactive three-way merge is a future feature.

31. **~~Capture quality as validation metric.~~ RESOLVED.** Added section 6 (Capture Quality) to validation.md. Measures precision, recall, classification accuracy, context envelope quality, and decomposition quality against a human-labeled gold standard. Identified as highest-risk area.

32. **~~Piggyback curation is best-effort.~~ RESOLVED.** Acknowledged in curation.md. System degrades gracefully — records stay unclassified longer, nothing breaks. `/gramaton-curate` is the guaranteed fallback.

## Won't Block Building

3. **~~Subagent fallback.~~ RESOLVED.** Three capture modes documented in agent-integration.md: subagent (preferred, zero context pollution), inline (agent classifies directly, low-medium context cost), raw (no classification, deferred to curation). Framework capability determines which mode. All produce valid records.

8. **~~contextual_role field.~~ RESOLVED — pulled into v0.1.** Single optional String property, absent on most records. Values: `foundational`, `illustrative`, `attributed_belief`, `counterfactual`. Only set on refuted/deprecated records. Case studies demonstrate clear need. Cost is negligible.

12. **~~Go package structure.~~ RESOLVED — see D29.** Flat, domain-named packages at root. No `internal/`, no `pkg/`. Tools as separate packages. Consumer-defined interfaces. `main.go` as composition root.

33. **~~Multi-store / multi-tenancy.~~ RESOLVED — see D27.** One store per user is the default and recommended setup. Cross-domain connections are the point. `--data-dir` as escape hatch. Export/import for sharing subsets (full, recursive node, topic, branch, date range). Import deduplicates by content hash.

34. **~~Storage growth under "never delete."~~ RESOLVED — known tradeoff, not a bug.** Sized for aggressive scenarios: 300 repos × 100 records = 30K nodes ≈ 500MB with embeddings and chunks. 75K nodes ≈ 1.2GB. Manageable on any modern laptop. Flat hash list commits show strain at 75K+ nodes (2.4MB per commit, 10-50ms diffs) — prolly tree upgrade path exists via StorageBackend interface. Future escape hatches (none needed for v0.1): `gramaton gc --older-than`, commit squashing, export + fresh start. Bulk ingestion uses batch commits (one per repo, not per file) and batch embeddings to manage overhead.

35. **~~Concept node decay inheritance.~~ RESOLVED.** Concept nodes don't need their own decay. Linked records have their own effective scores — the agent sees staleness when inspecting. `gramaton inspect` on a concept node includes a computed `evidence_health` summary ("2 active, 3 stale, 5 decayed") so the agent gets an instant read on hub freshness. No stored property, no inheritance — just a derived field in the inspect output.

36. **~~Chunk search result attribution.~~ RESOLVED with Q24.** Chunk matches return the parent record ID. Sub-record matches return the sub-record's own ID. Distinguished by `chunk_of` vs semantic edge types.

## Resolved

| # | Resolution |
|---|---|
| 1 | Capture quality variance — internally consistent per-user, relative ranking holds |
| 2 | Over/under capture — over-capture is cheap (tenet 9), under-capture tuned via prompt |
| 4 | Context envelope — five domain-neutral structured fields |
| 5 | Concept emergence via evidence accumulation (D15) |
| 6 | Concept dedup — handled by curation, reduced by emergence model |
| 7 | Metadata completeness — everything optional, records work at any level |
| 9 | Manifest qualitative — generated during /gramaton-curate |
| 10 | Manifest staleness — non-issue, manifest is broad summary |
| 11 | Validation strategy documented (validation.md) |
| 13 | Content-addressed chunks from day one (tenet: do it once) |
| 14 | No bundled model, no ONNX, no CGo. Ollama/API/Bedrock (D21) |
| 15 | YAML config format |
| 16 | Deterministic curation: 5m interval, run on startup |
| 17 | Piggyback curation covers primary use case, direct API deferred to v2 |
| 18 | Flat hash lists, prolly trees behind same interface later (D19) |
| 19 | No cross-platform embedding concern — pure Go binary (D21) |
| 20 | First-run experience — `gramaton init` with guided provider setup |
| 21 | Ready to build — no remaining blockers |
