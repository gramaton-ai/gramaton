---
name: gramaton-review
description: Use for code review of Gramaton changes — PR review, branch review, pre-merge review. Runs the Gramaton-specific anti-pattern checklist: lock-across-I/O, APIError.Message leakage, SwapGraph misuse, missing input validation, drifted description constants, hardcoded caps, missing loopback gates, swallowed parseJSON errors. Triggers include "review this PR", "review the current branch", "review these changes", "code review this". Complements the generic /review skill with project-specific checks.
---

# gramaton-review

Every check below came from a real bug. Don't skip any without an explicit reason.

The 12 checks below are necessary but not sufficient. After walking them, do a second pass with the framing: "what could go wrong here that's not on this list?" Most regressions in this codebase have come from refactor behavior-preservation gaps and vacuous tests — neither of which a mechanical checklist reliably catches. For diffs >200 lines or those that touch multiple subsystems, consider spawning 2-3 independent review agents in parallel with focused prompts (correctness / security / test coverage) and synthesizing findings.

## Setup

```bash
git diff main...HEAD                # full diff
git log main..HEAD --oneline        # commits in scope
```

Identify:
- Which `api/<cluster>.go` files changed
- Which `server/bindings_*.go` files changed
- Whether any `core/` primitives changed
- Whether the diff adds/modifies HTTP routes or MCP tools

## Anti-pattern checklist

Walk each. For every finding, report: file, line, what's wrong, how to fix, severity.

### 1. Engine lock held across I/O

Grep the diff for `engine.RLock()` or `engine.Lock()`. For each callsite, check whether the critical section contains:
- `os.Open`, `os.Create`, `os.WriteFile`, `ioutil.*`, `io.Copy`
- `http.`, `.Get(`, `.Post(`
- Any embedder or LLM call (`.Embed(`, `.Generate(`, provider.Chat, etc.)
- `tar.`, `gzip.`, `zip.`
- Any loop over many records that hits storage

**Severity: CRITICAL.** Remediation: three-phase pattern (CONTRIBUTING.md:462). The real-world cost: one-second op blocks all writers for one second.

### 2. `err.Error()` leaked into `APIError.Message`

Grep for `ErrInternal(fmt.Sprintf` and `ErrInternal(.*err.Error()` and `ErrInvalid(.*%v.*err`. `APIError.Message` goes to clients; leaking `err.Error()` surfaces file paths, library-specific formats, internal detail.

**Severity: HIGH.** Remediation: `a.log.Warn("context", "err", err)` and return a generic message like `ErrInternal("failed to write export response")`.

### 3. `SwapGraph` misuse

For every `SwapGraph` callsite (only expected in branch checkout/merge):
- [ ] The replacement graph was built with `graph.NewWithCapacity(..., graph.WithEdgeStore(a.engine.EdgeStore()))`, NOT bare `graph.New()`. Bare `graph.New()` creates a fresh `MemoryEdgeStore`; edges added afterwards silently bypass bbolt persistence.
- [ ] On-disk HEAD/ref writes happen BEFORE the `SwapGraph` call, not after. If an on-disk write fails after `SwapGraph`, in-memory and disk diverge.

**Severity: CRITICAL** (data-loss class).

### 4. Swallowed `parseJSON` errors on optional bodies

Grep for `_ = parseJSON` and `parseJSON(.*); //`. An optional body must use the `errEmptyBody` sentinel:

```go
if err := parseJSON(r, &body, maxJSONBodySize); err != nil && !errors.Is(err, errEmptyBody) {
    s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
    return
}
```

**Severity: MEDIUM.** Remediation: sentinel pattern above.

### 5. Missing input validation at the api layer

For every new or changed api method that takes user-controlled identifiers (branch name, session id, collection name, file path):
- Branch names → `core.ValidBranchName` or equivalent
- Paths → `filepath.Clean` + prefix check against configured parent dir
- Identifier shape → explicit regex or length check against `api/validate.go` constants

"The transport validates" is **not acceptable**. Transports are thin; validation lives in api.

**Severity: HIGH** (path-traversal and injection class).

### 6. Hardcoded caps instead of `api/validate.go` constants

Grep the diff for numeric limits: `limit > 500`, `if n > 100`, `make([]byte, 4096)`, etc. Compare against `api/validate.go` constants (`MaxSearchTop`, `MaxLogLimit`, `MaxKeywords`, ...). Each hardcoded cap should either reference a constant or justify why a new one is needed.

**Severity: LOW** (maintenance), but bundle the fix with the rest.

### 7. Description constant drift

