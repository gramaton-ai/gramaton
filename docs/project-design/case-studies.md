# Case Studies

Real-world examples that test the design's handling of knowledge evolution, contradiction, supersession, and edge cases. These validate that the metadata model works across domains and failure modes.

---

## 1. Bredt's Rule — Overturning an "Immutable" Scientific Principle

**Domain:** Organic chemistry

**The knowledge:** Bredt's rule (1924) states that a bridgehead carbon in a small bridged ring system cannot participate in a double bond. Taught as a fundamental principle for 100 years.

**What happened:** In November 2024, UCLA's Garg Lab published in *Science* that they had synthesized anti-Bredt olefins in small bridged ring systems — directly violating the rule. Headlines: "time to rewrite the textbooks."

**How Gramaton handles it:**

Initial state:
```
Node: "Bredt's rule: bridgehead carbons in small bridged rings 
       cannot have double bonds"
  temporality: immutable
  knowledge_type: conceptual
  epistemic_status: well_established
  confidence: 0.95
```

After the UCLA research is captured:
```
Node: "UCLA (Garg Lab, 2024) synthesized anti-Bredt olefins in 
       small bridged rings. Published in Science."
  temporality: durable
  knowledge_type: semantic
  epistemic_status: well_established
  confidence: 0.95
  edge: --contradicts--> [Bredt's rule node]

Bredt's rule node updated:
  temporality: immutable → durable
  epistemic_status: well_established → contested
  confidence: 0.95 → 0.6
```

**What this tests:**
- Immutable → durable transition (nothing is truly immutable except tautologies)
- `contradicts` edge between new evidence and old rule
- The old rule is NOT deleted — it's still how most bridged ring chemistry works
- Knowledge diffing: "what changed about Bredt's rule?" shows the transition
- Audit trail: commit history shows when and why the status changed

**Design lesson:** `immutable` should be reserved for purely definitional/tautological statements ("a triangle has three sides," "HTTP 200 is defined as success in RFC 7231"). Named scientific principles — even century-old ones — should probably default to `durable` with high confidence. If an agent classifies something as immutable, curation can challenge it.

---

## 2. Pluto — Reclassified After 76 Years

**Domain:** Astronomy / general knowledge

**The knowledge:** "Pluto is the ninth planet in our solar system." Taught as fact from its discovery in 1930 to 2006.

**What happened:** In 2006, the International Astronomical Union redefined "planet," and Pluto was reclassified as a dwarf planet. The fact didn't change — Pluto is the same object. The classification framework changed around it.

**How Gramaton handles it:**

```
Node: "Pluto is the ninth planet in our solar system"
  temporality: durable → still durable (was never immutable — classifications are human constructs)
  epistemic_status: well_established → refuted
  confidence: 0.95 → 0.1
  contextual_role: foundational (v2 — needed to understand the reclassification)
  edge: --superseded_by--> "Pluto is a dwarf planet (IAU 2006 reclassification)"

Node: "Pluto is a dwarf planet per IAU 2006 reclassification"
  temporality: durable
  epistemic_status: well_established
  confidence: 0.95
  edge: --supersedes--> "Pluto is the ninth planet"
```

