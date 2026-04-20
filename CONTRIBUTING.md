# Contributing to Gramaton

Thanks for thinking about contributing. This document is the entry
point for anyone landing code in the repo -- whether that's a typo
fix, a bug report with a patch, or a meaningful feature.

A few things up front:

- **Project state**: alpha. The on-disk format, the API surface, and
  the MCP tool list will all still move. We try to flag breaking
  changes in the [CHANGELOG](CHANGELOG.md), but you should expect
  rough edges, occasional re-architecture, and a thin documentation
  trail in places.
- **License**: Apache 2.0. By submitting a pull request, you agree
  that your contribution is licensed under [Apache License,
  Version 2.0](LICENSE) -- the same license as the project. No
  separate CLA is required. (If you want to add an extra IP
  provenance signal, sign your commits with `git commit -s` -- it's
  not required today, just appreciated.)
- **Code of conduct**: be kind, assume good faith, focus on the
  work. Detailed CODE_OF_CONDUCT is forthcoming; in the meantime
  the [Contributor Covenant v2.1](https://www.contributor-covenant.org/version/2/1/code_of_conduct/)
  applies.
- **Scope**: contributions of any size are welcome -- typo fixes
  through major features. For anything beyond a small bug fix,
  **open an issue first to discuss the approach** before sending
  a PR. We'd rather sketch the design together than ask you to
  rework finished code.
- **LLM contributors**: if you work with an agentic LLM coding
  assistant, this repo ships conventions under
  [.claude/skills/](.claude/skills/) that encode the recipes below
  in invocable form. Claude Code auto-discovers them; other tools
  reading agent-skill markdown can adapt. See
  [CLAUDE.md](CLAUDE.md) for a short skill index.

The rest of this document covers, in order:

1. [How Gramaton is shaped (5-minute orientation)](#how-gramaton-is-shaped)
2. [Setting up your development environment](#setting-up-your-development-environment)
3. [Adding a feature: the recipe](#adding-a-feature-the-recipe)
4. [Worked example](#worked-example-the-admin-cluster-migration-pr-3)
5. [Patterns to reuse](#patterns-to-reuse)
6. [Anti-patterns we've learned the hard way](#anti-patterns-weve-learned-the-hard-way)
7. [Pre-merge checklist](#pre-merge-checklist)
8. [Commit and PR conventions](#commit-and-pr-conventions)
9. [Reporting bugs and getting help](#reporting-bugs-and-getting-help)

---

## How Gramaton is shaped

The full architecture lives in [docs/architecture.md](docs/architecture.md);
the design rationale is under [docs/project-design/](docs/project-design/).
This section is the minimum you need to find your way around.

### Layered packages

```
                  ┌──────────────┐
   user input →   │  CLI / MCP   │   protocol layer (CLI, MCP via stdio,
                  └──────┬───────┘   MCP via Streamable HTTP)
                         │ HTTP
                  ┌──────┴───────┐
                  │   Server     │   thin transport bindings; loopback
                  │ (transports) │   gates; request parsing
                  └──────┬───────┘
                         │ Go function call
                  ┌──────┴───────┐
                  │     api/     │   canonical operation surface --
                  │   package    │   typed Request/Response, errors,
                  └──────┬───────┘   lock discipline
                         │
                  ┌──────┴───────┐
                  │     core/    │   engine: graph, storage, indexes,
                  │    Engine    │   embeddings, LLM, search
                  └──────────────┘
```

The two most important things to internalise:

1. **The `api/` package is the canonical surface.** Every operation
   (capture, search, branch, backup, ...) is a method on `*api.API`
   with a typed `Request` and a typed `Response`. The HTTP server,
   the MCP stdio handler, and the CLI MCP proxy are all thin shims
   that translate transport-specific details into an `api.X(ctx,
   req)` call. Operations do **not** live in the transport layer.

2. **The engine has one big read/write lock.** `engine.RLock()` for
   reads, `engine.Lock()` for writes. Holding either across slow
   work (disk I/O, network, embedding) blocks the world. There's a
   well-defined three-phase pattern for splitting expensive ops --
   see [Patterns to reuse](#patterns-to-reuse) below. **Never hold
   any engine lock across I/O without a deliberate reason.**

### Where to find things

| You want to... | Look in... |
|---|---|
| Add or change an operation | `api/<cluster>.go` (e.g. `api/branches.go`, `api/sessions.go`) |
| Add or change an HTTP route or MCP tool | `server/bindings_<cluster>.go` |
| Add or change the CLI proxy that exposes an MCP tool to agents | `cli/mcp_proxy_<cluster>.go` |
| Touch the graph, storage, embeddings, or search | `core/`, `graph/`, `storage/`, `index/`, `embed/`, `search/` |
| Add or change validation caps + helpers | `api/validate.go` |
| Add or change error helpers | `api/errors.go` |
| Add a tool description shared by HTTP + MCP + CLI proxy | `api/<cluster>.go` -- exported `XxxDescription` constant |
| Update the changelog | `CHANGELOG.md` (under `[Unreleased]`) |

There are also `docs/` (developer-facing reference) and
`docs/project-design/` (the research/spec/rationale corpus). When
in doubt, those are the source of truth for "why is it like this?"

---

## Setting up your development environment

**Required:**

- Go 1.26 or newer (`go version`)
- A Unix-like filesystem (macOS or Linux). Windows isn't tested.

**Optional, depending on what you're working on:**

- An Ollama install for local embeddings/LLM (`brew install ollama`
  on macOS).
- An Anthropic, OpenAI, or AWS Bedrock account for the
  corresponding LLM/embedding providers.

**Build and test:**

```bash
git clone https://github.com/<your-fork>/gramaton.git
cd gramaton
go build .                  # produces ./gramaton
go test ./...               # full suite, ~5 minutes on a fast laptop
go test -race ./...         # race detector, slower
```

**Run locally:**

```bash
./gramaton init              # one-time config setup, picks providers
./gramaton serve --fg        # foreground daemon; Ctrl-C to stop
```

The default config writes to `~/.gramaton/`. To run against a clean
test store without touching your real data:

```bash
GRAMATON_CONFIG=/tmp/grama-test ./gramaton init
GRAMATON_CONFIG=/tmp/grama-test ./gramaton serve --fg
```

**Useful while developing:**

```bash
go test -v ./api/...                        # specific package, verbose
go test -run TestBackupSnapshotConsistency ./server/  # single test
go test -count=10 -race ./server/           # flush out flakiness
go vet ./...                                # static analysis
```

---

## Adding a feature: the recipe

For anything that touches the operation surface, follow this
shape. Five steps; each step has a "do this / don't do that"
checkpoint.

### Step 1 -- Design the api/ surface

Before writing any code, decide:

1. **What's the operation called?** Imperative verb. `BranchCheckout`,
   `BackupCreate`, `Reembed`. Use a noun-verb shape consistent with
   the existing cluster (`Curation*`, `Branch*`, `Collection*`).
2. **What does the request carry?** A typed struct, all fields
   tagged for both JSON and (where applicable) JSON Schema.
3. **What does the response shape look like?** Another typed
   struct. **No `map[string]any`.** If the response could be one of
   several shapes, use separate methods rather than a sum type.
4. **What error codes can it return?** Pick from the existing
   taxonomy in `api/errors.go`. Add a new helper only if no
   existing one fits.
5. **What's the MCP tool description?** Write a single sentence
   that an LLM agent reading the tool list will understand. This
   becomes a shared `XxxDescription` constant.

The skeleton looks like this:

```go
// api/example.go

// ExampleRequest is what callers send.
type ExampleRequest struct {
    ID    string `json:"id" jsonschema:"required record ID"`
    Limit int    `json:"limit,omitempty" jsonschema:"max results (default 50, max 500)"`
}

// ExampleResponse is what callers get back. Always typed; never
// map[string]any.
type ExampleResponse struct {
    Results []ExampleEntry `json:"results"`
    Total   int            `json:"total"`
}

// ExampleEntry is one row in the result.
type ExampleEntry struct {
    Field string `json:"field"`
}

// ExampleDescription is shared by HTTP, MCP, and CLI proxy. One
// short sentence that an LLM agent will understand.
const ExampleDescription = "Do the example operation. Returns up to limit results."

// Example does the thing.
func (a *API) Example(ctx context.Context, req ExampleRequest) (ExampleResponse, *APIError) {
    // 1. Validate.
    if req.ID == "" {
        return ExampleResponse{}, ErrMissing("id is required")
    }
    if req.Limit <= 0 {
        req.Limit = 50
    }
    if req.Limit > MaxLogLimit {
        req.Limit = MaxLogLimit
    }

    // 2. Acquire the right lock (see Step 2).
    a.engine.RLock()
    defer a.engine.RUnlock()

    // 3. Do the work.
    // ...

    return ExampleResponse{ /* ... */ }, nil
}
```

**Conventions:**

- The first parameter is always `ctx context.Context`, even when
  unused -- transports propagate cancellation, and methods that
  may grow cancel-aware behaviour shouldn't change shape later.
- The return is always `(<TypedResponse>, *APIError)`. **A non-nil
  `*APIError` means the operation did not commit.** Don't return
  partial state alongside an error -- it leaks.
- Validation caps live in `api/validate.go` (e.g. `MaxLogLimit`,
  `MaxKeywords`). Reference them; don't redefine.
- Descriptions are exported constants so HTTP, MCP, and CLI proxy
  all read the same string.

### Step 2 -- Decide lock discipline

For every operation, ask: **how long does it hold the engine
lock, and what does it do while holding it?**

The rules:

1. **Read-only operation, no I/O**: take `RLock`, do the work,
   release. Examples: `BranchList`, `Stats`.
2. **Write operation, no I/O**: take `Lock`, do the work, release.
   Examples: `BranchCreate`, `BranchDiscard`. Lock-hold should be
   measured in milliseconds.
3. **Write operation that needs I/O or expensive computation**:
   use the **three-phase pattern** (snapshot under RLock, work
   off-lock, apply under Lock). Examples: `BackupCreate`,
   `BranchCheckout`, `Reembed`. See [Patterns to
   reuse](#patterns-to-reuse) for the canonical form.

**Never hold an engine lock across `os.Open`, `io.Copy`,
`http.Get`, an embedder call, or anything else with unbounded
latency.** The engine lock is the most contended resource in the
process; everything else waits for it.

### Step 3 -- Wire the transports

Three transports consume `api/` methods. Add a binding to each
that's relevant to your operation.

**HTTP** (`server/bindings_<cluster>.go`):

```go
mux.HandleFunc("POST /v1/example", func(w http.ResponseWriter, r *http.Request) {
    var req api.ExampleRequest
    if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
        s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
        return
    }
    result, apiErr := s.api.Example(r.Context(), req)
    if apiErr != nil {
        s.writeAPIError(w, apiErr)
        return
    }
    s.writeJSON(w, http.StatusOK, result)
})
```

**MCP stdio** (same file, `registerXxxMCPTools` function):

```go
type exampleArgs struct {
    ID    string `json:"id" jsonschema:"record ID"`
    Limit int    `json:"limit,omitempty" jsonschema:"max results (default 50, max 500)"`
}
mcp.AddTool(mcpServer, &mcp.Tool{
    Name:        "gramaton_example",
    Description: api.ExampleDescription,
}, func(ctx context.Context, _ *mcp.CallToolRequest, args exampleArgs) (*mcp.CallToolResult, any, error) {
    done := s.mcpToolStart("gramaton_example")
    defer done(nil)
    result, apiErr := s.api.Example(ctx, api.ExampleRequest{ID: args.ID, Limit: args.Limit})
    if apiErr != nil {
        return mcpAPIErr(apiErr)
    }
    return mcpJSONResult(result)
})
```

**CLI MCP proxy** (`cli/mcp_proxy_<cluster>.go`):

```go
type proxyExampleInput struct {
    ID    string `json:"id" jsonschema:"record ID"`
    Limit int    `json:"limit,omitempty" jsonschema:"max results (default 50, max 500)"`
}

func registerExampleProxy(s *mcp.Server) {
    mcp.AddTool(s, &mcp.Tool{
        Name:        "gramaton_example",
        Description: api.ExampleDescription,
    }, func(ctx context.Context, req *mcp.CallToolRequest, args proxyExampleInput) (*mcp.CallToolResult, any, error) {
        return proxyPost("/v1/example", args)
    })
}
```

**Transport gates:** if the operation is destructive
(backup, restore, branch merge/discard, reembed) gate the HTTP
binding with `isLoopback(r)` and return 403 for non-loopback
callers. The MCP `/mcp` endpoint is also loopback-gated globally.

### Step 4 -- Write tests

Tests live alongside the binding (`server/bindings_<cluster>_test.go`)
or in the api package itself when they don't need the full server
setup.

The standard helper is `setupTestServer(t)` (in
`server/server_test.go`), which gives you a `*Server` plus a
`*core.Engine` configured for a temp directory. From there:

```go
func TestExampleHappyPath(t *testing.T) {
    srv, eng := setupTestServer(t)
    addRecord(t, eng, "seed")

    result, apiErr := srv.api.Example(context.Background(), api.ExampleRequest{ID: "..."})
    if apiErr != nil {
        t.Fatalf("Example: %v", apiErr)
    }
    if result.Total == 0 {
        t.Fatal("expected non-zero results")
    }
}
```

For **error paths**, assert on the typed code:

```go
_, apiErr := srv.api.Example(ctx, api.ExampleRequest{ID: ""})
if apiErr == nil {
    t.Fatal("empty ID should return ErrMissing")
}
if apiErr.Code != "missing_field" {
    t.Errorf("code = %q, want missing_field", apiErr.Code)
}
```

For **lock-discipline proof tests** (any operation using the
three-phase pattern), use the snapshot hook so timing isn't
fragile -- see [Patterns to reuse](#patterns-to-reuse).

### Step 5 -- Cleanup

When you finish a feature, look for what's now dead:

- Old handler functions superseded by your new `api/` method
- Service helpers that only the old handler called
- CLI proxy registrations now duplicated in a cluster file
- Test utilities that no longer have callers

The recent admin migration commit (`16f7693`) deleted ~1200 LOC
of dead code while adding ~1300 of new code. **Don't leave
half-replaced code in place.** If you can't delete it now (cross-PR
dependency), file an issue and reference it from the relevant
file's package doc.

---

## Worked example: the admin cluster migration (PR #3)

To make the recipe concrete, here's how PR #3 went together. The
goal was to migrate branch + backup operations from inline HTTP/MCP
handlers into the `api/` package, fix a snapshot-consistency bug in
backup along the way, and apply the three-phase lock pattern.

**Files added** (in this order):

1. `api/branches.go` -- 5 typed methods (`BranchList`, `BranchCreate`,
   `BranchCheckout`, `BranchMerge`, `BranchDiscard`), each with
   typed `Request`/`Response` and exported `XxxDescription`. The
   checkout and merge methods use the three-phase pattern; create
   and discard use straightforward `Lock` (the on-disk writes are
   single ref operations).
2. `api/backup.go` -- 5 typed methods (`BackupStatus`, `BackupCreate`,
   `BackupRestore`, `BackupExport`, `BackupImport`). The
   `BackupCreate` method uses the three-phase pattern: snapshot
   HEAD/refs/FORMAT under RLock, release, then run the actual
   `tar.gz` compression off-lock.
3. `backup/backup.go` (modified) -- new `CreateSnapshot(snap, ...)`
   function that takes the captured snapshot and writes a
   tar.gz that injects the snapshotted HEAD/refs/FORMAT instead
   of re-reading the live disk.
4. `core/engine.go` (modified) -- new `SwapGraph(g)` primitive
   that atomically replaces the engine's graph, used by branch
   checkout/merge after loading the new state off-lock.
5. `server/bindings_admin.go` -- HTTP routes (`/v1/branches*`,
   `/v1/backup*`, `/v1/restore`, `/v1/export`, `/v1/import`) and
   MCP tools (`gramaton_branch`, `gramaton_backup`). Loopback
   gates on the destructive HTTP routes.
6. `cli/mcp_proxy_admin.go` -- CLI MCP proxy bindings using
   shared description constants.

**Files deleted:**

- `server/handler_branches.go` (241 LOC -- inline HTTP handlers)
- `server/handler_backup.go` (265 LOC -- inline HTTP handlers)
- `server/mcp_admin.go` (450 LOC -- inline MCP tool callbacks)
- 14 shadowed orphan registrations in `cli/mcp_proxy.go`

**Tests added:**

- `TestBackupDoesNotBlockWrites` -- proof that the three-phase
  split releases the lock before compression, by racing a
  concurrent capture against an in-flight backup using the
  snapshot hook.
- `TestBackupSnapshotConsistency` -- proof that writes landing
  AFTER the snapshot phase do NOT appear in the archive.
- `TestBranchCheckoutOffLockGraphLoad` -- proof that branch
  checkout's graph parse runs off-lock.
- `TestBranchCheckoutEdgeStorePersistence` -- regression test for
  the SwapGraph + edge store divergence bug.
- Per-error-path tests for every new code path.

**Net diff**: -500 LOC. The migration produced both more tests and
less code -- typical for moving from scattered inline handlers to
the canonical surface. The full PR is commit `16f7693`; the
follow-up review-fix commit is `df5ef52`.

If you read one PR before starting work, read those two -- they're
the canonical example of the shape this codebase encourages.

---

## Patterns to reuse

### The three-phase lock pattern

Use this whenever an operation needs both consistency (a stable
view of the graph) and slow work (disk, network, embedding).

```go
func (a *API) ThreePhaseExample(ctx context.Context, req XxxRequest) (XxxResponse, *APIError) {
    // Phase 1: snapshot under read lock. Must be fast -- ideally
    // just a few field reads or a hash capture.
    a.engine.RLock()
    snapshot := captureWhatYouNeed(a.engine)
    a.engine.RUnlock()

    // Phase 2: do the slow work off-lock. The snapshot is your
    // stable view of the world; the engine may change underneath.
    // Use the snapshot, not live engine state.
    result, err := slowWorkUsingSnapshot(ctx, snapshot)
    if err != nil {
        a.log.Warn("threephase: slow work failed", "err", err)
        return XxxResponse{}, ErrInternal("...")
    }

    // Phase 3: apply the result under write lock. This phase
    // should also be fast -- a single transactional commit, an
    // atomic file rename, a SwapGraph.
    a.engine.Lock()
    defer a.engine.Unlock()

    if err := apply(a.engine, result); err != nil {
        return XxxResponse{}, ErrInternal("...")
    }
    return XxxResponse{ /* ... */ }, nil
}
```

**Real examples:** `api.BackupCreate`, `api.BranchCheckout`,
`api.BranchMerge`, `api.Reembed`.

**Snapshot hook for tests** (used by `BackupCreate`): the api
exposes `SetBackupSnapshotHook(chan)` which closes the channel
after phase 1 finishes. Tests can then deterministically race
work against the in-flight phase 2 without `time.Sleep`. If
you add a new three-phase operation that needs a deterministic
race in tests, follow the same hook pattern.

### The error taxonomy

```go
api.ErrMissing("foo is required")          // 400, code "missing_field", retryable
api.ErrInvalid("foo must be one of ...")   // 400, code "input_error",  retryable
api.ErrNotFound("record xyz not found")    // 404, code "not_found",    not retryable
api.ErrConflict("name already taken")      // 409, code "conflict",     not retryable
api.ErrUnavailable("LLM not configured")   // 503, code "unavailable",  not retryable
api.ErrForbidden("loopback only")          // 403, code "forbidden",    not retryable
api.ErrInternal("save failed")             // 500, code "internal_error", retryable
api.ErrPrepareRequired("call prepare first") // 409, code "prepare_required"
```

The `Retryable` flag is part of the contract; clients use it to
decide whether to back off and retry. Pick the right helper; don't
write a one-off error.

### Validation caps

All input caps live in `api/validate.go`:

```go
const (
    MaxKeywordLength    = 256
    MaxSearchTop        = 1000
    MaxLogLimit         = 500
    MaxLogTraversal     = 5000
    MaxTopicLength      = 1024
    MaxReembedBatch     = 500
    MaxProjectionFields = 64
    MaxFilterKeys       = 20
    // ... etc
)
```

Reference these constants from your method; don't bake numeric
limits into the operation. If you need a new cap, add it to the
const block with a comment explaining the rationale (security
boundary, memory bound, fairness, etc.).

### Description constants

Every MCP tool's description is an exported `XxxDescription`
constant in the api file:

```go
const BackupCreateDescription = "Create a snapshot-consistent backup..."
```

The HTTP binding doesn't use it (HTTP doesn't expose tool
descriptions), but the MCP server-side binding and the CLI proxy
both read it. If the description text drifts between the two
transports, agents see inconsistent help; the constant prevents
that.

### Test setup helpers

- `setupTestServer(t)` returns `(*Server, *core.Engine)` configured
  for a temp directory. Backup dir is sibling to data dir
  (avoiding the in-its-own-walk race).
- `addRecord(t, eng, content)` adds a node + commits, returns its ID.
- `doRequest(t, srv, method, path, body)` for HTTP-level tests.
- `doRequestFrom(t, srv, method, path, body, remote)` when you need
  a non-loopback origin (for testing loopback gates).

---

## Anti-patterns we've learned the hard way

Each of these came from a real bug. The "why" matters; learning
the rule without the story doesn't stick.

### Don't hold an engine lock across I/O

Holding `engine.RLock()` (or worse, `engine.Lock()`) across a
network call, a disk read, or an embedder invocation blocks all
writers (or all readers + writers) for the duration. **In the worst
case this is seconds.**

We had this in `BackupCreate` pre-PR-3: the RLock was held for the
entire tar.gz pass. A one-second backup blocked every concurrent
write. Fix: phase 1 captures HEAD/refs in microseconds; phase 2
does the slow work off-lock.

If you find yourself wanting to hold a lock across slow work, the
answer is almost always the three-phase pattern.

### Don't return `err.Error()` in `APIError.Message`

`APIError.Message` is what clients (and end-users) see. Wrapping
the underlying Go error string into it can leak internals: file
paths, network addresses, library-specific error formats.

We had this in `BackupExport`: `ErrInternal(fmt.Sprintf("write
response: %v", err))` shipped `io.Writer` error detail to clients.
Fix: log the err with `a.log.Warn("...", "err", err)` and return a
generic `ErrInternal("failed to write export response")`.

### Don't construct a fresh graph with `graph.New()` for `SwapGraph`

The engine is constructed with a shared `BboltEdgeStore`. If you
build a replacement graph with `graph.New()`, it gets a fresh
`MemoryEdgeStore`, and edges added on the swapped-in graph
silently bypass bbolt persistence.

We had this bug in PR #3's first version of `BranchCheckout` --
edges added on a checked-out branch were lost on restart. The fix
is in `core/engine.go`: use the documented `EdgeStore()` accessor
to share the engine's edge store with the new graph:

```go
newGraph := graph.NewWithCapacity(graph.DefaultCacheCapacity,
    graph.WithEdgeStore(a.engine.EdgeStore()))
```

If you call `SwapGraph` from any new code path, the documentation
on `SwapGraph` itself spells out the constraint.

### Don't write HEAD after `SwapGraph`

`SwapGraph` mutates in-memory engine state. If a subsequent on-disk
write fails (the HEAD file, a ref file), the engine is on the new
state but disk says otherwise -- a retry could silently make the
broken state stick.

Always write the on-disk pointers FIRST, then call `SwapGraph` last.
Both `BranchCheckout` and `BranchMerge` follow this order
post-PR-3 review.

### Don't silently swallow `parseJSON` errors on optional bodies

If your endpoint accepts an optional body, the natural pattern
looks like:

```go
_ = parseJSON(r, &body, maxJSONBodySize)  // BAD
```

But this also discards REAL parse errors -- a malformed body that
should have surfaced as 400 silently defaults instead.

Use the `errEmptyBody` sentinel:

```go
if err := parseJSON(r, &body, maxJSONBodySize); err != nil && !errors.Is(err, errEmptyBody) {
    s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
    return
}
```

### Don't skip `core.ValidBranchName` (or any input validator) on a code path

Every method that takes user-controlled identifiers (branch names,
session IDs, file paths) must validate them at the entry point.
Skipping validation on one of five sibling methods because "the
caller will validate" is how filesystem-traversal bugs land.

If you find yourself thinking "the transport will validate this",
move the validation to the api layer instead -- transports are
thin and should stay that way.

### Don't accept user-supplied filesystem paths without confinement

`api.BackupRestore` originally took an absolute `.tar.gz` path
with no confinement check, letting a caller restore from any
file on the host. The fix: `filepath.Clean` + prefix check
against the configured backup directory.

If your operation takes a filesystem path from the user, it
**must** be confined to a safe parent directory.

---

## Pre-merge checklist

Before opening a PR, walk through this list. PR template will
include the same.

- [ ] **Builds clean**: `go build ./...` exits 0, no warnings.
- [ ] **Tests pass**: `go test ./...` is green. New behaviour has new tests.
- [ ] **Race-clean**: `go test -race ./...` is green for any change
      touching the engine, indexes, or anything goroutine-y.
- [ ] **`go vet`**: no new warnings.
- [ ] **Lock discipline**: no engine lock held across I/O. Three-phase
      pattern used where applicable.
- [ ] **Error taxonomy**: existing helpers used; new helpers justified.
- [ ] **Validation**: input caps reference `api/validate.go` constants.
      User input validated at the api layer, not the transport.
- [ ] **Descriptions**: shared via `XxxDescription` constants.
- [ ] **Loopback gates**: destructive HTTP endpoints check `isLoopback`.
- [ ] **Cleanup**: dead code removed (or deletion-blocked-by-X
      issue filed).
- [ ] **Changelog**: `[Unreleased]` section in `CHANGELOG.md` updated.
      Categorise as Added / Changed / Deprecated / Removed / Fixed /
      Security.
- [ ] **Docs**: if behaviour changes the user contract, update
      `docs/architecture.md` or the relevant `docs/project-design/`
      file. If the contract is internal-only, update package-level
      Go doc.

---

## Commit and PR conventions

### Commit messages

We use **multi-paragraph commit messages**. The subject line is one
sentence in imperative mood; the body explains **why**, not just
what.

```
<verb> <what> in <where>

<paragraph explaining the motivation: what problem does this
solve, what user-visible pain or future-author pain motivated it>

<paragraph or two explaining the approach and any tradeoffs that
weren't obvious. Cite related commits/PRs/issues by hash or number.>

<paragraph if needed for migration notes, deprecations, or follow-up
work.>
```

Example (lifted from commit `df5ef52`):

```
Address review findings from PRs #1-3 (admin migration)

A multi-agent review of the three admin-cluster migration commits
turned up one critical correctness bug, two security gaps, and a
handful of medium-severity issues. This commit fixes all of them
and adds regression coverage.

Critical: SwapGraph + edge store divergence
-------------------------------------------
[explanation of the bug]

[explanation of the fix]

[regression test reference]

[other findings, in priority order]
```

The body uses headers (`Critical:`, `High:`, etc.) when fixing
multiple things. For single-issue commits, prose is fine.

### PR descriptions

Use the PR template (in `.github/PULL_REQUEST_TEMPLATE.md` -- to be
added). At minimum:

- **Summary**: 1-3 sentences on what this PR does.
- **Motivation**: what problem this solves.
- **Approach**: how, and any tradeoffs.
- **Tests**: what you added; what you ran.
- **Migration notes**: if anything is deprecated, broken, or
  requires a config change.

### Versioning + CHANGELOG

We follow [semver](https://semver.org/):

- **MAJOR**: incompatible API changes.
- **MINOR**: backward-compatible additions.
- **PATCH**: backward-compatible fixes.

Every PR adds an entry to the `[Unreleased]` section in
`CHANGELOG.md` under the appropriate category:

```
## [Unreleased]

### Added
- new gramaton_example tool for ... (#123)

### Fixed
- BackupRestore now confines path to backup dir (CVE-pending)
```

Releases consolidate `[Unreleased]` into a versioned section. The
maintainer handles the version bump and tag; contributors should
NOT bump versions in their PRs.

---

## Reporting bugs and getting help

- **Bug reports**: open a GitHub issue with reproduction steps,
  expected vs actual behaviour, and `gramaton --version` output.
  Templates in `.github/ISSUE_TEMPLATE/` (to be added).
- **Security vulnerabilities**: do NOT open a public issue. Email
  details per `SECURITY.md` (to be added). We aim to respond within
  72 hours.
- **Questions, design discussions**: open a GitHub Discussion (to
  be enabled at public release) or open an issue tagged
  "question". For non-trivial designs, prefer discussion-first
  over PR-first.

---

## Thanks

Gramaton exists because writing systems that remember and reason
across time is a real problem worth solving for individual builders
and small teams. Every careful contribution -- typo fix or major
feature -- makes the system better. Thanks for being here.
