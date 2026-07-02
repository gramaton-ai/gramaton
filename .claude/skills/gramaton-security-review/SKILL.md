---
name: gramaton-security-review
description: Use for security review of Gramaton changes. Checks Gramaton-specific vulnerability classes: path traversal in user-supplied filesystem paths, loopback gates on destructive endpoints, input validation at the api layer, APIError.Message information leakage, optional-body parse sentinels, SwapGraph + edge-store sharing, MCP tool arg bounds, and public-repo hygiene (maintainer PII, employer name, third-party attribution, reputational tone, inclusive language, internal tracker / phase identifiers). Triggers include "security review this branch", "security review this PR", "audit for security issues", or when the diff touches filesystem paths/auth gates/user identifiers/error surfaces/prose-bearing files. Extends the generic /security-review with Gramaton-specific checks.
---

# gramaton-security-review

Focused on Gramaton's concrete attack surface. Each check maps to a documented past vulnerability or an anti-pattern class in CONTRIBUTING.md:574.

## Setup

```bash
git diff main...HEAD
```

Classify the diff:
- Touches filesystem paths? → run section 1
- Adds HTTP routes or MCP tools? → run sections 2 and 3
- Touches user-controlled identifiers (branch names, session ids, collection names)? → run section 4
- Touches error construction or error-return paths? → run section 5
- Touches `SwapGraph` or graph replacement? → run section 6
- Touches any prose-bearing file (comments, docs, README, CHANGELOG, fixtures, sample configs, test names, commit message body)? → run section 9

Run all sections anyway if the diff is substantial.

## 1. Path traversal / unconfined filesystem paths

**Past incident:** `api.BackupRestore` originally accepted an absolute `.tar.gz` path with no confinement, letting a caller restore from any file on the host.

Checks:
- [ ] Every api method that accepts a path argument runs `filepath.Clean` on it.
- [ ] Every cleaned path is prefix-checked against a safe parent directory (e.g. the configured backup dir, the configured data dir). Prefix check uses `strings.HasPrefix(cleaned, parent+string(os.PathSeparator))` — not bare `HasPrefix(cleaned, parent)` (which would match `/data` against `/data-evil/foo`).
- [ ] Symlink escape considered. If the op follows symlinks, ensure the resolved target is also within the confinement boundary. `filepath.EvalSymlinks` where relevant.
- [ ] Relative paths are resolved against a *fixed* base, not `os.Getwd()`.

Path args come in through:
- HTTP JSON body fields
- MCP tool arguments (destination/source paths)
- CLI flags that forward to the api

**Severity: CRITICAL** (host-compromise class).

## 2. Loopback gates on destructive HTTP routes

**Rule:** every HTTP route that writes, overwrites, exports, imports, or destroys state MUST start with a loopback check.

Destructive route indicators: path contains `backup`, `restore`, `export`, `import`, `merge`, `discard`, `reembed`, `delete`, `purge`, `branch/create`, `branch/checkout`, `collection/delete`, `collection/migrate`.

```go
if !isLoopback(r) {
    s.writeError(w, http.StatusForbidden, "forbidden", "loopback only", false)
    return
}
```

