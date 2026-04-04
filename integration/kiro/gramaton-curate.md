# gramaton-curate

Run knowledge store maintenance tasks.

## When to Use

- When any Gramaton response shows `"overdue": true` in curation
- Periodically during longer sessions
- When the user explicitly asks for curation

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
   gramaton_capture(
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
