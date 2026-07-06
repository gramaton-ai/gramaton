# Remote access

Gramaton is single-user and local-first: by default the server binds to
loopback, so only processes on the same machine reach it. Remote access is an
opt-in that lets your *other* machines reach one store over your own network,
with token authentication and a pinned TLS certificate. It is still your
server on your own hardware, not a hosted service.

Remote access is different from [sharing](sharing.md): sharing hands over a
frozen *copy* of a store's data (a directory), while remote access keeps one
*live* store on a host that clients read (and write, unless it is frozen)
over the network.

## Host: enable remote access

On the machine that holds the store, turn on remote access:

```bash
gramaton remote enable --host workstation.local
gramaton stop && gramaton serve   # restart for it to take effect
```

`remote enable`:

- mints a random bearer token (`~/.gramaton/remote.token`, mode 0600),
- generates a self-signed TLS certificate (ECDSA P-256) whose SANs are the
  supplied `--host` names plus loopback,
- turns on a **separate** TLS-only listener (default port 42983) in the
  store's config; the plain loopback listener is unchanged,
- prints a one-line credentials bundle (`gramaton-remote:<base64>`) to hand
  to each client.

Flags:

- `--host <name>` (repeatable) — a hostname/IP clients will use to reach the
  server. Each becomes a certificate SAN and a URL in the bundle.
- `--bind <addr>` — the address to bind (default: all interfaces).
- `--port <n>` — the TLS listener port (default: 42983).
- `--admin-ops` — allow authenticated remotes to run path-taking admin
  operations (see [Endpoint tiers](#endpoint-tiers)).
- `--force` — replace existing token/certificate material, backing up the old
  files (ISO8601 timestamp in the name) first.

### Run the host as a managed service

Remote serving disables the server's idle auto-shutdown, so it stays up for
clients. Run it under your platform's service manager rather than a login
shell:

- **launchd** (macOS): a LaunchAgent that runs `gramaton serve` with
  `KeepAlive` set.
- **systemd** (Linux): a `--user` service that runs `gramaton serve` with
  `Restart=on-failure`.

Point the service at the right store with `--config-dir` (or `--store <name>`)
when it is not the default store.

### Turn it off

```bash
gramaton remote disable            # stop serving remotely; keep token + cert
gramaton remote disable --purge    # also delete the token and certificate
```

`disable` keeps the token and certificate by default, so a later `remote
enable` can turn remote access back on without issuing a new bundle (clients
keep the same pin). `--purge` deletes them; the next `enable` mints fresh
material and every client must re-import the new bundle. Restart the server
after either.

## Client: connect to a remote store

On each client machine, paste the bundle:

```bash
gramaton --store home remote add   # paste the bundle when prompted
```

`remote add`:

- verifies the server before writing anything — a pinned-TLS, authenticated
  probe proves the bundle points at the real server and the token is
  accepted, so a bad bundle fails here cleanly,
- writes the client config (`remote.url`, `remote.pin`, `remote.token_file`)
  into the store's own `config.yaml`,
- registers the store's MCP entry (`gramaton-<name>`) with every detected AI
  tool, so an agent can reach the remote store right away.

Pass `--bundle <file>` (or `-` for stdin) instead of the prompt, and
`--no-harness` to skip the MCP-entry registration.

Every later command for that store — CLI, hooks, and the MCP proxy — dials the
remote server. Commands that open the store's files directly (`serve`,
`backfill`, `repair`, `validate`) are refused in remote mode; run those on the
host.

### Named vs default

Remote stores are **named** (`--store <name>`), so a remote store sits
alongside your local default instead of replacing it — the recommended setup.
A store that already owns local data cannot be pointed at a remote (that would
strand the data); create the remote under a fresh name instead.

A machine whose *only* Gramaton is a remote store can `remote add` with no
`--store`, making the remote its default so plain `gramaton search` works
without a `--store` flag.

## Reaching a store read-only

You can keep your everyday memory as your local default (writable) and reach a
second store on the host — a frozen reference you read from several machines
without any of them writing to it. Freeze that store on the host, and every
machine you connect sees it read-only.

```bash
# On the host: freeze the store, then enable remote access
gramaton store freeze
gramaton remote enable --host workstation.local
gramaton stop && gramaton serve

# On another machine: connect it as a named read-only store
gramaton --store archive remote add
```

Read-only is enforced by the host's engine (the frozen `STORE` manifest), not
by the client: a machine that tries to write gets a clear "store is read-only"
rejection, and its MCP surface registers only the read tools. A valid token
otherwise grants full write access to a non-frozen store, so freeze the store
on the host if you want it read-only — there is no per-connection read-only
grant.

## Endpoint tiers

An authenticated remote caller can reach the knowledge surface and the
pathless admin operations (search, save, collections, sessions, curation,
backup-create). Operations that take a caller-supplied filesystem path —
restore, export/import, store carve/add, session archive, local-path ingest —
and process control (shutdown, debug) stay **loopback-only** unless the host
was enabled with `--admin-ops`. A bearer token proves identity, not that a
path is safe, so path-taking operations are gated separately.

## Troubleshooting

**"could not reach the server on any bundle URL"** — the host is not running,
the bundle's address is wrong, or a firewall blocks the port. Confirm the
server is up on the host and that the client can reach `<host>:42983`.

**"server rejected the bundle token"** — the bundle is stale (the host ran
`remote enable --force`, or `remote disable --purge` then `enable`, since it
was issued). Re-issue the bundle on the host and re-run `remote add`.

**Pin mismatch / TLS handshake failure** — the certificate changed (a forced
rotation on the host). Re-issue the bundle; the pin travels in it, so
re-importing is all the client needs.

**The agent cannot see the remote store** — the MCP entry was not registered
(you ran `remote add --no-harness`, or the AI tool was installed afterward).
Run `gramaton store sync-harness`, then restart the AI client. `gramaton store
list --harness` shows which tools each store is registered with.

See [docs/configuration.md](configuration.md) for the full `server.remote` and
`remote` config reference.