**What this tests:**
- Supersession chain: old claim → superseded_by → new claim
- Refuted knowledge retained for context (you can't understand the reclassification without the original claim)
- The original record answers "what did people used to think about Pluto?" — a valid query
- An agent searching "is Pluto a planet?" should find both records, with the superseding one ranked higher via epistemic_status and confidence

---

## 3. Stress Causes Ulcers — Nobel Prize for Proving the Opposite

**Domain:** Medicine

**The knowledge:** "Peptic ulcers are caused by stress and spicy food." Medical consensus for decades. Treatments focused on stress reduction and diet.

**What happened:** In 1982, Barry Marshall and Robin Warren discovered that most ulcers are caused by *Helicobacter pylori* bacteria. The medical establishment resisted for years. Marshall famously drank a petri dish of H. pylori to prove his point. They won the Nobel Prize in 2005.

**How Gramaton handles it:**

```
Node: "Peptic ulcers are caused by stress and dietary factors"
  epistemic_status: well_established → refuted
  confidence: 0.9 → 0.1
  contextual_role: foundational (understanding medical history)
  edge: --contradicted_by--> "H. pylori causes most peptic ulcers"
  edge: --contradicted_by--> "Marshall & Warren Nobel Prize 2005"

Node: "Most peptic ulcers are caused by H. pylori infection, treatable with antibiotics"
  epistemic_status: well_established
  confidence: 0.95
  edge: --supersedes--> "ulcers caused by stress"
```

**What this tests:**
- Complete reversal: well_established → refuted
- The refuted knowledge is retained because it explains WHY ulcer treatment used to focus on stress
- Multiple evidence records (the discovery, the Nobel Prize) linked to the same contradiction
- An agent advising on ulcer treatment should find the current knowledge, NOT the refuted claim
- But an agent researching "history of ulcer treatment" should find both, with the relationship clear

---

## 4. "Humans Only Use 10% of Their Brain" — False but Contextually Necessary

**Domain:** Neuroscience / popular science

**The knowledge:** The claim "humans only use 10% of their brain." Thoroughly debunked by neuroimaging studies — virtually all brain regions have known functions, and PET/fMRI scans show activity across the entire brain during normal tasks. But the myth persists in popular culture, self-help literature, and media (the film *Lucy* was built on this premise).

**How Gramaton handles it:**

```
Node: "Humans only use 10% of their brain"
  epistemic_status: refuted
  confidence: 0.95 (we are confident this is false)
  contextual_role: attributed_belief
  edge: --contradicted_by--> [neuroimaging studies, neuroscience textbooks]

Node: "The '10% of the brain' myth is widely believed and frequently 
       referenced in popular culture and self-help media"
  epistemic_status: well_established
  confidence: 0.9
  knowledge_type: semantic
  edge: --discusses--> [10% brain myth node]
```

**What this tests:**
- The critical distinction between de re (about the thing itself) and de dicto (about what is believed)
- `refuted` with high confidence — we're confident it's wrong, not uncertain about it
- The belief itself IS knowledge — the claim is false but the existence of the myth is true
- An agent writing about neuroscience should clearly indicate the claim is refuted
- An agent explaining why someone referenced "untapped brain potential" needs the belief record to provide context

---

## 5. API Endpoint Versioning — Practical Technical Supersession

**Domain:** Software engineering

**The knowledge:** "The user service endpoint is `/api/v2/users`." Correct when captured. Six months later, the team migrates to v3.

**How Gramaton handles it:**

```
Node: "User service endpoint is /api/v2/users"
  temporality: durable
  confidence: 0.9 → 0.3
  epistemic_status: well_established → deprecated
  valid_until: 2026-10-15 (set when v3 migration was completed)
  edge: --superseded_by--> "/api/v3/users is the current user service endpoint"

Node: "/api/v3/users is the current user service endpoint"
  temporality: durable
  confidence: 0.95
  epistemic_status: well_established
  valid_from: 2026-10-15
  edge: --supersedes--> "/api/v2/users"
```

**What this tests:**
- Routine supersession — the most common knowledge evolution pattern
- `valid_from` / `valid_until` bitemporal tracking
- An agent building code should find v3, not v2
- An agent debugging a legacy system might need to know v2 existed
- Knowledge diffing: "what changed about the user service?" shows the endpoint migration

---

## 6. User Preference Change — Simple Durable Knowledge Update

**Domain:** Personal knowledge

**The knowledge:** "User prefers dark mode in all IDEs." Captured in January. In June, the user says "I'm switching to light mode, dark mode gives me headaches."

**How Gramaton handles it:**

```
Node: "User prefers dark mode in all IDEs"
  confidence: 0.95 → 0.1
  epistemic_status: well_established → deprecated
  edge: --superseded_by--> "User prefers light mode (dark mode causes headaches)"

Node: "User prefers light mode in all IDEs. Switched from dark mode 
       due to headaches."
  temporality: durable
  confidence: 0.95
  edge: --supersedes--> "dark mode preference"
```

**What this tests:**
- The simplest supersession case — a preference changed
- The old preference is retained because the agent might need to know WHY it changed
- An agent configuring an IDE should find the current preference
- Audit trail shows when the preference changed

---

## 7. Evolving Architecture Decision — Multi-Step Knowledge Evolution

**Domain:** Software engineering

**The knowledge:** A team's caching strategy evolves over 18 months.

```
Month 1:  "We use Redis for caching" (durable, well_established, confidence 0.9)
Month 6:  "Redis is hitting memory limits at scale" (temporal, probable, confidence 0.7)
            → edge: --constrains--> "Redis for caching"
            → Redis node confidence drops: 0.9 → 0.7
Month 12: "Evaluated Memcached as Redis replacement" (temporal, speculative, confidence 0.5)
            → edge: --related_to--> "Redis for caching"
Month 14: "Migrated caching from Redis to Memcached" (durable, well_established, confidence 0.9)
            → edge: --supersedes--> "Redis for caching"
            → Redis node: epistemic_status → deprecated, confidence → 0.2
Month 18: Agent asks "what's our caching strategy?"
            → finds Memcached record (high score)
            → related: Redis record (deprecated, low score) with full history
```

**What this tests:**
- Knowledge evolution over time — not a single event, a gradual transition
- Multiple records accumulating around the same topic
- The graph tells the story: constraint → evaluation → migration → supersession
- Knowledge diffing: `gramaton diff --since [month 1] --topic "caching"` shows the entire evolution
- Audit trail on each record shows when and why it changed
- A concept node "caching strategy" would emerge (referenced by 4+ records) and serve as the hub

---

## What These Cases Validate

| Design Feature | Cases That Test It |
|---|---|
| Immutable → durable transition | Bredt's rule |
| Supersession chains | Pluto, API versioning, caching evolution, user preference |
| Refuted but retained | Bredt's rule, ulcers, 10% brain myth |
| `contradicts` edge | Bredt's rule, ulcers, 10% brain myth |
| `contextual_role` | Bredt's rule, Pluto, ulcers, 10% brain myth |
| Knowledge diffing | Bredt's rule, API versioning, caching evolution |
| Audit trail | All cases — every transition is a commit |
| Decay behavior | API versioning (deprecated v2 fades), user preference (old pref fades) |
| Domain neutrality | Chemistry, astronomy, medicine, neuroscience, engineering, personal |
| Concept emergence | Caching evolution (concept hub from 4+ records) |
