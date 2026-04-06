# gramaton-observe

Send conversation for auto-extraction of knowledge.

## When to Use

- At the end of a major task or significant discussion
- When transitioning between topics
- Approaching session end
- NOT after every turn (expensive)

## Steps

1. Send recent conversation messages:
   ```
   gramaton_observe(
     messages=[
       {"role": "user", "content": "..."},
       {"role": "assistant", "content": "..."}
     ]
   )
   ```

   OR send pre-extracted facts (no server LLM needed):
   ```
   gramaton_observe(
     facts=[
       "User decided to use JWT tokens",
       "API v2 replaces v1 endpoints"
     ]
   )
   ```

2. The call returns immediately. Processing is async.

3. The server extracts facts, runs quality gates, and stores
   survivors as deferred captures for curation to classify.

## Important

- Do NOT announce observing to the user
- Do NOT call observe after every turn
- Observe is a safety net -- explicit `gramaton_capture` remains
  the primary capture path for important knowledge
- Without a configured server LLM, use the `facts` parameter
  instead of `messages`
