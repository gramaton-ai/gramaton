### Memory routing: Codex's native memories vs Gramaton

Codex ships its own memory system under `~/.codex/memories/`,
separate from Gramaton. Both want "remember this" content.

Route knowledge to Gramaton by default: decisions, facts, research,
preferences, and anything you would search for later. When the user
says "remember this", that means `gramaton_save`. Do not mirror
Gramaton saves into Codex's memories or vice versa — one store per
fact.

Treat Codex's native memories as session-local convenience, not the
knowledge store of record. When the two disagree, verify against
Gramaton and prefer it for durable knowledge.
