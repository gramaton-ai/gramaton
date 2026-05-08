# Changelog

All notable changes to Gramaton are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **CONTRIBUTING.md notes the C-toolchain prerequisite for
  `go test -race` on Windows.** Race detector requires CGO; CGO
  requires gcc; Windows doesn't ship with one. The doc now points
  to `winget install BrechtSanders.WinLibs.POSIX.UCRT` as the
  simplest install. Surfaced when the Windows debug session
  installed it ad-hoc to verify a fix locally; doc gap closed.

### Fixed

- **`cli/integration_test.go::runCmd` had a Windows-only pipe-buffer
  deadlock that hung any cli integration test producing >4KB of JSON
  output.** The helper redirected `os.Stdout` to a pipe write end
  (`w`), ran `rootCmd.Execute()` synchronously, then read from the
  pipe read end (`r`) AFTER Execute returned. POSIX anonymous pipes
  default to ~64KB so most outputs fit and writes never block;
  Windows anonymous pipes default to ~4KB, so any cli command
  producing >4KB JSON would block in `Write` waiting for a reader
  that hadn't started yet. Surfaced as `TestCLISearchSort` and
  `TestCLISearch` consuming the full 10-min go-test budget on
  Windows CI; the goroutine dump showed a 4134-byte write blocked
  in `os.(*File).Write` from `cli.printJSON`. Fix: drain the read
  end concurrently in a goroutine via `io.Copy`. Standard pattern,
  no buffer-size dependency. Covers every cli integration test, not
  just the two that were running at timeout-fire on prior CI runs.
  Diagnosed by parallel Windows-side Claude Code session; macOS-side
  applied. Closes #50.

### Changed

- **`config.JobsConfig` adds a `MaxSyncBatchSize` field**, mirroring
  the existing `MaxAsyncBatchSize`. Default 0 (use the
  `MaxSyncBatchSize = 500` constant). Operators can set it lower to
  push more requests onto the async path; in-package tests use it
  to exercise sync/async cap-boundary behavior without paying for
  500+ records per test. Closes #49.

### Fixed

- **`TestCaptureBatchAsyncLargerThanSyncCap` was over-sized for its
  contract.** The test's purpose is to verify "items > sync cap
  accepted via async" — a single boundary check. It used to submit
  `MaxSyncBatchSize+50 = 550` items, taking 5+ minutes on Windows
  under race + parallel-suite load and blowing through the 10-min
  per-package go-test budget. Now configures
  `cfg.Jobs.MaxSyncBatchSize = 10` and submits 11 items: same proof,
  ~99% faster on every platform (0.89s on macOS in a quick check
  vs the prior 5+ min on Windows). The cap-as-config plumbing
  changes are independently useful as an operator dial. Closes #49.

- **`TestCaptureBatchChunkedRefMapPersistedAcrossChunks` timed out
  on Windows under race because `chunkNumBlocker.waitParked` used
  a hard 10-second budget.** The test runs a 3-chunk async batch
  and waits for the runner to park at chunk 2's chunk_save inject;
  on Windows under race detector + parallel-suite load, chunk 1's
  bbolt Save + jobs-store AdvanceStatus + chunk 2 prep can take
  8-10 seconds, spilling past the budget. The existing
  channel-based synchronization in the blocker is correct
  (despite the issue title naming a "race") -- the only problem
  was the wall-clock timeout being too short for Windows. Fix:
  wrap the budget in `testutil.Timeout`, same one-line pattern
  used by `pollUntilTerminal` in capture_batch_async_test.go:20.
  Windows now gets 30 seconds; POSIX is unchanged. Closes #16.

- **Five Windows-only test failures from the Phase D backlog were
  test-fixture bugs, not production bugs.** Three real root causes
  across `internal/setup/`:
  (1) `TestRegisterClaudeHooksIdempotentAndPreserving` used
  `t.Setenv("HOME", tmp)` to redirect the home directory, but on
  Windows `os.UserHomeDir` reads `%USERPROFILE%` not `$HOME`.
  Production correctly wrote to the real runner's
  `~/.claude/settings.json` (an empty map); the test re-read its
  tmp seed and saw no change. Fix: also set `USERPROFILE` next to
  every `t.Setenv("HOME", ...)` (two callsites in `hooks_test.go`).
  Closes #32.
  (2) `TestStepVerifySkipEverything` and
  `TestStepVerifyLLMWithGoodKeyFile` asserted on POSIX-only output
  lines (`"Config file permissions: 0600"`,
  `"LLM: ... 0600 perms"`). Production already emits
  Windows-equivalent lines (`"... skipped on Windows (NTFS ACL
  model)"`, `"... perm check skipped on Windows"`); tests now
  switch the expected substring on `runtime.GOOS`.
  (3) `TestStepVerifyLLMWithKeyFileWrongPerms` and
  `TestStepVerifyHooksNonExecutable` assert that production emits
  warnings about perm-bit / exec-bit problems. On Windows those
  checks are correctly skipped (NTFS ACL model; no exec bit), so
  there's no warning to assert. Tests now `t.Skip` on Windows.
  Closes #33.

- **`core/TestConcurrentReadsAndWrites` exhausted its 10s context
  budget on Windows under load, surfacing as a misleading "data
  corruption" assertion.** The test seeded 10 nodes, then ran 10
  readers + 3 writers (60 writes total) under a hard
  `context.WithTimeout(10*time.Second)`. On Windows under race
  detector + parallel-suite load, bbolt writes are dramatically
  slower than POSIX; the budget ran out before all 60 writes
  completed and writers exited early. The final node count came
  back as ~40-50 instead of 70 and the test fired
  `t.Fatalf("...(data corruption)")` -- but the data was consistent,
  just incomplete relative to the test's optimistic budget. Fix:
  inline `windowsTimeout` helper (3x scaling on Windows, no-op
  elsewhere) and wrap the test's context timeout + deadlock
  watchdog. Same shape as `testutil.Timeout`, inlined because
  `core/` can't import `testutil/` (cycle). Closes #40.

- **`server/panicLogDedup` LRU eviction was non-deterministic on
  Windows.** The dedup map stored `time.Now()` per fingerprint and
  picked the oldest by `t.Before(oldestTime)`. Windows' `time.Now`
  has ~15.6ms tick resolution; rapid sequential inserts could
  collapse to the same timestamp, leaving Go's nondeterministic map
  iteration to choose which entry got evicted. Test
  `TestPanicLogDedupMaxSize` failed under race+load when the
  expected-evicted entry happened to be iterated after another
  same-timestamp entry. Fix: replace `map[string]time.Time` with
  `map[string]panicEntry{seenAt time.Time; seq uint64}` and select
  the eviction victim by minimum `seq` (per-instance monotonic
  insertion counter). TTL pruning still uses `time.Now` since that's
  a real-world deadline; only LRU ordering becomes clock-independent.
  Closes #39.

- **`cli/readFileJSON` left an open file handle when calling
  `os.Remove` on the just-read input file.** The function used a
  `defer f.Close()` set up after `os.Open`, then called `os.Remove`
  in the success path while `f` was still open (defers fire on
  return, after the body completes). On POSIX this works because
  inode-unlink + open-handle is legal; on Windows mandatory file
  locks block the remove until the handle closes, so the cleanup
  silently no-opped and the temp file persisted. Fix: explicit
  `f.Close()` immediately before `os.Remove` in the success path.
  The deferred Close still runs after function return -- it
  becomes an "already closed" no-op, error silently discarded by
  the `defer` (consistent with the pre-fix behavior of the same
  defer). Closes #30.

- **Two Windows-only test flakes from the Phase D backlog.**
  (1) `internal/setup/TestWizardImportBranchMissingFile` asserted
  on a literal `"No file at /tmp/..."` substring against wizard
  output. Production code is portable (uses `os.IsNotExist`); the
  bug was test-side. The wizard's `expandUserPath` runs `filepath.Abs`
  on the user-supplied path, which converts `/tmp/...` to
  `D:\tmp\...` on Windows, so the substring never matched. Fix
  constructs the test path via `filepath.Join(t.TempDir(), ...)` so
  the assertion uses the same OS-native shape the wizard echoes.
  Closes #34. (2) `TestCaptureBatchWallClockSpeedup` hard-asserted
  parallel-batch wall-clock < serial wall-clock with `t.Errorf`,
  which flaked on a loaded ubuntu-latest run (par=1.4s, seq=944ms,
  ratio=0.67) even though batch correctness was unaffected. Softened
  to `t.Logf` — same shape as PR #14's softening of
  `TestEmbedConcurrentScratchDistinct`. Extended CONTRIBUTING.md's
  "scheduler-observable behavior" anti-pattern entry with a third
  bullet covering wall-clock comparisons across execution modes.
  Closes #36.

- **`/v1/restore` (and CLI `gramaton restore`, which proxies to it)
  was silently broken on Windows.** The handler called
  `backup.Restore(...)` while the engine still held bbolt + mmap
  handles inside `dataDir`; on Windows, mandatory file locks made
  `os.Rename(dataDir, replacedDir)` fail with
  ERROR_SHARING_VIOLATION. POSIX papered over the leak via
  inode-rename semantics, so the bug was invisible on macOS/Linux
  but every Windows user trying to restore a backup hit it. Fix:
  split `core.LoadEngineWithOptions` into a config/storage/providers
  half and a new `Engine.OpenFiles` body covering bolt + indexes +
  graph + jobs + sweeper. `Engine.CloseFiles` releases just the
  file-backed pieces (mirror of OpenFiles). `api.BackupRestore` now
  closes the engine's files, runs the on-disk swap, then re-opens
  against the restored data dir; on extract failure it re-opens
  against the (untouched) original dir so subsequent requests
  succeed. The injected `EngineOption` slice is retained on the
  Engine struct so `WithVectorIndex` / `WithEmbedder` / `WithLLM`
  survive the close+reopen cycle (otherwise tests using these would
  silently lose them after a restore). Closes #35.

- **Windows portability sweep round 2: CRLF in templateForClient,
  remaining engine-cleanup misses, hooks exec-bit assertion,
  symlink Windows skips.** Phase B sweep on commit `3687e53` (after
  the round-1 cluster #25/#26/#27/#28 landed) exposed five
  Windows-only failure classes that the round-1 helpers didn't
  cover. (1) `internal/setup/templateForClient` uses
  `strings.Replace` patterns with literal `\n`, but the embedded
  template files (`templates/*.md`) are checked out CRLF on Windows
  with git's autocrlf=true, so the addendum-marker strip silently
  no-ops and the resulting templates carry an unfilled
  `<!-- CLIENT_ADDENDUM -->`; root cause is in production code,
  fix normalizes embedded inputs to LF and the integration drift
  test normalizes the file-side compare. Clears
  `TestTemplateForClientKiroOmitsRoutingBlock`,
  `TestTemplateForClientUnknownReturnsBaseAlone`, and
  `TestIntegrationSnapshotsMatchCanonical/Claude_Code`. (2)
  `core.TestWrapLLMReplacesProvider` and `core.TestSaveAndReload`
  open engines without `t.Cleanup(eng.Close)`, leaking bbolt
  handles into `t.TempDir`'s `RemoveAll` (the same pattern PR #15
  fixed for testutil but never swept here — `setupTestEngine`'s
  inline pattern wasn't reused). (3) `internal/setup/hooks_test.go`
  asserts `info.Mode().Perm()&0o111 != 0` on materialized hook
  scripts; Windows reports `0o666` regardless of actual perms, so
  the assertion always errors. Skipped on Windows; the shebang
  check (which is OS-agnostic) still runs. (4) `cli/input_test.go`
  and `cli/tempdir_test.go` use `os.Symlink` with `t.Fatalf` on
  failure; Windows requires the `SeCreateSymbolicLink` privilege
  (admin or Developer Mode), which CI Windows runners don't
  provide. Skipped on Windows; the symlink-handling production
  paths are still covered on Linux/macOS CI. The remaining 8
  failures from the same Phase B sweep are deferred to follow-up
  GH issues (mmap-unmap on Windows, JSON path encoding,
  `step_verify` output divergence, `TestBackupAndRestoreRoundTrip`
  engine-lifecycle around restore, etc.) — those need their own
  investigation rather than another sweep.

- **`testutil.RegisterEngineCleanup` + sweep of test setups missing
  `engine.Close`.** PR #15 fixed the bbolt-handle leak in
  `testutil.NewEngine` (engine never closed before `t.TempDir`'s
  `RemoveAll` ran), but other packages reimplemented the same pattern
  inline without the cleanup — `backup/export_test.go`'s
  `setupTestEngine` was the loudest victim, leaking handles into
  Windows TempDir cleanup and producing the cluster of `unlinkat
  ...indexes.db: process cannot access the file because it is being
  used by another process` failures across `backup/`'s `TestExport*`
  / `TestImport*`. Adds `testutil.RegisterEngineCleanup(t, eng)` as
  a one-call API; updated `backup/export_test.go` and
  `curation/curation_test.go` (the two with no existing cleanup) to
  use it. Other test setups already had ad-hoc `t.Cleanup(eng.Close)`
  calls; left as-is. Future test setups should call the helper rather
  than reimplementing.

- **Windows test correctness sweep: file-mode + path-separator + timeout
  helpers.** Three classes of Windows-portability bugs in tests had
  been masked by the package-timeout cascade closed in PR #20.
  Bundled fix: introduce `testutil.AssertFileMode`,
  `testutil.AssertDirMode`, and `testutil.Timeout` helpers; replace
  the four POSIX-mode assertions in `backup/` and `internal/setup/`
  with the new helpers (Windows reports a fixed 0o666/0o444 based on
  the read-only flag, not POSIX bits — the helpers verify
  existence + type but skip the mode check on Windows); fix the
  `shouldExcludeSnapshot` test to use `filepath.Join` for inputs
  (the function uses `filepath.Separator` correctly, but the test
  was hardcoding `"refs/main"` forward-slashes); scale
  `pollUntilTerminal` and `waitEntered` test-helper deadlines via
  `testutil.Timeout` so Windows under -race gets 3x the budget. Per-
  callsite literals stay; only the helpers themselves apply the
  multiplier so future call sites benefit automatically. Closes #21,
  #22, #23.

### Changed

- **CI's race-detector job now runs `go test -race -short ./...`.**
  Three test-package timeouts (TestScaleMeasurement at 1000-record
  default, TestCaptureBatchAsyncLargerThanSyncCap at MaxSyncBatchSize+50,
  the cli/integration suite at 81 in-process command tests) were
  routinely blowing through Go's 10-minute per-package alarm on
  Windows under race-detector overhead. These tests are now gated
  behind `testing.Short()` and skipped under `-short`. Functional CI
  (non-race) on every platform still exercises them at full scale, so
  real regressions in heavy paths still surface; the race-CI signal
  is preserved on the ~95% of test code that doesn't have scale-
  derived runtime. TestScaleMeasurement's CI default also drops from
  1000 to 100 records (its assertions are structural, not threshold-
  based; `GRAMATON_SCALE` env var stays for real measurements).
  Closes #17.

### Fixed

- **Tar header names now use forward slashes on every host OS.** The
  archive walker in `backup/backup.go` set `header.Name` via
  `filepath.Join("data", rel)`, which is OS-native and produced
  `data\jobs.db` on Windows. The POSIX tar spec requires forward
  slashes in header names regardless of host OS, so the resulting
  archive was malformed: `Restore` on Windows extracted entries to
  bogus paths or skipped them, leaving callers with empty bbolt
  files. The visible symptom was `TestRestoreRestoresJobs` panicking
  with a nil-bucket dereference (the bbolt file was zero-bytes after
  extraction, so `bolt.Open` succeeded but `tx.Bucket("jobs")`
  returned nil). Switched the three header-name callsites to
  `path.Join("data", filepath.ToSlash(rel))`. Adds a regression
  test that creates a nested chunk path, scans every entry name in
  the resulting archive for backslashes, and verifies the file
  lands at the OS-native restored path. CONTRIBUTING.md gains a
  matching anti-pattern entry. Closes #18.

- **Test stability sweep: order-dependent assertions in concurrent tests.**
  Two more instances of the timing-fragile test pattern surfaced
  during the GH #12 PR runs: `TestCompleteWithModelAnthropicFallback`
  (curation) asserted `models[0] == "" && models[1] == "override-model"`
  on a mock-LLM slice appended to from two parallel goroutines —
  changed to set-based assertion. `TestParallelLLMConcurrency`
  (curation) hard-asserted `peak > 1` on observed concurrency — the
  elapsed-time bound (already in the test) is the authoritative
  parallelism gate; the peak observation is now a soft `t.Logf`
  signal. Pairs with the `TestEmbedConcurrentScratchDistinct` /
  `TestCaptureBatchAsyncCancelBeforeFirstChunk` fixes that landed
  earlier same-day. CONTRIBUTING.md gains an anti-pattern entry
  documenting lower-bound concurrency assertions and positional
  assertions on parallel-collected slices.

- **`testutil.NewEngine` now closes the engine before its temp dir is
  removed.** The helper called `t.TempDir()` (which auto-registers
  `os.RemoveAll` cleanup) and `core.LoadEngineWithOptions` (which
  opens bbolt files for `indexes.db`, jobstore, etc.) but never
  registered cleanup to call `eng.Close()`. On Linux/macOS, POSIX
  inode semantics let `RemoveAll` unlink files with open handles
  (the inode persists until the handle closes). On Windows, the OS
  refuses to unlink files held by another process, making every
  testutil-using test fail at teardown. The new `t.Cleanup` runs
  LIFO before `t.TempDir`'s cleanup, draining bbolt handles before
  `RemoveAll` fires. Closes the testutil half of #12.

- **`TestCaptureBatchAsyncCancelBeforeFirstChunk` now accepts the
  documented partial-commit-on-cancel outcome.** The chunked async
  runner explicitly supports partial commits: when cancel arrives
  mid-flight, the runner finalizes Job with whatever has committed
  so far. The test only handled `cancelled-with-0-items` and
  `completed`; on loaded CI runners the 2-item single-chunk batch
  often lands before cancel can short-circuit it, producing a third
  legitimate state (`cancelled-with-N>0-items`) the test rejected.
  The test now verifies the in-store count matches `Job.Result.Added`
  rather than asserting 0 items.

- **`TestEmbedConcurrentScratchDistinct` no longer fails on loaded CI
  runners.** The test asserted that `pool.maxLive >= 2` to prove
  concurrency, but whether the Go scheduler actually runs the 8
  goroutines in parallel during their `Embed` calls is timing-
  dependent — on loaded runners they can serialize and the assertion
  fails despite no real regression. The hard `t.Errorf` is now a
  soft `t.Logf` warning; the test's correctness contract (Get/Put
  balance, no deadlock, no buffer corruption) is unchanged. Closes
  the bert-pool flake observed on PR #13's first CI run.

- **`storage.Write` no longer fails on Windows when the same content
  is written concurrently.** Content-addressed dedup means two
  goroutines writing identical data race to rename their temp files
  into the same destination. On Linux/macOS, `os.Rename` silently
  overwrites and the race is benign. On Windows, `os.Rename` returns
  `EACCES` when the destination already exists or is held by another
  process, so the second writer would fail. The new
  `renameWithDedupRecovery` helper checks for the destination after
  a rename failure: if dest exists, the dedup invariant means another
  writer landed the same content first, so the operation succeeds
  cleanly and the deferred temp cleanup removes the stale temp.
  Closes the storage half of #12 (Windows CI flake under race
  detector).

### Changed

