---
name: pre-merge-check
description: Use before opening a PR, committing a substantive change, or declaring a feature done. Runs the mechanical Pre-merge Checklist from CONTRIBUTING.md — build, test, race, vet, changelog presence, description constant consistency. Triggers include "ready to commit", "about to open a PR", "ship this", "is this done", "final check". Always run this before telling the user a task is complete.
---

# pre-merge-check

Mechanical gate. If any step fails, do not tell the user the work is done — fix the failure first.

## Run in order

### 1. Build

```bash
go build ./...
```
Must exit 0 with no warnings. If it fails, fix before proceeding — no point running tests against broken code.

### 2. Full test suite

```bash
go test ./...
```
All green. New behavior must have new tests. If a test you didn't write fails, it's still your problem — investigate whether your change broke it.

### 3. Race detector

```bash
go test -race ./...
```
Required for any change touching `core/`, `engine.go`, `index/`, `graph/`, `storage/`, or anything with goroutines. Skip only for pure doc/comment/changelog PRs.

### 4. Vet

```bash
go vet ./...
```
No new warnings. Existing warnings are not your problem unless you introduced them.

### 5. Changelog presence

Confirm `CHANGELOG.md` has an entry under `[Unreleased]` that describes the diff. Categorize:

- **Added** — new tool, new endpoint, new feature
- **Changed** — behavior change (user-visible)
- **Deprecated** — marked-for-removal
- **Removed** — deletion of a tool/endpoint/feature
- **Fixed** — bug fix
- **Security** — vulnerability fix (CVE track)

Draft the entry now if missing. Do NOT bump the version — that's the maintainer's job at release time.

### 6. Description constant consistency

For each operation touched in the diff, verify the same `api.XxxDescription` constant is referenced in:
- `api/<cluster>.go` (definition)
- `server/bindings_<cluster>.go` (MCP server-side)
- `cli/mcp_proxy_<cluster>.go` (CLI proxy)

Quick check:
```
grep -rn 'XxxDescription' api/ server/ cli/
```
If any transport uses a string literal for description text instead of the constant, fix it. Agents see inconsistent help otherwise.

### 7. Loopback gates on destructive routes

If the diff added or modified HTTP routes that are destructive (backup, restore, export, import, merge, discard, reembed, delete, purge), confirm each has `if !isLoopback(r)` as the first check. Miss this and a remote caller can invoke destructive ops.

### 8. Lock discipline sanity

Scan the diff for:
- `a.engine.RLock()` or `a.engine.Lock()` followed by any of: `os.Open`, `os.Create`, `io.Copy`, `http.`, `.Embed(`, `.Generate(`, `tar.`, `gzip.`
- If found, the op is holding the lock across I/O. Fix with the three-phase pattern before merging.

### 9. Commit shape (only if about to commit)

Multi-paragraph commit message. Subject = imperative, ≤70 chars. Body explains *why*, not just *what*. Headers (`Critical:`, `High:`, etc.) for multi-issue commits. Model: commits `16f7693` and `df5ef52`.

## Output

Report a punch list: each item either ✓ or ✗ with the failure detail. If all ✓, tell the user the branch is ready to push/commit. If any ✗, do NOT report the work as complete — state what needs fixing.

## Scope guardrail

If the diff is trivial (typo, single-line doc change), steps 3/6/7/8 may not apply. Say so explicitly rather than silently skipping.
