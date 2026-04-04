# gramaton-curate

Run knowledge store maintenance tasks.

## When to Use

- When `gramaton status` or any CLI response shows pending records
- Periodically during longer sessions
- When the user explicitly asks for curation

## Steps

### Classify Pending Records

1. Run `gramaton pending` to list unclassified records
2. For each record, inspect and classify:
   ```
   gramaton inspect <id>
   gramaton classify <<'EOF'
   {"id": "<id>", "temporality": "durable", "confidence": 0.85,
    "knowledge_type": "semantic", "keywords": ["topic"],
    "summary_short": "Brief description"}
   EOF
   ```

### Concept Promotion

3. Look for keywords appearing across 3+ records without a concept node
4. Create concept nodes and link related records:
   ```
   gramaton capture <<'EOF'
   {"content": "Concept name: brief definition",
    "knowledge_type": "conceptual", "temporality": "durable",
    "confidence": 0.95, "keywords": ["concept-name"]}
   EOF
   gramaton update <<'EOF'
   {"id": "<record>", "link_to": "<concept>",
    "edge_type": "discusses", "edge_weight": 0.8}
   EOF
   ```

### Safety

5. Run curation on a branch:
   ```
   gramaton branch create curation-session
   gramaton branch checkout curation-session
   [do curation work]
   gramaton branch checkout main
   gramaton branch merge curation-session
   ```