- **`internal/version` falls back to `runtime/debug.BuildInfo` when
  ldflags aren't injected.** Binaries installed via `go install
  github.com/gramaton-ai/gramaton@latest` previously reported
  `Version: dev`, `Commit: unknown`, `Date: unknown` because Go's
  install path doesn't pass our build-tag ldflags. The package now
  reads the embedded BuildInfo at startup and fills sentinel values
  from the module's `Main.Version` and the binary's VCS settings
  (`vcs.revision` -> short commit, `vcs.time` -> build date).
  Explicit ldflags-set values still win — hand-built and
  release-pipeline binaries are unchanged. Net: `go install` users
  now see accurate version output without any change to their
  install command.

## [0.3.0-alpha.1] - 2026-05-07

Third foundational era. Gramaton evolved from a single Memory store
through three shape changes: Memory only → +Collections (exhaustive
structured lists) → +Sessions (automatic conversation extraction).
This is the first publicly-announced release.

### Changed

- **Pre-public-release docs sweep.** Comprehensive re-read of every
  user-facing and integrator-facing doc against the current code,
  catching drift the recent feature work left behind. Specific
  fixes: tool-count claims aligned with the actual MCP tool registry;
  log-filename references corrected (`gramaton.log`, not
  `server.log`); `gramaton_capture_batch` description rewritten
  (sync mode is per-item failure isolation, not all-or-nothing);
  `gramaton init` wizard step order corrected in the CLI table;
  `gramaton_search` data flow in `architecture.md` extended with
  the snapshot / cursor pagination paths; `clear_mode` collection
  knob explained instead of waved at; four-knob curation framing
  used consistently where collections are introduced; `content_fields`
  schema field documented end-to-end; `auto_close_collection_status`
  resolve flag spelled out. Also future-proofed the README's
  "single-user / no auth" caveat (was bound to `v0.1`).
- **Internal phase identifiers scrubbed from docs and code comments.**
  Internal Gramaton-development phase markers (T-NN, P[0-3]-NN,
  F1 Layer N, Phase N follow-on) and parenthetical tickets like
  `(P2-08)` removed from doc files, `CHANGELOG.md` entries, and
  ~30 Go source files' comments. They were Gramaton-internal
  shorthand that was cryptic to public readers; sentences that
  referenced them were rewritten to preserve the technical context
  without the identifier (e.g., "regression for P2-08 fix #1: a
  hung task..." → "regression for the per-task-timeout fix: a
  hung task..."). The `D-NN` numbering inside
  `docs/project-design/design-decisions.md` is preserved — those
  are heading anchors for the doc, not tracker references. Test
  fixture content using fictional ticket strings as record titles
  is preserved (test data, not phase IDs). Also removed the
  misplaced internal `p2-06-writesession-plan.md` plan doc from
  `docs/project-design/`.
- **Public-readiness scrub: removed Amazon-internal `Isengard` tool
  reference** from `docs/providers.md`. Replaced the credential-
  manager refresh discussion with a generic phrasing that doesn't
  signal employer-specific tooling.

### Added

- **Hot-path MCP tools pinned as `alwaysLoad` to bypass tool-search
  deferral.** Claude Code's tool-search (on by default) defers MCP
  tool schemas until the agent calls `ToolSearch` to fetch them,
  paying ~50-100ms per tool per session on first use. Pin the 12
  tools the agent reaches for on every substantive session
  (`gramaton_search`, `gramaton_inspect`, `gramaton_capture`,
  `gramaton_session_prepare`, `gramaton_session_commit`,
  `gramaton_collection_add`, `gramaton_collection_items`,
  `gramaton_collection_update`, `gramaton_collection_list`,
  `gramaton_resolve`, `gramaton_link`, `gramaton_curation`) via
  `_meta: {"anthropic/alwaysLoad": true}` in their MCP registration.
  Added `mcpAlwaysLoadMeta()` helper in `server/mcp.go` so the pin
  reads cleanly at each registration site.
  - The remaining ~24 tools (branch / backup / reembed / classify /
    pending / status / duplicates / stats / explore / log / diff /
    history / collection lifecycle ops / unlink / update /
    session_get / session_start / collection_add_batch / ...) stay
    deferred so context budget isn't burned on tools the agent uses
    rarely or never in a typical session. Net split: ~1.8K tokens
    eager / ~3.7K tokens still deferred.
  - Forward-compatible: clients that don't implement tool-search
    (older Claude Code, other MCP clients) ignore the `_meta` field
    and behave as before.
  - `TestMCPHotPathToolsHaveAlwaysLoadMeta` enforces the pinned set.
    Asymmetric check: every named hot-path tool MUST carry the meta
    flag; no other tool may. Catches both dropped pins (refactor
    nukes Meta) and creep (new tool quietly pins itself without
    weighing the context-budget tradeoff).

- **Claude Code auto-memory routing block + per-client template
  scaffolding.** `gramaton init` now installs a routing rule into
  Claude Code's CLAUDE.md telling the agent to prefer Gramaton over
  Claude Code's built-in `~/.claude/projects/<slug>/memory/`
  auto-memory for new captures. Decision rule: would the agent fail
  at its job if this content weren't loaded into every conversation?
  Yes (behavioral rule) → auto-memory. No (specific fact, decision,
  context) → Gramaton. Existing auto-memory entries are unchanged;
  the rule governs future routing only. Routing block sits between
  the introductory framing and the deeper retrieval/capture sections
  so the agent registers it before specific tool guidance.
  - Per-client template architecture: `internal/setup/templates/`
    holds a shared `base.md` plus per-client addenda
    (`claude_addendum.md`, `kiro_addendum.md`).
    `templateForClient` substitutes the client's addendum at a
    `<!-- CLIENT_ADDENDUM -->` marker in the base; unfilled markers
    are stripped cleanly. Adding a new client (Codex, Cursor, etc.)
    is dropping in a new addendum file plus a `case` statement;
    install code stays untouched.
  - Init-time detection of existing auto-memory at
    `~/.claude/projects/*/memory/MEMORY.md` prints a one-line notice
    so users aren't confused about which store new captures land in.
  - `integration/claude-code/CLAUDE.md` (the user-readable snapshot
    referenced from README and integrator-guide) is now drift-tested
    against the canonical template via
    `TestIntegrationSnapshotsMatchCanonical`. Refresh after
    canonical edits with
    `go test ./internal/setup -update-integration`. Closes the
    snapshot/canonical drift class that left the file weeks-stale.
  - Kiro's addendum is reserved as a placeholder; idiomatic
    multi-file Kiro install is tracked separately.
  - Tracker: GitHub issue #3.

- **Filtered `gramaton export` + new `json` array format.** The
  CLI's `--format`, `--keywords`, etc. now actually filter
  server-side (previously the body fields were silently ignored).
  `--format` accepts `jsonl` (canonical line-delimited form,
  default), `json` (parseable JSON array — new shape), `csv`, and
  `markdown`. Filter args mirror `gramaton_search`'s most useful
  subset (`text`, `match`, `store`, `keywords`, `temporality`,
  `knowledge_type`, `epistemic_status`, `resolution`, `since`,
  `meta`); when none are set the export remains a full-store dump
  (legacy compat). The export is exhaustive over the matched set
  with no candidate cap — the CLI is the "give me everything"
  escape valve from the MCP-side pagination cap.
  - Streaming three-phase pattern (RLock → collect IDs; per-record
    RLock → fetch + write) eliminates the lock-across-I/O
    anti-pattern that previously held the engine RLock for the
    whole serialize. Concurrent writers are no longer blocked
    across an export.
  - JSON-array output streams (`[`, records with commas, `]`)
    rather than buffering the whole array; large exports stay
    bounded in memory.
  - `gramaton_search` truncation messages now point at
    `gramaton export --query "..." --output results.jsonl` for
    the genuine-exhaustion path, complementing cursor pagination.
  - Part of the search-ergonomics work (PR C).

- **`gramaton_search` cursor pagination + page table.** Fresh
  searches materialize up to `search.pagination.candidate_cap`
  ranked candidates (default 500, hard ceiling 1000) into a
  short-lived snapshot keyed by a ULID `query_id`. The response
  carries `page`, `page_size`, `total`, `next_cursor`, `query_id`,
  and a `pages` table with cursors for every page in the snapshot.
  Subsequent calls with `cursor` slice into the same snapshot at
  the encoded boundaries — record content is fetched fresh per
  page so modifications surface immediately while the match set
  stays stable for the snapshot's TTL.
  - Random-access pagination via the page table enables map-reduce
    fan-out: parent agent dispatches subagents to non-adjacent
    pages in parallel.
  - `cursor` calls ignore other filter args (text, match, filter,
    etc.) — the cursor encodes its slice against the original
    snapshot. Response carries `ignored_params` listing what was
    dropped so the caller knows.
  - Snapshot TTL configurable via `search.pagination.snapshot_ttl`
    (default 20m). Expired snapshots return
    `{error: "snapshot_expired"}` so the agent can re-issue the
    original query.
  - Snapshot stores IDs + scores only (~50 bytes each); content
    is re-fetched on each page response. Memory footprint is
    trivial (~500KB across 10 concurrent snapshots at the cap).
  - `core.SnapshotStore` is the new in-engine cache; one per
    engine instance, started/stopped with the engine. Eviction
    is periodic + lazy on access.
  - Legacy `top` field falls back to `page_size` when the new
    field isn't supplied, preserving back-compat for existing
    callers. `MaxSearchTop = 1000` cap unchanged.
  - Part of the search-ergonomics work (PR B; the filtered CLI
    export half is a separate follow-up PR).

### Changed

- **Export format `json` semantic flip; new `jsonl` is canonical
  for line-delimited.** `gramaton export --format json` previously
  produced JSON Lines (Content-Type `application/x-ndjson`) — the
  shape didn't match the name, which routinely confused users
  trying `gramaton export --format json | jq`. After this change:
  - `--format jsonl` produces JSON Lines (the historical `json`
    behavior). New canonical name; **default**.
  - `--format json` produces a parseable JSON array
    (Content-Type `application/json`). New shape. The shape
    consumers expecting `--format json` typically want.
  - `csv`, `markdown` unchanged.
  - Backup-package functions mirror this: `backup.ExportJSON` now
    writes a JSON array; the historical JSONL writer is renamed
    `backup.ExportJSONL`. Per-format `*ByIDs` variants accept a
    pre-collected ID list for filtered exports.

- **`gramaton_collection_items` gains a `match` parameter for
  case-insensitive substring search.** Narrows an exhaustive
  collection list to items whose `field.*` string properties contain
  the substring. Composes with `filter`: e.g.
  `filter={"status":"open"} + match="auth"` returns open items
  mentioning "auth". Mirrors `gramaton_search`'s `match` semantics
  (literal substring, distinct from vector similarity), scoped to a
  single collection. Preserves the exhaustive-by-contract guarantee
  — items just have to satisfy filter + match. Part of the
  search-ergonomics work (PR A).

- **`content_fields` declaration on collection schemas.** Schemas
  may now declare an ordered `content_fields: [name1, name2, ...]`
  list naming the fields that constitute the canonical text
  representation for LLM-stage curation (classify, summarize,
  contradictions, concept synthesis) and embedding. Each name must
  reference a declared `type=string` field; non-string types
  (enum, number, date, boolean) are rejected at schema validation
  with an explicit error. The five `curation=standard` starter
  templates ship with explicit declarations: `backlog`
  `[title, details]`, `todo` `[title, notes]`, `reading-list`
  `[title, author, notes]`, `journal` `[title, entry]`,
  `references` `[title, description, notes]`.

- **RecordContent activation: collection items now flow through
  the autonomous curation pipeline.** Previously, `curation=standard`
  on collection items was a no-op: items were eligible for the
  pipeline (`processing_status=captured`) but bounced at every LLM
  stage's `content_full` guard because collection items don't
  populate `content_full`. The new `core.RecordContent(node,
  contentFields)` and `curation.RecordContentFor(g, recordID)`
  helpers synthesize LLM-grade text from `field.*` properties
  using the schema's `content_fields` (or a wide concat for
  schemaless paths). Memory records pass through `content_full`
  unchanged. Classify, summarize, observation extraction, concept
  synthesis, and contradiction detection now read through these
  helpers. The result: collection items in `curation=standard`
  collections actually get classified, get `content_short`
  generated, and participate in concept synthesis.

### Changed

- **`DefaultCuration` flipped from `CurationStandard` to
  `CurationNone`.** Collections created without a template and
  without an explicit `curation` value default to `none` (no LLM
  work). The flip preserves the *observed* user-facing behavior
  pre-activation (where `curation=standard` was theatre because
  collection items had no `content_full` to feed the LLM stages):
  bare-bones `gramaton_collection_create(name="X")` continues to
  produce no LLM work, but now does so explicitly rather than via
  inert default. Templates that want LLM-driven enrichment
  declare `curation=standard` explicitly.

- **`curation=standard` requires `content_fields` on the schema.**
  At create time, a collection with effective `curation=standard`
  must satisfy one of: a curation=standard template (which
  declares `content_fields`), or a custom schema with explicit
  `content_fields` declared. `gramaton_collection_create` rejects
  `curation=standard` on schemaless / no-content_fields requests
  with a clear error. Protects against noisy concept synthesis
  and content_short polluting cross-store search snippets when
  the LLM has nothing structured to work with.

- **`gramaton_collection_update` triggers re-embed + reclassify
  when `RecordContent` changes.** When an update mutates a field
  declared in `content_fields`, the BM25 index and vector
  embedding are refreshed to reflect current text, and the item's
  `processing_status` is flipped to `captured` so the next
  curation cycle re-runs classify and summarize (on
  `curation=standard` collections). Updates to non-`content_fields`
  fields (status enums, dates, etc.) leave the indexes and
  pipeline state untouched. Three-phase pattern: schema/content
  read under RLock, embedding outside the lock, validate + apply +
  index under the write lock.

- **Collection items inherit the collection's name + description in
  the indexed text.** Items added via `gramaton_collection_add` and
  `gramaton_collection_add_batch` now have the collection's `name`
  and `description` (when set) prepended to both the BM25 input
  and the embedding input at insert time. An item titled "Phase 5
  follow-on" in a collection named "Gramaton development" now
  surfaces for a "Gramaton" search via lexical BM25 match;
  previously only vector proximity caught it (and only when the
  collection's domain semantically overlapped). Existing items
  aren't reindexed retroactively; new items get the prefix going
  forward.

- **Three-knob curation model + Layer 1 cross-collection
  supersession filter.** Decomposes the single `curation` field into three
  orthogonal collection-level knobs:
  - `curation`: `none | standard` (LLM analysis intensity — controls
    classify, summarize, observation_extract, concept synthesis)
  - `supersession`: `none | collection | store` (unchanged)
  - `contradictions`: `on | off` (new — controls whether `contradicts`
    edges are generated from records in this collection)

  The 4-level `curation` enum (`none|minimal|standard|full`)
  collapses to `none|standard`. Legacy stored values `minimal` and
  `full` are normalized on read (`minimal` → `none`, `full` →
  `standard`) so existing collections work without a migration
  sweep. Writes reject the legacy values.

  Layer 1 supersession filter (`curation/deterministic.go`): the
  blanket "skip all collection members" guard is replaced with a
  per-pair check via the new `EffectiveCurationFor` resolver. Within-
  collection records at `supersession=collection` now consolidate
  on duplicates (the shopping-list dedup contract); cross-collection
  records at `supersession=collection` correctly do not supersede
  each other (the original cross-collection-contamination
  fix). Memory orphans (effective `supersession=store`) keep today's
  global dedup behaviour.

  Per-stage skip gates added to classify, summarize,
  observation_extract, batch classify, and contradictions. Each
  reads its mapped knob via `EffectiveCurationFor` and skips when
  the effective value excludes the stage. Idempotent-on-dup behaviour
  on `gramaton_collection_add` / `_batch` (previously gated on
  `curation=minimal`) now gates on `curation=none`.

  Multi-collection resolution rule (a record with multiple
  `member_of` edges): destructive knobs go most-restrictive
  (supersession), additive knobs go most-permissive (curation,
  contradictions). One-line principle: never irreversibly modify a
  record without unanimous agreement; always enrich when any
  collection wants enrichment.

  `gramaton_collection_add` and `_batch` now stamp `processing_status`
  on item nodes based on the collection's curation knob (standard →
  `captured`, none → `processed`). Note: items in `curation=standard`
  collections are now eligible for the autonomous pipeline pool, but
  still bounce at every stage's `content_full` guard because
  collection items don't populate `content_full`. Activation of
  collection items in the LLM pipeline is tracked separately as a
  P1 follow-up.

  New starter templates: `journal` (standard / none / off — daily
  entries / observation logs) and `references` (standard / collection /
  off — bookmarks / recipes / places / contacts / code snippets).

  `gramaton_inspect` returns `effective_curation: {curation,
  supersession, contradictions}` for record nodes (Memory records,
  collection items, session segments). Field is omitted on
  structural / container nodes (collections, sessions, topics) and
  concept-synthesis nodes.

  Docs updated: `server/guide/collections.md` (three-knob model,
  templates table, multi-collection resolution),
  `server/guide/curation.md` (stage→knob mapping table),
  `docs/integrator-guide.md` (rewrites of curation=minimal / full
  references), `README.md` (per-collection knob pointer).

- **Final pre-merge sweep for the async-jobs feature.** Three parallel review agents
  (correctness, security, test coverage) reviewed the full L1-L6
  branch and signed off as ready to merge. Mechanical fixes from
  their findings:

  - `cfg.Jobs.MaxBatchBytes` is now actually consulted by the
    envelope validator. The constant is the fallback; an operator
    lowering it in `config.yaml` to throttle abuse no longer hits a
    silent no-op.
  - Sync-mode save-failure reason changed from `chunk_1_save_failed`
    (misleading: sync doesn't chunk) to `save_failed`. Chunked
    runner's `chunk_N_save_failed` taxonomy is unchanged.
  - `WithTenant` godoc now spells out the trust-boundary contract
    so a future remote-callable mode wirer reads tenant from a
    verified identity (signed JWT, OIDC subject, authenticated
    middleware), not a wire-controlled HTTP header or JSON field.
  - Removed an orphan `finalizeChunkedRunning` doc comment that
    described an inlined helper.
  - `docs/architecture.md` now lists the `jobs/` package and
    documents the sync + async (multi-chunk) capture-batch flows
    with their lock-cycle and finalize-state diagrams.

### Added

- **Multi-chunk async runner + cross-chunk edge fixup.**
  The earlier landing shipped the async path as a single-chunk runner with edges in
  the same Save call as items. This change makes it actually chunk:

  - Items split into chunks of `MaxSyncBatchSize` (500 by default;
    overridable in tests via `SetChunkSizeForTests`). Each chunk
    acquires the engine write lock, commits its own items in a
    single Save (`capture_batch chunk N/M`), releases the lock,
    persists progress (ProcessedCount + ClientRefToID) to the
    JobStore. A status reader sees real motion as chunks land.
  - After all chunks commit, a separate edge-fixup phase resolves
    every edge against the now-fully-populated ClientRefToID map
    and saves them in one Save (`capture_batch edge fixup`).
    Cross-chunk edges (source and target in different chunks) just
    work — by the time the fixup runs, every ClientRef has an
    assigned ULID.
  - Cancel between chunks: the runner re-reads Job.Status before
    each chunk and before fixup. A cancel observed at any point
    short-circuits with `cancelled` status; previously-committed
    chunks remain in the store.
  - Per-chunk save failure: scoped rollback (only the failing
    chunk's nodes leave PropIdx + VecIdx + BM25 + SecIdx + Graph);
    Job lands in `failed/chunk_N_save_failed`; Result.Added carries
    every chunk before N.
  - Edge-fixup save failure: every node chunk stayed on disk;
    Job goes to `failed/edge_fixup_failed`; Result.EdgesFailed
    lists every edge with code `fixup_failed` so the caller can
    replay each via gramaton_link. Successfully-added edges are
    rolled back from the in-memory edge store before the failure
    response so on-disk state matches in-memory state.

  `MaxAsyncBatchSize` raised from 1000 to 10000 now that the chunker
  bounds per-chunk lock-hold; `MaxBatchBytes` (256MB) remains the
  byte-level safety net. Sync mode (`Wait=true`) is unchanged — items
  + edges still commit in one combined Save.

  New `FaultPhaseEdgeFixup` test seam: returning a non-nil error from
  Inject for that phase forces the fixup commit to fail. Tests use
  this to pin the fixup-failure recovery path.

  New `(*API).SetChunkSizeForTests(int)` test-only method overrides
  the runner's chunk size so tests can exercise multi-chunk
  behavior with 30-item batches instead of 1500-item batches.
  Production never sets it.

### Changed

- **Async-jobs review-pass B: test hardening.** Closes the test gaps the
  L3-L5 review flagged. Adds `go.uber.org/goleak` (test-only dep)
  via `api/main_test.go`'s `TestMain` so a leaked async runner
  goroutine fails the suite with a named stack instead of silently
  leaking.

  New deterministic test helpers in `api/capture_batch_review_test.go`:
  - `blockingInjector` — a FaultInjector that BLOCKS inside Inject
    until the test releases it via a per-phase channel. Tests that
    used to be race-tolerant ("if A or B passed") now park the
    runner inside Phase 3 and assert exactly the failure path
    they're testing.
  - `dedupEmbedder` — returns identical vectors for identical
    content. Lets supersession tests deterministically trigger the
    dedup path against a known seed.

  New tests:
  - `TestCaptureBatchAsyncRunnerPanicRecovery` — pins the
    `recoverAsyncPanic` path via the new `FaultPhasePanic` seam.
    Confirms the Job lands in `failed/panicked: <msg>` and that
    subsequent submits work normally.
  - `TestCaptureBatchAsyncCancelDuringRun`,
    `TestCaptureBatchResultTimeoutDeterministic`,
    `TestCaptureBatchAsyncStatusBeforeFirstChunk`,
    `TestCaptureBatchAsyncConcurrentStatusReaders` — runner-blocked
    versions of the L5 race-tolerant tests.
  - `TestCaptureBatchSupersessionDeterministic`,
    `TestCaptureBatchInternalSupersession` — pin same-batch and
    second-call supersession via the dedupEmbedder.
  - `TestCaptureBatchTenantOwnership` — cross-tenant Status / Cancel
    / Result / JobsList all surface ErrNotFound; same-tenant access
    works.
  - `TestCaptureBatchClientTokenPerTenant` — same ClientToken across
    tenants creates distinct Jobs; same-tenant retry returns the
    prior JobID idempotently.
  - `TestJobsListNoClientTokenInSummary` — the JSON wire shape no
    longer includes client_token in JobSummary.
  - `TestJobsListBoundsKind`, `TestJobsListBoundsClientToken`,
    `TestJobsListBoundsSinceFormat`, `TestJobsListOffsetCap`,
    `TestCaptureBatchResultTimeoutCap`,
    `TestCaptureBatchResultTimeoutNegative` — pin the new
    validation bounds from review-pass A.
  - `TestCaptureBatchAsyncLargerThanSyncCap` — async accepts
    MaxSyncBatchSize+50 items (was previously rejected by the
    masked sync cap).
  - `TestCanonicalEdgeWeightDefaultNormalized` — `Weight=nil` and
    `Weight=&0.5` hash identically.
  - `TestReservedNamespaceCaseBypass` — every case/whitespace
    variant of `_gramaton.` is rejected.
  - `TestSecIdxRolledBackOnSaveFailure` — pins the SecIdx purge in
    Save-failure rollback.
  - `TestJobsListBoundsSinceFormat` proves the `time.Parse` error
    sanitizer doesn't echo input verbatim.
  - HTTP wiring tests for `GET /v1/capture/batch/{id}/{status,
    cancel, result}` and `GET /v1/jobs` in
    `server/handler_capture_batch_test.go`.

  Vacuous tests removed or replaced (with reference comments
  pointing at their deterministic replacements):
  - `TestCaptureBatchSupersession` (gated on `if len(Superseded) > 0`
    which the stub embedder rarely satisfied) → replaced.
  - `TestCaptureBatchAsyncRestartRecovery` (only asserted
    `j.Kind`) → removed; recovery semantics covered in
    `core/jobstore_wiring_test.go`.
  - `TestCaptureBatchResultTimeout` (race-tolerant) → replaced.
  - `TestCaptureBatchCancelPersistFailureBothFail` and
    `RetrySucceeds` rewritten to park the runner inside chunk_save
    so cancel races a non-terminal job; the retry test asserts
    `onceInjector.fired == true`.

- **Async-jobs review-pass A: hardening + multi-tenancy foundation.** Lands
  before the multi-chunk runner to address the critical and medium findings from
  the L3-L5 review.

  Critical fix:
  - `server.Run()` and `server.Shutdown()` now call
    `api.ShutdownAsync(ctx)` after `httpServer.Shutdown` and before
    `engine.Close()`. Without this, a SIGTERM during an in-flight
    async batch tore down the bbolt handle while the runner was
    mid-Save (panic + possible mmap corruption). The CHANGELOG
    claim "Server callers ... drain runners before the engine close"
    is now true.

  Multi-tenancy foundation (single-tenant deployments unaffected):
  - `jobs.Job` gains an additive `TenantID` field (omitempty).
  - `jobs.ListFilter` gains `TenantID`; `filterMatches` always
    requires equality (empty matches empty in single-tenant).
  - `jobs.Store.FindByClientToken(token, tenant)` is scoped: same
    token across different tenants doesn't collide.
  - `api.WithTenant(ctx, id)` / `tenantFromContext(ctx)` helper plus
    `tenantOwnsJob(caller, jobTenant)`. `CaptureBatchStatus`,
    `CaptureBatchCancel`, and `CaptureBatchResult` enforce
    ownership: cross-tenant access surfaces `ErrNotFound` (not
    `ErrForbidden`) so existence isn't leaked. `JobsList` always
    filters by caller tenant.
  - `JobSummary` no longer surfaces `ClientToken`. Listing other
    tenants' tokens would let one caller guess another's
    idempotency window. Single-job lookup with an explicit JobID
    still returns ClientToken via `CaptureBatchStatus`.
  - Until real caller identity wires in, `tenantFromContext` returns
    "" everywhere, all filters degenerate to single-tenant
    behavior, and Job records carry `TenantID=""`. The data layer
    is ready for the multi-tenant switch-over without a migration.

  Mechanical fixes:
  - `JobsListRequest`: bounded `Kind` (`MaxKindLen=64`), `Since` /
    `Until` (`MaxRFC3339Len=64`), `ClientToken` (UUID-validated),
    `Offset` (`MaxJobsListOffset=100k`). `time.Parse` errors no
    longer echo caller input verbatim into `APIError.Message`.
  - `CaptureBatchResultRequest.TimeoutMS` capped at 30 minutes
    (`MaxResultTimeoutMS`); negative values rejected. HTTP route's
    `parseIntParam` cap reduced from `1<<31-1` to the constant.
  - Reserved-namespace check on meta keys now normalizes via
    `ToLower(TrimSpace(k))` so `" _GRAMATON.foo"` and similar
    bypasses are also rejected.
  - Sync vs async item-count cap reordered: the mode-specific cap
    (sync=500 / async=1000) is computed before
    `validateBatchEnvelope` runs, so an async request with 600 items
    no longer fails the sync cap before reaching async validation.
  - `EdgeSpec.Weight=nil` and `Weight=&0.5` now produce identical
    canonical bytes via `defaultedEdgeWeight`. ClientToken-based
    retries that serialize the default explicitly vs implicitly are
    no longer rejected for body mismatch.
  - Async runner's `finalizeCancelled` skips when Job is already
    cancelled; previously it overwrote the cancel handler's
    CompletedAt + FailureReason on a race.
  - Save-failure rollback now also purges `SecIdx` (BboltSecondary
    Index) so field-existence and timestamp entries don't ghost
    after a Save error.

  Time-range filter semantics: `jobs.ListFilter.CreatedAfter` and
  `CreatedBefore` are now INCLUSIVE (the previous strict-after /
  strict-before behavior didn't match the documented "lower-bound"
  and "upper-bound" wording). Existing callers using exact-second
  bounds will see one extra job per query.

  Test seam: new `FaultPhasePanic` constant. When an injector returns
  a non-nil error for that phase, `runCaptureBatchCore` panics with
  the error message — pinning the async runner's panic-recovery path
  in tests. `FaultPhaseJobstoreFailMark` (declared but unused)
  removed.

### Added

- **Async mode for `gramaton_capture_batch` + 4 companion MCP tools.**
  Setting `wait=false` returns a `job_id` immediately
  and runs the commit in a background goroutine; the new tools poll
  and control the job:

  - **`gramaton_capture_batch_status(job_id)`** — read-only Job
    snapshot (status, total_items, processed_count, errors[],
    client_ref_to_id map, client_token, failure_reason). Polling
    is cheap and never touches the engine write lock.
  - **`gramaton_capture_batch_cancel(job_id)`** — flips the Job to
    `cancelled` atomically (state-machine guarded; cancelling a
    terminal job is an idempotent no-op). One automatic retry on
    transient JobStore.Update failure to tolerate brief bbolt
    contention. Signals the runner's context so an in-flight embed
    exits cleanly.
  - **`gramaton_capture_batch_result(job_id, timeout_ms)`** — blocks
    with poll backoff until the Job reaches a terminal state, then
    returns the full CaptureBatchResponse (added/failed/edges/
    edges_failed/stats). On timeout: returns the current snapshot
    with a `timeout` error code (HTTP 504). Default timeout from
    `cfg.Jobs.ResultDefaultTimeout` (30 minutes).
  - **`gramaton_jobs_list`** — enumerate persisted jobs by status /
    kind / client_token / time range, paginated (default limit 50,
    max 200). Returns lightweight summaries; heavy Result payload
    requires `gramaton_capture_batch_status` on a specific id.

  Async runner: panic-recovers (marks the Job
  `failed/panicked: <message>` so a panic doesn't leave a stuck
  running job). Honors a cancel that arrives before the runner
  schedules (`cancelled_before_start`), races to advance pending →
  running atomically (CAS-style; ErrInvalidTransition means a cancel
  won and the runner exits cleanly), and propagates context to the
  embedder so cancellation during embed is observed.

  Single-chunk runner: the entire batch commits in one Save call
  inside the goroutine. Multi-chunk processing + cross-chunk edge
  fixup land separately. The async path unblocks larger batches
  (`MaxAsyncBatchSize` = 1000) and removes the "HTTP timeout vs
  server completion" race for clients on slow networks; lock-hold
  semantics are unchanged from sync.

  New `(*API).ShutdownAsync(ctx)` cancels in-flight runners and
  waits for them to exit so the engine can close cleanly. Server
  callers (and tests with `t.Cleanup`) drain runners before the
  engine close.

  HTTP routes added: `GET /v1/capture/batch/{job_id}/status`,
  `POST /v1/capture/batch/{job_id}/cancel`,
  `GET /v1/capture/batch/{job_id}/result`, `GET /v1/jobs`.

- **Intra-batch edges in `gramaton_capture_batch`.**
  `Edges []EdgeSpec` in the request body lets the caller create
  intra-batch and to-existing-record edges in the same commit as
  the items they connect. Each EdgeSpec resolves source and
  target via either an existing record id (`source_id`,
  `target_id`) or a within-batch label (`source_client_ref`,
  `target_client_ref`); exactly one of each pair must be set per
  endpoint. Forward and backward references resolve correctly
  regardless of item position. Edges share the items' single
  Save call (one ActionLink CommitAction per edge); save-failure
  rollback removes the in-memory edges before the node rollback.

  Edges are capped at `len(Items) * MaxBatchEdgeMultiplier` (10x);
  exceeding this is an envelope-level ErrInvalid. Per-edge
  failures land in `edges_failed[]` with one of these codes:
  `source_item_failed` / `target_item_failed` (referenced item
  failed Phase 0 validation); `source_id_not_found` /
  `target_id_not_found` (id or ref doesn't resolve); `self_loop`
  (source and target resolve to the same node); `duplicate_edge`
  (same source/target/type tuple already in the batch);
  `invalid_type` (empty or over-length); `invalid_weight`
  (NaN/Inf or out of [0,1]); `missing_endpoint` (neither id nor
  ref supplied, or both supplied for the same endpoint).
  `weight=0.0` is a valid edge weight; the canonical hash
  includes Edges so a different edge set rejects ClientToken
  reuse.

- **`gramaton_capture_batch` MCP tool + `POST /v1/capture/batch`
  HTTP route + CLI proxy.** Sync mode: stores up to
  500 records in a single call sharing one engine write lock and
  one batch-embed call. Each item follows the `gramaton_capture`
  shape with an optional `client_ref` echoed back in the response;
  per-item validation failures land in `failed[]` while the rest
  commit. Phase 0 enforces a 256MB total content-byte budget
  (`MaxBatchBytes`) and rejects `_gramaton.*` reserved meta keys.
  `client_token` (UUID) provides cross-call idempotency: the same
  body returns the prior `job_id`; a different body with the same
  token is rejected with `conflict`. `skip_supersession=true`
  disables auto-dedup for migration imports. Records are stamped
  with `meta._gramaton.import.job_id` so a caller can recover
  orphaned records via search even if the response is lost.
  Engine-first / JobStore-second commit ordering: a Save failure
  rolls back the in-memory PropIdx + VecIdx + BM25 + Graph entries
  for the batch's nodes (no ghost-vector window) and marks the
  Job `failed/chunk_1_save_failed`; a JobStore.Update failure
  after Save logs loudly and leaves the records on disk for
  caller-side recovery. Async mode (`wait=false`) is reserved for
  a later landing and currently rejected.

  New `api.FaultInjector` + `SetFaultInjector` test seam exposes
  named phases (`chunk_save`, `jobstore_update`,
  `jobstore_fail_mark`) so tests can exercise rare error paths
  without disturbing real storage.

- **`core.Engine` opens the `jobs.Store` and runs restart recovery
  on init.** `(*Engine).JobStore()` exposes the live
  handle. In-flight jobs (status pending/running) from a prior run
  are flipped to `failed/server_restart` BEFORE engine init returns,
  so the HTTP listener can never observe stale running jobs after
  a crash + restart. A background TTL-based GC sweeper goroutine
  runs at `cfg.Jobs.SweepInterval` (default 1h); `Engine.Close()`
  cancels it cleanly via context and waits for clean shutdown.
  Disable the sweeper by setting `SweepInterval=0`.

  New `JobsConfig` block in `config.yaml`:
  ```yaml
  jobs:
    retention:
      completed: 90d
      failed:    365d
      cancelled: 90d
    sweep_interval:         1h
    result_default_timeout: 30m
    max_async_batch_size:   1000
    max_batch_bytes:        268435456  # 256MB
  ```
  Zero values use defaults via the `0 = use default` convention.

  Backup integration: `backup.Snapshot` gains an optional
  `JobsDB *bolt.DB` field. When populated (api/ backup callsite
  passes `engine.JobStore().DB()`), the walker takes a
  bbolt-native coherent snapshot via `View+WriteTo` to avoid
  torn-page reads under concurrent writes. The `shouldExcludeSnapshot`
  list still excludes `indexes.db` (derived state) but now
  INCLUDES `jobs.db`. Restore picks up the included file
  automatically.

- **New `jobs/` package: persistent tracking for long-running async
  operations** (prereq for `gramaton_capture_batch`).
  Bbolt-backed store in a dedicated `jobs.db` file separate from
  the engine's index store and graph commit log. Search isolation
  by construction: jobs are not graph nodes, so curation and search
  paths never see them.

  Surface:
  - `jobs.New(path) (*Store, error)` opens or creates `jobs.db`.
  - `Job` struct with FormatVersion, Kind (capture_batch /
    f4_import / future), Status, ClientToken + RequestHash for
    idempotency, ClientRefToID for cross-chunk edge resolution,
    Result (json.RawMessage), Errors slice, FailureReason.
  - State machine with explicit transition whitelist:
    pending→{running, cancelled, failed}, running→{completed,
    failed, cancelled}. Terminal states have no outgoing edges.
  - `Update(j)` validates transition atomically inside a single
    bbolt write tx; rejects invalid with `ErrInvalidTransition`.
    Same-status updates (e.g., bumping ProcessedCount) bypass
    the whitelist.
  - `AdvanceStatus(id, newStatus, mutator)` is the CAS-style
    helper for runners: read + validate + mutator-applied + write,
    all in one tx.
  - `Create`, `Get`, `Delete`, `FindByClientToken` (linear scan,
    returns most recent on token collision), `ListInFlight`
    (pending+running for restart recovery), `List(filter)` →
    `JobSummary` (lightweight projection without Result).
  - `RunGC(now, RetentionPolicy{Completed, Failed, Cancelled})`
    deletes terminal jobs older than configured TTL; in-flight
    jobs never GC'd; zero retention duration means "keep
    forever" for that status.
  - Schema-evolution policy: per-Get migration on stale
    FormatVersion. v1 is current; legacy v0 records (no
    FormatVersion field) are migrated and persisted on first Get
    so subsequent Gets are no-ops. Future versions chain
    migrations in `migrate()`.

  Tests cover: roundtrip with all fields populated; valid +
  invalid transitions table-driven (every whitelist edge);
  AdvanceStatus race with barrier-channel + count=20; reopen
  after Close; corrupted JSON returns clean error not panic;
  legacy v0 schema migration is idempotent; future format
  version returns `ErrFutureFormatVersion`; List filter table
  (status / kind / token / time-range / pagination); GC respects
  TTL and skips in-flight; Supersedes chain walks; concurrent
  readers + writer.

  ~1634 LOC (675 impl + 959 test). Standalone-mergeable; engine
  wiring lands in a follow-up.

### Changed

- **BERT embedder: `Model.Forward` now takes a `*Scratch` parameter
  instead of holding scratch buffers on the Model itself.** Refactor
  with no behavior change — the previous shared scratch field is
  replaced first by a per-Provider Scratch and then (Layer B) by a
  `sync.Pool` of Scratches reused across Embed calls. Sets up the
  structure for upcoming concurrency layers (RWMutex on the
  Provider, parallel inner-loop fanout in Embed). New: exported
  `Scratch` type and `NewScratch(maxSeq, cfg)` constructor. Audited:
  every buffer location in Forward is written before being read (so
  Scratch reuse is safe without zeroing); Model has zero mutations
  during Forward (so concurrent Forward against the same Model with
  disjoint Scratches is safe); Tokenizer.Encode is read-only on
  Tokenizer fields. New tests: `TestForwardScratchReuseSafety`
  deliberately fills a recycled Scratch with NaN and verifies
  Forward output matches a fresh-Scratch run byte-for-byte;
  `TestEmbedScratchReuse` verifies the pool reuses Scratch across
  sequential calls; `TestEmbedConcurrentScratchDistinct` verifies
  Get/Put accounting under barrier-released goroutines. Layer C
  flips the Provider mutex to `sync.RWMutex` so multiple Embed
  goroutines can run concurrently against shared read-only Model
  with their own pooled Scratches; Close still takes write Lock
  and blocks until in-flight Embeds release. New tests:
  `TestEmbedConcurrentDeterminism` (8 goroutines, byte-identical
  to sequential reference), `TestEmbedConcurrentVariableLengths`
  (variable-length inputs across goroutines), `TestEmbedConcurrentClose`
  (Close blocks for in-flight Embed; subsequent Embed returns
  closed-error), `TestEmbedConcurrentMixed` (no panic, no
  garbage). Layer D adds **inner-loop fanout in Embed**: a single
  `Embed(ctx, []string{N strings})` call now runs Encode + Forward
  in parallel goroutines via `golang.org/x/sync/errgroup`, bounded
  by the new `embedding.max_workers` config knob (default
  `min(GOMAXPROCS, 8)`). Single-text Embed takes a fast path that
  avoids goroutine overhead. Tests:
  `TestEmbedSingleCallDeterminism` (output matches per-text
  sequential reference), `TestEmbedSingleCallCtxCancel` (cancelled
  ctx returns immediately, no goroutine leak),
  `TestEmbedMaxWorkersBound` (configured cap honored), 
  `TestEmbedSingleCallSingleItem` (fast path correctness). All
  race-detector clean at `-count=20`. Layer E adds benchmarks
  (`BenchmarkEmbedSequential`, `BenchmarkEmbedSingleCallSize`,
  `BenchmarkEmbedMultiCaller`, `BenchmarkEmbedScratchPoolPressure`)
  with measured numbers on Apple M3: N=500 single Embed in 5.5s
  (well under F1's 15s target), 5.6x speedup at N=64 vs sequential.
  Layer F adds **speedup gates** —
  `TestEmbedSpeedupGateSingleCall4` (≥1.8x at 4 workers),
  `TestEmbedSpeedupGateSingleCall8` (≥2.5x at 8 workers),
  `TestEmbedSpeedupGateMultiCaller4` (≥1.8x), and
  `TestEmbedSpeedupGateMultiCaller8` (≥2.5x). Skip cleanly on
  underprovisioned hardware (NumCPU < threshold). Measured on
  Apple M3: 3.20x / 4.90x / 3.31x / 4.68x — all comfortably
  above the conservative gates. Layers A-F of bert-parallel-embed
  COMPLETE; design at
  `~/workspaces/gramaton-inspection/bert-parallel-build.md`.
  New runtime dep: `golang.org/x/sync/errgroup`.

- **api/ Collection methods + `SessionCommit` now return typed
  response structs instead of `map[string]any`.** Eleven Collection*
  methods (`CollectionCreate`, `CollectionList`, `CollectionItems`,
  `CollectionAdd`, `CollectionRemove`, `CollectionUpdate`,
  `CollectionMove`, `CollectionRename`, `CollectionDelete`,
  `CollectionSchemaRead`, `CollectionSchemaUpdate`,
  `CollectionMigrate`) plus `SessionCommit` now return concrete types
  defined in `api/collection_responses.go`. Wire-format unchanged --
  `omitempty` plus pointer types preserve byte-identical JSON output
  for HTTP and MCP. The error contract is preserved: non-nil
  `APIError` returns the zero value of the response struct (no
  partial-state leakage). `SessionCommit` and `CollectionMigrate`
  responses now carry an optional `failed []ItemFailure` slice for
  future partial-success surfacing; today both ops still abort on
  first error per the APIError contract, so the slice is reserved.
  Touches `api/collections.go`, `api/sessions.go`, the new
  `api/collection_responses.go`, and the test files that switched
  from map indexing to struct field access.

- **Every Save site now emits structured
  `graph.CommitAction` descriptors, enforced by a CI lint.** Three
  layers:

  - 1a (`42bbef4`): all CommitAction kinds extracted as named
    constants in `graph/commit.go` (~25 of them across user-driven
    api/ verbs and curation cycle verbs). Existing api/ inline
    string literals refactored to reference the constants. No
    behavior change -- every kind value is byte-identical to the
    prior literal so existing commits parse unchanged.
  - 1b (`d477c3b`): the 8 Save sites that previously emitted no
    action at all (api branch merge / session_create+commit+archive
    / reembed, core repair, server revert+ingest, backup CSV import)
    now carry the appropriate Action* constant. `gramaton_log`
    filtering by these kinds works for the first time.
  - 1c (`ae7aa08`): every curation pass emits per-record
    CommitActions. 13 curation kinds total: `curation:classify`,
    `curation:summary`, `curation:link`, `curation:supersede`,
    `curation:contradiction_check`, `curation:concept_emerge`,
    `curation:concept_enrich`, `curation:section_link`,
    `curation:observation_extract`, `curation:lifecycle`,
    `curation:quality_repair`, `curation:gc`, `curation:self_heal`.
    Multi-record events (linking, supersession, contradiction
    pairs, section linking, concept emergence) emit one action per
    affected record so `gramaton_log(record_id=X)` finds the
    commit regardless of which side X was on. New tests in
    `curation/actions_test.go` pin the kinds to canonical strings
    + verify classify and summarize emit the expected actions.
  - 2: new `tools/lint/saveactions/` AST tool enforces the rule for
    new Save sites. Walks every non-test Go file, fails on
    `Save(...)` / `SaveOrLog(...)` call sites lacking a
    CommitAction. Pragma exemption `//gramaton:saveactions:exempt`
    immediately above the call. Skips `*_test.go` files and
    `testutil/` directories. Wired into the `pre-merge-check`
    skill as step 9.

  Two in-flight design questions resolved (Q1 fine-grained
  colon-separated taxonomy, Q2 emit one action per affected
  record, Q3 pragma + skip-tests + hard-error).

- **New top-level `HOW_TO_USE_GRAMATON.md`** for end users driving
  Gramaton through Claude or another agent. Practical tips on
  capture, search, backlog management, common pitfalls, and how to
  verify things landed. All examples synthetic. Calls out the
  load-bearing fact that session prepare/commit does NOT close
  collection items — closure is an explicit `gramaton_resolve` call,
  with the inferred-from-conversation alternative rejected to avoid
  silent state loss on partial work. README's docs index now
  surfaces it as the first row.
- **`docs/integrator-guide.md` gains parallel session-≠-closure notes**
  in both the Sessions and Collections sections so the agent-author
  audience hits the rule from either entry point. Includes the
  recommended close-then-prepare ordering and a pointer at the
  `auto_close_collection_status` heuristic from `df2ab44` for
  callers that want the schema status field flipped automatically.

- **Pin the rerank/decompose model-flow with end-to-end tests + doc the
  provider asymmetry.** Closes the gap from the post-Layer-1 review:
  prior tests asserted (a) `cfg.ModelForTask(...)` returned the right
  string and (b) `DecomposeQuery` invoked `CompleteWithModel`, but no
  test pinned that the resolved model string actually flowed through
  to the LLM. New `search/rerank_test.go` adds a `modelRecordingReranker`
  and two tests covering default-tier resolution and tier-override
  pickup; `search/decompose_test.go` gains
  `TestDecomposeQueryThreadsModelArgToLLM` with the equivalent
  recording mock. Also adds a "Provider asymmetry" section under
  `docs/configuration.md` LLM block noting that OpenAI and Bedrock
  ignore the per-call model override and use their construction-time
  model — meaning per-task tier dials are anthropic-only in practice.

- **Doc cross-refs updated for the new `llm:` schema.**
  `docs/configuration.md` rewrites the LLM and curation sections to
  match the new shape; `docs/benchmarks.md` updates the
  benchmark-store contradiction-disable example;
  `docs/project-design/data-model.md` retargets the four
  `*_attempts` retry-cap references; `docs/project-design/design-decisions.md`
  updates D36 (tiered LLM models) and D39 (cost caps) for the new
  field paths and adds a one-paragraph note recording that the
  consolidation kept behavior identical.

- **`config.Save()` now emits section banners + per-field comments**
  in the rendered `config.yaml` for the `llm:` block. The renderer
  walks an encoded `yaml.Node` tree and attaches `HeadComment`s from
  a path-keyed registry (`config/comments.go`); the `tasks:` map is
  re-sorted to the canonical `LLMTask` order so save output is
  deterministic across runs. Operators opening
  `~/.gramaton/config.yaml` get inline guidance for every dial under
  `llm:` -- including a CLI-shim TOS warning on `provider:`, a
  pointer to provider model-catalog URLs above the `llm:` block, and
  a "do not edit" warning above `llm.curation:`. Tests:
  `config/render_test.go`. Other top-level sections render without
  comments today; future passes can extend the registry.
- `Defaults()` now pre-populates `llm.models.tasks` with the
  canonical task -> tier mapping so the rendered file shows the
  affordance explicitly. Removing a key still falls back to
  `defaultEffortForTask`; the in-code default and the `Defaults()`
  map mirror each other and must be updated together.

- **LLM configuration consolidated under a single `llm:` block.** The
  prior top-level `llm_curation:` block and the `search.rerank_*`
  fields are folded into `llm:`. New shape:
  - `llm.rerank.{enabled,candidates}` (was `search.rerank_enabled` /
    `search.rerank_candidates`).
  - `llm.models.{low,medium,high}` plus `llm.models.tasks` (a
    map<task name, tier>) replaces the per-task `*_effort` named
    fields under `llm_curation`. The existing six task names stay
    (`classification_short`, `classification_long`, `summarization`,
    `contradiction`, `concept`, `manifest`) and two new ones land:
    `rerank` and `decompose`.
  - `llm.cost_limits.*` collects the caps that previously sat on
    `LLMConfig` directly (`max_calls_per_day`, `max_calls_per_session`,
    `max_cost_usd_per_day`, `rate_limit_interval`,
    `max_response_tokens`) plus the relocated
    `llm.cost_limits.max_cost_usd_per_run` (was
    `llm_curation.max_cost_usd_per_run`).
  - `llm.curation.*` carries the autonomous-cycle tuning knobs that
    used to live at `llm_curation.*`. Sub-grouped:
    `llm.curation.contradiction.*`, `llm.curation.concept.*`,
    `llm.curation.retries.*`.
  - `search/rerank.go` and `search/decompose.go` now resolve their
    model via `cfg.ModelForTask(TaskRerank)` /
    `cfg.ModelForTask(TaskDecompose)` and call `CompleteWithModel`
    instead of the bare provider default.
  - `CurationTask` is preserved as a deprecated alias for `LLMTask`.

### Removed

- **`llm.model`.** Every code path now resolves its model via the
  task tier system. Provider clients fall back to `llm.models.medium`
  for `Complete()` calls without an explicit model. Wizard paths
  (OpenAI, Bedrock) populate the full Models triple instead of a
  single fallback string.

### Fixed

- **Self-heal cascade clears the stale `repair_needed_llm` flag on
  Tier-1/2/3 outcome paths.** Pre-fix, only Tier-4 wrote
  `repair_needed_llm`; if a record was Tier-4 flagged on cycle N,
  then content_short was rewritten between cycles (manual edit,
  external repair, supersession) and cycle N+1 hit Tier-1 clean,
  Tier-2 strip, or Tier-3 fallback, the flag was left set
  alongside the new `repair_method` value -- producing
  contradictory state ("repair_method=stripped + repair_needed_llm=
  true"). Cosmetic today (no code currently filters on
  `repair_needed_llm`), but the LLM-escalation pass that pass
  reads it would pick up records that no longer need repair,
  wasting LLM budget. The new `clearStaleRepairFlag` helper is
  called from each successful-repair / now-clean outcome path and
  only writes when the flag is currently set (no spurious writes
  on records that were never flagged). Also clears
  `repair_input_hash` so audit reads stay coherent. Regression
  tests at
  `curation/self_heal_test.go:TestDetectAndRepairSummaryClearsStaleFlagOn{Tier1Clean,Tier2Stripped,Tier3Fallback}`
  plus a no-op-when-not-flagged test that pins the commit chain
  doesn't advance on clean+unflagged records.

### Added

- **Bedrock clients classify and surface `AccessDeniedException` and
  `ResourceNotFoundException` with actionable hints.** Pre-fix, a
  first Bedrock call against a non-enabled model returned the raw
  SDK `AccessDeniedException` and the user had to know that the
  *most common* cause is "model access not granted in the Bedrock
  console." Now both `llm/bedrock` and `embed/bedrock` wrap the SDK
  error with a hint pointing at
  `https://docs.aws.amazon.com/bedrock/latest/userguide/model-access-modify.html`
  and call out both possible causes (IAM permission missing OR
  model access not granted). `ResourceNotFoundException` similarly
  gets a region-availability + model-id hint. The original SDK
  error survives `errors.As`/`errors.Is` so retry and telemetry
  paths that key on the type keep working. Per-model dedup map
  prevents a tight curation loop on a non-enabled model from
  flooding the log. Tests cover the wrap, the dedup, and the
  pass-through-other-errors path.

- **`docs/providers.md` documents the five AWS auth patterns +
  the static-keys-rewrite gotcha.** New "AWS auth patterns"
  section covers SSO, role_arn-in-profile, `credential_process`
  (recommended for non-SSO credential managers), EC2/ECS instance
  roles, and static IAM keys. Calls out the long-running-daemon
  failure mode where another tool rewrites `~/.aws/credentials`
  in place and the SDK doesn't see the update — with two clean
  workarounds (wrap as `credential_process`, or switch to SSO).
  Notes that `gramaton init` verifies credentials via
  `sts:GetCallerIdentity` and surfaces the model-access docs link
  on the first AccessDeniedException.

### Changed

- **Setup wizard now verifies and displays the resolved AWS
  identity for the Bedrock branch.** After collecting profile +
  region, the wizard calls `sts:GetCallerIdentity` and shows the
  user the account, principal ARN, and region Gramaton will use.
  On failure (expired SSO session, missing profile, region
  mismatch, etc.) the wizard surfaces the SDK error, names the
  three most common fixes (`aws sso login`, `~/.aws/config`
  inspection, region check), and offers a retry. Catches
  credential issues at config-write time instead of letting them
  fail silently in the first curation cycle. Verification is
  injected through a `Wizard.awsVerifier` field; production wires
  the real `verifyAWSProfile` impl, tests use a stub returning a
  synthetic identity. Partial — model-access-error detection +
  docs are follow-ups.

### Added

- **AWS SDK observability through slog.** `internal/awscfg.Load`
  now wires the SDK's smithy logger into slog (Debug for
  retries / signing / credential refresh, Warn for SDK warnings),
  enables `aws.LogRetries | aws.LogDeprecatedUsage` in client log
  mode, and emits a Debug line with the resolved config knobs
  (region, whether a profile was named, whether explicit env-var
  creds resolved) -- never the credential values themselves.
  `llm/bedrock` and `embed/bedrock` log Info on client
  construction with `(region, profile_set, model[, family])` so
  the daemon log carries an audit trail of which AWS principal
  surface each process is using. Wizard's verification path also
  logs Info on success and Warn on failure.

- **`gramaton_resolve` now auto-flips the collection-item `status`
  field when the schema has one.** Previously, closing a collection
  item (a backlog ticket, a todo, a reading-list entry) required
  two tool calls -- `gramaton_collection_update(status=closed)` AND
  `gramaton_resolve(resolution=completed)` -- because the record
  layer (resolution + valid_until) and the collection-item layer
  (schema-defined `status` enum) had no automatic bridge. Easy to
  forget the second call; stale `status:open` items lingered.
  Now `Resolve` walks every `member_of` edge, looks up each
  collection's schema, finds an enum field named `status`
  (case-insensitive), and writes a closed-equivalent value mapped
  from the resolution verb (`completed`/`superseded` →
  `[completed, done, finished, resolved, closed]`;
  `abandoned`/`obsolete` → `[abandoned, cancelled, canceled, dropped]`;
  first match in the schema's enum wins). Schemas without an enum
  `status` field fall through to a refreshed `CollectionWarning`
  naming only the unflipped collections. Response gains an
  `auto_closed_status` map (`collection_name → applied value`) so
  callers see what got flipped. New `auto_close_collection_status`
  request flag (default `true`; set `false` to skip the
  collection-layer write while still expiring the record). 11
  regression tests at `api/resolve_test.go` cover all 5 shipped
  templates plus the opt-out, multi-collection, and unit cases.

- **Self-heal cascade short-circuits when a record was already
  Tier-4 flagged against the same `content_short`.** Pre-fix,
  every server boot re-walked all flagged records through the
  sanitize+sentence-split cascade and re-wrote `repair_needed_llm`,
  `repaired_at`, `repair_method` even though no tier could salvage
  the record (the input was unchanged). The cascade now stores a
  16-char FNV64 hash of `content_short` at flag time
  (`repair_input_hash`) and returns `outcomeClean` early on
  re-entry when the flag is set and the hash matches. Cost saving
  is bounded by Tier-4 record count × 3 SetProp/boot. Regression
  test at
  `curation/self_heal_test.go:TestDetectAndRepairSummarySkipsRedundantFlag`.

- **`Engine.FlushAccess` dedups consecutive Save-failure logs.**
  Pre-fix, a stuck disk would emit one Error log + full err
  detail every 30s indefinitely under the access-flusher tick.
  Now the first failure logs at Warn, failures 2-9 (and 11-19,
  etc.) drop to Debug, and every 10th consecutive failure escalates
  to Error with the running count. A subsequent successful flush
  emits a one-line "save recovered" Info and resets the counter.
  Counter is per-engine (not per-process); guarded by the existing
  engine write lock that FlushAccess already holds. Regression test
  at `core/engine_test.go:TestFlushAccessResetsFailureCounterOnSuccess`.

- **`securityHeaders` panic-recover dedups repeated stack dumps.**
  Pre-fix, a buggy client retrying a panic-trigger flooded the
  log with kilobyte stack dumps at Warn on every request. Now the
  recover defer fingerprints (FNV64 of panic value + first
  non-runtime stack frame) and suppresses identical fingerprints
  within a 1-minute TTL, dropping duplicates to Debug. The
  fingerprint cache is bounded at 1024 entries with oldest-evict
  on overflow to bound memory under a flood of unique panics.
  Regression tests at
  `server/middleware_test.go:TestSecurityHeadersDedupsRepeatedPanics`
  + unit tests on the dedup struct.

- **Manifest qualitative_summary no longer cites record counts.**
  The LLM-generated `manifest_summary` text occasionally cited
  numbers ("...with 285 conceptual records...") that didn't match
  any field in the surrounding response (the actual counts on the
  same store were 38 and 416 under different scopes). Most plausibly
  inferred or paraphrased rather than copied. Updated the system
  prompt to instruct: describe shape qualitatively, do not include
  record counts, percentages, or numeric values; numeric stats are
  surfaced separately.

- **`StatsResponse` and `EnhancedStatus` now document the
  conceptual-count and pending-vs-candidates distinctions inline.**
  `gramaton_stats.knowledge_type.conceptual` counts every record
  with that type, including derived concept nodes; the curation
  manifest's `records_by_type.conceptual` counts only user-captured
  records. Both are correct under their own semantics; the docstring
  explains the difference. Likewise `pending_count` (work to do --
  records awaiting classification) and `concept_candidates`
  (telemetry signal -- keywords above emergence threshold) answer
  different questions and the docstring on `EnhancedStatus` calls
  that out. Tool descriptions for `gramaton_stats` and
  `gramaton_curation` updated correspondingly.

### Fixed

- **Observations no longer inflate concept emergence clusters and
  evidence_count.**
  Observations inherit `content_keywords` verbatim from their
  parent (`curation/observe.go`), so
  `LookupKeyword("content_keywords", kw)` returned both the parent
  record and each observation child. Two follow-on effects:
  emergence cluster size double-counted siblings (a parent with N
  observations registered as 1+N members), and `enrichConcepts`
  counted every inbound non-structural edge so observation->concept
  `instance_of` edges added by the same emergence pass also counted
  toward `evidence_count`. The audit on this store flagged
  concepts with evidence_count = parents + observations rather than
  parents alone. Now the
  emergence candidate filter and the evidence_count audit both
  skip nodes with `node_type=="observation"`. Existing observation
  instance_of edges become unused (still in the graph, but
  excluded from the count); the next `enrichConcepts` cycle
  recomputes the corrected count. Regression tests at
  `curation/curation_test.go:TestEmergeFiltersObservations` and
  `curation/enrich_concepts_test.go:TestEnrichConceptsExcludesObservationSources`.

- **Observations are no longer treated as orphans by curation.**
  Observation nodes carry an `observation_of` edge to their parent,
  but observation_of is filtered out of `SemanticEdgeCount` as
  structural — so observations look 0-semantic-edge. Pre-fix, every
  curation cycle added them to `orphanIDs` and the orphan-linking
  pass added weak `related_to` edges from observations to similar
  real records (and observations also showed up as link _targets_ for
  real-record orphans). Both directions were inferred-edge pollution.
  Now observations are skipped from the orphan candidate set in
  `RunDeterministic` and excluded as link targets in the orphan
  similarity search. `Manifest.OrphanCount` no longer over-counts.
  Regression test at
  `curation/runner_test.go:TestOrphanLinkerSkipsObservations`.

- **Concepts are now embedded inline during LLM synthesis instead of
  waiting for `gramaton reembed` to catch up.**
  `enrichConceptSyntheses` previously wrote `content_full`,
  `content_short`, and `synthesis_status` but left the concept
  vectorless — concepts only got embeddings when the operator ran
  `gramaton reembed` (which has no autonomous trigger). Concept-match
  telemetry and any concept-based PRF were silently blind for any
  concept the reembed pipeline had not yet processed; on this store,
  378 concepts existed without embeddings before today's manual
  reembed sweep. The synthesis flow now batches each successful
  synthesis through the embedder outside the engine lock (three-phase
  pattern, mirroring observation extraction), then registers the
  vector + `embedding_model` under the engine write lock and adds the
  concept to the vec index. Embed failure is logged-and-skipped so
  reembed remains the back-fill safety net. Regression test at
  `curation/enrich_concepts_test.go:TestEnrichConceptSynthesesEmbedsConcept`.

- **`propIdx` now always rebuilds from graph at engine startup.**
  The previous gate (`propLoaded := s.propIdx.Count() > 0` in
  `core/indexes.go`) skipped rebuild whenever the index had any
  data — but when the bbolt-backed index was partial (some keys
  present from earlier indexing, others missing for newer-keyed
  records), that short-circuit left the missing keys unindexed
  forever. Concrete impact: `gramaton_search(missing=["confidence"])`
  silently dropped records that had confidence set; the new
  Phase A-minimal `ExcludeConcepts` filter passed concepts through
  on the first deploy because `propIdx.Lookup("node_type", "concept")`
  returned empty despite 378 concepts in the graph (workaround was
  commit 91a6f9a's direct graph walk; this commit is the underlying
  fix). `propIdx.AddTx` is idempotent via the dedup in `addToIDSet`,
  so re-walking is cheap when the index is already complete.
  Regression test at `core/engine_test.go:TestRebuildPrimaryIfMissingAlwaysRebuildsProp`.

- **`ExcludeConcepts` filter now reads `node_type` directly off graph
  nodes instead of going through `propIdx.Lookup`.** The property
  index has a load-path issue where rebuild is skipped when
  `propIdx.Count() > 0` (`core/indexes.go:275`), leaving older nodes'
  properties unindexed. Production exhibited this: 378 concept nodes
  in the graph but `propIdx.Lookup("node_type", "concept")` returned
  zero, so the Phase A-minimal filter passed concepts through. Direct
  graph walk is O(N) per query but reliable; ~1ms at current N=1369.
  Caught when verifying the new binary against a real query that
  should have matched the TZ-fragile concept cluster — concepts were
  appearing in `top_k` despite the new filter. Tests pass with both
  the old and new approaches; production fix bypasses the index
  partial-load bug. The underlying property-index load logic is its
  own bug (filed separately if it bites again).

### Added

- **Concept-match telemetry: structured `concept_match` slog events
  on every search where a concept embedding scores above threshold.**
  New `search.ScanConceptMatches` walks all `node_type=concept` nodes,
  computes cosine against the query embedding, and returns matches
  with their live member IDs (inbound `instance_of` edges, filtered
  for non-historical source nodes). Wired into `api/search.go` after
  search returns; emits via `a.log.Info("concept_match", ...)` with
  fields: `query`, `top_k`, `matches` (each match: id, keyword,
  cosine, live_members). New `TelemetryConfig.ConceptMatchEnabled`
  (default true) and `ConceptMatchThreshold` (default 0.7) gate the
  emission. No behavior change beyond observability — concepts are
  excluded from results (Phase A-minimal); the telemetry simply
  records what would have been there. Used to gather data on whether
  concept-based query expansion (PRF) would actually help on real
  queries before committing to ship it. Cost is bounded (~1ms per
  query at <100 concepts) and events only fire on matches. Tests at
  `search/concept_telemetry_test.go` pin threshold gating, live-vs-
  historical member filtering, dimension mismatch fail-silent.

- **Default search now excludes concept nodes; opt-in via
  `include_concepts=true`.** New `SearchRequest.IncludeConcepts` field
  (api/search.go) and matching `--include-concepts` CLI flag flip the
  new `Query.ExcludeConcepts` filter in `search.Tool.filterCandidates`.
  Concept nodes are LLM-synthesized cross-record summaries; pre-fix
  they competed with their own member records for top-N slots, with
  no special boost or penalty. Sample data showed duplicate-cluster
  concepts split access uniformly (5 concepts at access=5 each =
  cluster surfacing redundantly), diluting retrieval quality.
  `gramaton_explore` and `gramaton_inspect` are unchanged — concepts
  remain walkable and inspectable when their IDs are known. Tests at
  `search/search_test.go` (`TestSearchExcludeConcepts`) pin both
  default-exclude and opt-in-include behavior.

- **Member-set overlap gate at concept emergence.** New
  `concepts.member_overlap_threshold` config (default 0.6,
  `ConceptsConfig.MemberOverlapThreshold`) suppresses duplicate concept
  emergence when a candidate keyword's member set Jaccard-overlaps an
  existing or peer-pending concept above the threshold. Each member
  record carries 5-6 `content_keywords`; pre-fix, several keywords that
  happened to cluster on the same evidence set each spawned their own
  concept — sample data showed at least 12 burst clusters in a 2-week
  window producing 40+ near-duplicate concepts (e.g. `TZ-fragile tests`
  / `parseDateArg UTC midnight` / `test timezone bugs` were three
  separate concepts about the same bug). Post-fix, peer keywords are
  folded into a single concept's `content_keywords` as aliases. New
  `DeterministicResult.ConceptsAliased` counter and a `concept alias
  added` log line surface the merge. Setting the threshold to 0 reverts
  to legacy behavior. Lives in `curation/deterministic.go`; tests at
  `curation/emerge_concepts_test.go`.

- **`gramaton_search` and `gramaton search` CLI gain a
  `processing_status` filter.** New `SearchRequest.ProcessingStatus`
  field (api/search.go) and matching `--processing-status` CLI flag.
  Wired into `search.Query` + the existing `enumFilter` machinery in
  `search.Tool.filterCandidates`, so the property index does the
  lookup and `!value` negation works the same as the other enum
  filters. Surfaced specifically by the operator-triage flow for
  records marked `processing_status="stuck"` (records that exhausted
  classify retries — see commit 1b16b80). Test coverage:
  `TestSearchFilterByProcessingStatus` in `search/search_test.go`
  pins both exact match and `!stuck` negation. Pre-fix the docs
  advertised this filter but it didn't exist; caught by the
  semantic-drift lens of gramaton-review.

### Changed

- **Curation per-task timeout default raised from 30s to 90s.**
  `curation.task_timeout` wraps each autonomous phase
  (classify, summarize, concept, contradict, manifest) so one hung
  LLM call can't starve a cycle. The 30s default was tight for
  multi-wave phases — `classify` and `summarize` can do up to 3
  sequential waves of 4 parallel calls (BatchSize=10, 4 workers), so
  on slower providers (Bedrock cross-region, throttled APIs, larger
  models) a phase legitimately at 25-35s would tickle the timeout
  even when nothing was actually hung. 90s gives multi-wave phases
  comfortable headroom while still catching genuine hangs well
  before the 5-minute cycle deadline. The cycle deadline is
  unchanged. Knob is in `~/.gramaton/config.yaml` under
  `curation.task_timeout` for sites that want to override.

- **Comprehensive doc sweep across 16 files reflecting the last three
  days of changes.** Three parallel drift-survey agents identified ~30
  findings; the bulk of them clustered around the retired
  `gramaton_observe` tool, the temporal-queries surface (Phases 2-9),
  collection templates, and configuration knobs added during engine-
  debt cleanup. Fixes:
  - `internal/setup/agent_instructions.md`: curation cadence corrected
    from "every 5 minutes" to "configurable cadence (default 1 minute,
    set via `curation.interval`)" — the embedded source ships into
    every fresh `gramaton init`, so the wrong cadence was being
    written into ~/.claude/CLAUDE.md and ~/.kiro/steering/gramaton.md
    on every install.
  - `integration/docs/custom-agents.md`: rewrote MCP tool list (38
    tools across 9 clusters, grouped by cluster), updated REST
    endpoint reference (added /v1/intake, /v1/sessions/*,
    /v1/branches/*/checkout, /v1/curation/{batch,drain},
    /v1/collections/{id}/items/batch, /v1/health, /v1/records/{id}/history;
    removed the retired /v1/observe), replaced "When to Observe" section
    with intake/sessions guidance.
  - `integration/claude-code/CLAUDE.md`: dropped soft-deprecation note
    for `gramaton_observe` (tool retired).
  - `integration/claude-code/subagent-capture.md`: added legacy/fallback
    banner — the autonomous-classify-via-subagent pattern conflicts with
    the user-initiated capture rule and the autonomous-curation /
    sessions paths now cover the autonomous side.
  - `integration/claude-code/subagent-curate.md`: added banner clarifying
    the file is for `autonomous: false` deployments only.
  - `server/guide/capture.md`: removed stale "Deprecated:
    gramaton_observe" section.
  - `server/guide/collections.md`: complete operations list (added 5
    missing tools: add_batch, rename, delete, schema, migrate),
    `template` parameter, `as_of=T` on `_items`, idempotent dedup on
    `curation: minimal`, behaviour fields section, templates section
    listing the 5 starters, temporal-queries cross-reference.
  - `server/guide/search.md`: added date-range patterns (`since`,
    `until`, `last_accessed_after`, `valid_before`) and a
    temporal-queries cross-reference.
  - `docs/integrator-guide.md`: added Temporal queries subsection,
    CLI parity note, `gramaton preflight` / `gramaton init --force`
    pointers, `template` and `as_of=T` references in collection ops,
    `temporal-queries` added to the live-reference topic list.
  - `docs/architecture.md`: noted the panic-recover defer in
    `securityHeaders` (commit 910b268), updated curation row with
    1-minute default cadence + `task_timeout` + startup self-heal,
    updated LLM provider row with `CompleteStructured` capability.
  - `docs/configuration.md`: added `llm.max_response_tokens`
    and `curation.task_timeout` yaml entries; cleaned up stale
    "doc older than 2026-04: was 5m" parenthetical on
    `curation.interval`.
  - `docs/providers.md`: noted `gramaton init --force` re-run path
    with API-key preservation.
  - `docs/project-design/data-integrity.md`: dedup threshold from 0.95
    to 0.92 (post-D37); action enum from `flag | reject | merge_silent`
    to `supersede | reject` (default `supersede`).
  - `docs/project-design/embedding.md`: AVX2+FMA3 amd64 matmul kernel
    documented as shipped (was "planned").
  - `docs/project-design/collections.md`: collection-templates entry
    updated — five starter templates ship.
  - `README.md`: CLI table additions (`gramaton preflight`, `--force`
    on `init`, `gramaton history`, `gramaton repair`, `gramaton hook`),
    `gramaton_intake` row in MCP Records cluster, `temporal-queries`
    added to guide topics list, new Community section linking
    SECURITY.md and CODE_OF_CONDUCT.md, Windows row in Documentation
    table.
  - `CONTRIBUTING.md`: dropped "to be added" parenthetical on the
    SECURITY.md disclosure pointer (file landed in commit 6563d80).

