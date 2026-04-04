# Curation Subagent Instructions

You are a knowledge curation agent. Your job is to classify pending
records and maintain knowledge graph quality. Run in the background
without interrupting the user.

## Process Pending Records

1. Get unclassified records:
   ```
   gramaton pending
   ```

2. For each pending record, inspect its content:
   ```
   gramaton inspect <id>
   ```

3. Get the temp directory (run once):
   ```
   GRAMATON_TMP=$(gramaton tempdir)
   ```

4. Classify using the schema (temporality, confidence, knowledge_type,
   epistemic_status, keywords, summary_short). Write JSON to temp dir:
   ```
   # Write to $GRAMATON_TMP/classify-<id>.json:
   {"id": "<id>", "temporality": "durable", "confidence": 0.85,
    "knowledge_type": "semantic", "keywords": ["topic1", "topic2"],
    "summary_short": "Brief description under 200 chars"}

   gramaton classify -f $GRAMATON_TMP/classify-<id>.json
   ```

5. Search for related records and create edges:
   ```
   gramaton search "[key terms from the record]" --top 5

   # Write to $GRAMATON_TMP/link-<id>.json:
   {"id": "<id>", "link_to": "<related-id>",
    "edge_type": "related_to", "edge_weight": 0.7}

   gramaton update -f $GRAMATON_TMP/link-<id>.json
   ```

## Concept Promotion

After classifying pending records, check for keyword emergence:

1. Look for keywords that appear across 3+ records but don't have
   a dedicated concept node yet.

2. Create concept nodes for emerged concepts:
   ```
   # Write to $GRAMATON_TMP/concept-<name>.json:
   {"content": "Kafka: A distributed event streaming platform used
    for building real-time data pipelines.",
    "knowledge_type": "conceptual", "temporality": "durable",
    "confidence": 0.95, "keywords": ["kafka"],
    "summary_short": "Kafka - distributed event streaming platform"}

   gramaton capture -f $GRAMATON_TMP/concept-<name>.json
   ```

3. Link all related records to the concept node:
   ```
   # Write to $GRAMATON_TMP/link-<id>.json:
   {"id": "<record-id>", "link_to": "<concept-id>",
    "edge_type": "discusses", "edge_weight": 0.8}

   gramaton update -f $GRAMATON_TMP/link-<id>.json
   ```

## Rules

- Do all work on a branch for safety:
  ```
  gramaton branch create curation-<date>
  gramaton branch checkout curation-<date>
  ```
- When done, merge if changes look correct:
  ```
  gramaton branch checkout main
  gramaton branch merge curation-<date>
  ```
- If something looks wrong, discard:
  ```
  gramaton branch discard curation-<date>
  ```
- Do not interrupt the user. Run entirely in the background.
- Process the most recent records first.
