# Security Policy

## Supported Versions

Gramaton is alpha software. Only the `main` branch receives security
fixes. Tagged releases are frozen; to pick up a security fix, update
to the latest `main` (or the next tag once one is cut).

| Version | Supported |
|---------|-----------|
| `main`  | Yes       |
| Tagged releases | Upgrade to latest `main` |

## Reporting a Vulnerability

**Please do not open a public GitHub issue for security-sensitive
reports.** Use GitHub's private vulnerability reporting instead:

1. Go to the [Security tab](https://github.com/gramaton-ai/gramaton/security)
   of the repository.
2. Click **Report a vulnerability**.
3. Fill in what you observed, how to reproduce it, and the impact
   you believe it has.

This routes the report privately to the maintainer; only people you
explicitly add to the advisory can see it.

If GitHub's private reporting is unavailable to you for any reason,
open a minimal public issue titled "security contact request" (no
details) and a maintainer will reach out through another channel.

## What to Expect

Gramaton is currently maintained by one person in their spare time,
so there is no formal SLA. Realistic expectations:

- **Acknowledgement**: within ~7 days. If you don't hear anything,
  a polite nudge on the advisory is welcome.
- **Triage and assessment**: usually within 2-3 weeks of
  acknowledgement, depending on scope.
- **Fix or mitigation**: timeline depends on severity and
  complexity; critical issues are prioritised.
- **Coordinated disclosure**: we will discuss a disclosure timeline
  with you before publishing any advisory. Public disclosure
  typically lands alongside the fix landing on `main`.

## Scope

In scope:

- The `gramaton` CLI and `gramaton serve` HTTP/MCP server.
- On-disk store format, read/write integrity, and backup/restore
  flow.
- Authentication of loopback-gated HTTP endpoints.
- Handling of API keys and other secrets pulled from
  `~/.gramaton/config.yaml` or the environment.

Out of scope (please do not report):

- Issues that require the attacker to already have write access to
  a user's home directory or the store directory.
- Issues in upstream dependencies -- report those directly
  upstream. We will update once a fix is available.
- Denial-of-service via unbounded local requests (the project
  assumes a trusted local user at this stage).

## Credit

With your permission, we will acknowledge your report in the
CHANGELOG entry and the published advisory. Let us know in the
report whether you'd like to be credited and, if so, how.

Thank you for helping keep Gramaton and its users safe.