### Fixed

- **Observation extractor no longer re-embeds parents on persistent
  embed failure.** `extractAndCreateObservations` (curation/observe.go)
  selects parents with `processing_status="processed"` AND no
  `observation_of` outbound edge AND content over the min length;
  success writes an observation node + edge per extracted sentence.
  Pre-fix: an embed failure (line 168 area) skipped the parent
  without writing any state, so the next deterministic curation tick
  re-extracted the same parent and re-paid the embedding cost (up to
  20 sentence embeddings per parent). Same shape as the LLM-cost
  retry bugs but a different cost class -- embedding is CPU/GPU on
  local providers (bert/ollama) and billed input tokens on paid
  providers, so the counter prevents quiet token burn even though
  no LLM is involved.
  Fix: a new `Curation.MaxObservationAttempts` field (yaml:
  `curation.max_observation_attempts`, default 5, 0 disables).
  Default is higher than the LLM-cost counters (3) because
  embedding failures are typically transient (timeout, OOM, rate
  limit). Failed cycles now write `observation_extract_attempts`
  (Int64) and the truncated reason in `last_observation_extract_error`
  on the parent. The candidate-selection guard skips parents past
  threshold. A successful cycle clears the counter back to 0.
  Failure-tracking writes happen inside the existing
  `WithWriteBatch` transaction so they share the same lock and
  bbolt commit.
  Tests in `curation/observe_test.go` (new file with a
  `configurableObsEmbedder` test double): failure increments the
  counter, threshold excludes the parent at selection (embedder not
  called), `MaxObservationAttempts=0` disables, success clears the
  counter. data-model.md gained two new property rows for the
  observation-tracking pair. configuration.md documents the new
  knob.

