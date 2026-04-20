---
name: gramaton-security-review
description: Use for security review of Gramaton changes. Checks Gramaton-specific vulnerability classes: path traversal in user-supplied filesystem paths, loopback gates on destructive endpoints, input validation at the api layer, APIError.Message information leakage, optional-body parse sentinels, SwapGraph + edge-store sharing, MCP tool arg bounds. Triggers include "security review this branch", "security review this PR", "audit for security issues", or when the diff touches filesystem paths/auth gates/user identifiers/error surfaces. Extends the generic /security-review with Gramaton-specific checks.
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

## Output

Group findings by severity (CRITICAL / HIGH / MEDIUM / LOW), file:line, one-line summary, remediation. Explicitly state "no findings" if nothing turned up — don't invent issues.

End with a recommendation: safe to merge / requires fixes / requires design discussion.

## Boundary with `gramaton-review`

This skill goes deep on vulnerability classes. General code-quality review (test coverage, description drift, hardcoded caps, lock-across-I/O as a correctness-not-security issue) lives in `gramaton-review`. Run both for destructive or auth-sensitive PRs.
