---
name: new-operation
description: Use when adding a new operation to the api/ package (new capture/search/branch/backup/etc method, new MCP tool, new HTTP endpoint). Scaffolds the typed Request/Response, XxxDescription constant, wires HTTP + MCP stdio + CLI MCP proxy transports, and stubs tests. Triggers include "add a new MCP tool for X", "new api operation", "add an endpoint for Y", "scaffold Z under api/". Do NOT use for migrating an existing inline handler — that's migrate-to-api.
---

# new-operation

The full recipe is in CONTRIBUTING.md under "Adding a feature: the recipe". This skill is the actionable checklist form. Read CONTRIBUTING.md once if you haven't; then use this as the per-step enforcer.

## Before you write code

Decide and say out loud:

1. **Cluster** — which existing cluster does this belong to? (admin, collections, history, maintenance, records, search, sessions.) If none fits, that's a design question — stop and discuss with the user.
2. **Verb shape** — noun-verb, imperative. `BackupCreate`, `BranchCheckout`, `Reembed`. Match siblings in the cluster.
3. **Destructive?** — if yes, you'll need a loopback gate on the HTTP route.
4. **Slow work?** — if yes (disk I/O, network, embedding), use the three-phase lock pattern, not straight `Lock()`. See CONTRIBUTING.md:462.

## Files to touch

For cluster `<cluster>` and operation `Example`:

| File | What to add |
|---|---|
| `api/<cluster>.go` | `ExampleRequest`, `ExampleResponse`, `ExampleDescription` const, `func (a *API) Example(ctx, req) (ExampleResponse, *APIError)` |
| `server/bindings_<cluster>.go` | HTTP route handler + MCP tool registration in `registerXxxMCPTools` |
| `cli/mcp_proxy_<cluster>.go` | CLI proxy registration using `proxyPost("/v1/...", args)` |
| `server/bindings_<cluster>_test.go` | Happy path + at least one error-path test |
| `CHANGELOG.md` | Entry under `[Unreleased]` → `### Added` |

## Enforceable conventions

Before declaring done, verify each:

- [ ] `Request`/`Response` are **typed structs**. No `map[string]any`. No `interface{}` return. Every field has a `json:"..."` tag; every field an agent can set has a `jsonschema:"..."` tag with bounds/units where applicable.
- [ ] First method parameter is `ctx context.Context`, even if unused inside.
- [ ] Return type is `(<Response>, *APIError)`. Non-nil `APIError` → operation did not commit. No partial state.
- [ ] Error helpers come from `api/errors.go`: `ErrMissing`, `ErrInvalid`, `ErrNotFound`, `ErrConflict`, `ErrUnavailable`, `ErrForbidden`, `ErrInternal`, `ErrPrepareRequired`. Never `fmt.Errorf` or a raw `&APIError{...}`.
- [ ] `APIError.Message` never contains `err.Error()`. Log the underlying err with `a.log.Warn("...", "err", err)`; return a generic message. (CONTRIBUTING.md:595)
- [ ] Input caps reference `api/validate.go` constants (`MaxLogLimit`, `MaxSearchTop`, etc.). If you need a new cap, add it there with a comment explaining the bound.
- [ ] User-controlled identifiers are validated at the api layer (e.g. `core.ValidBranchName`). Not at the transport.
- [ ] User-supplied filesystem paths are `filepath.Clean`'d and prefix-checked against a safe parent.
- [ ] `ExampleDescription` is an **exported const** in the api file. HTTP binding may not read it; MCP server-side and CLI proxy both must.
- [ ] MCP tool registration uses `api.ExampleDescription`, not a string literal. Same in the CLI proxy. If the two drift, agents see inconsistent help.
- [ ] Destructive HTTP routes gate with `if !isLoopback(r) { return 403 }`.
- [ ] Optional HTTP body uses the `errEmptyBody` sentinel pattern (CONTRIBUTING.md:640); never `_ = parseJSON(...)`.

## Lock discipline

Pick one:

- **RLock + fast** — `a.engine.RLock(); defer a.engine.RUnlock()`. For read-only, no-I/O ops.
- **Lock + fast** — `a.engine.Lock(); defer a.engine.Unlock()`. For write ops with millisecond work.
- **Three-phase** — snapshot under RLock, slow work off-lock, apply under Lock. For anything touching disk, network, or embeddings. Canonical examples: `api.BackupCreate`, `api.BranchCheckout`, `api.Reembed`.

If you use three-phase, add a `Set<Op>SnapshotHook(chan)` so tests can race deterministically (see `api.BackupCreate` for the pattern).

## Tests

In `server/bindings_<cluster>_test.go`:

- Use `setupTestServer(t)` → `(*Server, *core.Engine)`.
- Seed with `addRecord(t, eng, "...")` as needed.
- Call the api method directly: `srv.api.Example(context.Background(), api.ExampleRequest{...})`.
- Assert on typed fields, not stringified output.
- Add at least one error-path test per error code the method returns (e.g. empty id → `ErrMissing`, bad value → `ErrInvalid`).
- If three-phase: add `TestXDoesNotBlockWrites` using the snapshot hook, and if state changes, `TestXSnapshotConsistency`.

## Done check

Run `gramaton-review` and `gramaton-security-review`, then `pre-merge-check`, before declaring finished. The mechanical gate alone won't catch convention slips or behavior-preservation gaps — the reviews are where those surface.