- **gramaton_reembed no longer re-pays for the same failing records on
  every invocation; session-commit promotion now writes
  embedding_model on success.** Two coupled fixes.

  Main fix: api.Reembed's failure path at api/reembed.go:159 logged
  the error and continued without writing per-record state. The
  candidate selection at line 67 picks records whose embedding_model
  is missing or differs from the configured embedder; a record whose
  embed call kept failing (oversized content past the model's context
  window after halving truncation, content-policy refusal on paid
  embedders, persistent dimension mismatch) stayed without an
  embedding_model and re-entered the candidate set on every reembed
  invocation, re-paying full embed cost. Reembed is manual-only (CLI
  / MCP tool / HTTP endpoint), so per-call frequency is bounded by
  the operator -- but pathological records still re-cost on every
  call. New config: LLMCurationConfig.MaxEmbedAttempts (yaml:
  llm_curation.max_embed_attempts, default 3, 0 disables). Failed
  embed calls now write an embed_attempts counter (Int64) and the
  truncated reason in last_embed_error (max 200 runes via
  strutil.TruncateRunes); records past the threshold are skipped at
  selection time. Successful re-embed clears the counter to 0.

  Companion fix: api/sessions.go:915 IndexNode call only sets
  embedding_full from the supplied vec; it never set embedding_model
  on the new memory node. So every successful session-commit
  promote-to-memory path (the default) created records that LOOKED
  like they needed re-embedding from the candidate-selection
  perspective. Without this companion fix, the new embed_attempts
  counter would correctly bound damage on consistently-failing
  records, but every successful promotion would still re-enter
  reembed's candidate pool and pay one full embed cycle each time
  -- defeating most of the cost savings. Now session-commit
  explicitly writes embedding_model on the promoted node when the
  embedder succeeded.

  Pre-action validation gate from the tracker confirmed:
  - Reembed is manual-only (no background scheduler invokes it).
  - The four embed callsites (capture, session-commit, intake, bulk
    ingest) still leave records with missing embedding_model on
    failure.
  - capture's applyPreEmbedded WAS already setting embedding_model
    on success; the companion bug only affected sessions.
  - The helper from curation/task_retry.go is in package curation;
    couldn't reuse from package api without a cross-package
    extraction. Pragmatic call: inline ~25 lines in api/reembed.go,
    defer the extraction.

  Tests in api/reembed_test.go (new file with a configurableEmbedder
  test double): failure increments embed_attempts; N consecutive
  failures exclude the record from the next invocation's candidate
  set (embedder not called); MaxEmbedAttempts=0 disables (no counter
  writes); successful re-embed clears the counter. Companion fix
  test deliberately deferred -- the change is mechanical (one-line
  add inside the success-with-vec branch) and visible in code
  review; an end-to-end session-commit test would be heavy fixture
  for marginal coverage gain.

  Build, vet, race detector on api, full test suite all green.