Checks:
- [ ] Gate is the **first** statement in the handler, before any body parsing or state touching.
- [ ] Gate uses `isLoopback(r)`, not a regex on `r.RemoteAddr` (which is spoof-prone behind proxies).
- [ ] The global `/mcp` endpoint gate is intact (shouldn't be disabled by the diff).

**Severity: HIGH** (remote exploitation class).

## 3. MCP tool argument bounds

For every MCP tool added or modified:

- [ ] Every `jsonschema` tag on arg fields has sensible bounds (max length, max items, min/max numeric). An LLM agent will pass whatever the schema allows; unbounded means DoS surface.
- [ ] Integer/size limits reference `api/validate.go` constants.
- [ ] String length limits on identifiers, paths, text blobs.
- [ ] Array fields have a max item count.
- [ ] Tool description does NOT document destructive side effects as harmless — if an op deletes, the description should say so. Under-documentation is a social-engineering risk for the calling agent.

**Severity: MEDIUM.**

## 4. Input validation at the api layer

**Rule:** user-controlled identifiers are validated in the api method, not the transport.

- [ ] Branch names pass `core.ValidBranchName` (or equivalent regex — allowed chars, max length, reserved names rejected).
- [ ] Session ids validated for shape (typically UUID-like or a known format).
- [ ] Collection names validated.
- [ ] Record ids, edge ids validated.
- [ ] Validation is done at **every** entry point — if five sibling methods take the same kind of identifier, all five validate. "The caller will validate" is how bugs land.
- [ ] If the change adds or modifies a validator, the regression test feeds DIRTY input and asserts CLEAN output (or rejection). A test that asserts validation behavior but seeds already-clean input proves nothing — and won't fail when the validator silently degrades.

**Past incident pattern:** skipped validation on one of N sibling methods → attacker uses that method as the entry point.

**Severity: HIGH.**

## 5. Information leakage through `APIError.Message`

**Past incident:** `BackupExport` returned `ErrInternal(fmt.Sprintf("write response: %v", err))` which leaked `io.Writer` error detail (including internal paths) to clients.

Checks:
- [ ] No `ErrXxx(...err.Error()...)` or `fmt.Sprintf(..."%v"..., err)` passed into an APIError constructor.
- [ ] No file paths, memory addresses, stack frames, library-specific strings appear in APIError messages.
- [ ] Underlying errors are logged with `a.log.Warn("context", "err", err)` and the API message is generic ("failed to write export response", not `io.Writer` detail).
- [ ] `APIError.Cause` may hold the raw err for `errors.Is`/`errors.As`, but the Cause is never serialized to clients — verify that's still true (nothing in the diff changed `writeAPIError`/`mcpAPIErr` to emit Cause).

**Severity: MEDIUM** (info disclosure class).

## 6. `SwapGraph` correctness

Graph replacement ops have unique invariants:

- [ ] Replacement graph is built with `graph.NewWithCapacity(..., graph.WithEdgeStore(a.engine.EdgeStore()))`. Bare `graph.New()` creates a fresh `MemoryEdgeStore`; subsequent edge writes silently bypass bbolt persistence → edges lost on restart.
- [ ] On-disk HEAD/ref writes happen BEFORE `SwapGraph`. If an on-disk write fails AFTER `SwapGraph`, in-memory and disk diverge and a retry may make the broken state stick.
- [ ] `SwapGraph` is called under `engine.Lock()`, never under `RLock()` or off-lock.

**Severity: CRITICAL** (data-loss class, not traditional infosec but same severity rating).

## 7. Optional body sentinel

- [ ] Every HTTP handler that accepts an optional body uses `errors.Is(err, errEmptyBody)` to distinguish empty from malformed. Silent error swallow (`_ = parseJSON(...)`) hides 400-worthy malformed input.

**Severity: MEDIUM.**

## 8. Secrets / API key hygiene

- [ ] No API keys, credentials, or tokens introduced in the diff (env var references OK, literal values not).
- [ ] If config files were added, they do not include placeholder values that look like real secrets.
- [ ] `~/.gramaton/config.yaml` is not committed.

**Severity: HIGH** if violated.

## 9. Public-repo hygiene (maintainer PII / employer / attribution / tone)

This repo is public. Source, tests, fixtures, README, CHANGELOG, and commit-message bodies are all visible. The diff must not introduce content the maintainer would not be comfortable publishing.

**Hot-button identifiers — resolve at runtime, do NOT spell out here.** This skill is itself committed; hardcoding the strings would defeat the check. Pull the actual values from the maintainer's global CLAUDE.md and auto-memory entries (notably `feedback_employer_privacy`, `feedback_legal_attribution`, `feedback_api_key_safety`, and the user-profile entry for personal contact info). If you can't access those, ask the maintainer for the grep list before proceeding.

Grep the diff (across code, tests, fixtures, docs, README, CHANGELOG, and the pending commit-message body) for:
- [ ] Maintainer's employer name(s) or any product/team name attributable to that employer. Bright-line: must not land in this repo.
- [ ] Maintainer's personal email or other contact identifiers.
- [ ] Absolute home-directory paths (`/Users/<name>/...`, `/home/<name>/...`) in fixtures, sample configs, comments, or test data. Replace with `$HOME` or a stub.
- [ ] Machine names, hostnames, or LAN identifiers leaked from logs or screenshots.

**Third-party / external project names** (judgment, not grep):
- [ ] Newly-introduced names of companies, products, models, or projects that did not previously appear in the repo. Per `feedback_legal_attribution`, named references should be cleared with the maintainer when there's any trademark, comparative-claim, or license-implied attribution question.
- [ ] Comparative or evaluative claims about named third parties ("X is slow", "Y gets this wrong"). Either remove the name, soften to neutral phrasing, or escalate.

**Reputational tone** (read added prose, not grep):
- [ ] Comments, doc prose, error messages, test names, CHANGELOG entries, and commit-message bodies contain no snark, ad hominem, or named-party criticism. CHANGELOG entries are public release notes — same bar as docs.
- [ ] No internal-chat-sounding phrasing in public docs/comments: "ping me", "@here", "let's huddle", "circle back", "FWIW just shipping this", "yolo".
- [ ] No mentions of internal teams, internal meeting names, internal product nicknames, or internal architecture terms attributable to any employer.
- [ ] No defensive comparisons to specific commercial products by name ("better than X", "unlike Y"). Class-level comparisons are fine ("vs vector stores generally", "unlike most agent-memory tools").
- [ ] No casual swearing in user-facing prose, comments, or test names. (Internal-feeling expletives are tonally jarring in public OSS.)

**Fixture / sample-data hygiene:**
- [ ] Capture examples, session transcripts, README walkthroughs, and test fixtures contain only synthetic content. Real conversations or records from the maintainer's personal store must not be copied in.

**Internal tracker / phase identifiers** (grep-driven):

Gramaton-development-internal markers that are cryptic to public readers and make the docs feel like internal artifacts that escaped. None of these leak privacy or security, but they're public-readiness clutter.

- [ ] No `T-NN` (e.g. `T-02`, `T-08`) — internal task IDs.
- [ ] No `P[0-3]-NN` (e.g. `P2-06`, `P1-78`) — phase ticket IDs.
- [ ] No `F[0-9] Layer N` or `F1-Layer N` (e.g. `F1 Layer 6`) — feature-phase markers.
- [ ] No `"Phase N follow-on"`, `"Wave N"`-style multi-commit-series labels.
- [ ] No raw 26-char Crockford-base32 ULIDs (`01[A-HJKMNP-TV-Z0-9]{24}`) used as `Tracker:` / `tracker` references in comments, CHANGELOG entries, or doc prose.

Apply across: `*.go` comments (production AND test), `CHANGELOG.md`, `docs/**/*.md`, `api/guide/*.md`, `README.md`, `HOW_TO_USE_GRAMATON.md`, `CONTRIBUTING.md`, `integration/**/*.md`, commit-message bodies in scope.

**Distinguish from things that should NOT be scrubbed:**
- `D-NN` (`D40`, `D33`) used as section / decision-record headings in `docs/project-design/design-decisions.md` — those are doc-internal anchors, not tracker references.
- `Phase 0/1/2/3` describing **algorithmic phases** of operations (e.g., "Phase 1: snapshot under read lock; Phase 2: do slow work off-lock") — those describe code structure, not project history.
- Test fixture content where a ULID or `T-NN` string is the literal *value* being stored / asserted (record id, content_full, title field) rather than a tracker reference in a comment. Test data, not tracker references.
- Example ULIDs in docs that explain the ULID format (e.g., `01H5K9E2GJ7A8NQXR5VT3M4BCW` shown as "this is what a ULID looks like").

When stripping a tracker reference, preserve the surrounding technical context. e.g., `// Tracker 01K... covers the unrelated dedup follow-up` → `// the unrelated dedup follow-up is covered separately`. Don't just delete sentences that mentioned a tracker — rewrite to keep the meaning.

**Inclusive language** (grep-driven):

Standard public-OSS inclusive-language scrub. Apply to code comments, identifier names (variables / functions / types), test names, doc prose, CHANGELOG entries, and commit-message bodies. Third-party-dep symbol names you're calling are out of scope; only Gramaton-authored content.

- [ ] `whitelist` / `blacklist` → `allowlist` / `blocklist`.
- [ ] `master` / `slave` → `primary` / `replica` (DB / replication), `main` / `branch` (version control), or `controller` / `worker` (job queues).
- [ ] `dummy` (placeholder) → `placeholder` or `stub`. (`dummy data` → `sample data` or `synthetic data`.)
- [ ] `sanity check` → `smoke test`, `quick check`, or `confidence check`.
- [ ] `grandfathered` → `legacy exception` or `pre-existing`.
- [ ] `man-hours` / `man-day` → `person-hours` or `engineering-hours`.

**Severity:**
- **HIGH** for any maintainer-PII, employer-name, or API-key leak (bright-line).
- **MEDIUM** for unvetted third-party references, reputational-tone issues, and inclusive-language violations — flag and ask, don't silently proceed.
- **LOW** for internal tracker / phase identifiers — clutter, not danger; flag for batch cleanup.

## Output

Group findings by severity (CRITICAL / HIGH / MEDIUM / LOW), file:line, one-line summary, remediation. Explicitly state "no findings" if nothing turned up — don't invent issues.

End with a recommendation: safe to merge / requires fixes / requires design discussion.

## Boundary with `gramaton-review`

This skill goes deep on vulnerability classes (including public-repo hygiene). General code-quality review (test coverage, description drift, hardcoded caps, lock-across-I/O as a correctness-not-security issue) lives in `gramaton-review`. Run both for destructive or auth-sensitive PRs.
