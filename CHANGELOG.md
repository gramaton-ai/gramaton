# Changelog

All notable changes to Gramaton are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`hooks/claude_code.go` — Go port of the four Claude Code hook
  scripts (Phase 2 of Windows support, `01KQ0DNH8S97F13R4ZS2EDDWH4`).**
  `ClaudeCodeSessionStart`, `ClaudeCodeStop`, `ClaudeCodePreCompact`,
  `ClaudeCodePostCompact` replicate the behavior of the legacy
  `hooks/claude-code/*.sh` scripts: decode stdin JSON, validate the
  session_id against `^[A-Za-z0-9_-]+$`, shell out to `gramaton
  session start/get/archive` via `RunGramaton` (now an overridable
  package-level var for tests), write the `current-session.json`
  shared pointer and the per-cwd session file, count uncaptured
  segments pre-compaction, write the `.precompact-uncaptured` /
  `.compacted` flag files. Handlers are fail-open — every error
  logs to `~/.gramaton/hooks.log` and returns cleanly so Claude
  Code is never blocked. 11 unit tests cover each handler's happy
  path + its security guards (unsafe session_id rejected before
  any CLI shellout), state-file writes, counter resets, and both
  branches of the pre-compact count logic. Not yet wired into the
  binary — commit 4 introduces the `gramaton hook <event>` cobra
  subcommand that dispatches to these.

- **`hooks` Go package — shared state helpers for the upcoming
  `gramaton hook <event>` subcommand (Phase 2 of Windows support,
  `01KQ0DNH8S97F13R4ZS2EDDWH4`).** Introduces `hooks/state.go`
  with the primitives that every Claude Code / Kiro hook handler
  needs: `HookInput` stdin decoder (session_id with agent_id
  fallback for Kiro), `ValidSessionID` regex guard against path-
  traversal shapes, atomic counter R/W (`ReadCounter`,
  `WriteCounter`, `IncrementCounter`, `ResetCounter`), `Logger`
  for tagged append-only writes to `~/.gramaton/hooks.log`,
  `RunGramaton` for CLI shellout (respects `GRAMATON_BIN` env),
  cross-platform `CwdSlug` that handles Windows drive-letter
  colons (`C:\Users\b\foo` → `C-Users-b-foo`), `ExtractThreshold`
  env-override reader, and `atomicWriteFile` helper (tmp + rename).
  The R-M-W race in `IncrementCounter` against simultaneous hook
  fires from different processes is documented and accepted — same
  as the legacy shell script, costs at most one lost turn which
  just nudges extraction one turn later. 17 tests cover the edge
  cases (valid/invalid session IDs, corrupt counter files, empty
  and malformed stdin, Windows/Unix slug parity, logger append,
  env overrides), all green under -race.

### Changed

- **D34 + BERT matmul benchmark comments reality-calibrated from
  real AMD64 Windows hardware validation
  (`01KPVBNJGNQFJF5KN7TS9RS9M7`).** The AVX2+FMA3 kernel was
  validated on a Ryzen 7 5800X3D (Zen 3): all scalar-vs-SIMD
  parity tests pass including tile-boundary edge cases, and
  measured throughput is ~90 GFLOPS single-core (~63% of
  theoretical peak, tuned-BLAS ballpark). `docs/project-design/
  design-decisions.md` D34 gains an "Update 2026-04-24" note
  with the measurements. Benchmark comments in
  `embed/bert/math_test.go` were updated to reflect observed
  reality: AttnProj ~411µs (target <500µs — holds), FFN Up/Down
  ~1.67ms / ~1.65ms (original aspirational target <1ms was
  work-volume optimistic; live target is <2ms, which scales
  linearly from AttnProj and is not a kernel-efficiency issue),
  AttnScores ~21µs (target <50µs — holds).

- **`server/info.go` split + `RequestShutdown` refactored for
  cross-platform shutdown (Phase 1 of Windows support,
  `01KQ0DNH8S97F13R4ZS2EDDWH4`).** `IsProcessAlive` moves into
  per-OS files: `server/info_unix.go` uses `syscall.Signal(0)`,
  `server/info_windows.go` uses `windows.OpenProcess` with
  `PROCESS_QUERY_LIMITED_INFORMATION`. `Server.RequestShutdown`
  now non-blocking-sends on a new `s.shutdownCh` field rather
  than `os.FindProcess(os.Getpid()).Signal(syscall.SIGTERM)` —
  the old approach worked on Unix but Windows only supports
  `os.Kill` for self-signaling, which is ungraceful. The channel
  approach is cross-platform and cleaner: no OS signal round-trip,
  main loop already selected on the channel for idle shutdown.
  New `TestRequestShutdownNonBlocking` pins the invariant that
  50 concurrent calls return quickly with first-reason-wins.
  Together with commits 2-4, Phase 1 is complete: the full
  Gramaton tree cross-compiles for `GOOS=windows GOARCH=amd64`
  and passes race tests on macOS.

- **`index/flat_mmap.go` migrated to `internal/mmap` (Phase 1 of
  Windows support, `01KQ0DNH8S97F13R4ZS2EDDWH4`).** Four direct
  `syscall.Mmap`/`Munmap` callsites (remap + rewrite-path unmap +
  Close-path unmap + initial Mmap) replaced with `mmap.Region`.
  The remap-after-Flush flow is preserved exactly: unmap the old
  region, truncate + rewrite, open a new region. No behavior
  change on Unix. Combined with the safetensors migration, the
  full Gramaton tree now cross-compiles for `GOOS=windows
  GOARCH=amd64` — Phase 1's load-bearing blocker is clear.

- **`embed/bert/safetensors.go` migrated to `internal/mmap`
  (Phase 1 of Windows support, `01KQ0DNH8S97F13R4ZS2EDDWH4`).**
  Six direct `syscall.Mmap`/`Munmap` callsites replaced with
  `mmap.Region` from the new internal package. Per-error-path
  cleanup consolidated into a small `fail` closure so we never
  leak the mapping or fd. No behavior change on Unix; the file
  now cross-compiles and runs on Windows. Bert unit tests
  unchanged, still green under `-race`.

### Added

- **`internal/mmap` package: cross-platform read-only file mapping
  (Phase 1 of Windows support, `01KQ0DNH8S97F13R4ZS2EDDWH4`).** New
  `Region` type with `Open`, `Bytes`, `Close`. Unix implementation
  wraps `syscall.Mmap`/`Munmap` (MAP_SHARED, PROT_READ); Windows
  implementation wraps `CreateFileMapping` + `MapViewOfFile` via
  `golang.org/x/sys/windows`. Both platforms share the same public
  surface via a `//go:build` split. Close is idempotent and safe
  on a nil receiver. Round-trip, empty-file, negative-size,
  multi-page, and double-close tests run on every CI OS. Unblocks
  commits 3-4 (safetensors + flat_mmap migrations) without
  touching the existing syscall callsites yet.

- **GitHub Actions CI with three-OS matrix (Phase 1 of Windows support,
  `01KQ0DNH8S97F13R4ZS2EDDWH4`).** New `.github/workflows/ci.yml`
  runs `build`, `test`, and `test -race` on `ubuntu-latest`,
  `macos-latest`, and `windows-latest`; `vet` on ubuntu only (static
  analysis is portable). Go 1.26, 20-minute per-job timeout,
  `fail-fast: false` so one OS failing doesn't cancel the others.
  Windows jobs will be red until the mmap abstraction lands in
  commits 2-5 of Phase 1; this is expected and unblocks
  surface-assumption discovery on the other two OSes in the
  meantime.

- **Structured-output implementation for OpenAI and Bedrock
  (Cluster 2 Phases 2b + 2c, `01KQ05MEQE2VMNG0SWSV0ZR9RH`).** Both
  providers now return `SupportsStructuredOutput()=true` and have
  real `CompleteStructured` implementations.
  OpenAI uses `response_format: {type: "json_schema", json_schema:
  {strict: true, schema: ...}}` on `/v1/chat/completions`. Strict
  mode is the enforcement knob — without it, OpenAI treats the
  schema as a hint rather than a contract. Supported on gpt-4o
  and later; older models return an API error, which the caller's
  structured-path-error fallback catches transparently.
  Bedrock uses the Converse API's `toolConfig` with a forced
  single-tool choice (`ToolChoiceMemberTool` pinning the
  `emit_output` tool). Schema passes through `document.NewLazyDocument`
  from `github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document`.
  Response's `toolUse.input` is marshaled back to `json.RawMessage`
  via `MarshalSmithyDocument`. Works cleanly for Claude-family
  models on Bedrock; non-Claude models that don't support tool-use
  return an API error and the caller falls back to Complete.
  Both paths record usage via `telemetry.Record` identically to
  the text path, so `llm_usage.json` reconciliation stays correct.
  5 new tests (openai `TestSupportsStructuredOutput`,
  `TestCompleteStructuredSuccess`, `TestCompleteStructuredAPIError`;
  bedrock `TestSupportsStructuredOutput`, `TestCompleteStructured`
  using the same Smithy-skip guard the existing `TestComplete` uses).