- **Contradiction-check failures no longer re-ask the same pairs every cycle.**
  `detectContradictions` (curation/autonomous.go) was patched in D38 to
  drain the candidate pool by writing `no_contradiction` edges on
  positive-LLM-affirmation no-conflicts. Failure paths (LLM transport
  error, JSON parse error, batch-level failures in batched mode) were
  missed: they did `continue` and produced no persistent state, so
  the random-shuffle of the next read phase re-selected the same pair
  and re-asked. Same cost-bleed shape as the bug D38 documented. The
  unit here is a *pair*, not a record, so the per-record retry
  pattern (classify_attempts, summary_attempts, ...) doesn't apply
  directly — per-pair state has to live on an edge between the pair.
  Caught in the codebase-wide pathological-retry sweep.
  Fix: a new edge type `contradiction_check_skipped` is written
  on failed checks, carrying `attempts` (Int64), `last_error`
  (String, max 200 runes), and `checked_at` (Timestamp) properties.
  The read-phase `hasEdge` guard at autonomous.go:1678 was reworked
  to be type-aware: a `contradiction_check_skipped` edge with
  `attempts < MaxContradictionAttempts` is a *soft skip* (pair
  stays in the candidate pool and gets retried next cycle); the
  same edge with `attempts >= max`, or any other edge type, is a
  *hard skip*. Subsequent failures on a pair increment the existing
  edge's `attempts` rather than creating duplicate edges. Both
  single-pair and batched LLM paths capture failures; in batched
  mode a whole-batch failure marks every pair in the batch with
  the shared reason. New config: `LLMCurationConfig.
  MaxContradictionAttempts` (yaml: `llm_curation.
  max_contradiction_attempts`, default 3, 0 disables → legacy
  infinite-retry).
  Decision deferred: no first-class operator API to discover and
  bulk-reset stuck contradiction pairs. Today's triage flow is
  `gramaton_explore`
  from one record → find `contradiction_check_skipped` edges →
  `gramaton_unlink` per pair. Mediocre UX; matches the alpha-
  software bar. Will revisit if a real workflow emerges.
  Tests in `curation/autonomous_test.go`: failure creates soft-fail
  edge; subsequent failures increment the same edge in place
  (no duplicates); attempts hit threshold → pair locked out;
  MaxContradictionAttempts=0 disables; whole-batch LLM error in
  batched mode marks all pairs. data-model.md gained a "Curation
  markers" edge category covering `no_contradiction` and the new
  `contradiction_check_skipped`.

- **Manifest summary no longer recomputes on the same failing fingerprint.**
  `generateManifestSummary` (curation/autonomous.go) maintains a
  positive cache (`ManifestCache.Hash` + `Summary`) keyed by a
  content-derived store-state fingerprint. The cache only updated on
  success — so a fingerprint that consistently failed (LLM error,
  schema drift on the manifest prompt, content-policy refusal,
  empty-after-trim) recomputed the same hash next cycle, hit "cache
  miss" again, and re-called the LLM. Same loop. Caught in the
  codebase-wide sweep on 2026-04-25.
  Fix: a negative cache. `ManifestCache` gains `LastFailedHash` and
  `FailedAttempts`. On failure (LLM error OR empty-after-trim — the
  second was a separate latent bug since empty `cache.Summary` fails
  the positive-cache guard), the negative-cache fields advance. On
  the next cycle, if `LastFailedHash == currentHash &&
  FailedAttempts >= MaxManifestAttempts`, the LLM call is skipped
  with an Info log line. The negative cache clears automatically
  when (a) the fingerprint changes (store state moved -- operator
  gets a free retry), or (b) any later success lands (model behavior
  likely improved). New config: `LLMCurationConfig.MaxManifestAttempts`
  (yaml: `llm_curation.max_manifest_attempts`, default 3, 0 disables).
  No on-disk persistence — a server restart with the same store
  may retry once before re-tripping the negative cache, which is
  fine (manifest is a single LLM call, not expensive bundle work).
  Tests in `curation/autonomous_test.go` cover four cases: bounds
  retries on persistent failure, clears on success, clears on hash
  change (fresh budget per distinct store state), empty-after-trim
  treated identically to LLM error.

- **Anthropic batch classification path inherits the per-record retry
  bound.** `RunBatchClassification` (curation/batch.go) submits all
  pending records as one Anthropic Message Batch and applies results
  when the batch ends. Pre-fix the result handler had two failure
  paths that didn't write per-record state: the non-succeeded
  sub-result branch (line 231 area, errored/expired/canceled
  sub-requests) and the JSON-parse-error branch (line 244 area). Both
  did `continue` without bumping `classify_attempts` -- so a record
  that consistently failed in batch mode kept `processing_status =
  "captured"` forever and re-entered every batch invocation. The
  sequential-mode fallback path (`runSequentialBatch` →
  `runAutonomousInner` → `classifyPending`) was already protected by
  commit 1b16b80, but the Anthropic-specific batch path was
  missed. Same failure shape, different code path. Caught in the
  codebase-wide sweep.
  Fix: both failure paths now push to a `failedBatch` slice; after
  the apply loop, a single pass calls `recordTaskFailure` (the
  helper factored in commit 5126aff) with the same `classify_attempts`
  / `last_classify_error` / `processing_status="stuck"` policy used
  by the autonomous path. Records past `MaxClassifyAttempts` flip
  to `"stuck"` and are excluded from future batch invocations (the
  selection at the top of `RunBatchClassification` already filters
  by `processing_status="captured"`).
  Also fixed: `applyClassification` now calls `recordTaskSuccess` on
  the just-classified record to clear `classify_attempts`, mirroring
  the autonomous-success path. Without this, a record that failed
  N-1 times in autonomous mode and then succeeded via batch mode
  would keep a stale counter and incorrectly push to stuck on its
  next autonomous failure.
  Regression test: `TestApplyClassificationClearsAttempts` in
  `curation/batch_test.go` seeds a record with `classify_attempts=2`,
  calls `applyClassification`, asserts the counter cleared to 0 and
  status flipped to `processed`.

- **Concept synthesis no longer infinite-retries on persistent failure;
  `recordTaskFailure` / `recordTaskSuccess` helpers factored out.**
  `enrichConceptSyntheses` selects concept nodes with
  `synthesis_status="pending"` and asks the LLM to synthesize content
  from member summaries. Pre-fix: LLM transport errors, JSON parse
  errors, short responses (concepts at positions past the response
  array), and empty syntheses all took the `continue` (or `break`)
  path without writing any per-concept state -- the concept stayed
  pending and re-entered the candidate set every cycle. Concept
  syntheses BUNDLE multiple records' member summaries per LLM call,
  so a single failure rebills the entire batch's input tokens. Same
  failure shape as the classify (commit 1b16b80) and summary (commit
  0b94e1d) retry bugs; caught in the codebase-wide sweep on
  2026-04-25.
  Fix part 1: a new `LLMCurationConfig.MaxSynthesisAttempts` field
  (yaml: `llm_curation.max_synthesis_attempts`, default 3) caps
  consecutive failures per concept. Failed syntheses now write a
  `synthesis_attempts` counter (Int64) and the truncated error
  reason in `last_synthesis_error` (200 runes). At threshold,
  `synthesis_status` flips to `"stuck"` -- the existing selection
  guard (`ss != "pending"`) auto-excludes stuck concepts. Successful
  synthesis clears the counter. Tests in `curation/autonomous_test.go`
  cover batch-level-failure, marks-stuck-at-threshold,
  max-zero-disables, success-clears-attempts.
  Fix part 2: factored a shared `taskRetryPolicy` /
  `recordTaskFailure` / `recordTaskSuccess` helper trio (now in
  `curation/task_retry.go`) and refactored both classify and
  summarize to use it. The classify case maps to a policy with
  `StatusKey="processing_status", StatusValueAtMax="stuck"` (status
  flip excludes); the summary case has no status flip (selection
  guard checks `summary_attempts >= max` directly); the synthesis
  case mirrors classify. Pure refactor for the existing two sites
  -- their tests still pass without modification. Helps the next
  HIGH issues in the codebase-wide sweep (batch path, reembed,
  contradictions) reuse the same primitive instead of duplicating
  it again.

- **Summary generation no longer infinite-retries on persistent failure.**
  `generateSummaries` selects records with `content_full` and no
  `content_short` (or section nodes with truncated summaries) and
  asks the LLM to produce a summary. Pre-fix: LLM errors and
  empty-after-trim outputs took the `continue` path without writing
  any per-record state, so a pathological record (oversized content,
  policy refusal, output that always trims to empty) re-entered the
  candidate set every cycle and billed input tokens forever. Same
  failure shape as the classify retry bug fixed in commit 1b16b80;
  caught in the codebase-wide sweep on 2026-04-25.
  Fix: a new `LLMCurationConfig.MaxSummaryAttempts` field (yaml:
  `llm_curation.max_summary_attempts`, default 3) caps consecutive
  failures per record. Failed summary attempts now write a
  `summary_attempts` counter (Int64) and the truncated error reason
  in `last_summary_error` (200 runes). Records at threshold are
  skipped at selection time on subsequent cycles — no separate
  "stuck" status flip, since the selection guard already excludes
  them. A successful summary (autonomous or via any path that writes
  `content_short`) resets the counter to 0.
  Operator triage: surface skipped records via
  `gramaton_search(missing=["content_short"])` and inspect
  `last_summary_error`. Setting `MaxSummaryAttempts: 0` reverts to
  the legacy infinite-retry behavior. Tests in
  `curation/autonomous_test.go` cover failure-bumps,
  skips-at-threshold, max-zero-disables, success-clears-attempts.

- **Auto-backup no longer retries every curation cycle on failure.**
  `runAutoBackup` runs as a post-curation-cycle hook (default ~1 min
  cadence) and gates its work behind `time.Since(s.lastBackup) >=
  schedule` (default 24h). Pre-fix: success advanced `s.lastBackup` to
  `time.Now()`, but failure returned without advancing it — meaning
  the next post-cycle hook still saw `elapsed >= 24h` and re-attempted.
  For deterministic failures (disk full, permission denied, configured
  backup directory is a regular file or unreadable, target volume
  unmounted) this re-attempted at curation cadence (~1 minute), not
  the intended 24h. On a large store each attempt walks the graph and
  compresses under RLock — meaningful production impact and constant
  Error log spam. Fix: advance `s.lastBackup = time.Now()` on the
  failure path too. The next attempt waits the full schedule before
  re-trying; an operator who has resolved the underlying problem can
  manually trigger a backup via `gramaton_backup` without waiting.
  Regression test: `TestAutoBackupAdvancesLastBackupOnFailure` in
  `server/server_test.go` sabotages the configured backup dir
  (writes a regular file at the path) and asserts `s.lastBackup` was
  updated post-failure.

- **`docs/project-design/data-model.md` `processing_status`
  enumeration corrected.** Pre-fix listed `captured | processed |
  pending` — but no code writes `pending` as a `processing_status`
  value (`pending` is a `synthesis_status` value at
  `curation/deterministic.go:571`, a different field), and the table
  was missing `deleted` (set by `api/delete_record.go:42`) and the
  newly-added `stuck` (commit 1b16b80). Updated to the canonical set:
  `captured | processed | stuck | deleted`. Caught by the semantic-
  drift lens of gramaton-review.

- **Pathological records can no longer infinite-retry through curation.**
  `classifyPending` previously had no per-record retry bound. Records
  selected by `processing_status="captured"` were re-attempted every
  cycle until they succeeded — but a record the LLM consistently can't
  classify (content > effective context window, content-policy
  refusals, output that always fails JSON parse, mid-call timeouts)
  stays `"captured"` forever, sits at the front of the FIFO pending
  queue (sort by `created_at` ASC), and bills input tokens on every
  cycle (~1440 retries/day at 1-min cadence; both Anthropic and OpenAI
  bill input tokens on cancelled streams). The cycle-level
  `MaxCallsPerRun=20` and the daily caps DID bound damage, but they
  trip the entire store's curation rather than just the offending
  record, and the >80%-error circuit breaker doesn't fire when 1 bad
  record is in a batch of 10 (10% error rate). Fix: a new
  `LLMCurationConfig.MaxClassifyAttempts` field (yaml:
  `llm_curation.max_classify_attempts`, default 3) caps consecutive
  failures per record. Failed classify attempts now write a
  `classify_attempts` counter and the truncated error reason in
  `last_classify_error`; on reaching the threshold, `processing_status`
  flips to `"stuck"` and the record is excluded from future classify
  cycles. A successful manual `gramaton_classify` (or any successful
  autonomous re-classify) clears the counter and resets the status.
  Setting `MaxClassifyAttempts: 0` reverts to the legacy
  infinite-retry behavior. Operators surface stuck records via
  `gramaton_search(processing_status="stuck")` and triage them
  manually. Same shape exists in `generateSummaries`; filed as
  separate follow-up. Tests in `curation/autonomous_test.go` cover
  failure-bumps-counter, marks-stuck-at-threshold, max-zero-disables,
  success-clears-attempts.

