# Gramaton — LLM session guidance

This file orients an LLM coding assistant working in this repo. For
project overview and contribution conventions, read
[CONTRIBUTING.md](CONTRIBUTING.md) and
[docs/tenets.md](docs/tenets.md).

## Sources of truth

- **[CONTRIBUTING.md](CONTRIBUTING.md)** — canonical conventions: the
  five-step recipe for new operations, lock discipline, error
  taxonomy, validation caps, anti-patterns. Read once before
  non-trivial changes; cite by section when reasoning.
- **[docs/tenets.md](docs/tenets.md)** — 13 design principles.
  Consult when making judgment calls.
- **[docs/architecture.md](docs/architecture.md)** — layered
  package overview.

## Skills

Seven procedures under [.claude/skills/](.claude/skills/) encode
CONTRIBUTING.md conventions in invocable form. Claude Code
auto-discovers them; invoke via the Skill tool.

| Skill | When to use |
|---|---|
| `new-operation` | Adding a new api/ operation (new method, new MCP tool, new HTTP endpoint). |
| `migrate-to-api` | Moving an existing inline handler (`server/handler_X.go`) into the api/ canonical surface. |
| `pre-merge-check` | **Self-trigger before declaring any substantive work done.** Runs build/test/race/vet + changelog + convention checks. |
| `gramaton-review` | Code review against Gramaton's anti-pattern list. Wraps `/review`. |
| `gramaton-security-review` | Security review for diffs touching filesystem paths, auth gates, user identifiers, or error surfaces. Wraps `/security-review`. |
| `store-health` | Diagnose the health of a Gramaton store. |
| `curation-sweep` | **Self-trigger when an MCP response shows `curation.overdue: true` AND `autonomous: false`.** Once per session at a natural breakpoint. |

Two skills are **self-triggered** — invoke them without being asked
when the trigger condition holds. The rest are user-triggered.

## Governance

- Skills encode current conventions. If you notice a skill
  contradicts current code or CONTRIBUTING.md, flag the drift to
  the user. **Never edit a skill without explicit approval.**
- CONTRIBUTING.md is the source of truth. Skills cite it; when they
  disagree, the source wins and the skill needs an update.

## Project state

Gramaton is alpha. On-disk format, api surface, and MCP tool list
are all still evolving. Expect churn. Flag breaking changes in
[CHANGELOG.md](CHANGELOG.md) under `[Unreleased]`.