- **Structured-output shortcut for classification — Anthropic path
  live (Cluster 2 Phase 2a, `01KQ05MEQE2VMNG0SWSV0ZR9RH`).** New
  `SupportsStructuredOutput()` + `CompleteStructured(ctx, schema,
  prompt)` methods on `llm.Provider`. Anthropic implementation uses
  the tool-use API with `ToolChoice: {type: tool, name: emit_output}`
  — the model is forced to respond via a schema-enforced `tool_use`
  block, so output is guaranteed to conform before it reaches our
  code. Eliminates the "chatty preamble around JSON" and "tool-use
  tag leakage" failure modes that `parseClassification` and Phase 1
  sanitizer had to defend against. OpenAI and Bedrock stubs return
  `SupportsStructuredOutput()=false` + error (follow-up commits
  will add `response_format: json_schema` / Converse tool-use);
  claudecli and kirocli permanently return false (subprocess
  wrappers can't enforce schema). `Metered` and `RateLimited`
  wrappers delegate.
  Classification call site in `curation.autonomous.go` now passes
  `classificationSchema` on the `llmWork` struct; `parallelLLM`
  routes through `CompleteStructured` when `schema != nil &&
  provider.SupportsStructuredOutput()`, with transparent fallback
  to `Complete` on structured-path error. Same `llmResult.response`
  string-typed interface, same `parseClassification` consumer on
  the other end — `parseClassification` receives clean JSON text
  either way. Summarization / contradiction / concept call sites
  unchanged for now; add schemas there if the classification path
  proves out.
  6 new tests: anthropic `TestSupportsStructuredOutput`,
  `TestCompleteStructuredSuccess`, `TestCompleteStructuredMissingToolUseBlock`;
  llm `TestMeteredDelegatesStructuredOutput`; plus 2 test-double
  conformance updates across 8 test files.

- **Server-start content-quality self-heal hook.** Server's `Run`
  and `StartHTTP` now spawn a one-shot async `curation.RunSelfHeal`
  pass at startup (non-blocking — fires after the HTTP listener is
  ready, cost is microseconds per record for sanitize.Field
  comparisons on a clean store). Catches any legacy drift between
  server restarts and any slippage from future bulk-import paths
  that might bypass api/ write-site sanitization. Running in the
  1-minute curation cycle was rejected as wasteful (Phase 1
  prevents new contamination at write time; most cycles would no-
  op). Manual on-demand sweeps remain available via
  `gramaton repair --content-quality`.

- **Content-quality self-heal pass (Cluster 2 Phase 3,
  `01KPZZNG45PC7D6HC8SQH3P9N1`).** New `curation.RunSelfHeal` walks
  every Memory + Session record, detects LLM tool-use-format
  contamination in `content_short` via the same `internal/sanitize`
  helper used at Phase 1 write sites, and applies a deterministic
  repair cascade:
    1. **Strip**: if sanitization yields ≥ 50 characters of clean
       prose, write that and clear `embedding_model` so the next
       reembed cycle refreshes against the corrected summary.
    2. **Fallback**: if strip yields too little, extract the first
       1-2 sentences of `content_full` (deterministic, no LLM) and
       use that as the repaired summary.
    3. **Flag**: if neither tier salvages anything, set
       `repair_needed_llm=true` on the record for a future
       LLM-escalation pass (not implemented in this landing).
  Each repaired record gets `repaired_at` + `repair_method`
  (`stripped` / `fallback` / `flagged`) audit properties so the
  method mix is observable without parsing logs.
  Exposed via CLI as `gramaton repair --content-quality` alongside
  the existing structural-integrity `gramaton repair`. Same
  server-must-be-stopped guard; `--dry-run` still covers structural
  checks only (self-heal only runs on a live repair today —
  dry-run support is a cheap follow-up if needed).
  10 regression tests in `curation/self_heal_test.go` cover each
  tier of the cascade, the no-op path for clean records, and
  `firstSentences` helper edge cases (no-punctuation input,
  maxChars truncation at sentence boundaries).

- **`internal/sanitize` package (Cluster 2 Phase 1,
  `01KPZZNG45PC7D6HC8SQH3P9N1`).** New helper `sanitize.Field(s)` and
  `sanitize.Validate(orig, cleaned, name, max)` strip LLM tool-use-
  format tail leakage (`</summary_short>`, `<parameter name=`, model
  stop tokens) from short metadata fields without mangling legitimate
  angle-bracket content (e.g. `"React's <Button> component"`).
  Applied at every write site that accepts an LLM-generated summary
  string: `api/capture.go`, `api/classify.go`, `api/update.go`,
  `api/sessions.go` segment path, and `curation.parseClassification`.
  User-input paths reject pure-contamination inputs with `ErrInvalid`
  via `sanitize.Validate`; the LLM-output path in curation silently
  drops contamination-only output with a warn log rather than
  overwriting clean existing values. Covers `summary_short` +
  `context_about` + `context_who` + `context_findable_by` +
  `context_prompted` + `context_related` + `context_source_type` +
  `context_time_sensitivity` + `context_reliability` +
  `context_capture_reason`. Deliberately does NOT apply to
  `content_full`: empirical scan 2026-04-24 found 3 contaminated
  records, all in summary fields only; aggressive strip on
  `content_full` would mangle records that legitimately discuss
  code / XML / tool-use format. Package lives in `internal/` to
  break the circular import (api/ already imports curation/).
  10 regression tests in `internal/sanitize/sanitize_test.go`
  including the exact observed pattern from the 3 contaminated
  records.

### Fixed

- **Cluster 2 Phase 2b+2c follow-up: OpenAI strict-mode schema
  compatibility.** gramaton-review flagged classificationSchema as
  incompatible with OpenAI's `response_format: json_schema` strict
  mode: strict requires `additionalProperties: false` on every
  object and forbids `minimum`/`maximum` on numerics. Without the
  fix, every classification call on an OpenAI-configured user
  would error at the strict-schema validation step, fall back
  silently to Complete, and emit a WARN log — doubling API cost
  and spamming telemetry.
  Fix: dropped `minimum: 0.0, maximum: 1.0` from confidence (the
  post-parse clamp in parseClassification at autonomous.go:1846
  already handles out-of-range) and added `additionalProperties:
  false`. Anthropic and Bedrock-via-Claude both accept the tighter
  schema without complaint. Added 2 tests missed in the earlier
  pass: openai `TestCompleteStructuredNoChoices` + bedrock
  `TestCompleteStructuredMissingToolUseBlock` (both mirror the
  existing equivalents for the text path).

- **Cluster 2 Phase 2a follow-up: review-gap cleanup.** gramaton-review
  on commit `8227dfd` flagged two MEDIUM gaps. (1) The structured-output
  fallback path in `curation/parallel.go` `runSingleWork` silently
  reverted to `Complete` on `CompleteStructured` error — a persistent
  provider regression would never surface. Added a `slog.Warn` on
  fallback with the record id + task + err so ops can see it.
  (2) End-to-end integration for the structured path was untested at
  the `parallelLLM` layer. Added `TestParallelLLMUsesStructuredWhenProviderSupports`,
  `TestParallelLLMFallsBackOnStructuredError`,
  `TestParallelLLMSkipsStructuredWhenNoSchema` using a new
  `structuredCapableMock` — verifies the capability-dispatch
  actually fires, the fallback fires on structured error, and
  schema-less work items bypass the structured path. Also added
  `TestCompleteStructuredAPIError` (anthropic 4xx/5xx surface as
  errors, not empty responses) and extended `TestMeteredRefusesWhenCapped`
  to confirm `CompleteStructured` also refuses when capped (regression
  guard for the "new Provider method silently bypasses the cap"
  class the classification batch bug was).

- **Cluster 1 follow-up: review-gap cleanup.** Dead defensive
  fallback in `curation/batch.go` (previously: `if model == ""
  { model = longModel }` after a map lookup that's always hit)
  replaced with an explicit-skip-and-warn path so unexpected
  `CustomID` echoes from Anthropic surface rather than silently
  mis-attribute. `core.Engine.WrapLLM` docstring tightened to
  spell out that `fn` runs under the write lock and must not do
  I/O, and that runtime use is unsafe. New tests:
  `TestFindMeteredDirect` / `TestFindMeteredThroughRateLimited` /
  `TestFindMeteredRawProviderReturnsNil` cover the provider-chain
  walk; `TestWrapLLMReplacesProvider` / `TestWrapLLMNilIsNoOp`
  cover the engine hook; `TestMeteredRecordCallErrorPath` covers
  the non-nil-error branch of `RecordCall`.

- **All LLM paths now flow through the `Metered` wrapper, not just
  curation (`01KPZYSJFRW8P41SWQC750FK4B`, `01KPZZAS2580FBEPPVQHVER64C`).**
  Previously only `curationLLM` was wrapped with `llm.NewMetered`
  (`server/server.go:312`, `:413`), so rerank, query decompose, and
  the classification Message Batches API made LLM calls that were
  invisible to `llm_usage.json`, bypassed `max_calls_per_day` /
  `max_cost_usd_per_day` cap enforcement, and landed in the lifetime
  `by_task: "unknown"` bucket. Reconciling local tracker numbers
  against the Anthropic usage CSV showed a several-thousand-call
  gap — large enough to matter for cost visibility.
  Fix: wrap once at server construction via a new
  `core.Engine.WrapLLM(fn)` method that replaces `e.prov.llm` and
  rebuilds the searcher subsystem so rerank picks up the wrapped
  reference. Every consumer (search rerank, decompose, curation,
  classification batch) now sees the `Metered` wrapper. Removed
  duplicate `NewMetered` calls at `server/server.go:312,413`.
  Classification batch bypass specifically fixed via new
  `llm.Metered.RecordCall(model, task, usage, latency, err)` method
  that lets out-of-band call paths (Anthropic Message Batches API)
  self-report; `curation/batch.go` now walks the provider chain via
  `findMetered(p)` and records per-result usage after
  `FetchResults`, tagged as `classification_short` or
  `classification_long` based on which tier was submitted. New
  regression test `TestMeteredRecordCallAccountsForBypassPath`
  verifies RecordCall feeds the tracker identically to the Complete
  path without touching the inner provider.

### Added

- **`SECURITY.md`** (`01KPV62W41C44YKMX9K782AEE9`): responsible-
  disclosure policy. Routes vulnerability reports through GitHub's
  private vulnerability reporting (Security tab → Report a
  vulnerability), keeping the maintainer's email off the repo. Lists
  supported versions (alpha: `main` only), triage expectations
  (solo maintainer, no SLA, ~7-day ack target), scope (CLI + serve +
  store format + loopback gates + secret handling), and credit
  policy. GitHub surfaces this on the Security tab and the
  Community profile.

- **`CODE_OF_CONDUCT.md`** (`01KPV62W41C44YKMX9K8TCK9MM`):
  Contributor Covenant v2.1, fetched verbatim from the canonical
  source at github.com/EthicalSource/contributor_covenant with the
  `[INSERT CONTACT METHOD]` placeholder replaced by
  `brandonlattin@gmail.com`. `CONTRIBUTING.md` no longer links out
  to the external Covenant URL; it points to the local file
  instead. Closes the "forthcoming" stub that has been in
  CONTRIBUTING since the repo went public-prep.

### Changed

- **`gramaton_collection_add_batch` now mirrors single-add's
  curation-profile dedup semantics (P1-78 follow-up,
  `01KPEERMWYTFPJSDCNAH148B7W`).** On `curation=minimal` collections
  (shopping-list / packing-list shape), duplicate titles land in
  `Added` with `deduplicated=true` pointing at the existing item's
  ID instead of `Failed` with `code=duplicate`. This matches the
  Phase 5 Layer 2 behavior already shipped for single-add, so a
  loader batch-importing into a minimal-curation collection gets
  idempotent results rather than spurious per-item failures.
  Intra-batch dedupes on minimal collections follow the same rule.
  On any other profile (standard / default), duplicates still land
  in `Failed` — T-02 semantics preserved.
  Dedup now also uses a shared `normalizeTitle` helper (trim +
  lowercase) across both paths. Previously batch used `ToLower`
  only, so `"foo"` would fail to dedupe against an existing
  `"foo "` under batch but would under single-add.
  `BatchAddSuccess` gains an optional `deduplicated` JSON field;
  omitted when false so the wire shape is unchanged for fresh
  inserts. Idempotent entries emit no `collection_add`
  CommitAction (nothing mutated); an all-dedupe batch skips the
  engine `Save` entirely.

- **`storage.ProllyTree.Diff` no longer degrades to a full scan on
  internal-vs-internal subtree differences (P1-54,
  `01KPED3C7C8S1MWTSF9ZZ1AD68`).** Previously the diff short-circuited
  only when the root hashes matched or both nodes were leaves; any
  internal-vs-internal case with differing hashes fell through to
  `allEntries` on both sides, reading every leaf chunk regardless of
  how localised the change was. The docstring promised "skips entire
  subtrees when their hashes match" but delivered that only at the
  root.
  Fix: merge-walk the two internal-node child lists; recurse into
  child pairs whose first-key aligns but whose content hash differs;
  skip the pair entirely when both hash and key match. Content-
  defined chunking keeps most boundaries stable across neighbouring
  commits, so typical single-record-change diffs now touch O(log N)
  chunks instead of O(N).
  `BenchmarkProllyDiffSmallChange` (2000-entry trees differing by 1
  entry): 3.05 ms → 0.29 ms, a **10.5× speedup** on Apple M3. The
  ratio grows with tree size (baseline scales linearly with entry
  count, fix scales with log of the tree size plus the number of
  actual changes). `gramaton_diff` is the direct beneficiary; any
  caller that lands on `graph.DiffCommits` inherits the win.
  Mixed internal/leaf depth (rare, happens after heavily-skewed
  rebalancing) still falls back to `allEntries`, now bounded to
  whichever subtree is smaller rather than both. Boundary-shift
  cases (rebalancing changed chunk boundaries) use the same
  fallback on the unmatched children; the caller's map-diff
  cancels any entries that happen to match on the other side.

### Added

- **CLI parity + `temporal-queries` guide topic (temporal-queries
  Phase 9, release-gate prep).** The user-facing CLI commands now
  match the MCP + HTTP surface for the temporal tools:
    - `gramaton log` gains `--since`, `--until`, `--action`
      (repeatable), `--exclude-curation`, `--include-records`.
      `--record` is kept for source-compatibility but documented
      as superseded by `gramaton history <id>`.
    - `gramaton diff` gains `--until`.
    - New `gramaton history <id>` subcommand with `--limit`,
      `--since`, `--until`, `--action`.
  A new `gramaton_guide(topic="temporal-queries")` topic covers
  the four axes (A/B/C/D), the four tools, and the anti-patterns
  agents keep falling into (client-side date filtering, bare
  `collection_items` for history questions, `search`+`inspect`
  fan-out). Topic content ships embedded in the binary.
  **Release gate: the cold-agent smoke test is manual.** Fresh
  Claude Code session with only CLAUDE.md defaults, prompt
  "what did I close yesterday in gramaton?", expect ONE call to
  `gramaton_log(since, until, actions=[resolve, collection_update],
  include_record_mutations=true)`. Fail = any fan-out, bare
  `collection_items`, or >3 tool calls. If the smoke test fails,
  iterate on tool descriptions before alpha ships.

- **Idempotent `gramaton_collection_add` on minimal-curation
  collections (temporal-queries Phase 5 Layer 2).** When a
  collection's `curation` profile is `minimal` (shopping-list /
  packing-list style), a second `collection_add` with a
  case-insensitive + trim-normalised title match returns the
  existing item's ID with `deduplicated: true` in the response
  instead of `ErrConflict`. Short-content items ("eggs", "milk")
  treat identical content as the same item; Layer 1's existing
  collection-member skip already blocks cross-collection
  contamination at the auto-supersession path. Structured
  collections (default `standard` curation -- backlog, todo, etc.)
  keep the T-02 `ErrConflict` behaviour: same-title-different-
  context is legitimate there. Layer 3 (context-enriched embedding
  input) and per-stage curation-profile skip logic stay deferred
  to a Phase 5 follow-on.

- **Collection templates + five starter templates (temporal-queries
  Phase 7).** `gramaton_collection_create` now accepts a
  `template` parameter. When set, the named template populates any
  caller fields that are left empty (schema, description,
  clear_mode, supersession, curation) via shallow merge --
  caller-provided fields always win. Ships with five starter
  templates embedded in the binary:
    - `backlog` -- dev tickets, full curation, rich schema.
    - `todo` -- personal tasks, standard curation, due-date field.
    - `reading-list` -- books/articles with notes, `clear_mode:
      unlink` so paused reads stay as records, full curation on
      captured notes.
    - `shopping-list` -- grocery-shaped short-content items,
      `curation: minimal` so embed + concepts run but LLM-expensive
      stages don't waste compute on "eggs".
    - `packing-list` -- trip-scoped checklist, minimal curation.
  Template descriptors live in `api/templates/*.yaml`, loaded via
  `//go:embed` on first use. `api.LookupTemplate(name)` and
  `api.ListTemplates()` are the programmatic surface; a future
  creation-wizard or `gramaton template list` CLI can build on
  those.

- **`gramaton_log` extended with `actions` / `exclude_curation` /
  `include_record_mutations` (temporal-queries Phase 8).** The
  commit-timeline tool gains three filters that compose with
  Phase 2's date range to cover the agent-facing temporal-query
  smoke test ("what did I close yesterday") in a single call:
    - `actions: ["resolve", "collection_update"]` filters commits
      by Phase 3's CommitAction.Kind. A commit matches when any of
      its actions' Kind is in the filter. Empty-array input is
      rejected with ErrInvalid (distinct from "no filter").
    - `exclude_curation: true` drops commits whose Message starts
      with `"curation:"`. Message-prefix based so it works against
      pre-D3 commits without needing the Phase 3 migration to
      cover curation emission.
    - `include_record_mutations: true` enriches each LogEntry with
      a per-record `Mutations []MutationSummary` slice built from
      the commit's CommitActions. Each summary carries
      `{record_id, kind, field, title, summary_short}`; title /
      summary_short come from the HEAD record when available.
      Capped at 20 mutations per commit (exposed via
      `mutations_truncated: true` when the cap fires) to keep
      response size predictable on curation-heavy days.
  HTTP: `GET /v1/log` accepts `?action=<kind>` (repeated) or
  comma-separated `?actions=a,b`, plus `?exclude_curation=true`
  and `?include_record_mutations=true`. MCP + CLI proxy carry the
  typed fields directly.

- **D3 structured `CommitAction` on commits (temporal-queries Phase 3,
  partial land).** Commits now carry an optional `Actions []CommitAction`
  slice alongside `Message`. Each action has `Kind` (e.g.
  `"resolve"`, `"collection_update"`, `"capture"`) and optional
  `RecordID` / `Field` for record-scoped intent. `engine.Save`
  gains a variadic `actions ...graph.CommitAction` parameter that
  routes to the new `graph.SaveWithActions`; old-style `engine.Save(msg)`
  call sites keep working with `Actions == nil` (and the field is
  omitempty on write so pre-D3 binaries read post-D3 commits
  unchanged). `WriteSession.AddAction` accumulates actions inside
  `engine.WithWriteBatch` closures for batched callers.
  Migrated api/ sites: `api.Resolve`, `api.Capture`,
  `api.Update`, `api.Classify`, `api.DeleteRecord`, `api.Link`,
  `api.Unlink`, and every `api/collections.go` Save site
  (create / add / add_batch / remove / update / move / rename /
  retire / unretire / schema_update / migrate). Deferred to a
  follow-on: `curation/` cluster per-record actions and the
  `tools/lint/saveactions` AST lint that prevents future drift.
  Phase 8's `gramaton_log(actions=[...])` filter is now wire-
  ready against these commits.

- **Collection behaviour config: `clear_mode`, `supersession`,
  `curation` (temporal-queries Phase 4).** `gramaton_collection_create`
  accepts three optional behaviour knobs that tell future phases how
  to treat items in the collection. Stored as plain node properties
  (`collection_clear_mode`, `collection_supersession`,
  `collection_curation`) so Phase 5 (dedup layers) and Phase 8 (log
  filters) can read them without parsing the item-schema blob.
  Values:
    - `clear_mode`: `resolve` (default) or `unlink`
    - `supersession`: `collection` (default), `store`, or `none`
    - `curation`: `full`, `standard` (default), `minimal`, or `none`
  Getters `api.CollectionClearMode(n)`, `CollectionSupersession(n)`,
  `CollectionCuration(n)` fall back to the default when the
  property is absent, which is why no migration sweep is needed for
  existing collections -- consumers that read these fields through
  the getters get correct-by-default behaviour regardless of when
  the collection was created.

- **`gramaton_collection_items(as_of=...)` — point-in-time
  membership (temporal-queries Phase 6).** Passing `as_of` switches
  the tool from HEAD to historical-commit mode: the response lists
  the members the collection had at the commit at-or-before
  `as_of` (via D7's `TSIndex.CommitAt`), with each member's
  per-commit state read through `NodeHashInCommit` + CAS. Response
  carries `as_of` + `semantics: "point_in_time"` so agents see
  which contract produced the result. Future dates rejected with
  `ErrInvalid`; as_of values before the collection's creation
  return an empty item list with the semantic fields still set
  (so agents can distinguish "didn't exist yet" from "empty at
  HEAD"). The existing filter / projection / sort knobs apply to
  the historical read unchanged; migration accounting is skipped
  because historical snapshots are read-only.
- **`api.validateAsOf` helper.** Extracts the as_of parse +
  future-rejection that future `as_of` readers across search /
  inspect / stats will reuse when that phase lands.

- **Date-range params on the three temporal tools (temporal-queries
  Phase 2).** `gramaton_diff` gains an `until` parameter (defaults to
  HEAD) so callers can ask for a bounded window instead of
  `since → HEAD`. `gramaton_log` and `gramaton_history` gain
  `since` + `until`. All three feed through one validator
  (`validateSinceUntil`) that parses both dates and rejects
  `since > until` with `ErrInvalid`. With D7 in place, date-bounded
  calls bypass `MaxLogTraversal` because the range itself bounds
  the work -- the walker starts at `CommitAt(until)` and stops when
  `commit.Timestamp < since`. `gramaton_diff`'s since-hunter walker
  was replaced with `TSIndex.CommitBefore(since)`, which preserves
  the inclusive-of-`since` semantic (a commit at exactly `since` is
  INCLUDED in the diff window, matching the pre-D7 behaviour).
- **`graph.TSIndex.CommitBefore(t)`** — strict-before variant of
  `CommitAt`. Returns the latest commit whose timestamp is strictly
  less than `t`. Used by `Diff` for the since-boundary, where an
  equality match must not be returned (otherwise the diff window
  would exclude commits at exactly `since`).

### Changed

- **`graph.DiffCommits` now treats a `nil` oldCommit as "no prior
  state"** (every entry in the new commit is reported as `Added`),
  instead of nil-dereffing on `oldCommit.NodeTreeRoot`. Latent panic
  uncovered while writing Phase 2's diff-with-empty-since tests --
  no in-tree caller actually triggered it in practice, but
  `gramaton_diff` without a `since` value would have crashed the
  server once a populated store met that code path.

### Fixed

- **`gramaton_history` prevHash stale-across-gap (RC-4).** The
  per-record history walker's `prevHash` wasn't updated when
  `NodeHashInCommit` returned `found=false`. If a record was
  deleted at commit C2 and recreated at C3 with the same content
  hash as its original creation at C1, the walker compared C1's
  hash against the stale `prevHash` carried across the C2 gap and
  silently dropped the C1 entry. Fix: reset `prevHash` to empty
  on `found=false` so a later reappearance registers as a first-
  appearance. Regression covered by
  `TestHistoryRC4DeleteRecreateSameHashSurfacesBoth`. No user-
  facing API path reuses IDs today (ULIDs are generated on
  `AddNode`), so the bug required branch-merge-shaped flows or
  the new `graph.AddNodeWithIDForTest` hook to repro.

- **D7 timestamp-indexed commits (temporal-queries Phase 1).** New
  `graph.TSIndex` type, a bbolt-backed index mapping commit
  timestamps to commit hashes. Every `engine.Save()` now adds an
  entry to the `commit_timestamps` bucket keyed by 8-byte big-
  endian unix nanos + `#` + 12-char commit hash prefix; lexicographic
  key order equals chronological order so cursor `Seek` gives
  O(log N) snap-to-prior lookups (`CommitAt`) and range scans
  (`CommitsBetween`). Foundational primitive for upcoming temporal
  queries (Axis A / B / D); replaces the walker-model HEAD-backward
  traversal that was bounded by `MaxLogTraversal=5000` and broke
  for deep-history date queries on active stores. Exposed on the
  engine via `Engine.TSIndex()`. Also adds `graph.LoadCommitMeta`,
  a commit-only read helper that callers use for chain traversal
  without paying for the prolly-tree load in `Graph.Load`.
- **`gramaton migrate` CLI subcommand.** One-shot, idempotent
  migration for the 1 → 2 store format bump. Opens the store via
  a migration-private code path that bypasses the boot gate,
  walks the commit chain HEAD → root to backfill the timestamp
  index, and writes `FORMAT=2` on success. Refuses to run while
  a server is active. Rerun-safe after a partial failure because
  bbolt Puts are idempotent on identical (key, value) pairs.

### Changed

- **Store format bumped 1 → 2 with refuse-to-boot gate.**
  `internal/version.StoreFormatVersion` is now 2. `core.CheckFormatVersion`
  rejects v1 stores with a clear message pointing at `gramaton
  migrate`; no auto-upgrade at boot by design (single-user
  lifecycle, manual migration is simpler than resumable-sentinel
  code). Fresh stores still write the current version
  transparently. Collection-level defaults (`clear_mode`,
  `curation`) were considered for the migrate sweep but deferred
  to Phase 4 when those fields land on the collection schema;
  read-time fallback covers correctness in the interim.

- **Wizard step-branch tests** (`internal/setup/step_bootstrap_test.go`,
  `step_verify_test.go`, expanded `step_llm_test.go`). Per-step
  coverage of branches previously exercised only through the two
  `wizard_test.go` smoke paths:
    - Step 1 (bootstrap): Skip / OpenAI-key / Bedrock (with and
      without profile) / data-dir-perms branches.
    - Step 2 (LLM): Skip / help-then-skip / Anthropic-empty-key-
      falls-back-to-skip / Bedrock / customize-caps-with-invalid-
      inputs branches.
    - Step 5 (verify): skip-everything baseline / config-perms /
      BERT / OpenAI-key-present / OpenAI-key-missing / LLM-key-good /
      LLM-key-wrong-perms / hooks-executable / hooks-missing-exec
      branches.
  Package coverage: 51.6% → 67.4%. Tests are offline and
  deterministic (fake MCP backend, scripted prompter); the
  network-dependent BERT fallback branch is gated behind
  `GRAMATON_TEST_NETWORK=1`.
- **AVX2 ↔ generic matmul parity test**
  (`embed/bert/math_test.go::TestMatMulKernelParity`). Existing
  `TestMatMul*` cases call the dispatched public `MatMul`, which
  on AMD64 with AVX2+FMA3 runs the assembly kernel exclusively
  and never exercises the pure-Go `matMulGeneric` path. New test
  runs both kernels on aligned-body, M-remainder, N-remainder,
  both-remainders, below-SIMD-threshold, and BERT-sized shapes
  (128x384x385, 128x384x1536) and asserts element-wise equality.
  Guards against a future SIMD-kernel regression surfacing only
  under full BERT inference.

### Changed

- **README + `docs/providers.md` updated for the interactive
  wizard.** README Quick Start now describes the 5-step wizard
  flow (bootstrap → LLM → MCP → hooks → verify) and documents
  `--non-interactive` as the legacy scripted path. The CLI
  quick-reference table's `gramaton init` row reflects the new
  behavior. `docs/providers.md` BERT setup line notes the
  wizard menu; the pre-existing "AVX2 kernel is planned" note
  is replaced by current state (AVX2+FMA3 hand-written kernel
  on Haswell+; Rosetta and pre-Haswell fall back to the pure-Go
  path; `TestMatMulKernelParity` guards correctness).
- **`config.trimConfigStrings` no longer touches `LLM.APIKey` or
  `Embedding.APIKey` literals** (M3 from the post-wizard security
  review). Previously the load-time whitespace trimmer ran
  `strings.TrimSpace` on every string field including inline
  API-key values. Current providers (Anthropic / OpenAI / Bedrock)
  ship whitespace-free keys so no bug exists today, but a future
  proxy emitting padded tokens would have been silently
  corrupted. `APIKeyFile` and `APIKeyEnv` paths / env names are
  still trimmed -- those are user-typed identifiers, not opaque
  secrets. New regression test `TestLoadDoesNotTrimAPIKeyLiterals`
  guards the carve-out.

### Security

- **Hook script JSON emission and path-component validation.** The
  Claude Code and kiro-cli hook scripts under `hooks/` (mirrored
  into `internal/setup/embed_hooks/`) previously interpolated
  `$SESSION_ID`, `$GRAMATON_SESSION_ID`, and `$CWD` verbatim into
  `cat > ... <<ENDJSON` heredocs and used `$SESSION_ID` as a
  filesystem path component without validating its shape. Inputs
  come from trusted processes (Claude Code's JSON envelope +
  `gramaton session start` output) so there was no live
  exploitation, but a stray `"` or newline in upstream output
  would have corrupted the emitted JSON, and a crafted session id
  containing `..` or `/` would have escaped
  `~/.gramaton/hook-state/` confinement. Hardening:
    - Every script now runs
      `case "$SESSION_ID" in *[!A-Za-z0-9_-]*) exit 0 ;; esac`
      (or the `CLIENT_SESSION_ID` equivalent) after extraction, so
      non-UUID-ish shapes fail closed.
    - `session-start.sh` and `pre-compact.sh` now emit JSON via
      `python3 -c 'import json,sys; sys.stdout.write(json.dumps({...}))'`
      with values passed as argv, so `json.dumps` handles quote
      and newline escaping.
  The two trees (`hooks/` and `internal/setup/embed_hooks/`) stay
  in sync via `go:generate cp -rp`; unifying the duplication is
  a post-OSS TODO.

### Added

- **Interactive setup wizard scaffolding (`internal/setup/`)** -- first
  pass of the `gramaton init` interactive wizard. Goal: replace the
  one-shot `init` with a guided walkthrough covering embedding provider
  selection, LLM provider + API key entry, MCP client registration,
  and auto-capture hook installation. Driven by the pre-OSS decision
  that even tech-capable users benefit from a wizard (first-impression
  UX is the highest-leverage pre-push investment, see Memory record
  2026-04-22 on target audience).

  This pass lands:
    - New `internal/setup` package with `Prompter` and `Writer`
      interfaces so every step is unit-testable without driving a real
      terminal. `TerminalPrompter`/`TerminalWriter` for production,
      `ScriptedPrompter`/buffered writer for tests.
    - Wizard orchestration: `Wizard.Run` drives 4 user-visible steps
      plus Step 0 (fresh-vs-import branch) and a final verification.
    - Step 1 (Knowledge store) fully implemented: 5-option menu for
      embedding provider (BERT default / Ollama / OpenAI / Bedrock /
      Skip), with model download for BERT and config-only setup for
      the others.
    - Step 2 (Autonomous curation) fully implemented: feature-map
      comparison showing what Gramaton does with vs without an LLM,
      provider selection (Anthropic / OpenAI / Bedrock / help / skip),
      API-key entry with hidden input, test-call validation for
      Anthropic, cost-cap sub-prompt with `$5/day + 500 calls/day`
      defaults and a customize branch. Enables `search.rerank_enabled`
      automatically when an LLM is configured.
    - Step 3 (MCP client auto-detect + registration) now implemented:
      detects `claude` (Claude Code) and `kiro` (kiro-cli) on PATH,
      shells out to `claude mcp add --scope user gramaton gramaton --
      mcp` for registration. Pre-checks `claude mcp list` for existing
      gramaton entry so re-running the wizard is idempotent. kiro-cli
      path is best-effort (tries the claude-compatible `mcp add`
      syntax); documents its uncertainty and surfaces a clear
      fall-back-to-manual error if the syntax differs. Partial
      success supported: one client registered + another failed is a
      valid outcome, per-client warn/check lines.
    - Step 4 (automatic-capture hooks installer) now implemented:
      hook scripts for Claude Code and kiro-cli are shipped inside
      the binary via `//go:embed internal/setup/embed_hooks` so
      `go install` builds carry working hooks without needing a
      repo clone. On user confirm, scripts are materialized to
      `<configDir>/hooks/<client>/` with 0700 dirs + 0755 scripts.
      For Claude Code: auto-patches `~/.claude/settings.json` by
      parsing existing JSON, preserving every unrelated top-level
      key AND every user-owned hook entry, and replacing only
      gramaton-owned entries (identified by `/.gramaton/hooks/`
      command-path prefix). Idempotent on re-run (byte-identical
      JSON check), backs up settings.json before writing, atomic
      tmp+rename write. For kiro-cli: scripts materialized but
      auto-patch skipped because kiro-cli's hook config schema
      isn't verified in our corpus; the wizard prints the
      materialized paths and a manual-config hint. Tech-debt: the
      embedded hook tree duplicates the canonical hooks/ tree at
      repo root; a go:generate directive re-copies on demand but
      a follow-up should unify the two locations (post-OSS item).
    - Step 5 (verification) now implemented: persists config via
      config.Save, then runs a sequence of graceful health checks:
      config file permissions (warns if != 0600), data directory
      writability via a probe file, per-provider summaries for
      embedding + LLM (including api_key_file existence and
      perms), MCP registration survey via `claude mcp list`, and
      per-client hook installation + executable-bit check. Each
      check reports ✓ or ⚠ with a specific remediation hint.
      Network-facing test-calls (LLM ping, embedding round-trip)
      and log-file error scan are deferred to a dedicated
      `gramaton doctor` post-OSS command.
    - `cli/init.go` rewritten as a TTY-dispatcher: interactive mode
      invokes the wizard, `--non-interactive` (or piped stdin) keeps
      the legacy scripted bootstrap flow for backward-compat with CI.
    - Unit tests for prompt/output helpers and two orchestration
      smoke tests covering the import branch and the skip-everything
      fresh path.

  Design decisions are documented inline in each file, including:
  Haiku as default curation tier for cost, rerank-on-when-LLM-
  configured, non-validated-OpenAI-key (vs validated-Anthropic),
  deferred-import-restore, skip-first-class-but-warned, Bedrock
  Anthropic-model-only scope, $5/day cap derivation, and per-cycle
  cap hidden behind customize.

  Not in this pass (explicit follow-ups, documented in step_mcp.go
  and step_hooks.go stubs): MCP client auto-detect + config
  injection, hooks installer, embed.FS for shipping hook scripts
  with the binary, full `gramaton doctor`-style verification.

- **AVX2 + FMA3 matmul kernel for amd64 BERT inference** -- new
  `embed/bert/matmul_amd64.s` assembly kernel mirrors the arm64 NEON
  implementation, processing K in 8-float chunks via 256-bit YMM loads
  and `VFMADD231PS`. Dispatcher in `embed/bert/matmul_amd64.go` gates
  on `cpu.X86.HasAVX2 && cpu.X86.HasFMA` at runtime; pre-Haswell
  hardware and Rosetta 2 fall through to the existing pure-Go tiled
  implementation. Closes a first-impression performance gap on
  Intel/AMD hosts where BERT embedding was 5-8x slower than on
  Apple Silicon. Architectural note: amd64 has only 16 YMM registers
  (vs 32 V registers on arm64), so the 4x4 output tile is computed
  in two K-passes -- rows i/i+1 then i+2/i+3 -- with bT loads
  repeated across passes. Register pressure stays within 16 YMM at
  the cost of 2x bT memory reads; FMA throughput is the bottleneck
  at BERT matmul sizes, not bT bandwidth, so the split-pass approach
  is expected to land close to arm64's measured speedup. Build tag
  on `matmul_generic.go` changed from `!arm64` to `!(arm64 || amd64)`
  so pure-Go MatMul stays available for other architectures.
  Correctness: existing `TestMatMul*` tests cover the new path with
  identical assertions; benchmarks in `math_test.go` measure the
  four BERT matmul shapes. Cross-compilation and vet are clean;
  on-hardware validation pending.

### Security

- **OSS-readiness scrub (pre-public-release)** -- audited the repo
  for PII, secrets, and public-unfriendly content ahead of pushing
  to GitHub. HEAD scrubs:
  - Two sensitive few-shot examples in `curation/prompts.go`'s
    classifyPrompt were replaced with generic equivalents. Because
    one predated the start of the `[Unreleased]` window, a history
    rewrite was required: 151 descendant commits were re-hashed
    and the orphaned pre-rewrite chain (via a stale feature
    branch) was deleted and gc'd to release it. Every commit on
    that branch was verified to be a content-duplicate of a main
    twin before deletion.
  - Absolute `/Users/b/...` paths scrubbed from HEAD in
    `cli/session_test.go` (test fixture) and `docs/benchmarks.md`
    (templated `data_dir`; `claude mcp add` switched to the bare
    `gramaton` command on PATH).
  - A user-specific path convention in another few-shot record
    was changed to a generic one so prompts shipped to the LLM
    don't leak personal filesystem layout.
  - Added a "CLI shims (unsupported -- use at your own risk)"
    subsection to `docs/providers.md` documenting that the
    `claude-cli` and `kiro-cli` providers automate interactive
    vendor CLIs outside their intended use and may result in
    vendor account suspension or ban.
  - Normalized CLI-shim provider names in
    `docs/project-design/design-decisions.md` to match the
    config-accepted hyphenated spelling.
  Accepted as-is after review: residual `/Users/b/` paths in four
  historical commits (low-severity username leak), the intentional
  `StripAPIKeys` regression fixtures in `backup/backup_test.go`,
  and test-only placeholder API keys throughout the test suite.

### Changed

- **Code-comment audit and cleanup (pre-public-release)** -- audited
  all ~8000 lines of Go code comments across 196 production files
  for (a) references to files/symbols/tickets that no longer exist,
  (b) comments that contradict current code, and (c) task-specific
  chatter that a public reader can't resolve. Results:
  - Stripped ~100 internal ticket/Wave-phase tags from code comments
    (shapes like `(Wave 5 P1-58.)`, `(P1-07: <explanation>)`, and
    inline `before P1-24 landed`). These referenced a private
    internal tracking system with no public analogue. Substantive
    WHY rationale around the tags was preserved -- only the dead
    pointer itself was removed. Tickets that DO resolve in
    CHANGELOG.md or `docs/project-design/design-decisions.md`
    (P1-23, P1-40, P1-45, P1-59, P1-69, P1-74, P1-76, P1-78, P2-06,
    T-06, D12, D40, etc.) were kept.
  - Dropped a stale "ported from server/service_collections_test.go"
    lineage comment in `api/collections_test.go` (the cited file was
    removed long ago).
  - Fixed a comment in `config/config.go` that referenced
    `ObserveConfig.Enabled` after the field was removed in the
    config-drift sweep; now points at the surviving ObserveConfig
    struct.
  - Corrected two design-decision misattributions in `core/engine.go`
    and `core/indexes.go` that cited D1 (Metadata Is the Product)
    and D3 (Filter -> Rank -> Traverse) for decisions those entries
    don't cover -- rewrote to reference the current behavior
    (bge-small-en-v1.5 default dimension) directly rather than citing
    the wrong D-numbers.
  - Deleted three pure-WHAT comments in `curation/` that restated
    the for-loop immediately below them, per the project's
    "explain WHY not WHAT" convention.
  No behavior change. Build, vet, and touched tests all clean.

- **Inclusive-language rename**: `whitelist` / `blacklist` ->
  `allowlist` / `blocklist` throughout the codebase. Touches
  `backup/backup.go` (including the unexported `stripToAllowlist`
  function), `backup/backup_test.go` (test function
  `TestStripAPIKeysAllowlistGaps`), `api/collections.go`
  (projection-field comments), `.gitignore` comments, and the
  user-facing `jsonschema` descriptions on the `fields` parameter
  of `gramaton_collection_items` in both
  `cli/mcp_proxy_collections.go` and `server/bindings_collections.go`.
  No behavior change.

### Fixed

- **P1-59: manifest-summary cache key now reflects epistemic /
  temporality / confidence shifts** -- `generateManifestSummary`'s
  state fingerprint was `records=N|types=...|keywords=top15|span=...`,
  which silently treated two very different stores as "same" if they
  shared top-15 keywords + knowledge_type histogram + record count.
  Bulk reclassification (50 records sliding speculative ->
  well_established with confidence going from low to high) didn't
  change any of the included dimensions, so the cached manifest
  summary stuck across the change. Extended the fingerprint to
  include sorted histograms of `epistemic_status`, `temporality`,
  and a quartile-bucket histogram of `confidence` (low <0.4 / mid
  0.4-0.7 / high >=0.7 / unset). Quartile-style bucketing keeps the
  fingerprint stable to small drift while still busting the cache
  on the kind of bulk shift that matters. Two new regression tests
  in `curation/autonomous_test.go`.

- **P3-07 nits batch (cluster A wrap-up)** -- two surviving items
  from the original P3-07 bundle, the rest were resolved or absorbed
  by other work in this Unreleased window:
  - `logging.RotatingWriter.enforcebudget` -> `enforceBudget` to
    match Go method-naming convention.
  - `config.Save` now documents the file/dir permission convention
    (0700 dirs, 0600 files for everything under `~/.gramaton/`,
    since these may carry credentials).
  Bullets already addressed elsewhere: yaml.Decoder strict-mode
  (P1-38, commit 1c8b665), Observe.Enabled default (config-drift
  sweep, commit 34b3fb8), safeProperties indentation (Wave 7
  P1-74), truncate maxLen byte/rune (T-08, separate item),
  three integration-doc drifts (T-10, separate item).

- **`limits.max_json_size` is now honored by HTTP bindings** -- the
  cap was hardcoded to 1 MB in `server/validation.go` behind a TODO,
  while `config.LimitsConfig.MaxJSONSize` defaulted to 2 MB. Every
  `parseJSON` call site in bindings_{records,collections,sessions,
  search,admin,maintenance}.go, handler_{ops,intake}.go now routes
  through a new `getMaxJSONSize()` helper that reads from the process-
  level serverLimits (set in `Server.New`). Zero/negative config
  falls back to the previous 1 MB hardcoded value so tests that
  bypass `Server.New()` still get a safe cap. Net effect: a user's
  configured `limits.max_json_size` in yaml now actually takes
  effect. Default max body moves from 1 MB to 2 MB as documented.
  New test coverage in `server/validation_test.go` for both the
  config-driven and zero-value-fallback paths.

### Removed

- **Dead config fields cleaned up (config-drift sweep, cluster A)** --
  nine yaml fields had no enforcement code anywhere in the repo; strict
  YAML decoding (1c8b665) turned their presence in `config.yaml` into
  a false promise. Removed from the schema:
  - `observe.enabled`, `observe.default_confidence`,
    `observe.default_temporality`, `observe.substance_min_length`,
    `observe.feedback_loop_hours`, `observe.feedback_loop_similarity`,
    `observe.retrieval_tracking`, `observe.retrieval_similarity` --
    knobs for the original LLM-driven `/v1/observe` endpoint, which
    was removed when the sessions flow replaced it. Config cleanup
    didn't land with the handler deletion. `curation/observe.go` is
    a separate, surviving deterministic TF-IDF extractor that only
    reads `observe.max_facts_per_call`; the struct is now a single-
    field holder, with doc comment clarifying the split.
  - `limits.max_nesting_depth`, `limits.max_writes_per_second` --
    aspirational safety caps with no enforcement (no recursive depth
    validator, no per-client rate limiter). Their `Defaults()` values
    were cargo; nothing consulted them.
  Any `config.yaml` with these keys now fails loud at load time with
  the offending key + line, the same way typos did after 1c8b665.

### Fixed

- **`server.auto_start` is now honored** -- the field has been in
  `config.yaml` since the named-store design but `cli/client.go` never
  read it; the CLI auto-spawned regardless. Users running gramaton
  under systemd/launchd who set `auto_start: false` got a second
  server forked anyway. `serverURL()` now loads the effective config
  (defaults -> global -> per-store) and returns a clear error
  (`no running server (server.auto_start=false); run gramaton serve first`)
  when auto_start is disabled and no server is running. Config load
  errors fall open to the historical default (true).
- **`search.session_dedup_enabled` actually defaults to `true`** --
  documented as the default, but `Defaults().Search` omitted the
  field so Go's zero-value (`false`) took over. A Memory record and
  its extracted Session segment could both surface in a result set,
  despite docs promising the segment would be suppressed. Fixed in
  `Defaults()`. The server test helper `setupTestServer` now flips
  it back to `false` with a comment -- most tests assert cross-store
  visibility of overlapping content, which dedup was hiding.
- **`docs/configuration.md` defaults match code** --
  `search.retrieval_candidates: 100` -> `200`,
  `search.rerank_candidates: 20` -> `50`. The `observe:` section is
  rewritten around the surviving TF-IDF extractor. The `limits:`
  block drops the two dead fields. Stale `server/handler_observe.go`
  reference in the `llm.model` doc comment removed.

### Added

- **P1-38: config `Validate()` + strict YAML decoding** -- config/config.go
  gains an exported `Validate(*Config) error` that runs after `normalize`
  in `Load` and `LoadWithFallback`. Enforces: LLM provider allowlist
  (`anthropic`, `openai`, `bedrock`, `claude-cli`, `kiro-cli`, or empty),
  embedding provider allowlist (`bert`, `ollama`, `openai`, `bedrock`,
  or empty), `Server.Port` in [0, 65535], `Decay.Rates.Immutable == 0`,
  and non-negativity on decay rates + scoring + BM25 weights. Sum-to-1
  is NOT enforced because search/score.go re-normalizes meta weights
  at runtime and BM25 RRF weights have a documented non-unit default
  (1/2/3). The `overlay` helper now decodes via `yaml.NewDecoder` with
  `KnownFields(true)`: typos (e.g. `scoring:\n  weight_similarty: 0.5`)
  fail loud at startup with the offending key + line instead of silently
  reverting to defaults. Empty files remain a valid no-op overlay.
  Also corrected a doc comment that listed LLM providers as `claudecli`
  / `kirocli` when the actual switch accepts only the hyphenated
  `claude-cli` / `kiro-cli`. Validate() was added without splitting
  config.go into multiple files; the 2026-04-17 in-file sectioning
  (user-facing vs internal-tuning) remains sufficient for navigation.

### Changed

- **Named-store config now deep-merges (cluster A #1)** --
  `config.LoadWithFallback` was an "if-exists-use-store, else-use-global"
  fallback, which silently zero-valued any section absent from a
  partial per-store `config.yaml`. A minimal override (e.g. only
  `server.port`) would drop the global's `llm:` block, making
  `server.New` refuse to start; the background launcher then
  returned a bare `"timeout after 10s"` with no underlying error
  because `waitForServer` never surfaced the child's stderr.
  Rewrote the loader to layer defaults -> global overlay -> store
  overlay -> normalize once. Keys absent from a layer inherit from
  the layer beneath; explicit empty YAML (`key: []`, `key: {}`)
  replaces. `Load` shares the same overlay/normalize helpers.
  `cli/serve.go::startBackground` now tails `~/.gramaton/gramaton.stderr`
  on readiness timeout and folds the last ~2KB into the error.
  New regression tests: `TestLoadWithFallbackMergeInheritsFromGlobal`
  (store inherits global's `llm:` + `logging:` when only overriding
  `server.port`) and `TestLoadWithFallbackSamePathNoDoubleLoad`.
  Docs for `docs/configuration.md`, `docs/benchmarks.md`,
  `docs/project-design/glossary.md`, and D35 in design-decisions.md
  all updated; the "copy the full global as a workaround" guidance
  is gone.
- **P2-06 Stages 2+3: WriteSession pattern for batched index writes** --
  closes the stashed `*bolt.Tx` race hazard across five bbolt-backed
  types (`BboltPropertyIndex`, `BboltBM25Index`, `BboltSecondaryIndex`,
  `BboltCollectionCache`, `BboltEdgeStore`) by threading the
  transaction explicitly. `SetBatch`/`ClearBatch` are gone from all
  five. Mutating methods grew `*Tx`-suffixed variants
  (`AddTx`/`RemoveTx`/`PutTx`/etc.) that accept `*bolt.Tx` plus an
  optional companion cache (`*BM25Batch`, `*EdgeBatch`). Non-Tx
  methods remain as convenience wrappers that open their own
  `db.Update`. New `core.WriteSession` type owns one shared tx +
  caches for a batched write phase; `Engine.WithWriteBatch`'s fn
  signature is now `func(*WriteSession) (mutated bool, err error)`.
  `graph.Graph.AddEdgeTx` threads tx through the graph boundary.
  `indexSet.batch()` rewrites to construct a `WriteSession` and
  flush the BM25/Edge caches before commit. Rebuild path
  (`rebuildIndexes`) uses `AddTx`/`AddPreTokenizedTx` directly
  instead of the old SetBatch type-assertion dance. Callers
  migrated: `curation/observe.go`, `curation/deterministic.go`,
  `backup/import.go` (both ImportJSON and ImportObsidian),
  `api/collections.go` batch path. 80+ non-batched call sites to
  `Engine.SetProp`/`Graph.AddEdge`/etc. unchanged -- only code
  inside `WithWriteBatch` closures changes (~10 call sites total).
  Closes all three hazard classes (torn pointer read, stale-pointer
  cross-goroutine use, companion-map race). See D40 for architectural
  rationale and `docs/project-design/p2-06-writesession-plan.md`
  for the stage-by-stage plan.
- **kirocli output filtering anchored to line-start (P1-69)** -- the
  Credits / Time footer and trust-warning lines were filtered via
  `strings.Contains` on any substring match, which would silently
  drop legitimate model responses containing those phrases mid-
  sentence (e.g. "The bank statement shows Credits: 4"). Replaced
  with regex + prefix matching that requires the chrome pattern at
  line start. Three new tests in `llm/kirocli/kirocli_test.go`
  assert mid-line occurrences of "Credits:", "Time:", and
  "kiro.dev/docs" survive; the existing tests for footer/warning
  stripping continue to pass.
- **Bedrock Cohere embedder distinguishes query vs document (P1-40)**
  -- new optional `embed.QueryEmbedder` interface. Bedrock Cohere
  implements `EmbedQuery` with `input_type="search_query"`; the
  existing `Embed` continues to use `search_document` for indexed
  content. Cohere's retrieval benchmarks show measurable cosine-
  similarity degradation when queries use the document input type,
  so search-time query embedding now routes through
  `embed.EmbedForQuery` (in `api/search.go`) and the `queryEmbedder`
  type-assertion in `search.Tool.Execute`. Providers that don't
  distinguish (OpenAI, Ollama, Titan, BERT) fall back to `Embed`
  unchanged.
- **LLM.Model vs LLM.Models.* time-bomb reduced (P1-76)** -- curation
  path stops reaching into `cfg.LLM.Models.Medium` directly;
  `modelForTaskLabel` now goes through `cfg.ModelAtEffort` so the
  tier-map access is confined to `config.go`. Server startup logs a
  Warn when any effort tier (`llm.models.low/medium/high`) is empty,
  naming the tiers and the fallback model; curation silently falling
  back to the provider default was the original surprise the ticket
  called out. `ModelForTask` remains the single entry point for
  mapping a curation task to a concrete model.

### Added

- **HTTP LLM provider retry on 429 / 5xx (P1-23)** -- new
  `llm/httpretry` package implements exponential backoff + decorrelated
  jitter with Retry-After parsing (seconds or HTTP-date). Wired into
  `llm/anthropic` and `llm/openai` via `DoWithRetry`, which handles
  the buildReq/client.Do loop and closes intermediate response bodies
  before retrying. Default policy: 4 attempts, 500ms -> 1s -> 2s -> 4s
  with jitter, 30s cap. Bedrock uses the AWS SDK's retryer bumped
  from MaxAttempts=3 to 5 with a 30s max backoff in
  `internal/awscfg/awscfg.go`. A single 429 wave from a provider no
  longer fails arbitrary classification records in a curation batch.
  Context cancellation breaks the retry loop promptly. Non-retryable
  status codes (4xx other than 429) return to the caller after a
  single attempt.
- **`Engine.TryRLock` + writeJSON/writeJSONLocked collapse (P1-45)**
  -- `writeJSONLocked` is gone. `writeJSON` is now safe to call from
  any handler regardless of engine lock state: `curationStatus` uses
  an opportunistic `engine.TryRLock` to refresh the cache when
  possible, falling back to the stale (possibly zero) cached value
  rather than deadlocking when a caller holds the write lock. Closes
  the RWMutex-not-reentrant footgun and finishes T-06 step 4.
- **`Engine.WithWriteBatch` helper (T-06)** -- consolidates the
  write-phase recipe (Lock -> bbolt index batch -> Save -> Unlock)
  into a single call so curation and bulk-insert paths stop drifting
  on error handling, logging, and the "skip save when nothing
  changed" gate. `fn` returns `(mutated bool, err error)`: a false
  `mutated` skips Save (no-op commits waste fsync + HEAD writes),
  an error skips Save and is wrapped with the phase label. Logs
  `batch_ms` / `save_ms` at Info so lock-hold duration is observable
  per phase. Migrated `curation/deterministic.go` (previously
  unbatched -- every SetProp / AddEdge / IndexNode did its own
  bbolt fsync under the monolithic write lock; a busy cycle with
  hundreds of mutations now lands in a single bbolt transaction)
  and `curation/observe.go` observation extraction. Closes portions
  of P1-30, P1-31, P1-45; step 4 (curation envelope cache) was
  already done when writeJSON grew the 5s cache.
- **USD-denominated LLM cost caps (T-07 step 2)** -- two new config
  fields: `llm_curation.max_cost_usd_per_run` bounds a single curation
  cycle, `llm.max_cost_usd_per_day` bounds the daily aggregate. Both
  default to 0 (disabled) and complement the existing count caps
  rather than replacing them. Cost is estimated via
  `llm.EstimateCost` using per-task token counts from the cycle
  recorder plus the per-model pricing table. Motivated by the
  contradiction-drain bleed (~950 Sonnet calls at ~$0.018/call stayed
  comfortably within count budget; a USD cap catches that class of
  incident directly). Unknown models contribute 0 to the cost total,
  so keeping count caps set is still recommended -- documented in
  `docs/configuration.md` under "Cost and call caps" and design
  decision D39.

### Changed

- **CLI provider telemetry (T-07 step 4)** -- `claudecli` now parses
  the per-model token block from `claude -p --output-format json`
  (`modelUsage.<model>.{inputTokens, outputTokens,
  cacheReadInputTokens, cacheCreationInputTokens}`) and records it
  via `telemetry.Record`, so cost flows through the standard pricing
  pipeline the same way anthropic/openai/bedrock do. `kirocli` still
  reports zero: its output surfaces only a "Credits:" footer (not
  USD or tokens), and cross-provider aggregation expects tokens --
  operators relying on `kirocli` should use `max_calls_per_day` as
  the cost safety net.
- **Cycle summary log polish (T-07 step 5)** -- the "autonomous
  curation complete" log now includes per-model cost (`cost:<model>
  $0.1234`) alongside the existing per-model classification counts,
  and the per-task breakdown gains a cost field (`tokens:<task>
  in=.../out=.../cache=.../cost=$X`). Map iteration is sorted so log
  lines are deterministic across cycles.
- **Per-provider LLM telemetry (T-07 steps 1+3)** -- token usage is now
  reported by every HTTP-based LLM provider, not just Anthropic.
  `telemetry.Record(ctx, CallUsage)` is the canonical recorder entry
  point; `openai` populates input/output/cache-read tokens from the
  `usage` response block (including `prompt_tokens_details.cached_tokens`
  when OpenAI returns it), and `bedrock` populates input/output from the
  Converse API's Usage field. Cache fields stay zero for providers that
  don't surface cache accounting. The `llm.Provider` interface gained
  `ProviderName() string`; `Metered.record` propagates the inner
  provider's name into `CallMetrics.Provider` instead of hardcoding
  `"metered"`, so per-provider accounting is meaningful when multiple
  backends run in one process. CLI providers (claude-cli, kiro-cli)
  still report zero -- their cost/credit parsing lands in T-07 step 4.

### Added

- **Bulk collection_add (P1-78)** -- new MCP tool
  `gramaton_collection_add_batch` and HTTP route
  `POST /v1/collections/{id}/items/batch` add up to 500 items in a
  single call. Batches schema validation + embedding + save into one
  engine lock cycle; ~20-50x faster than repeated single adds for
  N=100. Best-effort semantics: per-item validation and dedup
  failures reported in the Failed array, items passing pre-checks
  commit atomically. Intra-batch dedup uses first-write-wins.
  `CollectionAddBatchDescription` constant shared across HTTP,
  MCP, and CLI proxy transports.
- **LLM contributor skills** -- `.claude/skills/` ships agent skills
  encoding project conventions for LLM coding assistants:
  `new-operation`, `migrate-to-api`, `pre-merge-check`,
  `gramaton-review`, `gramaton-security-review`, `store-health`,
  `curation-sweep`, `benchmark-extract`. Claude Code auto-discovers
  them; other tools reading agent-skill markdown can adapt. Skills cite CONTRIBUTING.md
  as the source of truth rather than duplicating conventions. Added
  `CLAUDE.md` at the repo root as a short skill index and governance
  reference. `.gitignore` narrowed from a blanket `.claude/` rule to
  a default-deny allowlist: only `.claude/skills/` is shared; user-
  local settings and session state stay out of the repo.
- **HNSW vector index** -- pure Go Hierarchical Navigable Small World
  implementation behind the existing VectorIndex interface. O(log N)
  approximate nearest neighbor search replaces O(N) brute-force at
  scale. Dynamic switching: FlatIndex (exact) below `hnsw_threshold`
  (default 5000), HNSW above. Candidate-filtered queries fall back to
  flat scan on small candidate sets. Configurable M, efConstruction,
  efSearch. 1.00 recall@10 at 1000 vectors with default parameters.
- **Persisted BM25 index** -- BM25 term frequencies serialized as a
  content-addressed chunk alongside each commit (`bm25_root` field).
  On startup, the index loads from disk instead of re-tokenizing all
  content. Saves 10-30 seconds at 100K+ nodes. Backward compatible:
  old commits without `bm25_root` fall back to full rebuild.
- **Dirty tracking** -- graph tracks which nodes/edges are modified
  since the last save. `Save()` only marshals dirty items (O(K)
  instead of O(N) where K is typically 1-5 per mutation).
- **Incremental prolly tree** -- `ProllyTree.Update()` applies
  mutations by walking from root to affected leaves, modifying only
  touched chunks. O(K * depth) where depth is 3-4 for trees up to
  millions of entries. Falls back to full `Build()` for first save
  or branch switch.
- **Debounced access saves** -- `serviceInspect` and `serviceSearch`
  no longer trigger a full save to persist access_count. Access
  metadata is flushed by a background goroutine every 30 seconds
  and on shutdown. Eliminates 80-90% of unnecessary full saves.
- **--version flag** -- `gramaton --version` and `gramaton version`
  subcommand with commit hash, build date, and store format version.
  Version set via ldflags at build time. Replaces all hardcoded
  version strings.
- **Store format versioning** -- `FORMAT` file in the data directory
  tracks the on-disk store format version. Checked on engine load:
  stores from newer binaries are rejected with a clear upgrade
  message. Backward compatible: missing FORMAT file gets current
  version written automatically.
- **Collections** -- structured containers with schema enforcement and
  guaranteed exhaustive retrieval. Collections complement the knowledge
  graph for data that needs complete visibility (tasks, backlogs,
  checklists). Features: named collections with optional schema (6 field
  types: string, number, boolean, date, enum, enum[]), schema evolution
  with explicit migration, multi-collection membership, retirement
  (reversible), title-based dedup detection, field-based sorting.
  11 MCP tools (`gramaton_collection_*`) and 12 HTTP endpoints.
- **Integrator guide** -- documentation for agent builders covering
  graph vs. collection usage, capture/search best practices, collection
  patterns (PARA, kanban), schema definitions, and prompt guidance.
- **Structured metadata** -- records accept a `meta` map at capture
  and update time. Values (string, number, bool, string array) are
  stored as typed `meta.*` properties, validated at write time, and
  indexed in BM25 for keyword search. Search accepts a `meta` filter
  for exact-match pre-filtering on any meta field.
- **Faceted suggestions** -- when search results score below a
  configurable threshold (default 0.75), the response includes
  `suggestions.available_filters` showing meta field values found
  across results. Agents can use these to refine with explicit
  meta filters. Config: `search.suggestion_threshold`.
- **ACT-R scoring model** -- replaced 6-signal scoring (similarity,
  recency, freshness, frequency, activation, confidence) with 4-signal
  model (similarity, freshness, ACT-R activation, confidence). The
  ACT-R base-level activation `B = ln(n/0.5) - 0.5*ln(L)` unifies
  frequency and recency into a single theoretically-grounded signal
  based on Anderson & Schooler 1991. Spreading activation from
  neighbors is additive (A = B + S), matching ACT-R's full equation.
  Frequency signal removed (eval showed it was actively harmful).
  Default weights: similarity=0.55, activation=0.20, confidence=0.15,
  freshness=0.10.
- **Default embedding model changed** -- switched from `nomic-embed-text`
  (137M params, 768d) to `mxbai-embed-large` (335M params, 1024d).
  Eval showed +14% NDCG@5 and +12% MAP. Run `gramaton reembed` after
  upgrading to re-embed existing records.
- **Bedrock provider support** -- added Amazon Bedrock as a provider for
  both embeddings and LLM. Embedding supports Titan Embed V2 and Cohere
  Embed model families. LLM uses the Converse API (works with Claude,
  Titan, Llama, Mistral, and any Bedrock model). Auth via AWS named
  profiles, environment variables, or the default credential chain.
  Config: `embedding.provider: bedrock` / `llm.provider: bedrock`.
- **OpenAI-compatible provider** -- added OpenAI-compatible provider for
  both embeddings (`/v1/embeddings`) and LLM (`/v1/chat/completions`).
  Works with OpenAI, Azure OpenAI, vLLM, LiteLLM, Together, Fireworks,
  and any compatible API. API key is optional (local servers often don't
  need one). Config: `embedding.provider: openai` / `llm.provider: openai`.

### Removed

- **docs/project-design sunset pass.** Removed nine design docs
  under `docs/project-design/` that described superseded pipelines
  or were duplicates of top-level `docs/` content:
  `server-design.md` (pre-T-02 "v0.2" daemon spec, superseded by
  the `api/` unified surface); `observe-pipeline.md` (the
  `gramaton_observe` flow, soft-deprecated and replaced by the
  session prepare/commit two-tier model); `capture-and-processing.md`
  and `curation.md` (early subagent-classification-at-capture model
  with `/gramaton-process` and `/gramaton-curate` skills that were
  never built -- current reality is server-side autonomous curation
  plus agent piggyback fallback); `validation.md` (theoretical
  test methodology superseded by the LongMemEval benchmark work in
  `gramaton-inspection/benchmark-methodology.md`);
  `open-questions.md` (all items marked RESOLVED);
  `agent-integration.md` (taught the observe/subagent integration
  pattern, now outdated -- live integration guidance lives in the
  agent's `CLAUDE.md` and the top-level `docs/integrator-guide.md`);
  `tenets.md` and `architecture.md` (duplicates of the top-level
  `docs/tenets.md` and `docs/architecture.md`). `project-design/README.md`
  rewritten as a leaner index over the remaining 9 docs (data-model,
  retrieval, embedding, collections, foundations, case-studies,
  data-integrity, design-decisions, glossary). Git history preserves
  the removed files for recall. Phase-2 rewrites of the top-level
  `docs/architecture.md`, `integrator-guide.md`, and root `README.md`
  (which still reference the removed `gramaton_observe` tool) are
  tracked in `gramaton-inspection/documentation-plan.md`.
- **T-02 cascade cleanup stage C3 (collections).** Deleted
  `server/service_collections.go` (928 lines) and
  `server/service_collections_test.go` (967 lines), plus the
  duplicate `server/collections.go` (289 lines) whose schema
  types and field-type constants already live in
  `api/collection_schema.go`. All 12 server-level collection
  methods were unreachable via bindings after T-02 routed
  `bindings_collections.go` through `s.api.CollectionXxx`.
  Coverage ported to `api/collections_test.go`: 27 tests over
  Create/List/Items/Add/Remove/Update/Move/Rename/Delete/
  SchemaUpdate/Migrate, including schema validation, projection
  + filter edge cases, multi-membership, dedup, and schema
  evolution. Two tests pinned T-02 behavior changes: duplicate
  name and duplicate title now surface as `conflict` rather
  than `duplicate`-code or success-with-`duplicate=true` maps.
  The HTTP-boundary test (`TestCollectionItemsHTTPProjectionAndFilter`)
  stayed in `bindings_collections_test.go`; the
  `TestCollectionPerformance` test was preserved too.
- **T-02 cascade cleanup stage C2 (records).** Removed seven dead
  server-level record services from `server/service_records.go`
  (`serviceInspect`, `serviceUpdate`, `serviceClassify`,
  `serviceResolve`, `serviceLink`, `serviceDeleteEdge`,
  `serviceDeleteRecord`), their supporting types
  (`updateRequest`, `classifyRequest`, `resolveRequest`,
  `edgeRequest`) and validators (`validateUpdateRequest`,
  `validateClassifyRequest`) in `handler_records.go`, and the
  duplicate `inspectMetadataSummary` helper (live copy lives in
  `api/internal.go`). Coverage ported to new
  `api/records_test.go`: 10 tests over api.Inspect, api.Update,
  api.Classify, api.Resolve, api.Link, api.Unlink, and
  api.DeleteRecord. Kept `serviceCapture` + its helpers
  (`preEmbedContent`, `applyPreEmbedded`, `validateCaptureRequest`,
  `setOptionalProps`) since `handler_intake.go`'s `serviceIntake`
  still calls through to them; five capture tests retained in
  `service_records_test.go`. Also dropped four consts in
  `server/validation.go` that became unused after `serviceSearch`
  went in stage C1: `maxSearchTop`, `maxMissingFields`,
  `maxMatchLength`, `maxSearchHops`.
- **T-02 cascade cleanup stage C1 (search + test rewires).** Deleted
  `server/service_search.go` (the last method `serviceSearch` was
  unreachable via bindings since T-02 routed `bindings_search.go`
  through `s.api.Search`). Rewired the one test reference in
  `server/handler_sessions_test.go` to `srv.api.Search`. Also
  rewired `bindings_collections_test.go` helpers (`makeCollection`,
  dedup test seed) from `srv.serviceCollection{Create,Add}` to
  `srv.api.Collection{Create,Add}`. Deleted the unused `searchRequest`
  type in `server/handler_search.go`; kept `parseDateArg` (used by
  remaining server-level services).
- **T-02 cascade cleanup stage B (sessions).** Deleted
  `server/service_sessions.go` (1255 lines) and
  `server/service_sessions_test.go` (1659 lines): the server-level
  session subsystem was fully dead after T-02 routed
  `bindings_sessions.go` through `s.api.SessionXxx`. Tests ported to
  `api/sessions_test.go` (47 tests, package `api`) using a new
  `setupTestAPI` helper. Also removed the `preparedSessions` and
  `preparedSweepCancel` fields from `server.Server` (now owned end-
  to-end by `api.API`), and the three internal api helpers
  (`sessionAddTopic`, `sessionAddSegment`, `sessionUpdateSegmentCapture`)
  which had no callers after the helper-path tests were retired.
  `server/handler_sessions_test.go` helper `createSessionWithSegments`
  rewired to `srv.api.SessionStart/Prepare/Commit`. Test coverage for
  the session subsystem is now preserved at the api layer, where the
  logic actually lives. Full server + api suites pass with `-race`.
  Dropped: seven tests that exercised internal AddTopic/AddSegment/
  UpdateSegmentCapture helpers (not user-facing); ReadArchive tests
  (the function was never wired to HTTP/MCP and was flagged as a
  latent footgun in the T-02 security review).
- **T-02 cascade cleanup stage A (isolated orphans).** Deleted the
  three T-02 service orphans that were safely isolated (no test
  references): `serviceExplore` (`server/service_search.go`) and
  its `exploreRequest` type (`server/handler_search.go`);
  `servicePending` (entire `server/service_ops.go` file, 46 lines);
  `serviceCollectionSchemaRead` (`server/service_collections.go`).
  Also removed the consts in `server/validation.go` that became
  unused after the function deletions: `maxExploreDepth`,
  `maxEdgeTypes`, `maxExploreNodes`, plus the already-orphaned
  `maxDuplicatePairs`, `maxLogTraversal`, `maxTopicLength`,
  `maxFactLen`, `maxReembedBatch`. Build and full server test
  suite pass unchanged. Stage B (port
  `server/service_sessions_test.go` to `api/sessions_test.go`
  before deleting `server/service_sessions.go`) and stage C
  (records/search/collections equivalents) tracked as separate
  tasks.
- **T-02 dead code cleanup (server handlers + mcp wrappers).** Deleted
  941 lines across 8 files that were orphaned by the T-02 api/
  migration: `server/mcp_records.go`, `server/mcp_collections.go`
  (both entirely unreachable; `registerMCPTools` in `server/mcp.go`
  routes exclusively through the `bindings_*.go` path), plus
  `server/handler_sessions.go` and `server/handler_collections.go`
  (all contained only pre-T02 HTTP handlers with no live callers).
  Surgical deletes from `server/handler_records.go` (8 dead
  `handle*Record` / `handle*Edge` methods; kept the captureRequest/
  updateRequest/classifyRequest/resolveRequest/edgeRequest types and
  the live helpers `preEmbedContent`, `applyPreEmbedded`,
  `validateCaptureRequest`, `validateUpdateRequest`,
  `validateClassifyRequest`, `setOptionalProps`,
  `inspectMetadataSummary` used by `service_records.go`),
  `server/handler_search.go` (`handleSearch`, `handleExplore`; kept
  `parseDateArg` used by `service_search.go`),
  `server/handler_ops.go` (`handlePending`; kept `handleRevert`,
  `handleIngest`, ingest helpers), and `server/handlers.go`
  (`handleStatus`; kept `handleHealth`, `handleShutdown`,
  `handleDebugGoroutines`, `handleLLMStats`, `isLoopback`,
  `loopbackOnly`). No behavior change -- routing was already going
  through `bindings_*.go` paths. Follow-up candidates: the cascade
  of newly-exposed orphans (`serviceExplore`, `servicePending`,
  `serviceCollectionSchemaRead`, `startPreparedSweeper`, unused
  constants in `server/validation.go`) will need their own pass.

### Added

- **`gramaton_curation(action="drain_contradictions")` MCP action.**
  Artificially marks every in-window contradiction-candidate pair as
  `no_contradiction` without calling the LLM. Each edge carries
  `artificial: true` so a future recheck-pass can distinguish
  artificially-drained marks from genuine LLM verdicts. Tradeoff:
  real contradictions in the drained set will not be flagged. Use on
  stores where the pre-fix pool accumulated and the operator doesn't
  want to pay the ambient Sonnet cost of organic drain. Implemented
  as `curation.DrainContradictionsNoLLM(engine, cfg, logger)` in a
  new `curation/drain.go`, exposed via `api.CurationDrainContradictions`
  and the new MCP action. Three tests in `curation/drain_test.go`
  cover the happy path, skip-when-edged, and out-of-window cases.
  See design-decisions.md D38 for the underlying bug the drain
  compensates for.
- **`docs/benchmarks.md` gained a "Disable contradiction detection
  for benchmark stores" section.** Recommends setting
  `llm_curation.max_contradiction_checks: 0` in every benchmark
  store's `config.yaml` since contradiction output is not meaningful
  on test fixtures and the ambient Sonnet cost compounds across
  benchmark stores. Applied to the existing `longmemeval-bench`
  store's config (local edit, not in this commit).

### Fixed

- **Contradiction-detection candidate pool now drains on negative
  results.** Previously the autonomous curation pass at
  `curation/autonomous.go:detectContradictions` selected candidate
  pairs in the 0.5..0.85 similarity window, sent them to the
  configured LLM (Sonnet by default at `contradiction_effort:
  medium`), and wrote `contradicts` / `supersedes` edges on positive
  results. Negative results produced no persistent state. Because the
  read-phase `hasEdge` guard was the only "already checked" signal,
  the same pairs were eligible every cycle and the pool never drained
  on stores where the LLM's correct verdict was "no contradiction."
  Observed impact on a real store 2026-04-19→20: ~16 hours unbroken,
  ~950 Sonnet calls, 0 contradictions found, ~$17 burned. Fix: the
  write phase now persists negative results as `no_contradiction`
  edges carrying a `checked_at` timestamp property, which the
  existing `hasEdge` guard picks up on subsequent cycles. Draining
  is linear at `max_contradiction_checks` pairs per cycle until the
  pool is empty; per-cycle cost then goes to zero on stable stores
  and rises only with genuinely new-pair creation (captures, session
  commits, ingest). `AutonomousResult` grows a `no_contradiction_edges`
  counter for observability. Two new tests in
  `curation/autonomous_test.go` regression the behavior
  (`TestDetectContradictionsWritesNoContradictionEdge` and
  `TestDetectContradictionsSkipsPairsWithNoContradictionEdge`).
  Full reasoning, including why three alternative fixes (widen
  interval, narrow band, disable task) were rejected, is documented
  as D38 in `docs/project-design/design-decisions.md`. Operators
  running with pre-fix binaries should either upgrade or set
  `llm_curation.max_contradiction_checks: 0` as a mitigation.

### Changed

- **Collapsed `dedup.action` enum to `supersede | reject`.** The
  previous three-value enum (`flag | supersede | reject`) had `flag`
  and `supersede` behaving identically across all three capture
  paths -- both wrote `valid_until` + `resolution=superseded` + a
  `supersedes` edge, with the distinction existing only in the
  `DedupConfig.Action` comment. Default changes from `flag` to the
  explicit `supersede`. Legacy configs containing `action: flag` are
  silently coerced to `supersede` at `config.Load()` time (the
  behavior they were already getting). Unrecognized values now error
  at load so typos surface rather than being silently accepted.
  Capture-path comments in `api/capture.go`, `api/sessions.go`, and
  `server/service_records.go` updated to describe the default
  behavior explicitly. Three new test cases in `config_test.go`
  cover the legacy coercion, the empty-string default, and the
  typo-erroring path. Curation's dedup pass
  (`curation/deterministic.go`) was already independent of
  `DedupConfig.Action` and is unchanged. Full reasoning, including
  why two alternative shapes (a real warn-only `flag` mode, a
  `NearDuplicates` response field) were rejected, is documented as
  D37 in `docs/project-design/design-decisions.md`. Resolves dev
  collection item 01KPPXGF8FETMYWQDQC5H7VKYN.
- **Phase 3 of the documentation consolidation: targeted updates to
  the surviving `docs/project-design/` files.** Not rewrites -- each
  doc stays as historical design rationale but is scrubbed of stale
  claims and extended where post-T-02 material has a load-bearing
  story worth preserving. Changes:
    - **`design-decisions.md`**: added D31-D36 covering the three
      storage paths / decision rule (D31), the two-tier session model
      with `promote_to_memory` (D32), the `api/` canonical operations
      surface from T-02 (D33), pure-Go BERT as default embedder
      superseding D21's Ollama-as-default (D34), named stores via
      `LoadWithFallback` (D35), and tiered LLM models with per-task
      effort dials (D36). Each follows the existing Decision/Why
      format.
    - **`glossary.md`**: removed the nonexistent `gramaton raw` tier,
      softened the Subagent entry to reflect that sessions replaced
      most capture-time subagent usage, refreshed the
      `EmbeddingProvider` entry (four shipped implementations, BERT
      as default, BERT is in-process inference not HTTP). Added new
      sections for Sessions & Collections (Session, Session segment,
      Memory record, `promote_to_memory`, `extracted_as` edge,
      Session archive, Collection, Collection schema,
      `client_session_id`), Named stores (Named store, `LoadWithFallback`),
      and api / transports (api canonical surface, Transport, MCP
      cluster registrar).
    - **`embedding.md`**: added a first-class "Pure-Go BERT (Default)"
      section documenting the default provider and the amd64 matmul
      perf caveat. Demoted the "Ollama (Default)" section to
      "Ollama (Alternative)" while keeping the historical rationale.
      Rewrote the first-run experience with actual `gramaton init`
      output (BERT download) and a realistic failure-path fallback.
      Updated the provider interface to include `ModelID()` and
      `ContextWindow()` which the doc was missing.
    - **`retrieval.md`**: added a historical-rationale preamble at
      the top pointing live API-reference readers to
      `docs/integrator-guide.md` and `gramaton_guide(topic="search")`.
      Replaced all `gramaton raw` references with `gramaton inspect`
      + a source_ref note (three occurrences).
    - **`data-model.md`**: updated the `embedding_model` example from
      `nomic-embed-text:v1.5` to `bge-small-en-v1.5` to match the
      current default.
    - **`data-integrity.md`**: removed the stale `gramaton raw`
      reference in the path-validation bullet; replaced with the
      correct "the agent resolves `source_ref` itself" shape.
    - **`collections.md`, `foundations.md`, `case-studies.md`**:
      scanned for stale references; no changes needed.
    - **`project-design/README.md`**: already a leaner index over the
      9 survivors (regenerated in the phase-1 sunset pass); no
      additional change in phase 3.
- **`docs/providers.md` updated for BERT-as-default and audited against
  current provider code.** Adds a new first-class "Pure-Go BERT" section
  documenting the default provider: no external runtime, model cached
  at `~/.gramaton/models/<name>/`, custom HuggingFace repos supported,
  and the known amd64 performance caveat from
  `embed/bert/matmul_amd64.go` pending the AVX2 kernel. Ollama
  section rewritten as an "alternative local" path rather than a
  default; notes that `gramaton init` starts Ollama when
  `provider: ollama` is configured, but the runtime server does not
  supervise Ollama (a crashed Ollama causes embed calls to error and
  records land without vectors, still BM25-searchable). Bedrock LLM
  section notes the Converse API is model-agnostic (Claude / Titan /
  Llama / Mistral) per `llm/bedrock/bedrock.go:16`. "Mix and match"
  examples refreshed — default combo is now BERT embedding + optional
  Anthropic LLM; added pure-local (BERT + no LLM) and explicit
  disable-embedding examples. Dimension field called out per example
  since it must match the chosen model. Credential handling section
  made explicit: never inline keys, use `api_key_env` pointing at an
  env var name, and Gramaton fails startup if the env var is missing
  rather than running unauthenticated.
- **`docs/configuration.md` audited against current `config/config.go`
  and restructured.** Fixes drifted defaults: `server.idle_timeout`
  30m -> 4h, `embedding.provider` ""->bert, `embedding.model`
  mxbai-embed-large -> bge-small-en-v1.5, `curation.interval`
  5m -> 1m, `limits.max_summary_short` 500 -> 1000, `backup.enabled`
  false -> true. Drops the reference to `limits.max_summary_abstract`
  (field no longer exists). Adds sections for fields introduced
  since the doc was last touched: tiered `llm.models` (low / medium
  / high), six `llm_curation.*_effort` dials, cost-reduction
  toggles (`prompt_caching_enabled`, `manifest_cache_enabled`,
  `contradiction_check_reverse_edges`, `classify_short_prompt_compressed`),
  `llm_curation.max_calls_per_session`, the batch and coherence
  knobs (`contradiction_batch_size`, `synthesis_batch_size`,
  `synthesis_max_input_tokens`, `concept_coherence_min`,
  `long_classification_threshold`), `curation.observation_batch_size`
  and `observation_min_content_length`, and `search.session_dedup_enabled`.
  Adds a new top-level "Named stores" section documenting the
  `~/.gramaton/stores/<name>/config.yaml` layout and calling out the
  full-replace-not-merge silent-fail trap (dev collection item
  01KPMQ0N2KY2XY6KAMPW3WXF52): partial per-store configs can silently
  zero out sections and cause startup refusal. Marks the `observe:`
  section as soft-deprecated in favor of the Sessions flow. Restructures
  the file around the user-facing vs internal-tuning distinction that
  config/config.go itself uses. Surfaces one code/config-comment
  inconsistency during the audit: `dedup.action` values `flag` and
  `supersede` behave identically in `api/capture.go:142-174` (both
  mark the older record historical and add a supersedes edge; only
  `reject` differs), contrary to the config comment that describes
  `flag` as 'mark but don't delete'. Documented in the doc and tracked
  as dev collection item 01KPPXGF8FETMYWQDQC5H7VKYN.
- **`docs/integrator-guide.md` rewritten around the three storage
  paths.** Previous version was organized around "two retrieval
  modes" (Knowledge Graph vs Collections) and gave `gramaton_observe`
  as a first-class tool. Neither matches reality: T-02 introduced
  the three-way split (Memory / Sessions / Collections) and
  `gramaton_observe` is soft-deprecated in favor of the session
  prepare/commit flow. Rewrite reorganizes around the current
  model: sections for the decision rule ("will missing one item be
  a failure?"), Memory depth (four write paths, context envelope,
  classification guidance), Sessions depth (the commit triggers --
  decisions landing, topic pivots, the ~10-turn fallback --
  `promote_to_memory: false` for dead ends, the archive flow via
  shipped hooks at `hooks/claude-code/` and `hooks/kiro/`),
  Collections depth (schema field types verified against
  `api/collection_schema.go`, current `ErrConflict` dedup
  behavior replacing the soft `{duplicate: true}` response the old
  doc described -- that was the pre-T-02 shape), retrieval funnel
  (search -> inspect -> explore), agent prompt guidance, live
  reference pointer to `gramaton_guide(topic=...)`. Search-filter
  table expanded to match the actual `api.SearchRequest` fields
  (confidence/importance min+max, `missing`, `max_edges`,
  `similar_to`, `match`, `store`, `expires_before`, `since`,
  `random`, `sort` with the verified sort keys). Surfaced one
  code rough edge during the audit: the `store` filter takes
  plural `sessions` but result rows emit singular `session`.
  Called out in the doc as a known rough edge and tracked as
  Gramaton development collection item 01KPPX53EHSQ0884FW0Z857PTQ.
- **`docs/architecture.md` rewritten for the post-T-02 layered
  architecture.** Previous version predated T-02 and described a
  three-layer stack (CLI/MCP -> Server -> Engine) in which service
  methods lived inside the server package and held engine locks
  directly. Rewrite documents the four-layer stack that actually
  ships: transports (server/bindings_*.go, cli/mcp_proxy_*.go) ->
  api (canonical request/response types + locking discipline) ->
  core.Engine (composition root) -> data layer. Replaces claims
  about deleted symbols (serviceSearch, cli/mcp_proxy.go as a single
  file, store/ and testutil/ as top-level packages) with accurate
  descriptions: one file per operation in api/, nine MCP cluster
  registrars in server/mcp.go, the loopback-only /mcp mount,
  Engine holding graph + indexSet + providers + searcher +
  storage.Store behind a sync.RWMutex, provider factories in
  embed/embed.go and llm/llm.go. Adds concrete package map
  covering hooks/ (shipped Claude Code + Kiro integration
  scripts), llm/ telemetry surface (metered, pricing, ratelimit,
  usage), curation/ two-phase pipeline (deterministic + autonomous),
  storage/ file-level layout (cas, prolly, gc), and
  internal/{awscfg,version}. Keeps capture/search/session-commit
  data-flow examples updated to reflect api methods rather than
  server service methods. Explicit pointers into CONTRIBUTING.md's
  "Adding a new operation" recipe and the `.claude/skills/new-
  operation/` skill for the step-by-step. Every structural claim
  verified against current code (server/mcp.go, api/*.go,
  core/engine.go, graph/property.go, config/config.go, hooks/).
- **Pure-Go BERT is the official default embedding provider.** The
  `config.Defaults()` return value (`embedding.provider: "bert"`,
  `model: "bge-small-en-v1.5"`) already pointed at BERT, and
  `embed/setup.go` already tried BERT first with Ollama as fallback.
  This change aligns the user-facing story with that reality: the
  README Quick Start no longer instructs users to install Ollama
  (Gramaton downloads the BERT model itself on first run, ~130MB,
  no external runtime); the Features bullet lists BERT as default
  and Ollama as an alternative local option; the Configuration
  sample drops the explicit Ollama block and notes that an
  `embedding:` section is only needed to override the BERT default.
  `cli/init.go`'s `printNoOllama` fallback message was rewritten as
  `printEmbeddingSetupFailed`: when setup fails it now names the
  BERT-download path as the most likely cause (network), and lists
  Ollama, OpenAI, and Bedrock as alternatives rather than
  recommending Ollama. Known limitation: `embed/bert/matmul_amd64.go`
  still falls back to pure Go -- the AVX2 kernel TODO from
  `matmul_amd64.go:3` is real and tracked in the Gramaton development
  collection. Apple Silicon has the arm64 NEON kernel and is
  unaffected.
- **`README.md` rewritten for the post-T-02 surface.** Added a "Why
  Gramaton" section motivating the project against built-in agent
  memory and generic vector stores (without naming them) and listing
  the concrete questions the system is designed to answer. Added a
  new "Three Ways to Store Knowledge" section covering the Memory /
  Sessions / Collections split with the decision rule ("will missing
  one item be a failure?"). Rewrote the MCP tool table as five
  grouped clusters (Records, Search & discovery, Sessions,
  Collections, History & admin) matching the 38 tools actually
  registered by `server/mcp.go`. Dropped the stale `gramaton_observe`
  entry (the tool is no longer registered). Corrected the embedding
  provider list to include pure-Go BERT (`embed/bert/`). Corrected
  the feature bullet on concept emergence to reflect that candidate
  *promotion* is LLM-gated while candidate *detection* and existing-
  concept enrichment are always-on. Fixed the architecture diagram
  to show the `api/` canonical operations layer between transports
  and the engine. Added the named-store feature bullet. Added a
  CLI-reference entry for `gramaton mcp` (stdio proxy) and a
  Documentation-table entry for `CONTRIBUTING.md` and
  `docs/benchmarks.md`. Every feature claim verified against current
  code before landing. Phase 2 of the documentation consolidation
  tracked in `gramaton-inspection/documentation-plan.md`.
- **`gramaton_search` tool description rewritten with trigger-led framing.**
  The prior description was mechanical (`"Search Memory and Sessions.
  Returns results ranked by composite score..."`) and said nothing about
  when agents should call it. Observed failure mode (2026-04-20): agents
  write architecture answers, design docs, and competitive claims from
  general knowledge without first checking the store for project-specific
  prior thinking -- losing decisions that were already made. New
  description leads with "call BEFORE producing content that references
  project state" and lists concrete triggers: architecture questions,
  design/methodology writing, user references to prior sessions,
  reasoning where project-specific prior art may exist. Same pattern as
  the recent `session_prepare` rewrite. Shipped through the existing
  `api.SearchDescription` constant (no refactor needed; constant was
  already in place).
- **Session tool descriptions migrated to `api.SessionXxxDescription`
  constants.** `api/sessions.go` now defines `SessionStartDescription`,
  `SessionGetDescription`, `SessionPrepareDescription`, and
  `SessionCommitDescription` alongside the Request/Response types. Both
  MCP transports (`server/bindings_sessions.go` and
  `cli/mcp_proxy_sessions.go`) reference the constants instead of
  duplicating literal strings. Aligns with the existing convention used
  by capture + collections; structurally prevents the two MCP surfaces
  from drifting on help text. Also deleted `server/mcp_sessions.go`
  (dead code since the T-02 api/ migration -- `registerMCPTools` in
  `server/mcp.go` routes via `registerSessionsMCPTools` in bindings, not
  the older `registerMCPSessionTools`).
- **Leaner `session_prepare` extraction prompt.** The prompt returned
  by `gramaton_session_prepare` dropped from 202 lines (~9360 bytes)
  to 51 lines (~2525 bytes). Detailed content (field-role framework,
  classification heuristics per axis, question-type mapping, two-tier
  semantics) has been delegated to `gramaton_guide(topic="capture")`,
  `(topic="metadata")`, and `(topic="sessions")` -- the topics already
  carry the same material as the authoritative reference. The prompt
  retains the must-haves: the submission tool name, the full segment
  field list, the four core principles (synthesize-not-summarize,
  capture-don't-suppress, prospective-findability, skip-only-low-
  value), and explicit guide pointers. Per-call cognitive load on the
  LLM drops by ~73%; extraction quality shouldn't change because the
  reference material is now one guide call away. Updated in both
  embedded prompt files (`api/prompts/extraction.md`,
  `server/prompts/extraction.md`). Test
  `TestPrepareReturnsExtractionPromptWithSections` updated to assert
  the new contract (field names + principles + guide pointers),
  replacing the old per-section checklist.
- **`benchmark-extract` skill sub-agent contract refined.** Updated
  the sub-agent template in `.claude/skills/benchmark-extract/SKILL.md`
  to reflect the actual flow used during the 2026-04-20 pilot: read
  transcript from a staging file path (not inline in the prompt), call
  `session_start` first to get the ULID, then `prepare` + `commit` with
  the ULID (not the client_session_id). These details were learned
  empirically and the skill now matches what works end-to-end.
- **`gramaton_session_prepare` tool description rewritten for higher
  autonomous-trigger rate.** The prior description led with compaction
  and buried the actual triggers as "also call at natural breakpoints,"
  producing a regression vs the old `gramaton_observe` self-trigger
  cadence. New description leads with "EAGERLY throughout a conversation,
  not just at the end," names the rule-articulation trigger explicitly,
  adds a ~10-substantive-turns floor even without an explicit trigger,
  and flags bundling-at-session-end as an anti-pattern. Synced across
  the three description sites (`server/mcp_sessions.go`,
  `server/bindings_sessions.go`, `cli/mcp_proxy_sessions.go`).
  Corresponding "When to Call" guidance added to
  `server/guide/sessions.md` and `server/guide/capture.md`, and the
  shipped `integration/claude-code/CLAUDE.md` template was updated
  with the same trigger list + scheduled cadence + anti-pattern
  callout. Behavior change: LLM agents using the MCP surface should
  call prepare/commit more frequently during real-dev conversations.
- **Engine god-object split (P2-01)** -- `core/engine.go` reorganised
  into named subsystems and sibling pipeline packages across two PRs.
  PR1 extracted `providers`, `searcher`, and `indexSet` subsystems
  (engine shrank 1207 -> 952 LOC); `indexSet.applyToNode` consolidates
  cross-index updates so future indexes are picked up automatically
  rather than via edits to every node-creation path. PR2 extracted
  `chunking` and `dedup` to sibling packages (engine shrank 952 -> 564
  LOC); `core.Engine.PreChunk`, `ApplyChunks`, `CheckDedup`, and
  `IsContextLengthError` now delegate. `core.PreChunkResult` is a
  type alias for `chunking.Result` so existing callers compile
  unchanged. No public API signatures changed.
- **Server service layer extraction** -- extracted 14 service methods from
  HTTP handlers into `service_records.go`, `service_search.go`,
  `service_ops.go`. Both HTTP handlers and MCP tools now delegate to the
  same service layer, eliminating ~800 lines of duplicated business logic.
  Split `mcp.go` (1359 lines) into 5 focused files (`mcp.go` 66 lines,
  `mcp_search.go`, `mcp_records.go`, `mcp_ops.go`, `mcp_admin.go`).

### Fixed

- **MCP capture missing resolution on supersede** -- auto-supersession via
  MCP now sets `resolution` and `resolved_at` on the old record, matching
  HTTP handler behavior.
- **MCP inspect missing edge_id** -- inspect results via MCP now include
  `edge_id` in related entries, matching HTTP handler behavior.
- **MCP reembed blocking under lock** -- reembed via MCP now uses 3-phase
  approach (read lock to identify stale nodes, embed outside lock, write
  lock to apply). Previously held write lock during all embedding I/O.
- **MCP observe skipping validation** -- observe via MCP now validates
  message counts (max 100), content lengths (50KB), fact lengths (10KB),
  and message roles, matching HTTP handler behavior.
- **Ingest per-file content length** -- ingest handler now validates
  per-file content length before pre-embedding.
- **CLI url.PathEscape** -- added `url.PathEscape` to 6 CLI commands
  (inspect, classify, delete, update, resolve) to prevent path injection
  if record ID formats ever change.
- **Dead code removal** -- removed 4 unused validation functions from
  `cli/input.go`.

### Changed (continued)

- **Structural refactoring** -- `Engine.IndexNode` centralizes PropIdx +
  BM25 + VecIdx sync after node creation, replacing 10 copy-paste sites.
  `Engine.SetContentProp` refreshes BM25 on content changes. Shared
  `graph.IsStructuralEdge/IsStructuralChild/SemanticEdgeCount` replace
  5 duplicate `isChunkNode` and 2 duplicate edge-count functions.
  `search.computeSimilarities` extracted from 250-line monolith. Fixes
  missing BM25 indexing in ingest and import handlers, and stale BM25
  when curation updates content properties.
- **Scoring weight rebalance** -- Similarity weight increased from 0.35
  to 0.50, confidence reduced from 0.20 to 0.15, recency/freshness
  reduced from 0.15 to 0.10 each, frequency halved from 0.10 to 0.05,
  activation doubled from 0.05 to 0.10. Validated against 672 records
  across 6 content types (personal, academic, news, conversations,
  technical, reference) using NDCG@5 and MAP metrics. Fixes issue where
  high-confidence frequently-accessed records dominated search results
  regardless of topical relevance.

### Added

- **BM25 hybrid search with RRF fusion** -- BM25 keyword index runs
  alongside vector similarity. Results fused with Reciprocal Rank
  Fusion (RRF). Solves multi-concept query degradation where queries
  like "consciousness and memory" previously returned only single-
  concept results. Config: `search.bm25_k1`, `search.bm25_b`,
  `search.rrf_k`.
- **Jaccard dedup guard** -- auto-supersession at cosine >= 0.92 now
  requires Jaccard word-overlap >= 0.3 on content text. Prevents
  false positives on long documents with similar structure but
  different content (e.g., SEP philosophy articles).
- **Cross-section semantic linking** -- deterministic curation finds
  similar sections across different parent documents and creates
  `related_to` edges. Sibling exclusion prevents linking sections
  from the same article. Config: `curation.section_link_min`,
  `curation.max_section_links_per_run`.
- **RAPTOR-inspired concept node creation** -- autonomous curation
  converts keyword-based concept candidates into searchable concept
  nodes with LLM-generated summaries, embeddings, and `instance_of`
  edges from member records. Concept nodes act as retrieval hubs for
  collapsed-tree search. Config: `llm_curation.max_concepts_per_run`.
- **Section summary generation** -- autonomous curation detects section
  nodes with truncated summaries and generates proper LLM summaries.
- **Query decomposition fallback** -- `DecomposeQuery` splits multi-
  concept queries into sub-queries via LLM. `MergeResults` combines
  with RRF. `ShouldDecompose` triggers on low-confidence initial results.
- **Parallel LLM calls in curation** -- `classifyPending` and
  `generateSummaries` use a 4-worker pool for concurrent LLM calls.
  Concept node embedding moved outside the write lock.
- **Edge deletion endpoint** -- `DELETE /v1/edges/{edge_id}` for
  removing edges. Edge IDs now included in inspect response.
- **MCP proxy tools** -- `gramaton_unlink` (edge deletion) and
  `gramaton_history` (per-record change history). `gramaton_delete`
  intentionally excluded from MCP (destructive operations require
  CLI or HTTP API).
- **valid_until clearing** -- `PATCH /v1/records/{id}` with
  `valid_until: "clear"` removes valid_until, resolution, and
  resolved_at properties (reversing false supersession).
- **Multi-concept eval queries** -- 18 queries (8 personal, 10 SEP)
  targeting cross-concept retrieval for hybrid search validation.
- **Retrieval quality evaluation framework** (`eval/` package) --
  NDCG@k, Precision@k, MAP, Jaccard, confidence calibration metrics.
  Loads datasets from `~/.gramaton/eval/` (generated by gramaton-bench).
  Supports per-dataset and combined evaluation, weight configuration
  matrix testing. Mock and LLM-gated capture quality evaluation.
- **Backup status endpoint** -- `GET /v1/backup` lists existing backups
  without creating a new one.

- **On-demand server daemon** -- Gramaton now runs as an HTTP server
  that auto-starts on first CLI invocation and shuts down after idle
  timeout (default 30 min). Graph stays in memory for fast access.
  `gramaton serve` for explicit control (`--fg`, `--stop`).
- **Auto-supersession** -- when a new record is captured with high
  embedding similarity (>= dedup threshold) to an existing record,
  the server automatically sets `valid_until` on the old record and
  creates a `supersedes` edge. No agent involvement required.
- **MCP branch tool** -- all 5 branch operations (list, create,
  checkout, merge, discard) now work via MCP. Previously a stub.
- **Deterministic curation** -- background goroutine runs every 5
  minutes (configurable). Lifecycle transitions expire stale ephemeral/
  temporal records. Orphan linking creates `related_to` edges for
  records with zero connections. Duplicate consolidation auto-supersedes
  near-duplicate pairs. Concept candidate flagging identifies keywords
  above emergence threshold. Store manifest computes aggregate stats.
- **Autonomous LLM curation** -- when an LLM provider is configured
  (Anthropic API), the server classifies pending records, generates
  missing summaries, and promotes concept candidates. Rate-limited
  (default 20 LLM calls per cycle, batch size 10). Each mutation is
  atomic (no branches for routine classification).
- **LLM provider interface** -- `llm.Provider` with `Complete` and
  `ModelID`. Anthropic Messages API client as first implementation.
  Config supports env var name or direct API key.
- **Observe pipeline** -- `gramaton_observe` MCP tool and
  `POST /v1/observe` endpoint. Dual-mode: send raw conversation
  messages (server extracts via LLM) or pre-extracted facts (no LLM
  needed). Fire-and-forget with async processing. Three-layer feedback
  loop detection (dedup, recency, retrieval tracking). Quality gates
  filter noise before storage. Survivors stored as deferred captures
  (ephemeral, low confidence) for curation to promote or decay.
- **Retrieval tracker** -- server automatically tracks which records
  were served to agents via search, inspect, and explore. Used by
  observe quality gates to prevent feedback loops (re-extracting
  knowledge that was just retrieved). Thread-safe, time-bounded,
  zero API changes.
- **Garbage collection** -- deterministic curation phase that hard-
  deletes records meeting ALL criteria: unclassified 30+ days, zero
  access, zero edges, ephemeral, low confidence. Off by default,
  dry-run by default when enabled. Implements "never delete knowledge,
  forget noise" principle.
- **Curation dry-run** -- `gramaton_curation(action="dry_run")` runs
  the full autonomous LLM pipeline but returns planned changes instead
  of applying them. Preview what curation would do.
- **Contradiction detection** -- autonomous curation detects semantic
  contradictions between records with 0.5-0.85 cosine similarity.
  LLM evaluates pairs and creates `contradicts` or `supersedes` edges.
  Configurable thresholds and batch limits.
- **Store manifest qualitative summary** -- LLM-generated 2-3 sentence
  assessment of store strengths and gaps. Included in manifest alongside
  auto-generated stats. Top 20 keywords tracked for domain analysis.
- **Concept node enrichment** -- deterministic curation computes
  `evidence_count` and `last_evidence_at` on concept nodes from
  inbound edges each cycle.
- **Expiration visibility** -- metadata_summary now shows "expires in
  3 days" or "expired 5 days ago" instead of just "Current/Historical".
- **asserted_as_of field** -- new timestamp property for when the source
  made a claim (distinct from created_at when captured). Supported on
  capture, update, classify, search results, MCP tools, and import.
- **Record resolution lifecycle** -- `gramaton_resolve` MCP tool,
  `POST /v1/records/{id}/resolve` endpoint, and `gramaton resolve` CLI
  command. General-purpose lifecycle transitions: completed, superseded,
  abandoned, obsolete. Sets `resolution`, `resolved_at`, and auto-sets
  `valid_until` to deprioritize in search. Search supports `resolution`
  filter (including `unresolved` for records with no resolution set).
  Auto-supersession now sets `resolution: superseded` on old records.
  Resolution appears in metadata summaries and search facets.
- **Engine functional options** -- `LoadEngineWithOptions` supports
  dependency injection via `WithEmbedder` and `WithLLM` at construction.
  Dependencies immutable after init (no runtime setters).
- **Curation endpoints** -- `GET /v1/curation` (status, concept
  candidates, manifest) and `POST /v1/curation/trigger` (manual
  cycle, dry-run). `gramaton_curation` MCP tool with status/trigger/
  dry_run actions.
- **Enhanced curation status** -- response envelope now includes
  concept_candidates, stale_count, orphan_count, last_curated,
  and autonomous flag alongside pending_count and overdue.
- **Store manifest** -- computed each curation cycle: total records,
  edges, pending, orphans, stale, records by type, temporal range.
- `gramaton curation` CLI command with `--trigger` flag.
- **MCP diff and log tools** -- both now fully implemented. Previously
  stubs that returned error messages.
- CLI commands `stats` and `duplicates` for the new endpoints.
- **REST API** -- 24 HTTP endpoints for all operations (records CRUD,
  search, explore, branches, diff, log, revert, reembed, ingest,
  status). Standard response envelope with curation status and meta.
- **MCP integration** -- 19 MCP tools via Streamable HTTP at `/mcp`.
  Agents call `gramaton_search`, `gramaton_capture`, etc. as typed
  tools with no shell involvement. Uses official MCP Go SDK
  (Apache-2.0). Stdio transport via `gramaton mcp` for clients like
  Claude Code.
- **MCP proxy architecture** -- `gramaton mcp` is now a stateless HTTP
  proxy. Tool calls forward to the HTTP server instead of loading a
  separate engine in-process. Eliminates state divergence when multiple
  Claude Code sessions run simultaneously. Server auto-starts if not
  running. MCP processes can die and restart with zero consequences.
- **Backup status endpoint** -- `GET /v1/backup` lists existing backups
  without creating a new one. Used by `gramaton_backup(action="status")`.
- **CLI thin client** -- all CLI commands now delegate to the server.
  Same commands, same output format, backward-compatible with v0.1
  agent prompts. Auto-starts the daemon transparently.
- **Core engine extraction** -- graph engine, indexes, and providers
  extracted into `core/` package with `sync.RWMutex` for concurrent
  read/write safety. Embedding calls happen outside the lock.
- **Server lifecycle** -- port 42982, PID file discovery, flock-based
  race protection, health check verification, graceful shutdown.
- `gramaton tempdir` command -- prints the OS-appropriate temp
  directory path for agent file input
- `--file`/`-f` flag on `capture`, `classify`, `update` -- reads
  JSON from a file instead of stdin
- Auto-cleanup of temp files after successful read, stale file sweep
  (1 hour) on each write command invocation
- v0.2 server design document with all architecture decisions resolved

- **Advanced search features** -- 15 new query capabilities:
  - Filter-only queries (search without text, returns all matching records)
  - Sort by created_at, last_accessed, access_count, confidence,
    importance, content_length, edge_count, staleness (asc/desc)
  - Importance range filter (importance_min/max)
  - Negation on enum filters (prefix with `!`, e.g. `!ephemeral`)
  - Missing field detection (`missing: ["temporality"]` finds unclassified)
  - Keyword exact match (tag-based lookup, all specified must be present)
  - Access-based queries (last_accessed_before/after, access_count_min/max)
  - Validity window queries (valid_before/after, expires_before/after)
  - Full-text substring search (`match` param, case-insensitive, distinct
    from vector similarity)
  - Record-to-record similarity (`similar_to` with record ID, uses stored
    embedding)
  - Random/sample mode (partial Fisher-Yates, optionally filtered)
  - Faceted counts (per-field breakdowns alongside results)
  - Orphan/connectedness queries (min_edges/max_edges filter, edge_count sort)
  - Staleness detection (computed score from temporality + age + access)
- **Aggregates endpoint** -- `GET /v1/stats` and `gramaton_stats` MCP tool.
  Returns counts by temporality, knowledge_type, epistemic_status, and
  confidence distribution across the store.
- **Near-duplicate detection** -- `POST /v1/duplicates` and
  `gramaton_duplicates` MCP tool. Pairwise embedding similarity scan,
  configurable threshold and max pairs.
- **Keyword index** -- PropertyIndex now indexes individual tokens from
  StringList properties for exact tag lookup via `LookupKeyword`.
- **NodesWithKey** -- PropertyIndex method returns all node IDs that have
  a given property, enabling missing-field queries.
- Search results now include created_at, access_count, importance,
  content_length, edge_count, and staleness fields.

- **Backup/restore** -- `gramaton backup` creates compressed tar.gz
  archives with ISO8601 timestamps in filenames. Includes all store
  data (chunks, HEAD, refs) and sanitized config (API keys stripped).
  `gramaton restore` with interactive confirmation. Auto-backup via
  curation runner with configurable schedule and retention (default:
  2 backups, daily). POST /v1/backup and gramaton_backup MCP tool.
- **Export** -- query-driven export in JSON Lines, CSV, and Markdown
  formats. Reuses search filter infrastructure. POST /v1/export
  endpoint and `gramaton export` CLI command with --format and
  --output flags.
- **Import** -- JSON Lines import with property allowlist, new ULID
  assignment, edge remapping within import batch. CSV import with
  column mapping and aliases. Obsidian vault import with YAML
  frontmatter parsing and [[wikilink]] to edge conversion. Security:
  content sanitization, safe property allowlist, max 10K records per
  import, no edges to pre-existing records.
- **Search flag helpers** -- extracted addSearchFlags/buildSearchBody
  from search_cmd.go for reuse by export command.
- **Structured logging** -- JSON-formatted logs via `log/slog` with
  levels (debug/info/warn/error). File-based at `~/.gramaton/gramaton.log`
  with automatic rotation (50MB per file), gzip compression of old
  files, and configurable disk budget (default 512MB). Foreground
  mode also writes to stderr. Debug level logs every HTTP request
  with method, path, duration, and remote address. Curation logs
  include component tags for filtering.
- **Log rotation** -- built-in rotating writer with gzip compression.
  No external dependency (lumberjack). Enforces total disk budget by
  deleting oldest compressed files first.
- **Update endpoint extended** -- PATCH /v1/records/{id} and
  gramaton_update MCP tool now support valid_until, keywords, and
  summary_short in addition to existing metadata fields.
- **Named stores** -- multiple isolated knowledge graphs under
  `~/.gramaton/stores/<name>/`. Each store has its own data directory,
  server process, and optional config override (inherits global config
  if absent). Select via `--store <name>` flag or `GRAMATON_STORE`
  env var. Management commands: `gramaton store list|create|delete|rename`.
  Renaming supports the unnamed default store via the "default" alias.
  Backup filenames include the store name for identification. Store
  names validated: 1-64 alphanumeric/hyphen/underscore characters.

### Changed

- **CLI tenet 10 refactor** -- CLI package now imports only config,
  core, embed, and server (was 9 internal packages). Deleted duplicate
  engine.go (300 lines), dead curation.go, dead atomic.go. Ingest
  command rewritten to use server API instead of direct graph
  manipulation. Ollama setup extracted to embed/setup.go. Dead branch
  helpers removed.
- Search pre-embeds query text outside the lock to avoid blocking
  the server during Ollama model load
- Capture pre-embeds content outside the lock for the same reason
- Server does not auto-start Ollama -- connects to whatever
  embedding provider is configured

### Security

- Restrict `--file` to gramaton temp directory only, reject arbitrary
  paths
- Resolve symlinks before path validation to prevent symlink-based
  escapes
- Read via file descriptor to avoid TOCTOU between validation and
  read
- Verify input is a regular file (not device, pipe, or symlink)
- Strip absolute paths from error messages to prevent info disclosure
- Sweep removes symlinks unconditionally from temp directory
- HTTP server timeouts (read 10s, write 120s, idle 120s)
- Security headers on all REST responses (Content-Type, nosniff,
  no-store)
- MCP endpoint excluded from REST security headers (own content
  negotiation)
- Search input bounds: top capped at 1000, keywords at 100, missing
  fields at 50, match string at 1024 bytes, explore depth at 10,
  edge types at 50, duplicate pairs at 1000
- Float64 range validation on all confidence/importance parameters (0-1)
- Integer sign validation on access_count and edge count parameters
- Duplicate threshold validated 0-1 with safe default
- Error messages no longer echo raw user input (date, sort, order)
- Random mode uses partial Fisher-Yates (O(k) not O(n)) to prevent
  CPU exhaustion on large candidate sets
- Vector search bounded to 3x top results instead of full candidate set
- Log limit capped at 500, commit traversal depth at 5000
- Diff topic parameter length capped at 1024
- Branch name validation no longer echoes invalid characters
- Chunking no longer embeds under the write lock. Pre-embed and
  pre-chunk outside the lock, apply under lock (same fix applied
  to capture, MCP capture, and ingest). Eliminates server deadlock
  when content exceeds chunk threshold and Ollama is slow to respond.
- Ingest pre-embeds all files outside the lock in one batch instead
  of embedding each file under the write lock sequentially
- Reembed pre-embeds outside the lock using the same gather-embed-apply
  pattern as capture. Previously held the write lock during all external
  embedding calls, blocking the entire server for the batch duration.
- Content length validated against MaxContentLength on capture endpoint
  (previously only enforced on import)
- Keyword count (100) and per-keyword length (256 chars) validated on
  capture, update, and classify endpoints (previously only on search)
- String field length limits enforced: summary_short (500), summary_abstract
  (5000), source_ref (2048), all context fields (2048)
- Reembed batch size capped at 500 (previously unbounded)
- Log endpoint limit capped at 500 (previously unbounded)
- Diff topic parameter validated against maxTopicLength before use
- Export top parameter capped at 10000 (previously unbounded when
  explicitly set)
- Ingest filenames sanitized via filepath.Base to strip directory
  components from stored source_ref
- Backup archive validation tightened: HEAD file check requires root-level
  entry (data/HEAD or HEAD), not any nested file named HEAD
- Backup restore rejects symlinks, hardlinks, and other non-regular file
  types in tar archives (previously silently ignored)
- Config directory created with 0o700 permissions (was 0o755)
- Store name validated against allowlist regex before path resolution
  (prevents path traversal via --store flag or GRAMATON_STORE env var)

## [0.1.0] - 2026-04-04

First working release. CLI-driven knowledge store for AI agents with
property graph storage, vector search, and versioned persistence.

### Added

- **Graph engine** -- property graph with 8 typed properties (String,
  Float64, Int64, Bool, Timestamp, Vector, StringList, Bytes), ULID node
  and edge IDs, safe property accessors
- **Content-addressed storage** -- SHA-256 hashing, atomic writes via
  temp+rename, hex-validated paths, 0o700 directory permissions
- **Prolly trees** -- probabilistic B-trees with FNV-1a rolling hash for
  content-defined chunk boundaries, configurable chunk size and split
  probability, O(changes) commit diffs with subtree skipping
- **Versioned commits** -- v0 (flat hash lists) and v1 (prolly tree roots)
  format, backward-compatible loading, per-record history via
  NodeHashInCommit (O(log N))
- **Property index** -- exact match (serialized value map), range queries
  (sorted slice + binary search), substring search (case-insensitive
  Contains)
- **Vector index** -- flat index with cosine similarity, candidate set
  filtering, keyed by node ID
- **Search** -- 6-factor scoring model (similarity, recency, freshness,
  frequency, activation, confidence), metadata filtering (temporality,
  knowledge type, epistemic status, confidence range, date range),
  configurable result count
- **Spreading activation** -- access recording increments access_count and
  updates last_accessed, one-hop neighbor boosting with configurable base
  amount and attenuation factor
- **Document chunking** -- configurable threshold, chunk size, and overlap,
  word-boundary-aware splitting, parent-child edge linking
- **Dedup detection** -- vector similarity threshold check before capture
- **Embedding** -- Ollama provider with HTTP client (60s timeout),
  auto-start via lifecycle management, auto-model-pull, guided init with
  platform-specific install instructions
- **Branching** -- create, list, checkout, merge (fast-forward), discard,
  branch name sanitization
- **CLI commands** -- capture, search, inspect, explore, update, delete,
  classify, pending, ingest, reembed, branch, diff, log, revert, status,
  init
- **Diff** -- commit-level diff with prolly tree subtree skipping, --since
  and --topic flags, short hash resolution
- **Per-record log** -- track property changes across commits for a
  specific record, showing from/to diffs
- **Agent integration** -- Claude Code CLAUDE.md template, capture and
  curation subagent prompts, Kiro skill definitions, custom agent
  integration guide
- **Piggyback curation** -- curation status (pending_count, overdue)
  appended to search/inspect/explore responses, triggers agent-driven
  classification of pending records
- **Security hardening** -- input validation with LimitReader, BOM
  stripping, UTF-8 validation, null byte rejection, allocation guards
  (10M element max), symlink protection on ingest, 0o600 file permissions
- **Configuration** -- YAML config with all design doc defaults, prolly
  tree tuning parameters, activation settings, storage paths

[Unreleased]: https://github.com/gramaton-ai/gramaton/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/gramaton-ai/gramaton/releases/tag/v0.1.0
