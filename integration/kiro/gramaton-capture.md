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

4. Store via `gramaton_capture`:
   ```
   gramaton_capture(
     content="[knowledge to store]",
     temporality="[value]",
     confidence=[float],
     knowledge_type="[value]",
     keywords=["k1", "k2"],
     summary_short="[brief summary]",
     asserted_as_of="[only if source claim date differs from now]"
   )
   ```

5. Search for related records and create edges:
   ```
   gramaton_search(text="[key terms]", top=5)

   gramaton_link(
     id="[new-id]",
     target_id="[related-id]",
     edge_type="related_to",
     edge_weight=0.7
   )
   ```

## Do Not Capture

- Trivial exchanges or small talk
- Questions without answers
- Work-in-progress that hasn't solidified
- Your own generated responses
