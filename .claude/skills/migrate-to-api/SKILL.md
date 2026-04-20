---
name: migrate-to-api
description: Use when moving an existing inline HTTP/MCP handler (server/handler_X.go, server/mcp_X.go) into the api/ canonical surface. One-time-per-cluster work; mirrors PR #3 (commit 16f7693) admin migration. Triggers include "migrate the X cluster to api", "move handler_X.go under api/", "migrate inline handlers for Y". Do NOT use for greenfield operations — that's new-operation. This skill will retire once remaining handlers are migrated.
---

# migrate-to-api

You are repeating the shape of PR #3 (the admin-cluster migration). Read commits `16f7693` and `df5ef52` if you haven't — they're the canonical reference for what this migration looks like when done correctly.

## Scope check

Confirm with the user which cluster is being migrated. Cluster name should match an existing `server/handler_<cluster>.go` or `server/mcp_<cluster>.go` file set, e.g. `records`, `search`, `sessions`.

## Order of operations

Do this in order. Don't skip ahead; each step informs the next.

### 1. Inventory

List every operation currently inline:
- HTTP routes in `server/handler_<cluster>.go` (note paths, methods, request/response shapes)
- MCP tools in `server/mcp_<cluster>.go` or `server/mcp.go` registrations for this cluster
- CLI proxy registrations in `cli/mcp_proxy.go` that forward to those routes

For each operation, note: lock discipline, whether it does I/O, whether it's destructive.

### 2. Write `api/<cluster>.go`

For each operation, follow the `new-operation` skill checklist. Typed Request/Response, `XxxDescription` constant, error helpers from `api/errors.go`, validation caps from `api/validate.go`. Lock discipline:

- If the current inline handler holds a lock across I/O, this migration is where you fix that — use the three-phase pattern with a snapshot hook.
- If it already has correct lock discipline, just preserve it.

### 3. Add supporting primitives if needed

The admin migration added `core.SwapGraph` and `backup.CreateSnapshot` as new engine-level primitives. If your cluster needs similar (e.g. to split an op into snapshot + slow + apply phases), those primitives go in `core/` or the relevant subsystem package, NOT in `api/`.

**`SwapGraph` hazards** (from CONTRIBUTING.md:606):
- Must share `a.engine.EdgeStore()` with the new graph (don't use bare `graph.New()`).
- On-disk HEAD/ref writes must precede the `SwapGraph` call, not follow it.

### 4. Write `server/bindings_<cluster>.go`

Thin transport shims. Each route:
```go
var req api.XxxRequest
if err := parseJSON(r, &req, maxJSONBodySize); err != nil { ... }
result, apiErr := s.api.Xxx(r.Context(), req)
if apiErr != nil { s.writeAPIError(w, apiErr); return }
s.writeJSON(w, http.StatusOK, result)
```

Loopback-gate destructive routes. MCP tool registrations use `api.XxxDescription`, not string literals.

### 5. Write `cli/mcp_proxy_<cluster>.go`

One `registerXxxProxy(s)` function. Each tool uses `api.XxxDescription`. Forward with `proxyPost("/v1/...", args)`.

### 6. Delete the dead code

Don't leave half-replaced handlers. Delete:
- `server/handler_<cluster>.go`
- `server/mcp_<cluster>.go` (if it exists as a separate file)
- Shadowed proxy registrations in `cli/mcp_proxy.go`
- Service helpers that only the old handler called
- Test utilities that no longer have callers

PR #3 deleted ~1200 LOC while adding ~1300. Your migration should produce a similar "more tests, less code" outcome.

### 7. Write tests

In `server/bindings_<cluster>_test.go`:
- Happy path per operation
- Per-error-path: at least one test per distinct `ErrXxx` the method returns
- If three-phase: `TestXDoesNotBlockWrites` + `TestXSnapshotConsistency` using the snapshot hook
- Regression test for any bug the migration fixes along the way (see `TestBranchCheckoutEdgeStorePersistence` in PR #3)

### 8. Commit message

Multi-paragraph. Subject: `Migrate <cluster> cluster to unified api`. Body sections:
- Motivation (why this cluster, why now)
- Approach (three-phase? what primitives added?)
- Bugs fixed along the way (if any — cite with `Fixed:` header)
- Follow-ups (if any cross-PR work remains)

Model: commits `16f7693` and `df5ef52`.

### 9. Changelog

`[Unreleased]` → `### Changed` entry naming the cluster. If you fixed bugs along the way, separate `### Fixed` entries.

## Done check

- Run `pre-merge-check`.
- Run `gramaton-review` on your own branch before pushing.
- If any op touches filesystem paths or user-controlled identifiers, also run `gramaton-security-review`.
