# gramaton-capture

Capture knowledge into the Gramaton store.

## When to Use

- User makes a decision or states a preference
- A significant fact or insight emerges from discussion
- An architecture or design choice is made with reasoning

## Steps

1. Classify the knowledge:
   - temporality: immutable | durable | temporal | ephemeral
   - confidence: 0.0-1.0
   - knowledge_type: episodic | semantic | procedural | conceptual | reference
   - epistemic_status: well_established | probable | speculative | contested

2. Extract keywords from the content and conversation context

3. Write a summary_short (max 200 characters)

4. Store via CLI:
   ```
   gramaton capture <<'EOF'
   {
     "content": "[knowledge to store]",
     "temporality": "[value]",
     "confidence": [float],
     "knowledge_type": "[value]",
     "keywords": ["k1", "k2"],
     "summary_short": "[brief summary]"
   }
   EOF
   ```

5. Search for related existing records and create edges:
   ```
   gramaton search "[key terms]" --top 5
   gramaton update <<'EOF'
   {"id": "[new-id]", "link_to": "[related-id]",
    "edge_type": "related_to", "edge_weight": 0.7}
   EOF
   ```

## Do Not Capture

- Trivial exchanges or small talk
- Questions without answers
- Work-in-progress that hasn't solidified
- Your own generated responses
