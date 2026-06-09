# gramaton-curate

Manually classify pending records as a fallback when the server has
no LLM provider configured (`autonomous: false`). When the server
has an LLM, it runs curation itself every minute (default) --
classification, summarization, contradiction detection, concept
synthesis -- and this skill should not duplicate that work.
Deterministic curation (orphan linking, dedup, concept candidate
detection, lifecycle transitions) always runs server-side regardless
of LLM availability.

## When to Use

- When a Gramaton response's `curation` envelope shows
  `"overdue": true` AND `"autonomous": false`
- When the user explicitly asks for manual curation

## Preview Changes

Before running curation, you can preview what would change:
```
gramaton_curation(action="dry_run")
```
This runs the full LLM pipeline but returns planned changes
without applying them.

## Steps

### Classify Pending Records

1. Get unclassified records:
   ```
   gramaton_pending()
   ```

2. For each record, inspect and classify:
   ```
   gramaton_inspect(id="<id>")

   gramaton_classify(
     id="<id>",
     temporality="durable",
     confidence=0.85,
     knowledge_type="semantic",
     keywords=["topic"],
     summary_short="Brief description"
   )
   ```

### Concept Promotion

3. Look for keywords appearing across 3+ records without a concept node

4. Create concept nodes and link related records:
   ```
   gramaton_save(
     content="Concept name: brief definition",
     knowledge_type="conceptual",
     temporality="durable",
     confidence=0.95,
     keywords=["concept-name"]
   )

   gramaton_link(
     id="<record>",
     target_id="<concept>",
     edge_type="discusses",
     edge_weight=0.8
   )
   ```

### Safety

5. Run curation on a branch:
   ```
   gramaton_branch(action="create", name="curation-session")
   gramaton_branch(action="checkout", name="curation-session")
   [do curation work]
   gramaton_branch(action="checkout", name="main")
   gramaton_branch(action="merge", name="curation-session")
   ```