For every MCP tool added or modified:
- `api/<cluster>.go` defines `XxxDescription` as an exported const
- `server/bindings_<cluster>.go` MCP registration references `api.XxxDescription`
- `cli/mcp_proxy_<cluster>.go` references `api.XxxDescription`

If any transport uses a string literal, the descriptions will drift and agents see inconsistent help.

**Severity: MEDIUM.**

### 8. Missing loopback gates

For every destructive HTTP route (backup/restore/export/import/merge/discard/reembed/delete/purge):
```go
if !isLoopback(r) {
    s.writeError(w, http.StatusForbidden, "forbidden", "loopback only", false)
    return
}
```
must be the first check.

**Severity: HIGH** (remote exploitation class).

### 9. Returned partial state alongside `APIError`

Grep for return statements of shape `return XxxResponse{...fields...}, ErrXxx(...)`. Contract: non-nil `APIError` means the op did not commit, so the response should be the zero value. Returning partial state leaks.

**Severity: MEDIUM.**

### 10. Test coverage gaps

For each new or changed api method:
- [ ] Happy path test exists
- [ ] At least one error-path test per distinct `ErrXxx` return
- [ ] If three-phase: `TestXDoesNotBlockWrites` + `TestXSnapshotConsistency` using snapshot hook
- [ ] If the change fixes a bug: a regression test with a name that describes the bug
- [ ] **The bug-pin test FAILS when you mentally apply the pre-fix code.** A test that passes both pre-fix and post-fix proves nothing about the fix.
- [ ] **The test FIXTURE actually exercises the fixed path.** Easy mistake: a test asserts behavior X but seeds inputs that go through behavior Y. Walk the test inputs through the new code branch-by-branch and confirm they reach the fix.

Vacuous tests are worse than missing tests because they create a false sense of coverage. Common shapes: assertions on absence ("no record leaked") with no positive complement; test setup that doesn't exercise the fixed path (e.g., asserts a keyword filter when the fixture has no matching keyword).

**Severity: varies.** Missing tests ≠ broken code, but no-tests merges cost future bugs.

### 11. Doc and skill drift

Two drift surfaces to check:

- **Design docs:** for every exported symbol or MCP tool added/renamed/removed, grep `docs/architecture.md` and `docs/project-design/*.md` for the old shape. Flag mentions for update.
- **Skills:** if the diff modifies `CONTRIBUTING.md`, grep `.claude/skills/` for references to the changed sections (most skills cite CONTRIBUTING.md by section or line). A skill that references a renamed/renumbered/semantically-changed section needs updating.

**Severity: LOW**, but flag for the author. Never edit a skill without explicit approval — see CLAUDE.md governance.

### 12. Changelog entry

`CHANGELOG.md` has an entry under `[Unreleased]` describing the change. Correct category.

**Severity: LOW**, but required for every PR.

### 13. Refactor preservation (when the diff merges/splits/restructures existing code)

Refactor signature: roughly equal `-N/+M` counts in the same file, or one function deleted while another was added/expanded. Refactors silently regress when a behavior path in the original code has no equivalent in the new structure.

For each substantially-modified function:
1. Read the pre-image (`git show main:<path>` or `git diff main...HEAD <path>`'s `-` lines) and list every distinct branch / output the original took — filter conditions, `continue` placements, conditional appends to result slices, side effects on shared state.
2. For each branch, find where in the new code that input shape is handled. Confirm the output is equivalent — same branch taken, same fields written, same side effects.
3. Flag any branch the new code drops or re-routes. If you can't trace it to an equivalent path, that's a regression.

Real example: a curation commit merged three for-loops into one. The original `it2` had Rule 1's `continue` INSIDE its inner `if contentShort == kw && hasFullContent` block, so concept nodes where Rule 1 didn't fire fell through to Rules 2/3. The merged loop's concept branch had `continue` at the OUTER level — concept nodes silently never reached Rules 2/3. No mechanical check caught it; only walking the original branches against the new structure did.

**Severity: HIGH** when found. Refactor regressions are silent and load-bearing.

## Output shape

Report as a grouped punch list:

```
CRITICAL
- <file>:<line>  <one-line summary>  → <remediation>

HIGH
- ...

MEDIUM / LOW
- ...

NITS
- ...

TESTS
- <what's missing>
```

If nothing found, say so explicitly — don't invent findings. End with an overall verdict: ready to merge / blocking issues / requires revision.

## Scope

This skill does code correctness and convention checks. For security-specific deep-dive, chain into `gramaton-security-review`.