- **Panic-recover at the HTTP transport boundary; structured 500 instead
  of broken-pipe.** Pre-fix, a panic in any api/ method (unchecked type
  assertion, nil deref, unforeseen graph state) propagated past
  `securityHeaders` to net/http's stdlib recover, which logs the stack
  and closes the connection mid-response. Clients saw broken-pipe / EOF
  with no parseable error envelope; agents calling MCP tools saw
  transport-level errors with no retryable hint. `securityHeaders` now
  installs two ordered defers: a recover-defer (deferred LAST → runs
  FIRST) catches panics, logs the stack at Warn with req_id/method/path,
  and writes a structured `{code:"internal", message:"internal error",
  retryable:false}` 500 envelope when the response hasn't started; a
  request-log defer (deferred FIRST → runs LAST) ensures the standard
  request line still fires whether the handler returned normally or
  panicked. `http.ErrAbortHandler` is re-panicked so net/http's
  intentional-abort semantics survive. `statusRecorder` gained a
  `wroteHeader` flag (also flipped on first `Write`) so the recover
  defer can tell whether it's still safe to emit the structured body
  vs. log only. `writeError` now sets `Content-Type: application/json`
  idempotently — `securityHeaders` skips that header for the `/mcp`
  path (MCP negotiates its own type), so a panic-recover 500 on `/mcp`
  would otherwise emit a JSON body with no Content-Type. Covers REST
  and MCP-over-HTTP equally (panic in an MCP tool surfaces as HTTP 500
  with parseable body rather than reset connection; per-tool JSON-RPC
  error envelope is a possible follow-up if MCP UX warrants it).
  (P2: panic-recover middleware.)

