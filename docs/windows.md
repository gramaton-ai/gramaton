# Windows support

Gramaton runs natively on Windows 11 (amd64). Installation, configuration,
and day-to-day use are the same as on macOS or Linux modulo a few OS
semantic differences documented below.

## Installation

### Via `go install`

```powershell
go install github.com/gramaton-ai/gramaton@latest
```

`go install` places the binary in `%USERPROFILE%\go\bin\gramaton.exe`.
Make sure that directory is on your `PATH`. If it isn't, either add
it or copy the binary into a directory that is:

```powershell
Copy-Item "$env:USERPROFILE\go\bin\gramaton.exe" C:\Users\<you>\bin\
```

Then confirm:

```powershell
gramaton --version
```

### Pre-built binary

Official pre-built binaries (via GoReleaser) are planned for a
post-public-release milestone. Until then, build from source via
the `go install` path above, or cross-compile from a non-Windows
machine:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o gramaton.exe .
```

and copy the resulting `gramaton.exe` to the Windows box.

## First-run setup

```powershell
gramaton init
```

The interactive wizard walks the same five steps as on Unix. Two
Windows-specific callouts:

- **Permissions check** — the wizard skips the `0o600` mode-bit check
  on Windows because NTFS uses access-control lists (ACLs), not Unix
  mode bits. Your config and API-key files are owner-readable by
  default under your Windows profile's ACL inheritance. If you need
  a stricter guarantee, lock them down via the file's Properties →
  Security tab or `icacls`.
- **Hook installation** — Claude Code on Windows ships with Git Bash
  bundled, so the wizard materializes `.sh` proxy files for Claude
  Code hooks. Kiro CLI 2.0 is native Windows with no bundled bash, so
  the wizard materializes `.cmd` proxy files for Kiro. You don't
  need to install Git Bash separately for either path.

## Running the server

```powershell
gramaton serve --fg
```

Foreground mode (`--fg`) is the recommended way to run the server on
Windows — press Ctrl-C to stop. Background-detached mode (`gramaton
serve` without `--fg`) spawns a detached subprocess via
`CREATE_NEW_PROCESS_GROUP`; you can kill it later with
`gramaton serve --stop`.

If you want the server to start at login, use Windows Task Scheduler
to invoke `gramaton serve --fg`. Gramaton does not currently register
itself as a Windows Service — that's a post-v1 feature.

## Known Windows-specific behavior

- **Hook scripts are templates, not checked-in files.** On any OS,
  `gramaton init` generates the `~/.gramaton/hooks/**/*.{sh,cmd}` files
  from Go templates at install time. There's nothing to clone, and
  git's `core.autocrlf=true` can't corrupt shebangs.
- **Hooks forward to a Go subcommand.** The proxy scripts are thin —
  a single `exec gramaton hook <event>` line. All logic lives in the
  `gramaton hook` internal subcommand. Edit the proxy for temporary
  debugging (add `set -x`, etc.) but the real behavior to change
  lives in the Go code.
- **Python 3 is not required.** Earlier shell hooks used `python3`
  for JSON parsing; the Go subcommand uses `encoding/json` natively.
  This was a hidden dependency on Unix too — it's just most visible
  on Windows where Python isn't preinstalled.
- **AVX2+FMA3 matmul kernel is hand-written assembly.** Validated on
  Zen 3 (Ryzen 7 5800X3D) in April 2026. Intel Haswell / AMD
  Excavator and newer CPUs have the required features. Older CPUs
  fall back transparently to the pure-Go matmul — correct but
  ~20-50× slower for the BERT embedder. If you're on Windows on ARM
  (Snapdragon X etc.), build with `GOARCH=arm64` — the NEON kernel
  applies there too.
- **bbolt file locking** uses Windows `LockFileEx` under the hood;
  it works correctly but an unclean shutdown may leave a stale lock.
  `gramaton serve` on the next start detects and recovers. If you
  see repeated lock-file warnings, verify no zombie `gramaton.exe`
  is still running (`Get-Process gramaton`).

## Limitations deferred to later releases

- **Signed installers** (`.msi`, code-signed `.exe`) — planned for a
  later milestone. Install via `go install` for now.
- **Windows package managers** (winget, Scoop, Chocolatey) — planned
  for a later milestone.
- **Windows Credential Manager** integration for API keys — planned
  for a later milestone. API keys are currently read from
  `~/.gramaton/config.yaml` or env vars on every OS.
- **Windows Service** registration. Use Task Scheduler or run
  `gramaton serve --fg` from a terminal session for now.
- **WSL2 vs native** — both work. Native is the primary supported
  configuration since Kiro CLI and Claude Code both have native
  Windows builds. If you're already working in WSL2 for other
  reasons, Gramaton installed inside WSL2 is a fine Linux install.

## Reporting Windows issues

When reporting a Windows-specific bug, please include:

- Windows version (`[System.Environment]::OSVersion.Version`)
- CPU (`Get-WmiObject Win32_Processor | Select Name`) — especially
  relevant for the AVX2 matmul path
- Which shell you're running (cmd.exe vs PowerShell vs Git Bash)
- Relevant lines from `%USERPROFILE%\.gramaton\hooks.log` and
  `%USERPROFILE%\.gramaton\gramaton.log`
- The output of `gramaton init` if the bug was during setup

