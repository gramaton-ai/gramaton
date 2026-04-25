---
name: store-health
description: Use to diagnose the health of a Gramaton knowledge store. Aggregates the standard health probes into a single report — pending classification, orphans, duplicates, stale-temporal, refuted, expiring, ephemeral survivors. Identifies when piggyback curation should run and when dedup is worth attempting. Triggers include "check store health", "is my store healthy", "audit the knowledge store", "what does the store look like", "store status". Works against any Gramaton instance (the dev's dogfood store or a user's production store).
---

# store-health

## Purpose

Single-call diagnostic audit of a Gramaton knowledge store. Bundles seven probes — total stats, pending classification, orphans, duplicates, stale temporal records, refuted/expiring records, heavily-connected concept candidates — into one structured report with actionable recommendations.

The value over running `gramaton_stats` plus a few ad-hoc searches is the **cross-probe synthesis**: pending count interpreted against autonomous-curation availability ("pending > 0 AND autonomous: false → run curation-sweep"); orphan count interpreted against age ("many AGED orphans → capture pipeline isn't linking, investigate"); duplicate clusters interpreted against the auto-supersession threshold ("pre-existing pre-0.92 duplicates → optional dedup pass with consent"). Raw probe output gives you numbers; this skill gives you what to do.

When NOT to use:
- You only need one stat (use `gramaton_stats` directly).
- You're mid-task and don't want a context-eating audit (run at task boundaries).
- The store is brand new with under ~50 records (signals will be noisy and unactionable).

When TO use:
- User asks "is my store healthy?", "audit the store", or any general health question.
- After a substantial ingest pass (benchmark extraction, bulk import) to confirm the store landed cleanly.
- Periodically as a maintenance check on a long-lived store.

Output is one structured report. Use MCP tools, not the CLI, unless MCP is unavailable.

## Sequence

Run these in order and collect findings. Don't narrate — run silently, then report.

### 1. Baseline

```
gramaton_stats()
gramaton_curation(action="status")
```

Capture: total records, memory/session split, pending count, last curation timestamp, whether autonomous curation is configured.

### 2. Pending classification queue

```
gramaton_search(missing=["temporality"], top=25)
```

Records missing core metadata (temporality is the canonical indicator). If count > 0 and `curation.autonomous == false`, piggyback curation is needed → recommend `curation-sweep`.

If count > 100, something is wrong with the curation service itself — note this as a systems issue, not a store issue.

### 3. Orphans (disconnected knowledge)

```
gramaton_search(max_edges=0, top=25)
```

Records with zero edges. Some are legitimate (brand-new captures not yet linked by background curation). Large counts of aged orphans = curation regression or capture-without-linking pattern.

### 4. Duplicates

```
gramaton_duplicates(threshold=0.92)
```

Auto-supersession handles >= 0.92 on new captures, but pre-existing duplicates from before that feature landed still exist. Report the top 5 clusters. If the user wants them resolved, chain into a dedup pass (see "Remediation" below).

### 5. Stale temporal records

```
gramaton_search(temporality="temporal", sort="staleness", order="desc", top=10)
```

Temporal records that haven't been verified in a long time. These are candidates for re-verification or supersession. Don't delete — that violates tenet 8. Surface the list for the user to review.

### 6. Refuted or expiring

```
gramaton_search(epistemic_status="refuted", top=10)
gramaton_search(expires_before="<today + 30 days>", top=10)
```

Refuted records still exist in the graph for audit purposes (tenet 8) but shouldn't be surfacing in normal retrieval — confirm they're being deprioritized. Expiring ephemeral/temporal records should be resolved or renewed before expiry.

### 7. Heavily connected nodes (concept candidates)

```
gramaton_search(min_edges=5, sort="edge_count", order="desc", top=10)
```

Hot nodes — heavily linked records that might warrant promotion to concept nodes. Not an action item, just intelligence for the user.

## Report shape

Keep it tight. Format:

```
Store health as of <timestamp>

Size: <N> records (<memory>/<sessions>) · <M> edges · <K> collections
Curation: <autonomous|piggyback> · last run <when> · <pending> pending

Findings:
  [OK]      <probe>        <detail>
  [ATTENTION] <probe>      <detail>  → <recommended action>
  [ACTION]  <probe>        <detail>  → <recommended skill or command>

Recommendations (in order):
  1. <most impactful action>
  2. ...
```

Use `[OK]` / `[ATTENTION]` / `[ACTION]` as severity markers. `[ACTION]` means the user should do something; `[ATTENTION]` means watch but don't act; `[OK]` means clean.

## Common recommendations

- Pending > 0 AND autonomous = false → `curation-sweep` skill
- Large duplicate clusters → offer a dedup pass (section below)
- Many aged orphans → investigate whether capture-flow is running `gramaton_link`
- Refuted records high-ranking in searches → report to user; may need embedding re-visit
- No recent captures → check whether session extraction is firing

## Optional dedup remediation

If the user wants duplicates resolved (not automatic — destructive changes require consent):

For each duplicate cluster from step 4:
1. `gramaton_inspect` each record in the cluster.
2. Pick the canonical record (highest confidence, most edges, most recent asserted_as_of).
3. Propose: set `valid_until` on the non-canonical records (marks as historical), add `supersedes` edges from canonical → others.
4. Present the plan to the user. On approval, apply with `gramaton_update` + `gramaton_link`. Never `gramaton_delete` for dedup — use supersession.

## Fallback

If MCP tools are unavailable, fall back to `gramaton stats`, `gramaton curation status`, `gramaton search ...` CLI invocations (assuming `gramaton` is on PATH). Same sequence.