- **LLM provider inconsistencies: pricing prefixes, ignored-override
  warnings, configurable Anthropic max_tokens.** Audit named seven
  issues; addressed four:
  (1) `bedrock` and `openai` clients silently dropped the
  `CompleteWithModel(model, ...)` override -- callers expecting the
  cross-provider semantic that anthropic provides got their
  Complete redirected to the configured model with no signal. Both
  providers now log a one-shot Warn (`sync.Map`-deduped) when an
  override is requested that doesn't match the configured model,
  pointing operators at `llm.model` / `llm.models.*` config.
  (2) `LookupPricing` had `claude-opus-3` / `claude-sonnet-3` /
  `claude-haiku-3` prefixes that matched no real Anthropic model
  ID -- Claude 3 uses `claude-3-{tier}-...` (note position).
  Replaced with the correct prefixes (`claude-3-opus`,
  `claude-3-sonnet`, `claude-3-5-sonnet`, `claude-3-haiku`,
  `claude-3-5-haiku`); cost dashboards for Claude 3 deployments
  now actually compute non-zero costs.
  (3) `LookupPricing` returned the zero ModelPricing on miss with
  no signal -- callers got `$0` cost for unknown models silently.
  Now logs a one-shot Warn per missing model with a hint to add a
  pricing entry. Cost is still 0 (correct -- we don't have data),
  but the log makes it visible.
  (4) Anthropic `MaxTokens` was hardcoded `4096` on every messages
  call -- long curation outputs (concept synthesis on large stores)
  silently truncated. New `LLMConfig.MaxResponseTokens` config
  field threads through `anthropic.New`; defaults to 4096 to match
  prior behaviour. Operators who hit truncation can raise it.
  Skipped: (5) anthropic Content multi-block / tool-use (would
  require a richer message protocol; current text-only is fine for
  curation), (6) openai response-size LLM-vs-embed inconsistency
  (10MB vs 50MB are tuned to their respective payload shapes; not
  the same axis), (7) ratelimit interface-loses-concrete-methods
  (deferred to a separate cleanup).

- **CLI server-spawn fd leak; lock-free liveness probe; clean Ctrl-C
  on the MCP transport.** Audit named six issues; three were real
  production bugs:
  (1) `cli/serve.go::startBackground` opened
  `gramaton.stderr` for the spawned child but never closed the
  parent's reference. The fd lived until GC, leaking one per
  foreground spawn. Now closed explicitly after `child.Start()`
  succeeds (the child has its own dup'd fd via fork/exec).
  (2) `cli/serve.go::waitForServer` polled `/v1/status` (engine
  RLock) while the rest of the CLI uses `/v1/health` (lock-free).
  On a busy server with a long-held write lock, the status probe
  would queue behind it and the foreground spawn could time out
  even when the child was healthy. Switched to `/v1/health`.
  (3) `cli/mcp_cmd.go::runMCP` had no signal handling. Ctrl-C in a
  foreground invocation (or a parent sending SIGTERM) was trapped
  inside the SDK stdio loop until stdin closed. Wrapped the run
  with `signal.NotifyContext(SIGINT, SIGTERM)` so cancellation
  propagates cleanly.
  Skipped: the three test-fragility sub-fixes (parallel-unsafe
  global state, one-level flag reset, brittle SDK-shape assertion)
  -- they affect test infrastructure rigor, not production
  correctness, and the existing tests pass under the current
  parallel constraints.

### Changed

- **CLI error handling unified: single output stream, structured codes
  preserved.** Pre-fix the CLI had three error styles: (a)
  `fmt.Errorf+%w` paired with Cobra's stderr print, (b) `writeError`
  printing JSON to stdout AND returning an error that Cobra ALSO
  printed to stderr (dual-output -- pipe consumers saw both
  streams), (c) the `fmt.Errorf("input_error: %s", ...)` that capture
  used (already normalized in an earlier pass). Two structural fixes:
  (1) `writeError` now returns a `*silentError` sentinel; `Execute`
  detects it via `errors.As` and skips the redundant stderr print so
  the JSON on stdout is the only error artefact pipe consumers see.
  Exit code propagation unchanged.
  (2) `writeServerError(op, err)` helper unwraps a `*server.ErrorDetail`
  (preserved through the HTTP hop by parseResponse) and routes it
  through `writeError` -- so the server's structured Code/Message/
  Retryable land on stdout for HTTP 4xx/5xx instead of being collapsed
  into a Cobra-printed string. Network/timeout errors fall back to a
  generic "request_failed" code with the operation name. Migrated
  capture, classify, update, resolve, and all 5 branch commands. The
  `branch_error` ad-hoc code that branch.go used is replaced by the
  server's actual code (or "request_failed" for transport errors)
  -- a real improvement: callers can now distinguish "branch
  already exists" (ErrConflict) from "server unreachable".

- **CLI record subcommands share two new helpers; mcp_proxy split was
  already done.** Audit had two complaints: cli/mcp_proxy.go monolith
  (already split into 7 cluster files during the canonical-api refactor — one piece of the
  tracker is stale), and ~80% structural duplication across capture/
  classify/update/resolve. Added two helpers in `cli/input.go`:
  `readCommandInput(filePath)` (file-or-stdin reader returning
  writeError-formatted errors) and `extractRequiredID(input)` (pop
  the "id" field with missing_field error if absent). Migrated
  capture, classify, resolve, update; capture's previous
  `fmt.Errorf("input_error: %s", ...)` shape now matches the
  others' `writeError("input_error", ...)`. Net ~30 LOC saved.
  Skipped: branch.go's 5 subcommands -- each is 6-8 lines of
  serverGet/Post/Delete + writeError + printEnvelope. The
  consolidation would replace clear short functions with a
  closure-passing helper; not worth the indirection.

- **Server bootstrap drift fixed; Run() and StartHTTP() share helpers.**
  Pre-fix, `server.Run()` and `server.StartHTTP()` had ~50 lines of
  near-identical bootstrap (self-heal goroutine, curation runner,
  cancel storage). Drift had already started -- only StartHTTP()
  stored `s.curationCancel` for `Shutdown()` to find; Run() used a
  local `defer curationCancel()` so its curation goroutine couldn't
  be cleanly stopped via Shutdown. Extracted two helpers:
  `runStartupSelfHeal()` and `startCurationRunner()` (the latter
  always stores curationCancel on the server). Run()'s own shutdown
  path now reads `s.curationCancel` and stops the runner explicitly,
  matching StartHTTP's contract. Net: ~70 LOC saved, single source
  of truth for curation lifecycle. Shutdown semantics preserved.
  Documented the prepared-session sweeper TTL race in
  `api/sessions.go::preparedSessionTTL` so a future grace-period
  tweak has the rationale (race is theoretical: 30 min TTL vs
  seconds-to-minutes realistic agent flow). The idleWatcher
  hot-reload concern in the tracker is speculative future work --
  no live config-reload feature, no fix needed today.

- **Server preEmbedContent + ingest now thread request context.**
  An earlier audit (pre-canonical-api refactor) flagged that most service methods
  didn't take ctx. The canonical-api refactor review (commit f874cab) already wired
  ctx through every Session/Collection method and api/sessions.go,
  but a residual gap remained in `server/handler_records.go`:
  `preEmbedContent` called `Embed(context.Background(), ...)` on the
  capture path. `serviceCapture` had a `_ context.Context` parameter
  it ignored. `handleIngestFiles` didn't take ctx at all and was
  invoked without `r.Context()`. Fixed by threading ctx through:
  preEmbedContent now takes ctx and passes it to the embedder;
  serviceCapture uses its ctx instead of dropping it; handleIngestFiles
  takes ctx and its caller passes `r.Context()`. Client cancellation
  now propagates end-to-end on capture and ingest paths. Other
  remaining `context.Background()` callsites in server/ are
  long-running background tasks (curation, sweepers, auto-backup)
  whose lifetime is bound to their own cancel function -- those are
  correct uses.

- **Log-level discipline pass demotes 11 INFO sites to DEBUG.** The
  default INFO level was too chatty on hot paths and engine
  startup. Demoted: per-search step timings (filter / similarity /
  score / rerank — kept the "search timing" summary as INFO),
  access-flush lifecycle steps (acquiring lock, nothing-dirty,
  saving — kept the "done" line with save_ms as INFO), graph-load
  intermediate steps (node-tree-loaded, edge-store-already-
  populated, loading-edges — kept the "loading" and "load
  complete" anchors as INFO), and three migrate.go no-op-state
  lines (already at current version, no HEAD commit, write batch
  complete no-op). Search down to one INFO line per call instead
  of four. Engine load down to two INFO lines instead of five.
  Access flush down to one INFO line per real save instead of four
  per 30s. Convention codified in `CONTRIBUTING.md` so new code
  lands at the right level. (Audit log-level tracker.)

### Fixed

- **Documentation sweep — fix docs that promised behaviour code
  doesn't deliver.** Five doc fixes; four other tracker items
  verified already-correct or not reproducing.
  (1) `integration/claude-code/CLAUDE.md` said the curation cycle
  runs every 5 minutes; the default is `curation.interval = 1
  minute` and is configurable. Doc now says "configurable cadence
  (default 1 minute, set via `curation.interval`)".
  (2) `integration/kiro/gramaton-capture.md` listed only four
  `epistemic_status` values; the schema has had `refuted` for
  months. Added.
  (3) `integration/kiro/gramaton-observe.md` documented the
  retired `gramaton_observe` tool. Removed (see Removed section).
  (4) `embed/bert/bert.go` Provider doc said "concurrent Embed
  calls are serialized" but didn't mention that Close holds the
  same mutex (so Close-during-Embed is safe -- Embed re-checks the
  zeroed model field under the lock and returns "bert: provider
  closed"). Doc now describes the full synchronization contract.
  (5) `llm/llm.go` `Provider.CompleteWithModel` doc said unsupported
  models fall back to the default; reality is per-provider:
  anthropic honours overrides, openai/bedrock IGNORE the model arg
  entirely (model is fixed at client construction), claude-cli /
  kiro-cli route through subprocess. Doc now spells out per-provider
  semantics.

  Verified already-correct or not reproducing: `prolly.Diff`
  docstring (now accurately describes mark+fallback semantics);
  `secret.ResolveKey` (three-source priority + sk- legacy overload
  documented in detail); `BboltBM25Index` (has a Concurrency block
  citing the engine RWMutex); `flat_mmap.go` quantization warning
  (couldn't locate the specific contradiction the tracker named).

- **UTF-8 safe string-cutting helpers replace byte-indexing across
  five sites.** Pre-fix, several functions cut UTF-8 strings on byte
  boundaries when the cap was documented in characters, risking
  invalid trailing bytes for multi-byte input (CJK, accented Latin,
  emoji). The fix adds `internal/strutil.TruncateRunes` and
  `internal/strutil.TrimToValidUTF8` and migrates the call sites:
  (1) `curation/extract.go::splitSentences` — the boundary check
  after a sentence-terminal `.`/`!`/`?` was casting a single byte
  to rune (`unicode.IsSpace(rune(text[i+1]))`), missing whitespace
  for any multi-byte whitespace following (e.g. ideographic space
  U+3000). Now decodes the next rune properly via
  `utf8.DecodeRuneInString`.
  (2) `curation/autonomous.go::conceptShortSummary` — the function
  documented `maxLen` as characters but indexed bytes; multi-byte
  inputs could be cut mid-rune. Renamed to `maxRunes`, walked rune
  positions, used `strutil.TruncateRunes` for the no-boundary
  fallback. Cap semantics shift: callers (`deterministic.go`,
  `autonomous.go::generateSummaries`) all pass `200`. Pre-fix this
  was 200 bytes (~66 CJK chars, ~200 ASCII); post-fix this is 200
  runes (200 chars regardless of byte width). Multi-byte content
  is therefore allowed up to ~3x more bytes through this path.
  ASCII inputs unchanged.
  (3) `curation/autonomous.go::parseClassification` keyword cap —
  `kw[:100]` byte-truncation could land mid-rune for non-ASCII
  keywords. Wrapped in `strutil.TrimToValidUTF8` so the result is
  always valid UTF-8.
  (4) `backup/import.go::truncate` — local helper was already
  rune-correct but mis-named `maxLen`. Replaced with
  `strutil.TruncateRunes` (single source of truth).
  (5) `embed/bert/tokenizer.go::Encode` — the byte-by-byte trim
  loop after `text[:maxChars]` was a correctness fix in disguise:
  pre-fix called `utf8.ValidString(text[len-1:])` on a single byte,
  which returns false for ANY byte that isn't ASCII (continuation
  bytes are not valid UTF-8 strings on their own), so the loop
  would strip bytes off a complete trailing multi-byte rune even
  when no truncation was needed. Replaced with
  `strutil.TrimToValidUTF8`, which uses `DecodeLastRuneInString` to
  preserve complete runes. Practical impact for BERT input is
  small (text was already over the cap and would have been
  truncated anyway), but the new code is correct as-stated.
  Helpers have unit tests covering CJK, emoji, accented Latin, and
  invalid trailing bytes. New `TestSplitSentencesUTF8Boundary`
  exercises the ideographic-space boundary path.

- **Storage GC now refuses to sweep when prolly-tree marking is
  incomplete.** Pre-fix, `markProllyTree` swallowed `Read` and
  `json.Unmarshal` failures and returned silently; the recursive
  walk skipped that subtree's descendants. The downstream sweep
  in phase 2 would then delete those (legitimately reachable)
  chunks, silently destroying live data. Post-fix, `markProllyTree`
  surfaces failures via `result.Errors++`, and the main `GC`
  function short-circuits to `result, nil` (DeletedCount=0,
  ReachableCount populated up to the failure) when `Errors > 0`
  after the mark phase. The operator should investigate
  (`gramaton verify`, restore from backup) before retrying.
  `markFromTip` and `markCommitChunks` thread the `*GCResult` to
  reach `markProllyTree`. Regression test
  `TestGCRefusesToSweepWhenTreeChunkCorrupt` pins the invariant by
  pointing a commit at a non-existent NodeTreeRoot, then asserting
  Errors > 0, DeletedCount == 0, and that an orphan chunk
  survives.

### Changed

- **Search internals tightened across five small fixes.**
  None of these change observable behavior; the tracker's perf
  framings were mostly stale, but small structural improvements
  were worth landing in one pass:
  (1) `filterCandidates` now returns `map[string]struct{}` directly
  rather than a slice that the caller immediately converts back to
  a map. The slice round-trip cost ~500KB of allocation per search
  on a 20K-node store; the random-mode path materializes a slice
  locally where ordered iteration is needed. The unused `now`
  parameter is dropped.
  (2) `bfsReachable` now caps the visited set at 100K nodes (var,
  test-overridable) and logs a Warn on truncation. Defensive guard
  against a pathological proximity query on a million-node graph;
  no realistic search hits the cap. New `TestBFSReachableHitsCap`
  pins the truncation path.
  (3) `passesPropertyFilters` extracts three helper functions
  (`floatRangeOK`, `timestampStrictlyAfter`, `timestampStrictlyBefore`,
  `timestampSinceOK`) for the seven-or-so timestamp/numeric range
  filter checks that share semantics. `access_count` keeps its
  asymmetric min-strict / max-lenient handling inline because no
  other filter shares that shape -- the asymmetry is documented in
  a comment so the next reader doesn't fold it into a uniform
  helper. Behavior preservation walked filter-by-filter against
  the pre-image.
  (4) Session-dedup loop reads the segment's `captured_as` property
  (already populated at session_commit time) instead of walking
  EdgesFrom for `extracted_as`. Falls back to the edge walk for
  segments that pre-date the cached property, preserving back-compat
  for any session committed before that property landed.
  (5) `buildResult` and `buildMetadataSummary` accept `now time.Time`
  as a parameter; previously each call recomputed `time.Now().UTC()`
  inside, two calls ~1ns apart whose results could in principle
  disagree. Trivial consistency improvement.

### Removed

- **Deprecated kiro `gramaton-observe.md` integration doc deleted.**
  The `gramaton_observe` MCP tool was retired during the
  canonical-api / sessions migration (replaced by the
  `gramaton_session_prepare` / `gramaton_session_commit` two-phase
  flow). The kiro guide for it remained on disk and led future
  agents to the dead tool. Removed alongside other docstring
  fixes.

- **Dead `PropertyIndex.Range` and `BboltSecondaryIndex.NodesMissingField`
  APIs.** Both had zero production callers — `Range` was exposed via
  the `PropertyIndex` interface but unused; `NodesMissingField` was
  designed for `missing=` search filters but `search/search.go` uses
  `NodesWithKey` + manual diff against the property index instead
  (the property index is the authoritative source; the secondary
  field-existence index can drift on test paths that bypass
  `IndexNode`). Removing them drops the `Range` method from
  `MemoryPropertyIndex` and `BboltPropertyIndex` plus supporting
  machinery (`sorted` field, `rangeEntry` type, `isOrdered` helper,
  range-index handling in Add/Remove, the `deserializeValue` helper),
  and 13 tests that exercised the dead surface. `Property.Compare`
  remains because tests still assert its behavior; nothing in
  production calls it after this change. Surfaced during a deep
  read; the perf concerns originally raised were investigated and
  did not reproduce.

### Changed

- **Commit save split into `PrepareCommit` + `WriteCommit`; eliminates
  per-save orphan commit chunk.** The previous flow called
  `graph.SaveWithActions` (which wrote a commit chunk) and then
  `graph.RewriteCommit` (which re-wrote the chunk after attaching
  engine-managed index roots). Each save orphaned one commit JSON
  blob in the store, recovered later by GC. The new
  `graph.PrepareCommit` returns a `*Commit` with NodeTreeRoot and
  EdgeTreeRoot populated but the commit chunk not yet written;
  callers attach any extra fields and then call
  `graph.WriteCommit`. `core.Engine.commitInternal` uses the new
  pair, so a save now writes exactly one commit chunk. Dirty
  tracking now clears in WriteCommit (after the chunk lands), so a
  Prepare without a successful Write preserves dirty state for
  retry. `RewriteCommit` is removed (sole caller migrated). Two
  regression tests pin the deferred-hash/dirty contract and the
  single-chunk landing.

- **Skill editorial touch-ups** (`new-operation`, `benchmark-extract`,
  `store-health`). `new-operation`'s done-check now explicitly chains
  to `gramaton-review` + `gramaton-security-review` first, then
  `pre-merge-check`, matching the new "always run reviews first"
  guidance. `benchmark-extract`'s "What this skill does NOT do"
  section drops the speculative `(future)` skill names — siblings
  are described as user-driven separate passes, not promised future
  skills. `store-health` gets an explicit Purpose section at the
  top describing what the skill provides over raw probe output
  (cross-probe synthesis with actionable recommendations) and
  When-to-use vs When-NOT-to-use guidance.

- **Review skills (`gramaton-review`, `gramaton-security-review`,
  `pre-merge-check`) hardened against the bug shapes the post-curation-refactor
  review caught.** Two HIGH regressions of stated intent shipped past
  the original skill walks because the mechanical 12-check list and
  pre-merge gates didn't have framing for behavior preservation in
  refactors or for vacuous tests. Specific changes:
  (1) `gramaton-review` gains an intro paragraph reminding reviewers
  that the 12 checks are necessary but not sufficient, with explicit
  recommendation to spawn 2-3 independent review agents in parallel
  for diffs >200 lines or that touch multiple subsystems.
  (2) `gramaton-review` Check 10 (test coverage) gains two bullets:
  the bug-pin test must FAIL on the pre-fix code (regression-pin
  question), and the test FIXTURE must actually exercise the fixed
  path (vacuous-test guard).
  (3) `gramaton-review` gets a new Section 13 "Refactor preservation"
  describing how to walk pre-image branches against the new structure
  for diffs with refactor signature, with a real example from the
  curation-refactor concept Rule 2/3 fall-through bug.
  (4) `gramaton-security-review` Section 4 (input validation) gains
  a bullet requiring DIRTY-input fixtures for any added validator —
  same vacuous-test guard, security-flavored.
  (5) `pre-merge-check` gets a "Before running the mechanical gate"
  section recommending always-run gramaton-review +
  gramaton-security-review on substantive diffs, since bugs surface
  in unexpected places.
  (6) `pre-merge-check` Step 9 (commit shape) gains a refactor-
  specific bullet: commit body must articulate what behavior the
  refactor preserves, not just what changed.

### Fixed

- **`graph.Save` rejects the lazy-mode footgun.** The full-save branch
  iterates `g.nodes` directly via `sortedNodeIDs`, which only contains
  the cache-resident set; entering this branch with a populated
  `g.lastNodeTreeRoot` (i.e. lazy mode active) would silently emit a
  partial commit that drops every uncached node. Save now refuses
  loudly with an explicit error in this state. The legitimate
  full-save path is only first-save before any commit exists, where
  `g.nodes` is authoritative. Regression test pins the rejection.
  Defensive guard against future caller-side bugs; no current code
  path triggers the bad state.

- **`graph.Property.String()` no longer shadows `fmt.Stringer` and
  panics on non-string values.** Previously the typed accessor
  returned `p.str` and panicked for any other type, which meant any
  `slog`/`fmt` call that formatted a Property with `%v` crashed the
  server. The panicking accessor is renamed to `StringValue()`
  (matching `Float64()`/`Int64()`/etc. as the typed-asserting
  accessor); the new `String()` is a non-panicking `fmt.Stringer`
  implementation that delegates to `FormatValue()`. Three production
  callers in `index/property.go`, `index/bbolt_property.go`, and
  `api/collections.go` migrated to `StringValue()`. Regression test
  pins that `fmt.Sprintf("%v", prop)` returns a non-empty string for
  every Property type.

- **Curation review-pass cleanups (post-deep-review).**
  Six follow-up fixes from a deep review of the curation cycle
  commits:
  (1) `generateManifestSummary` keyword counts now come from the
  same live-only loop as the other manifest stats. Pre-fix, the
  historical-record filter only protected
  totalRecords/typeMap/etc. — `kwCounts` was sourced from
  `PropIdx().KeywordCounts()` which includes historical records, so
  adding/removing a historical record could still bust the
  manifest cache via the top-keywords fingerprint. Post-fix:
  inline kwCounts inside the filtered loop. Strengthened
  `TestManifestCacheIgnoresHistoricalRecords` to seed
  `content_keywords` and inject a distinctive "leakcanary" keyword
  on the historical record — pre-fix this would have busted the
  cache; post-fix the live-only count keeps it stable.
  (2) `RunDeterministic` merged loop now restores the original
  `it2` semantics where Quality Rules 2/3 fall through for concept
  nodes when Rule 1 doesn't fire. Pre-fix (curation-refactor first
  cut), the concept branch unconditionally `continue`d, so fresh
  concept nodes with template-style content_short and missing
  `embedding_short` no longer triggered Rule 3 (flag_embed) →
  concept embeddings drifted silently. Post-fix: a `rule1Fired`
  flag governs the skip; concept-only manifest/lifecycle work
  stays gated on `!isConcept`. New
  `TestRunDeterministicMergedLoopBranchesByNodeType` (renamed from
  `TestRunDeterministicSinglePassMixedNodes` to describe the
  invariant, not the implementation) seeds a fresh concept and
  asserts QualityRepairs+QualityFlags > 0, plus pins that
  existingConcepts suppresses duplicate concept proposal on a
  second run.
  (3) `runTaskWithTimeout` now bails immediately when the parent
  ctx is already cancelled — no per-task setup cost paid for
  remaining tasks when a cycle has been told to stop. New
  `TestRunTaskWithTimeoutBailsOnCancelledParent` pins it.
  (4) Test bug: `enrich_concepts_test.go` was calling
  `eng.HeadHashLocked()` without holding the lock. Switched to
  `eng.HeadHash()` (the locking variant); single-goroutine test so
  no race in practice, but the convention-violation could mask a
  real race on a future change.
  (5) New `TestEnrichConceptSynthesesLogsDimMismatch` exercises
  the user-visible payoff of the dim-mismatch surfacing fix: when a concept cluster
  has mixed-dimension embeddings, the warn log emits with
  "embedding dimension mismatch" + "gramaton reembed" hint. The
  pre-existing `TestMeanCosineToCentroidDimMismatchSurfaced` only
  asserted the third return value, not the log emission.
  (6) New `TestTaskCtxAttachesLabelOnSinglePath` /
  `TestTaskCtxAttachesLabelOnWorkerPath` /
  `TestTaskCtxNoTaskNoLabel` pin that `parallelLLM`'s deduplicated
  `taskCtx` helper attaches the telemetry task label correctly on
  both the single-item fast path and the worker-loop path.
  Drift-resistant — if the helper ever silently drops the label,
  these tests fail on either path. Plus minor cleanups: renamed
  `TestGenerateSummariesNonStructuralWithEdges` to describe the
  invariant; deflaked
  `TestRunTaskWithTimeoutCancelsHungTask` upper-bound check
  (50ms→1s tolerance for slow CI under -race).

- **Curation autonomous tasks no longer block each other on a hung LLM call.**
  Three targeted fixes:
  (1) Each task in the cycle (classify, summarize, concept,
  contradict, manifest) now runs under its own per-task sub-context
  with a wall-clock timeout (default 30s, configurable via
  `llm_curation.task_timeout`). Pre-fix, all five tasks shared the
  parent ctx and ran sequentially — so one stuck LLM call (e.g. a
  120s HTTP timeout) would consume the entire 1-minute curation
  cadence and silently starve every downstream task. The
  `runTaskWithTimeout` helper wraps each task; when the timeout
  fires, the in-flight LLM call's ctx cancels and the next task
  starts fresh. Setting timeout=0 in config disables the wrapper
  for legacy behavior.
  (2) `parallelLLM` no longer duplicates `telemetry.WithTask`
  setup. Pre-fix, the single-item fast path AND the worker loop
  each had their own copy of `if w.task != "" { callCtx =
  telemetry.WithTask(ctx, w.task) }`. Post-fix: both paths call a
  shared `taskCtx(ctx, w)` helper. Drift-resistant; if the
  task-context contract changes, both paths pick up the change.
  (3) `classifyPending` now checks ctx cancellation BEFORE the
  RLock + read-phase iteration. Pre-fix, a cancelled cycle still
  walked the entire pending list under RLock before noticing —
  wasted CPU + lock-holding for backlogs of any size. Four new
  tests pin the per-task timeout helper:
  TestRunTaskWithTimeoutCancelsHungTask (the load-bearing
  regression), TestRunTaskWithTimeoutCompletesNormally,
  TestRunTaskWithTimeoutZeroDisablesTimeout,
  TestRunTaskWithTimeoutNextTaskGetsFreshCtx (per-task isolation
  invariant — second task gets a fresh sub-ctx after the first one
  times out).

- **Curation cycle: redundant full-graph iteration + dead GC criterion + always-fire enrichment trigger.**
  Four targeted fixes in `curation/`:
  (1) `RunDeterministic` now collects manifest stats, lifecycle
  staleness, orphan candidates, quality issues, AND existingConcepts
  in a single pass (was three separate full-graph iterators: it,
  it2, cnIt). At 100k nodes on a 1m curation cadence that's 2 fewer
  full scans every 60s — the read-phase work shrinks by ~67%.
  Branching on `node_type == "concept"` keeps the per-category
  filter logic correct.
  (2) `collectGarbage` no longer requires `temporality == "ephemeral"`
  for deletion eligibility. The captured-status filter immediately
  above means the record has not been classified yet (LLM
  classification is what assigns temporality), so the strict
  ephemeral check filtered to ~0 matches in practice. Now: empty
  OR ephemeral both pass — aged-out unclassified debris actually
  reaches deletion. The other six GC criteria (captured + min age +
  zero access + low confidence + zero importance + zero edges) are
  unchanged. Five new regression tests pin: DeletesUnclassifiedDebris,
  StillDeletesEphemeralDebris, RespectsDurableTemporality (durable /
  temporal / immutable subcases), RespectsAgeFloor,
  RespectsAccessCount.
  (3) `enrichConcepts` no longer re-writes every concept with any
  inbound edge every cycle. Pre-fix gate
  `count != existingCount || count > 0` always fired once a concept
  had any evidence — producing a hot write loop on no-change cycles.
  Post-fix: only update when evidence_count changed OR
  last_evidence_at drifted forward (new edge from a source whose
  created_at exceeds the stored timestamp). Two new tests:
  `TestEnrichConceptsSkipsRedundantUpdates` (commit chain stable
  on no-op rerun) and `TestEnrichConceptsUpdatesWhenLatestEvidenceDrifts`.
  (4) `generateSummaries` walks `EdgesFrom(id)` once per node
  instead of twice. Pre-fix: `isChunkNode` ran an edge-walk for the
  Priority 1 filter, then a second edge-walk checked for `section_of`
  for Priority 2. Now a single loop captures both `isStructural`
  (any chunk_of/section_of) and `isSection` (specifically section_of).
  Behaviour-preserving: existing TestGenerateSummariesSkipsChunks
  + TestGenerateSummariesForTruncatedSections continue to pass; new
  TestGenerateSummariesNonStructuralWithEdges pins that records with
  semantic edges (related_to) correctly hit Priority 1.

- **Curation dedup safeguards + multi-dim embedding handling.**
  Five targeted fixes in `curation/`:
  (1) `verifyDedupJaccard` no longer skips the token-overlap check
  for short content. The previous behaviour returned true (= "yes,
  consolidate") whenever both sides were <200 chars, so cosine ≥
  0.92 alone could trigger auto-supersession. Short-content cosine
  is the *least* reliable signal (BERT subspaces compress short
  text into a region dominated by structural tokens), not the most.
  Now: when either side is <200 chars, the Jaccard threshold tightens
  from 0.3 to 0.5 — catches the false-positive shape "auth bug" vs
  "login bug" (Jaccard 0.33, was passing).
  (2) `meanCosineToCentroid` returns a third value `dimMismatched`
  counting members skipped due to embedding-dimension mismatch
  (e.g. embedding model changed mid-store). The caller now Warn-logs
  with a hint to run `gramaton reembed`. Pre-fix this was a silent
  skip producing misleadingly-low n counts at scale.
  (3) `ExtractObservations` docstring expanded to call out the
  `score=1.0` stub when sentence count ≤ maxObs (no TF-IDF ranking
  happens). Behaviour unchanged; the gap was that operators reading
  the score field assumed it meant "high confidence extraction".
  (4) `generateManifestSummary` now skips historical records
  (`valid_until` set + in the past) when computing the fingerprint.
  The manifest summarises the CURRENT state of the store, so
  counting superseded records inflated the totals and muddied the
  per-classification breakdowns. Cache invalidation still fires
  correctly on live-record supersession (live count drops 1, hash
  changes); historical-only mutations no longer bust the cache.
  (5) Weak-keyword denylist extended with `context`, `content`,
  `system` — generic LLM/agent vocabulary that was leaking into
  concept clusters and producing muddled "context"-themed
  syntheses. Six new tests: `TestVerifyDedupJaccard{Long,Short
  RejectsFalsePositives,MixedLengthUsesStricterThreshold}`,
  `TestMeanCosineToCentroidDimMismatchSurfaced`,
  `TestManifestCacheIgnoresHistoricalRecords`,
  `TestManifestCacheInvalidatesOnLiveSupersession`,
  `TestIsWeakConceptKeyword`. Three callers of
  `meanCosineToCentroid` updated to consume the new third return.

- **api.Diff regression coverage + timezone-fragile date tests
  (resolved as already-fixed).** A report had `api.Diff` returning
  empty buckets even when records were added between two commits.
  Investigation showed the underlying full-scan-degradation bug had
  already been fixed by commit `0207485` (storage: merge-walk prolly
  Diff) on 2026-04-23. Confirmed via two new positive-assertion
  regression tests that the original report was missing:
  `TestAPIDiffAddedNodeAppears` (added record surfaces in
  `resp.Added` for an empty-topic diff) and
  `TestAPIDiffTopicFilterPositive` (kafka record surfaces when
  filtered by topic=kafka, complementing the negative-only
  filter-leak test). En route, found and fixed three TZ-fragile
  date-construction patterns that had a real failing test on the
  user's machine: `time.Now().AddDate(0, 0, 1).Format("2006-01-02")`
  produces a local-TZ "tomorrow" string that `parseDateArg`
  decodes as UTC midnight — when local clocks are west of UTC
  across the day boundary, "tomorrow" parses to a UTC time
  earlier than HEAD. Fixed at all three callsites
  (`server/bindings_history_test.go` lines 214 / 281 / 308 and
  `api/collections_test.go` line 126) by switching to
  `time.Now().UTC().AddDate(...).Format(...)` so the test's frame
  matches the parser's. Made `TestAPIDiffUntilAtHeadMatchesNoUntil`
  and `TestCollectionItemsAsOfFutureRejected` go from
  intermittently-failing on west-of-UTC machines to deterministic.
  Also rewrote the docstring on `TestAPIDiffTopicFilterNegative`,
  which used to admit it was a workaround for the now-fixed
  full-scan bug, to reflect its real purpose as a leak-prevention
  test paired with the new positive case.

### Added

- **`gramaton preflight` environment-verification command.**
  A diagnostic command that
  answers "is my Gramaton install healthy?" in plain English.
  Pairs with `gramaton init`: init sets up first-time, preflight
  verifies every-time-before-use. Scope is the install/environment
  (config, providers, MCP registration, hooks, log errors) — store
  data is covered by `gramaton validate` / `repair` / `reembed`.
  Eight checks: Config (presence + parseability + perms), Data
  directory (writable), Embedding (provider configured + key file
  if cloud), LLM (provider + key file/env + perms), Server
  (running with live PID), MCP (gramaton registered with Claude
  Code via `claude mcp list`), Hooks (per-client script count +
  exec bits), Recent log errors (tail of gramaton.log scanned for
  ERROR lines). Output: `✓` / `⚠` / `✗` / `○` per check, with
  one-line remediation on warn/error. Non-zero exit on any error
  so CI / pre-flight scripts can gate on a clean run. 16 unit
  tests cover the per-check logic against pre-seeded
  filesystems. The `--fix` auto-remediation stretch goal is
  deferred to a follow-up — diagnose first, fix-flag once we have
  specific remediations per check.

### Fixed

- **Ctrl+C during `gramaton init --force` no longer destroys the
  user's pre-existing API key (Windows-support follow-up).**
  The wizard registers a rollback cleanup after
  writing each key file so that a mid-wizard interrupt undoes the
  write. Previously that cleanup was `os.Remove`, which on a
  --force re-run (where the key file already existed and was
  overwritten) would delete the file entirely — destroying both
  the old and new keys. Users who Ctrl+C'd partway through a
  reconfigure could be left with no key and no way back. New
  `writeWithRollback` helper captures the pre-existing content
  before the write and either restores it (if the file pre-
  existed) or removes the new file (if none did). Applied at
  all three addCleanup sites: step_llm.go's anthropic.key path,
  step_llm.go's openai.key path, and step_bootstrap.go's
  openai.key-for-embedding path. Two new regression tests:
  `TestWriteWithRollbackRestoresExisting` pins the restore
  behavior; `TestWriteWithRollbackRemovesWhenFreshWrite` pins
  the remove-on-fresh-install case.

### Changed

- **Step 0 menu clarifies that the "First time" branch is safe
  on re-runs.** Now reads
  `[1] First time  (or re-running to reconfigure — won't touch
  your existing data)` and `[2] Import a backup from another
  computer  (replaces data with the archive)`. Surfaces the
  destructive/non-destructive distinction so users don't hesitate
  on `init --force`. No behavior change.

### Added

- **Step 2 detects existing LLM API keys on re-run and offers to keep
  them (Windows-support follow-up).** Previously
  `gramaton init --force` forced users to re-paste their API key on
  every re-run even when the key file already existed at
  `~/.gramaton/anthropic.key` or `~/.gramaton/openai.key`. Now the
  wizard notices the existing file and asks `[Y] Keep / [n] Replace`.
  Keeping skips to validation (Anthropic) or config-caps prompt
  (OpenAI). Replacing falls through to the existing paste-new-key
  path. Bedrock unchanged — it uses AWS profile + region, not a
  local key file. New test `TestStepLLMAnthropicDetectsExistingKeyAndKeeps`
  covers the keep path; the wizard output must show "Anthropic key
  detected" and "Using existing key" before the test call fires.

### Changed

- **Step 4 asks once per detected client instead of one omnibus Y/n
  (agent-usage-instructions follow-up).** Previously a single
  "install for all?" prompt installed to every detected MCP client.
  Now each detected client (Claude Code, Kiro) gets its own Y/n so
  users can install for one and skip the other — useful when a user
  has heavy custom steering in Kiro but wants Gramaton to take care
  of the CLAUDE.md, or is testing Gramaton in Claude Code first
  before wiring Kiro. All prompts stay under the same Step 4 header;
  user sees a single logical step with branching inside. The step
  header text now also calls out Kiro's whole-file layout alongside
  Claude Code's fenced-block layout so users know what's about to
  land.

### Added

- **Kiro CLI support added to the agent-usage instructions install
  step (agent-usage-instructions follow-up).** Now when the
  wizard detects both Claude Code and kiro-cli, both get their
  user-scope instruction files installed. Kiro's model is different
  enough to deserve its own layout discriminator:
    - Claude Code (`~/.claude/CLAUDE.md`): shared file, merged with
      user content via BEGIN/END fence markers.
    - Kiro (`~/.kiro/steering/gramaton.md`): per-topic file in a
      multi-file directory. We own the file end-to-end; no fence
      markers needed. Users add their own steering topics as sibling
      files (e.g. `~/.kiro/steering/my-code-style.md`).
  Verified against Kiro's official docs (https://kiro.dev/docs/cli/steering/).
  New `instructionsLayout` type + `installWholeFile` /
  `installFencedBlock` branch. Three additional tests cover the
  whole-file-owned path (created / updated / unchanged). The
  previous "kiro-cli not yet supported" skip message is gone;
  both clients now install cleanly.

- **New wizard Step 4: install agent-usage instructions into
  ~/.claude/CLAUDE.md.** Surfaces the
  onboarding gap discovered on the Windows end-to-end test: Gramaton
  hooks were wired and MCP was registered, but Claude Code had no
  CLAUDE.md telling it when to autonomously call `gramaton_search`,
  when to capture, or how the Session flow works — so the agent
  defaulted to "I don't have memory of that" until the user
  explicitly prompted it. The new step offers (Y/n) to install a
  canonical agent-usage guide into each detected MCP client's
  instruction file. Content is fence-marker-bounded so re-runs
  (`gramaton init --force`) update only the gramaton-managed
  region; user content outside the fence is preserved. Skipping is
  first-class for users who curate their own CLAUDE.md. totalSteps
  bumped 4→5 (hooks becomes Step 5). Canonical template lives at
  `internal/setup/agent_instructions.md`, //go:embed'd into the
  binary. 7 new tests cover the fresh-file, append-to-existing,
  replace-fenced-block, idempotent-unchanged, unbalanced-fence-
  errors, per-client-path, and template-non-empty cases. Kiro-cli
  marked "not yet supported" (its user-scope instruction-file
  convention isn't verified); deliberate skip with a specific
  message rather than a guess-written file.

### Fixed

- **Gramaton server failed to start on Windows: fsync on directory
  returns "Access is denied".** The
  atomic-write discipline used in `core/refs.go`, `storage/store.go`,
  and `backup/backup.go` (write → fsync file → rename → fsync parent
  dir) worked on Unix but broke the server's startup path on Windows:
  NTFS doesn't allow opening a directory handle with sync access, so
  `os.File.Sync()` on a directory returns ERROR_ACCESS_DENIED. The
  server's write of `server.json` and its load of the engine's FORMAT
  file both hit this and aborted — which in turn made `gramaton mcp`
  fail to auto-start the HTTP server, which made Claude Code's
  `/mcp` show `gramaton · ✘ failed`. Fix: `fsyncDir` is now a no-op
  on Windows across all three call sites. Go's `os.Rename` on
  Windows uses `MoveFileEx` which is durable without the explicit
  parent-dir flush the POSIX model requires, so skipping the sync
  doesn't weaken durability guarantees. The three identical
  `fsyncDir` implementations are follow-up candidates for
  consolidation into a shared helper; for now keeping them parallel
  matches the existing code layout. Surfaced when `gramaton mcp`
  manually on Windows printed the real cause from the child
  process's stderr tail.

### Added

- **`gramaton init --force` re-runs the wizard on an already-
  initialized install.** Previously
  the init command bailed unconditionally when
  `~/.gramaton/config.yaml` existed, requiring users to delete the
  config file (losing their provider + API-key setup) to pick up
  wizard-touched state like the proxy-script templates or MCP
  registration logic. The new flag bypasses the guard so fix-ups
  can be rolled out without a full reset. The wizard remains
  idempotent internally (re-registering an MCP entry that already
  exists is a no-op, etc.), so `--force` is safe to re-run
  repeatedly.

### Fixed

- **Claude Code hooks failed to fire on Windows: path backslashes
  eaten by Git Bash.** The hook
  proxy paths `gramaton init` wrote to `~/.claude/settings.json`
  (e.g. `C:\Users\op\.gramaton\hooks\claude-code\session-start.sh`)
  reached Claude Code's bundled Git Bash, which then treated the
  backslashes as escape characters. `\U`, `\o`, `\.` etc. have no
  special bash meaning, so bash silently stripped the backslashes
  and tried to run `C:Usersop.gramatonhooksclaude-codesession-
  start.sh` — not a real file. Result: every Claude Code
  SessionStart hook on Windows logged
  `bash: ... No such file or directory` and hooks did nothing.
  Fix: `internal/setup/hooks.go::RegisterClaudeHooks` now
  `strings.ReplaceAll(path, "\\", "/")` before writing to
  settings.json. Git Bash accepts `C:/Users/op/.gramaton/...`
  natively. `strings.ReplaceAll` (not `filepath.ToSlash`) so the
  transformation is observable on non-Windows hosts too —
  no-op on Unix paths, which never contain backslashes.
  Regression test `TestPathNormalizationForClaudeBash`. Existing
  Windows installs need to re-run `gramaton init` to rewrite
  settings.json with the corrected form.

### Changed

- **CONTRIBUTING + architecture.md polish to close Phase 3 of
  Windows support.** CONTRIBUTING
  line 118 no longer says "A Unix-like filesystem (macOS or
  Linux). Windows isn't tested." — the three-OS CI matrix now
  covers Linux, macOS, and Windows. A new "Platform-guarded
  code" subsection under "Patterns to reuse" documents the
  decision rule: two-file build-tag split for compile-gated
  APIs (e.g., `syscall.Setsid`, `syscall.Mmap`), inline
  `runtime.GOOS` guard for <10 LOC of semantic divergence
  (perm-check skips, proxy-file-format selection). Both
  patterns are cited with concrete examples from the tree.
  `docs/architecture.md` gains one-line descriptions of
  `hooks/` (post-Phase-2 shape: Go subcommands + proxy
  scripts) and the new `internal/mmap/` package. Windows
  support is now complete across all three phases.

### Added

- **`docs/windows.md` — Windows-user documentation (Phase 3 of
  Windows support).** Covers
  installation via `go install`, first-run setup, per-OS hook
  proxy behavior (Claude Code uses `.sh` via bundled Git Bash;
  Kiro uses `.cmd` natively), known Windows-specific caveats
  (perm checks skipped under NTFS ACL model, python3 no longer
  required, bbolt lock recovery), and deferred items (signed
  installers, Credential Manager integration, Windows Service
  registration). README gains a platforms line mentioning Linux
  + macOS + Windows with a pointer to the new doc.

### Removed

- **Legacy shell hook scripts + embed_hooks duplication (Phase 2
  of Windows support).** Deletes
  `hooks/claude-code/*.sh` (4 files, 265 LOC), `hooks/kiro/*.sh`
  (3 files, 127 LOC), the entire `internal/setup/embed_hooks/`
  duplicate tree, and `hooks/hooks_test.go` (bash-invocation
  harness, 212 LOC — superseded by the in-package Go handler
  tests added in commits 1-3). Net: 15 files / ~800 LOC removed
  from the repo; zero behavior change because Phase 2 commits
  2-5 already replaced every runtime dependency on these files
  with Go code. Upgrading users should re-run `gramaton init`
  so the now-stale shell scripts at `~/.gramaton/hooks/**/*.sh`
  are replaced with the new proxy files — the init wizard
  materializes them unconditionally. Phase 2 is complete.

### Changed

- **Hook installation switches to Go-generated proxies (Phase 2
  of Windows support).** The wizard's
  `DefaultHookBackend.Materialize` no longer extracts embedded `.sh`
  files from `internal/setup/embed_hooks/` — it now synthesizes
  one-line proxy scripts from Go string templates at init time.
  Each proxy forwards stdin to `gramaton hook <event>` (wired in
  commit 4); the real hook logic lives in the `hooks/` Go package.
  The `//go:embed` directive and the `//go:generate cp` duplication
  scaffolding are gone. Cross-platform proxy matrix: `.sh` (with LF
  line endings, `#!/bin/bash\nexec gramaton hook <event>`) for
  Claude Code on all OSes and Kiro on Unix; `.cmd` (with CRLF,
  `@gramaton hook <event>`) for Kiro on Windows (Kiro CLI 2.0 is
  native Windows, no bundled bash). `isGramatonHookCommand` now
  normalizes `\` → `/` before its substring match so settings.json
  entries containing Windows-style paths are recognized as ours.
  `cli/session.go:cwdSlug` now delegates to `hooks.CwdSlug`
  (single source of truth for the Unix/Windows drive-aware slug).
  Three perm-verification checks in `internal/setup/step_verify.go`
  (config file perms, LLM api_key_file perms, hook exec-bit check)
  are now gated on `runtime.GOOS != "windows"` — NTFS ACLs are
  the real access-control mechanism on Windows and the Unix mode
  bits `os.Stat` synthesizes there are meaningless. The hook
  exec-bit check also accepts `.cmd` files as valid regardless of
  the mode bits (Windows associates `.cmd` via PATHEXT, not exec
  bit). Net: `internal/setup/hooks.go` is simpler and more
  portable, and `cli/session.go` becomes a one-line delegate. The
  old `hooks/*.sh` source tree and `internal/setup/embed_hooks/`
  will be deleted in commit 6 of this phase.

### Added

- **`gramaton hook <event>` CLI subcommand — wires the Go hook
  handlers to the binary (Phase 2 of Windows support).** New
  `cli/hook.go` registers
  a hidden cobra subcommand that dispatches one of seven
  positional-arg events (session-start, stop, pre-compact,
  post-compact, kiro-agent-spawn, kiro-user-prompt-submit,
  kiro-stop) to the matching `hooks.ClaudeCode*` or `hooks.Kiro*`
  handler. Hidden from `--help` because users never invoke it
  directly — Claude Code and Kiro do, via the proxy scripts that
  `gramaton init` will install in commit 5. Unknown event names
  surface as errors; handler errors are always swallowed (fail-
  open). Two tests: `TestHookDispatchCoversAllEvents` keeps the
  dispatch table in sync with the documented event list;
  `TestHookUnknownEventReturnsError` pins the surface-the-error
  contract for unknown event names.

- **`hooks/kiro.go` — Go port of the three Kiro hook scripts (Phase
  2 of Windows support).**
  `KiroAgentSpawn`, `KiroUserPromptSubmit`, `KiroStop` replicate
  `hooks/kiro/*.sh`. Two Kiro-specific quirks preserved exactly:
  (1) `session_id` falls back to `agent_id` when absent (Kiro
  payloads prefer agent_id while Claude Code uses session_id);
  (2) `KiroUserPromptSubmit`'s stdout contract — whatever the hook
  writes there is injected into Kiro's next-prompt context, so
  when the turn counter hits `GRAMATON_EXTRACT_INTERVAL` (default
  10) the handler emits the extraction reminder to stdout and
  resets the counter. Text of the reminder is preserved verbatim
  from the legacy shell script so agents see the same nudge. 10
  unit tests cover: agent-spawn happy path, session_id-preferred-
  over-agent_id, agent_id fallback, unsafe-id rejection, threshold
  logic below/at/env-overridden values, counter reset after
  reminder, stop increments, silent-on-missing-id. All green under
  -race on macOS; cross-compiles for windows/amd64.

- **`hooks/claude_code.go` — Go port of the four Claude Code hook
  scripts (Phase 2 of Windows support).**
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
  `gramaton hook <event>` subcommand (Phase 2 of Windows support).**
  Introduces `hooks/state.go`
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
  real AMD64 Windows hardware validation.** The AVX2+FMA3 kernel was
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
  cross-platform shutdown (Phase 1 of Windows support).**
  `IsProcessAlive` moves into
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
  Windows support).** Four direct
  `syscall.Mmap`/`Munmap` callsites (remap + rewrite-path unmap +
  Close-path unmap + initial Mmap) replaced with `mmap.Region`.
  The remap-after-Flush flow is preserved exactly: unmap the old
  region, truncate + rewrite, open a new region. No behavior
  change on Unix. Combined with the safetensors migration, the
  full Gramaton tree now cross-compiles for `GOOS=windows
  GOARCH=amd64` — Phase 1's load-bearing blocker is clear.

- **`embed/bert/safetensors.go` migrated to `internal/mmap`
  (Phase 1 of Windows support).**
  Six direct `syscall.Mmap`/`Munmap` callsites replaced with
  `mmap.Region` from the new internal package. Per-error-path
  cleanup consolidated into a small `fail` closure so we never
  leak the mapping or fd. No behavior change on Unix; the file
  now cross-compiles and runs on Windows. Bert unit tests
  unchanged, still green under `-race`.

### Added

- **`internal/mmap` package: cross-platform read-only file mapping
  (Phase 1 of Windows support).** New
  `Region` type with `Open`, `Bytes`, `Close`. Unix implementation
  wraps `syscall.Mmap`/`Munmap` (MAP_SHARED, PROT_READ); Windows
  implementation wraps `CreateFileMapping` + `MapViewOfFile` via
  `golang.org/x/sys/windows`. Both platforms share the same public
  surface via a `//go:build` split. Close is idempotent and safe
  on a nil receiver. Round-trip, empty-file, negative-size,
  multi-page, and double-close tests run on every CI OS. Unblocks
  commits 3-4 (safetensors + flat_mmap migrations) without
  touching the existing syscall callsites yet.

- **GitHub Actions CI with three-OS matrix (Phase 1 of Windows support).**
  New `.github/workflows/ci.yml`
  runs `build`, `test`, and `test -race` on `ubuntu-latest`,
  `macos-latest`, and `windows-latest`; `vet` on ubuntu only (static
  analysis is portable). Go 1.26, 20-minute per-job timeout,
  `fail-fast: false` so one OS failing doesn't cancel the others.
  Windows jobs will be red until the mmap abstraction lands in
  commits 2-5 of Phase 1; this is expected and unblocks
  surface-assumption discovery on the other two OSes in the
  meantime.

- **Structured-output implementation for OpenAI and Bedrock
  (Cluster 2 Phases 2b + 2c).** Both
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
  live (Cluster 2 Phase 2a).** New
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

- **Content-quality self-heal pass (Cluster 2 Phase 3).**
  New `curation.RunSelfHeal` walks
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

- **`internal/sanitize` package (Cluster 2 Phase 1).**
  New helper `sanitize.Field(s)` and
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
  curation.**
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

- **`SECURITY.md`**: responsible-disclosure policy. Routes
  vulnerability reports through GitHub's
  private vulnerability reporting (Security tab → Report a
  vulnerability), keeping the maintainer's email off the repo. Lists
  supported versions (alpha: `main` only), triage expectations
  (solo maintainer, no SLA, ~7-day ack target), scope (CLI + serve +
  store format + loopback gates + secret handling), and credit
  policy. GitHub surfaces this on the Security tab and the
  Community profile.

- **`CODE_OF_CONDUCT.md`**: Contributor Covenant v2.1, fetched
  verbatim from the canonical
  source at github.com/EthicalSource/contributor_covenant with the
  `[INSERT CONTACT METHOD]` placeholder replaced by
  `brandonlattin@gmail.com`. `CONTRIBUTING.md` no longer links out
  to the external Covenant URL; it points to the local file
  instead. Closes the "forthcoming" stub that has been in
  CONTRIBUTING since the repo went public-prep.

### Changed

- **`gramaton_collection_add_batch` now mirrors single-add's
  curation-profile dedup semantics.** On `curation=minimal` collections
  (shopping-list / packing-list shape), duplicate titles land in
  `Added` with `deduplicated=true` pointing at the existing item's
  ID instead of `Failed` with `code=duplicate`. This matches the
  behavior already shipped for single-add, so a
  loader batch-importing into a minimal-curation collection gets
  idempotent results rather than spurious per-item failures.
  Intra-batch dedupes on minimal collections follow the same rule.
  On any other profile (standard / default), duplicates still land
  in `Failed` — original semantics preserved.
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
  internal-vs-internal subtree differences.** Previously the diff short-circuited
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
  keep the original `ErrConflict` behaviour: same-title-different-
  context is legitimate there. Layer 3 (context-enriched embedding
  input) and per-stage curation-profile skip logic stay deferred
  to a follow-on.

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
  - Stripped ~100 internal ticket and phase tags from code comments.
    These referenced a private internal tracking system with no
    public analogue. Substantive WHY rationale around the tags was
    preserved -- only the dead pointer itself was removed.
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

- **Manifest-summary cache key now reflects epistemic /
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

- **Nits batch (cluster A wrap-up)** -- two surviving items
  from the original bundle; the rest were resolved or absorbed
  by other work in this Unreleased window:
  - `logging.RotatingWriter.enforcebudget` -> `enforceBudget` to
    match Go method-naming convention.
  - `config.Save` now documents the file/dir permission convention
    (0700 dirs, 0600 files for everything under `~/.gramaton/`,
    since these may carry credentials).
  Bullets already addressed elsewhere: yaml.Decoder strict-mode
  (commit 1c8b665), Observe.Enabled default (config-drift
  sweep, commit 34b3fb8), safeProperties indentation, truncate
  maxLen byte/rune (separate item), three integration-doc drifts
  (separate item).

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

- **Config `Validate()` + strict YAML decoding** -- config/config.go
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
- **WriteSession pattern for batched index writes** --
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
  rationale.
- **kirocli output filtering anchored to line-start** -- the
  Credits / Time footer and trust-warning lines were filtered via
  `strings.Contains` on any substring match, which would silently
  drop legitimate model responses containing those phrases mid-
  sentence (e.g. "The bank statement shows Credits: 4"). Replaced
  with regex + prefix matching that requires the chrome pattern at
  line start. Three new tests in `llm/kirocli/kirocli_test.go`
  assert mid-line occurrences of "Credits:", "Time:", and
  "kiro.dev/docs" survive; the existing tests for footer/warning
  stripping continue to pass.
- **Bedrock Cohere embedder distinguishes query vs document.**
  New optional `embed.QueryEmbedder` interface. Bedrock Cohere
  implements `EmbedQuery` with `input_type="search_query"`; the
  existing `Embed` continues to use `search_document` for indexed
  content. Cohere's retrieval benchmarks show measurable cosine-
  similarity degradation when queries use the document input type,
  so search-time query embedding now routes through
  `embed.EmbedForQuery` (in `api/search.go`) and the `queryEmbedder`
  type-assertion in `search.Tool.Execute`. Providers that don't
  distinguish (OpenAI, Ollama, Titan, BERT) fall back to `Embed`
  unchanged.
- **LLM.Model vs LLM.Models.* time-bomb reduced.** Curation
  path stops reaching into `cfg.LLM.Models.Medium` directly;
  `modelForTaskLabel` now goes through `cfg.ModelAtEffort` so the
  tier-map access is confined to `config.go`. Server startup logs a
  Warn when any effort tier (`llm.models.low/medium/high`) is empty,
  naming the tiers and the fallback model; curation silently falling
  back to the provider default was the original surprise the ticket
  called out. `ModelForTask` remains the single entry point for
  mapping a curation task to a concrete model.

### Added

- **HTTP LLM provider retry on 429 / 5xx.** New
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
- **`Engine.TryRLock` + writeJSON/writeJSONLocked collapse.**
  `writeJSONLocked` is gone. `writeJSON` is now safe to call from
  any handler regardless of engine lock state: `curationStatus` uses
  an opportunistic `engine.TryRLock` to refresh the cache when
  possible, falling back to the stale (possibly zero) cached value
  rather than deadlocking when a caller holds the write lock. Closes
  the RWMutex-not-reentrant footgun.
- **`Engine.WithWriteBatch` helper.** Consolidates the
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
  and `curation/observe.go` observation extraction. The curation
  envelope cache step was already done when writeJSON grew the 5s cache.
- **USD-denominated LLM cost caps.** Two new config
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

- **CLI provider telemetry.** `claudecli` now parses
  the per-model token block from `claude -p --output-format json`
  (`modelUsage.<model>.{inputTokens, outputTokens,
  cacheReadInputTokens, cacheCreationInputTokens}`) and records it
  via `telemetry.Record`, so cost flows through the standard pricing
  pipeline the same way anthropic/openai/bedrock do. `kirocli` still
  reports zero: its output surfaces only a "Credits:" footer (not
  USD or tokens), and cross-provider aggregation expects tokens --
  operators relying on `kirocli` should use `max_calls_per_day` as
  the cost safety net.
- **Cycle summary log polish.** The "autonomous
  curation complete" log now includes per-model cost (`cost:<model>
  $0.1234`) alongside the existing per-model classification counts,
  and the per-task breakdown gains a cost field (`tokens:<task>
  in=.../out=.../cache=.../cost=$X`). Map iteration is sorted so log
  lines are deterministic across cycles.
- **Per-provider LLM telemetry.** Token usage is now
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
  still report zero -- their cost/credit parsing lands in a future step.

### Added

- **Bulk collection_add.** New MCP tool
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
  `server-design.md` (pre-canonical-api "v0.2" daemon spec, superseded by
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
- **Canonical-api cascade cleanup stage C3 (collections).** Deleted
  `server/service_collections.go` (928 lines) and
  `server/service_collections_test.go` (967 lines), plus the
  duplicate `server/collections.go` (289 lines) whose schema
  types and field-type constants already live in
  `api/collection_schema.go`. All 12 server-level collection
  methods were unreachable via bindings after the canonical-api refactor routed
  `bindings_collections.go` through `s.api.CollectionXxx`.
  Coverage ported to `api/collections_test.go`: 27 tests over
  Create/List/Items/Add/Remove/Update/Move/Rename/Delete/
  SchemaUpdate/Migrate, including schema validation, projection
  + filter edge cases, multi-membership, dedup, and schema
  evolution. Two tests pinned the canonical-api behavior changes: duplicate
  name and duplicate title now surface as `conflict` rather
  than `duplicate`-code or success-with-`duplicate=true` maps.
  The HTTP-boundary test (`TestCollectionItemsHTTPProjectionAndFilter`)
  stayed in `bindings_collections_test.go`; the
  `TestCollectionPerformance` test was preserved too.
- **Canonical-api cascade cleanup stage C2 (records).** Removed seven dead
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
- **Canonical-api cascade cleanup stage C1 (search + test rewires).** Deleted
  `server/service_search.go` (the last method `serviceSearch` was
  unreachable via bindings since the canonical-api refactor routed `bindings_search.go`
  through `s.api.Search`). Rewired the one test reference in
  `server/handler_sessions_test.go` to `srv.api.Search`. Also
  rewired `bindings_collections_test.go` helpers (`makeCollection`,
  dedup test seed) from `srv.serviceCollection{Create,Add}` to
  `srv.api.Collection{Create,Add}`. Deleted the unused `searchRequest`
  type in `server/handler_search.go`; kept `parseDateArg` (used by
  remaining server-level services).
- **Canonical-api cascade cleanup stage B (sessions).** Deleted
  `server/service_sessions.go` (1255 lines) and
  `server/service_sessions_test.go` (1659 lines): the server-level
  session subsystem was fully dead after the canonical-api refactor routed
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
  latent footgun in the canonical-api security review).
- **Canonical-api cascade cleanup stage A (isolated orphans).** Deleted the
  three service orphans that were safely isolated (no test
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
- **Canonical-api dead code cleanup (server handlers + mcp wrappers).** Deleted
  941 lines across 8 files that were orphaned by the canonical-api
  migration: `server/mcp_records.go`, `server/mcp_collections.go`
  (both entirely unreachable; `registerMCPTools` in `server/mcp.go`
  routes exclusively through the `bindings_*.go` path), plus
  `server/handler_sessions.go` and `server/handler_collections.go`
  (all contained only pre-canonical-api HTTP handlers with no live callers).
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
  D37 in `docs/project-design/design-decisions.md`.
- **Phase 3 of the documentation consolidation: targeted updates to
  the surviving `docs/project-design/` files.** Not rewrites -- each
  doc stays as historical design rationale but is scrubbed of stale
  claims and extended where post-canonical-api material has a load-bearing
  story worth preserving. Changes:
    - **`design-decisions.md`**: added D31-D36 covering the three
      storage paths / decision rule (D31), the two-tier session model
      with `promote_to_memory` (D32), the `api/` canonical operations
      surface (D33), pure-Go BERT as default embedder
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
  full-replace-not-merge silent-fail trap: partial per-store configs
  can silently zero out sections and cause startup refusal. Marks
  the `observe:`
  section as soft-deprecated in favor of the Sessions flow. Restructures
  the file around the user-facing vs internal-tuning distinction that
  config/config.go itself uses. Surfaces one code/config-comment
  inconsistency during the audit: `dedup.action` values `flag` and
  `supersede` behave identically in `api/capture.go:142-174` (both
  mark the older record historical and add a supersedes edge; only
  `reject` differs), contrary to the config comment that describes
  `flag` as 'mark but don't delete'. Documented in the doc.
- **`docs/integrator-guide.md` rewritten around the three storage
  paths.** Previous version was organized around "two retrieval
  modes" (Knowledge Graph vs Collections) and gave `gramaton_observe`
  as a first-class tool. Neither matches reality: the canonical-api refactor introduced
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
  doc described -- that was the pre-canonical-api shape), retrieval funnel
  (search -> inspect -> explore), agent prompt guidance, live
  reference pointer to `gramaton_guide(topic=...)`. Search-filter
  table expanded to match the actual `api.SearchRequest` fields
  (confidence/importance min+max, `missing`, `max_edges`,
  `similar_to`, `match`, `store`, `expires_before`, `since`,
  `random`, `sort` with the verified sort keys). Surfaced one
  code rough edge during the audit: the `store` filter takes
  plural `sessions` but result rows emit singular `session`.
  Called out in the doc as a known rough edge.
- **`docs/architecture.md` rewritten for the post-canonical-api layered
  architecture.** Previous version predated the canonical-api refactor and described a
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
- **`README.md` rewritten for the post-canonical-api surface.** Added a "Why
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
  (dead code since the canonical-api migration -- `registerMCPTools` in
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
- **Engine god-object split.** `core/engine.go` reorganised
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

[Unreleased]: https://github.com/gramaton-ai/gramaton/compare/v0.3.0-alpha.1...HEAD
[0.3.0-alpha.1]: https://github.com/gramaton-ai/gramaton/compare/v0.1.0...v0.3.0-alpha.1
[0.1.0]: https://github.com/gramaton-ai/gramaton/releases/tag/v0.1.0
