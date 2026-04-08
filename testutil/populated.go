package testutil

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// PopulatedStore is the set of record IDs from a pre-populated engine.
// Fields are grouped by category for easy access in tests.
type PopulatedStore struct {
	// Work cluster (6 records)
	WorkReorg        string // episodic: team restructured
	WorkNewManager   string // episodic: new manager started
	WorkDeadline     string // temporal: project due date
	WorkMeeting      string // procedural: meeting cadence
	WorkOldTool      string // historical, superseded: old project tool
	WorkNewTool      string // durable: new project tool (supersedes old)

	// Health cluster (6 records)
	HealthDoctorVisit   string // episodic: annual checkup notes
	HealthExercise      string // procedural: exercise routine
	HealthAllergy       string // immutable: allergy info
	HealthSleep         string // semantic: sleep discovery
	HealthSupplement    string // refuted: supplement claim
	HealthPrescription  string // temporal: prescription reminder

	// Cooking cluster (5 records)
	CookingRecipe       string // procedural: favorite recipe
	CookingSubstitution string // semantic: ingredient substitution
	CookingDinnerParty  string // episodic: dinner party menu
	CookingDietary      string // durable: dietary constraint
	CookingTechnique    string // procedural: technique tip

	// Travel cluster (5 records)
	TravelSeat       string // immutable: flight seat preference
	TravelPacking    string // procedural: packing list
	TravelTrip       string // ephemeral: upcoming trip
	TravelLoyalty    string // durable: loyalty program info
	TravelPassport   string // temporal: passport renewal deadline

	// Finance cluster (4 records)
	FinanceBudget     string // procedural: budget rule
	FinanceExpense    string // procedural: expense process
	FinanceTax        string // temporal: tax deadline
	FinanceInvestment string // speculative: investment idea

	// Learning cluster (4 records)
	LearnBook        string // reference: book recommendation
	LearnCourse      string // episodic: course notes
	LearnRetention   string // semantic: spaced repetition
	LearnContested   string // contested: study technique

	// People (4 records)
	PersonVendor   string // temporal: vendor contact
	PersonNeighbor string // durable: neighbor info
	PersonBirthday string // durable: friend's birthday
	PersonDoctor   string // durable: doctor recommendation

	// TODOs (5 records)
	TodoOpen         string // unresolved, high importance
	TodoCompleted    string // resolved: completed
	TodoAbandoned    string // resolved: abandoned
	TodoObsolete     string // resolved: obsolete
	TodoOpenLow      string // unresolved, low importance

	// Orphans (4 records) -- no keywords, no edges
	Orphan1 string
	Orphan2 string
	Orphan3 string
	Orphan4 string

	// Pending (3 records) -- unclassified
	Pending1 string
	Pending2 string
	Pending3 string

	// Ephemeral/stale (3 records)
	EphemeralRecent string // created today
	EphemeralStale  string // created 2 weeks ago, never accessed
	EphemeralMeeting string // yesterday's meeting agenda

	// Chunked (1 parent + 3 chunks)
	ChunkedParent string
	Chunk1        string
	Chunk2        string
	Chunk3        string
}

// PopulatedEngine creates a new engine pre-loaded with ~50 realistic
// records covering every axis: all temporalities, knowledge types,
// epistemic statuses, resolution states, edge types, orphans, pending
// records, chunks, and multiple keyword clusters for concept emergence.
//
// All timestamps are deterministic relative to now, so tests don't flake.
// Embeddings use simple deterministic vectors grouped by cluster.
func PopulatedEngine(t *testing.T) (*core.Engine, *PopulatedStore) {
	t.Helper()
	eng := NewEngine(t)
	now := time.Now().UTC()

	s := &PopulatedStore{}

	// ---- Work cluster ----
	// Vectors: [0.9, 0.1, 0.0, ...]  (work-flavored)

	s.WorkReorg = Record("The engineering team was restructured into two squads: platform and product. Each squad has its own standup and backlog.").
		Temporality("durable").Confidence(0.95).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("work", "team", "engineering", "reorg").
		Summary("Engineering split into platform and product squads").
		CreatedAt(now.Add(-30 * 24 * time.Hour)).AccessCount(8).LastAccessed(now.Add(-2 * 24 * time.Hour)).
		Embedding([]float32{0.9, 0.1, 0.05, 0.0, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.WorkNewManager = Record("New manager started this week. Prefers async updates over daily standups. Wants written weekly summaries instead.").
		Temporality("durable").Confidence(0.9).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("work", "team", "manager", "communication").
		Summary("New manager prefers async updates and weekly written summaries").
		CreatedAt(now.Add(-7 * 24 * time.Hour)).AccessCount(3).LastAccessed(now.Add(-1 * 24 * time.Hour)).
		Embedding([]float32{0.85, 0.15, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.WorkDeadline = Record("Project milestone due by end of Q2. Need to have the API finalized and integration tests passing.").
		Temporality("temporal").Confidence(0.95).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("work", "deadline", "project", "milestone").
		Summary("Q2 milestone: API finalized + integration tests").
		Importance(0.8).
		CreatedAt(now.Add(-14 * 24 * time.Hour)).AccessCount(5).LastAccessed(now.Add(-3 * 24 * time.Hour)).
		Embedding([]float32{0.88, 0.12, 0.0, 0.05, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.WorkMeeting = Record("Team retrospective every other Friday at 2pm. Format: what went well, what didn't, action items. Keep it under 45 minutes.").
		Temporality("durable").Confidence(1.0).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("work", "team", "meeting", "retro").
		Summary("Biweekly retro: went well / didn't / actions, 45min max").
		CreatedAt(now.Add(-60 * 24 * time.Hour)).AccessCount(12).LastAccessed(now.Add(-5 * 24 * time.Hour)).
		Embedding([]float32{0.87, 0.13, 0.05, 0.0, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.WorkOldTool = Record("We use the shared spreadsheet for project tracking. Each row is a task, columns for status, owner, and due date.").
		Temporality("durable").Confidence(0.9).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("work", "project", "tracking", "tools").
		Summary("Using shared spreadsheet for project tracking").
		CreatedAt(now.Add(-90 * 24 * time.Hour)).AccessCount(15).LastAccessed(now.Add(-20 * 24 * time.Hour)).
		ValidUntil(now.Add(-10 * 24 * time.Hour)).
		Resolution("superseded").ResolvedAt(now.Add(-10 * 24 * time.Hour)).
		Embedding([]float32{0.86, 0.14, 0.0, 0.0, 0.05, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.WorkNewTool = Record("Switched to the kanban board app for project tracking. Much better visibility. Columns: backlog, in progress, review, done.").
		Temporality("durable").Confidence(0.95).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("work", "project", "tracking", "tools", "kanban").
		Summary("Switched to kanban board for project tracking").
		CreatedAt(now.Add(-10 * 24 * time.Hour)).AccessCount(6).LastAccessed(now.Add(-1 * 24 * time.Hour)).
		Embedding([]float32{0.87, 0.13, 0.0, 0.0, 0.07, 0.0, 0.0, 0.0}).
		Add(t, eng)

	// ---- Health cluster ----
	// Vectors: [0.0, 0.9, 0.1, ...]  (health-flavored)

	s.HealthDoctorVisit = Record("Annual checkup went well. Blood pressure normal, cholesterol slightly elevated. Follow up in 6 months.").
		Temporality("temporal").Confidence(1.0).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("health", "doctor", "checkup", "medical").
		Summary("Annual checkup: BP normal, cholesterol slightly high, follow up 6mo").
		CreatedAt(now.Add(-45 * 24 * time.Hour)).AccessCount(2).LastAccessed(now.Add(-30 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.9, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.HealthExercise = Record("Current routine: run 3x/week (30 min), strength training 2x/week, yoga on Sundays. Rest day Wednesday.").
		Temporality("durable").Confidence(0.95).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("health", "exercise", "fitness", "routine").
		Summary("Weekly exercise: run 3x, strength 2x, yoga Sunday, rest Wed").
		CreatedAt(now.Add(-20 * 24 * time.Hour)).AccessCount(10).LastAccessed(now.Add(-2 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.85, 0.15, 0.0, 0.0, 0.05, 0.0, 0.0}).
		Add(t, eng)

	s.HealthAllergy = Record("Allergic to shellfish. Reaction: hives and throat swelling. Carry antihistamines when eating out.").
		Temporality("immutable").Confidence(1.0).KnowledgeType("semantic").EpistemicStatus("well_established").
		Keywords("health", "allergy", "food", "medical").
		Summary("Shellfish allergy: hives + throat swelling, carry antihistamines").
		Importance(0.9).
		CreatedAt(now.Add(-180 * 24 * time.Hour)).AccessCount(4).LastAccessed(now.Add(-15 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.88, 0.12, 0.0, 0.0, 0.0, 0.05, 0.0}).
		Add(t, eng)

	s.HealthSleep = Record("Discovered that no screens 1 hour before bed dramatically improves sleep quality. Also keeping the room at 65F helps.").
		Temporality("durable").Confidence(0.8).KnowledgeType("semantic").EpistemicStatus("probable").
		Keywords("health", "sleep", "habits").
		Summary("No screens 1hr before bed + 65F room = better sleep").
		CreatedAt(now.Add(-25 * 24 * time.Hour)).AccessCount(3).LastAccessed(now.Add(-10 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.87, 0.13, 0.0, 0.0, 0.0, 0.0, 0.05}).
		Add(t, eng)

	s.HealthSupplement = Record("That magnesium supplement was supposed to help with sleep but multiple studies show no significant effect for people without deficiency.").
		Temporality("durable").Confidence(0.7).KnowledgeType("semantic").EpistemicStatus("refuted").
		Keywords("health", "sleep", "supplements").
		Summary("Magnesium for sleep: refuted for non-deficient people").
		CreatedAt(now.Add(-15 * 24 * time.Hour)).AccessCount(2).LastAccessed(now.Add(-12 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.86, 0.14, 0.0, 0.0, 0.0, 0.0, 0.08}).
		Add(t, eng)

	s.HealthPrescription = Record("Prescription refill needed by end of month. Call the pharmacy two days before to make sure it's ready.").
		Temporality("temporal").Confidence(1.0).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("health", "prescription", "medical", "reminder").
		Summary("Prescription refill: call pharmacy 2 days early").
		Importance(0.7).
		CreatedAt(now.Add(-5 * 24 * time.Hour)).AccessCount(1).LastAccessed(now.Add(-3 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.9, 0.1, 0.0, 0.0, 0.0, 0.05, 0.0}).
		Add(t, eng)

	// ---- Cooking cluster ----
	// Vectors: [0.0, 0.0, 0.9, ...]  (cooking-flavored)

	s.CookingRecipe = Record("The lemon garlic pasta recipe: cook spaghetti al dente, sautee garlic in olive oil, add lemon juice and zest, toss with pasta water, finish with parmesan.").
		Temporality("durable").Confidence(0.95).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("cooking", "food", "recipe", "pasta").
		Summary("Lemon garlic pasta: garlic + olive oil + lemon + pasta water + parm").
		CreatedAt(now.Add(-40 * 24 * time.Hour)).AccessCount(7).LastAccessed(now.Add(-4 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.9, 0.1, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.CookingSubstitution = Record("Greek yogurt works as a substitute for sour cream in most recipes. Same tanginess, less fat, more protein.").
		Temporality("durable").Confidence(0.85).KnowledgeType("semantic").EpistemicStatus("well_established").
		Keywords("cooking", "food", "substitution", "ingredients").
		Summary("Greek yogurt substitutes for sour cream in most recipes").
		CreatedAt(now.Add(-35 * 24 * time.Hour)).AccessCount(3).LastAccessed(now.Add(-8 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.88, 0.12, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.CookingDinnerParty = Record("Dinner party last Saturday: served the lemon pasta as starter, grilled vegetables, and chocolate mousse. Everyone loved the mousse recipe.").
		Temporality("ephemeral").Confidence(1.0).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("cooking", "food", "dinner", "entertaining").
		Summary("Dinner party: lemon pasta starter, grilled veg, chocolate mousse hit").
		CreatedAt(now.Add(-3 * 24 * time.Hour)).AccessCount(1).LastAccessed(now.Add(-2 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.87, 0.13, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.CookingDietary = Record("No refined sugar. Using honey or maple syrup as sweeteners instead. Exception: special occasions.").
		Temporality("durable").Confidence(0.9).KnowledgeType("semantic").EpistemicStatus("well_established").
		Keywords("cooking", "food", "health", "diet", "sugar").
		Summary("No refined sugar policy; honey/maple syrup instead, exceptions for occasions").
		CreatedAt(now.Add(-60 * 24 * time.Hour)).AccessCount(5).LastAccessed(now.Add(-6 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.1, 0.85, 0.05, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.CookingTechnique = Record("For crispy roasted vegetables: cut uniform size, toss with oil, spread on sheet pan without crowding, roast at 425F. Don't stir for first 15 minutes.").
		Temporality("durable").Confidence(0.9).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("cooking", "food", "technique", "roasting").
		Summary("Crispy roasted veg: uniform cut, uncrowded, 425F, don't stir 15min").
		CreatedAt(now.Add(-50 * 24 * time.Hour)).AccessCount(4).LastAccessed(now.Add(-7 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.9, 0.1, 0.0, 0.05, 0.0, 0.0}).
		Add(t, eng)

	// ---- Travel cluster ----
	// Vectors: [0.0, 0.0, 0.0, 0.9, ...]

	s.TravelSeat = Record("Always request a window seat. Aisle for flights over 6 hours so I can get up easier.").
		Temporality("immutable").Confidence(1.0).KnowledgeType("semantic").EpistemicStatus("well_established").
		Keywords("travel", "flights", "preference").
		Summary("Window seat default, aisle for 6+ hour flights").
		CreatedAt(now.Add(-120 * 24 * time.Hour)).AccessCount(6).LastAccessed(now.Add(-14 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.9, 0.1, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.TravelPacking = Record("Packing checklist: passport, chargers, medications, 1 week clothes in carry-on, toiletry bag, noise-canceling headphones, kindle.").
		Temporality("durable").Confidence(0.95).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("travel", "packing", "checklist").
		Summary("Packing: passport, chargers, meds, clothes, toiletries, headphones, kindle").
		CreatedAt(now.Add(-100 * 24 * time.Hour)).AccessCount(9).LastAccessed(now.Add(-20 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.88, 0.12, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.TravelTrip = Record("Trip to the coast planned for next weekend. Need to book the rental car and confirm the hotel reservation.").
		Temporality("ephemeral").Confidence(0.9).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("travel", "trip", "planning").
		Summary("Coast trip next weekend: book rental car, confirm hotel").
		CreatedAt(now.Add(-2 * 24 * time.Hour)).AccessCount(2).LastAccessed(now.Add(-1 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.87, 0.13, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.TravelLoyalty = Record("Hotel loyalty program: gold status, free breakfast included, late checkout available. Member number stored in password manager.").
		Temporality("durable").Confidence(1.0).KnowledgeType("reference").EpistemicStatus("well_established").
		Keywords("travel", "hotel", "loyalty", "rewards").
		Summary("Hotel loyalty: gold status, free breakfast, late checkout").
		CreatedAt(now.Add(-80 * 24 * time.Hour)).AccessCount(4).LastAccessed(now.Add(-30 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.86, 0.14, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.TravelPassport = Record("Passport expires in 8 months. Renewal takes 6-8 weeks. Need to submit renewal application soon.").
		Temporality("temporal").Confidence(1.0).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("travel", "passport", "renewal", "deadline").
		Summary("Passport expires in 8mo, renewal takes 6-8wk -- submit soon").
		Importance(0.8).
		CreatedAt(now.Add(-10 * 24 * time.Hour)).AccessCount(2).LastAccessed(now.Add(-5 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.85, 0.15, 0.0, 0.0, 0.0}).
		Add(t, eng)

	// ---- Finance cluster ----
	// Vectors: [0.0, 0.0, 0.0, 0.0, 0.9, ...]

	s.FinanceBudget = Record("50/30/20 rule: 50% needs, 30% wants, 20% savings. Track monthly in the budget spreadsheet.").
		Temporality("durable").Confidence(0.9).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("finance", "budget", "money", "savings").
		Summary("50/30/20 budget rule, tracked monthly in spreadsheet").
		CreatedAt(now.Add(-90 * 24 * time.Hour)).AccessCount(8).LastAccessed(now.Add(-7 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.9, 0.1, 0.0, 0.0}).
		Add(t, eng)

	s.FinanceExpense = Record("To submit expense reports: photograph receipt, upload to the portal within 30 days, select cost center, manager approves.").
		Temporality("durable").Confidence(1.0).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("finance", "work", "expense", "reimbursement").
		Summary("Expense reports: photo receipt, upload within 30d, select cost center").
		CreatedAt(now.Add(-70 * 24 * time.Hour)).AccessCount(6).LastAccessed(now.Add(-10 * 24 * time.Hour)).
		Embedding([]float32{0.1, 0.0, 0.0, 0.0, 0.88, 0.12, 0.0, 0.0}).
		Add(t, eng)

	s.FinanceTax = Record("Estimated tax payment due mid-June. Set aside 25% of freelance income. Keep all contractor invoices organized.").
		Temporality("temporal").Confidence(0.95).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("finance", "tax", "deadline", "freelance").
		Summary("Estimated tax due mid-June, set aside 25% of freelance income").
		Importance(0.9).
		CreatedAt(now.Add(-30 * 24 * time.Hour)).AccessCount(3).LastAccessed(now.Add(-5 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.87, 0.13, 0.0, 0.0}).
		Add(t, eng)

	s.FinanceInvestment = Record("Thinking about putting some savings into index funds. Low fees, historically good returns. Need to research tax-advantaged accounts.").
		Temporality("durable").Confidence(0.5).KnowledgeType("semantic").EpistemicStatus("speculative").
		Keywords("finance", "investment", "savings", "research").
		Summary("Considering index funds -- low fees, need to research tax advantages").
		CreatedAt(now.Add(-12 * 24 * time.Hour)).AccessCount(1).LastAccessed(now.Add(-11 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.86, 0.14, 0.0, 0.0}).
		Add(t, eng)

	// ---- Learning cluster ----
	// Vectors: [0.0, 0.0, 0.0, 0.0, 0.0, 0.9, ...]

	s.LearnBook = Record("Book recommendation: 'Thinking, Fast and Slow' by Kahneman. Covers cognitive biases, dual-process theory. Excellent for decision-making.").
		Temporality("durable").Confidence(0.9).KnowledgeType("reference").EpistemicStatus("well_established").
		Keywords("learning", "books", "reading", "psychology").
		Summary("Recommended: 'Thinking, Fast and Slow' -- cognitive biases, decisions").
		CreatedAt(now.Add(-55 * 24 * time.Hour)).AccessCount(3).LastAccessed(now.Add(-20 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.9, 0.1, 0.0}).
		Add(t, eng)

	s.LearnCourse = Record("Finished the online statistics course. Key takeaway: Bayesian thinking is more intuitive than frequentist for real-world decisions.").
		Temporality("durable").Confidence(0.85).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("learning", "course", "statistics", "bayesian").
		Summary("Stats course done: Bayesian > frequentist for real-world decisions").
		CreatedAt(now.Add(-22 * 24 * time.Hour)).AccessCount(2).LastAccessed(now.Add(-15 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.88, 0.12, 0.0}).
		Add(t, eng)

	s.LearnRetention = Record("Spaced repetition dramatically improves long-term retention. The forgetting curve is real. Review at increasing intervals: 1 day, 3 days, 7 days, 14 days.").
		Temporality("durable").Confidence(0.9).KnowledgeType("semantic").EpistemicStatus("well_established").
		Keywords("learning", "memory", "spaced-repetition", "study").
		Summary("Spaced repetition works: review at 1d, 3d, 7d, 14d intervals").
		CreatedAt(now.Add(-40 * 24 * time.Hour)).AccessCount(5).LastAccessed(now.Add(-3 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.87, 0.13, 0.0}).
		Add(t, eng)

	s.LearnContested = Record("Some people claim that listening to music while studying improves focus. The evidence is mixed -- it seems to depend on the type of task and the individual.").
		Temporality("durable").Confidence(0.5).KnowledgeType("semantic").EpistemicStatus("contested").
		Keywords("learning", "study", "music", "focus").
		Summary("Music while studying: contested evidence, depends on task and person").
		CreatedAt(now.Add(-18 * 24 * time.Hour)).AccessCount(1).LastAccessed(now.Add(-17 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.86, 0.14, 0.0}).
		Add(t, eng)

	// ---- People ----
	// Vectors: [0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.9, ...]

	s.PersonVendor = Record("Sarah is the point of contact for the office supply vendor. Email: on file. Contract renews annually in March.").
		Temporality("temporal").Confidence(1.0).KnowledgeType("reference").EpistemicStatus("well_established").
		Keywords("people", "vendor", "contact", "work").
		Summary("Sarah = vendor contact, contract renews March").
		CreatedAt(now.Add(-60 * 24 * time.Hour)).AccessCount(4).LastAccessed(now.Add(-14 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.9, 0.1}).
		Add(t, eng)

	s.PersonNeighbor = Record("Neighbor in unit 4B is named Marcus. Has a dog (golden retriever, named Bear). Offered to water plants when I travel.").
		Temporality("durable").Confidence(0.95).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("people", "neighbor", "home").
		Summary("Neighbor Marcus (4B), dog Bear, will water plants").
		CreatedAt(now.Add(-45 * 24 * time.Hour)).AccessCount(2).LastAccessed(now.Add(-30 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.88, 0.12}).
		Add(t, eng)

	s.PersonBirthday = Record("Alex's birthday is September 15th. Likes hiking gear, coffee, and mystery novels.").
		Temporality("durable").Confidence(1.0).KnowledgeType("reference").EpistemicStatus("well_established").
		Keywords("people", "birthday", "gifts").
		Summary("Alex birthday Sep 15: hiking gear, coffee, mystery novels").
		CreatedAt(now.Add(-100 * 24 * time.Hour)).AccessCount(3).LastAccessed(now.Add(-60 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.87, 0.13}).
		Add(t, eng)

	s.PersonDoctor = Record("Doctor recommended reducing caffeine to under 200mg/day. Also suggested trying meditation for stress management.").
		Temporality("durable").Confidence(0.85).KnowledgeType("semantic").EpistemicStatus("probable").
		Keywords("people", "health", "doctor", "caffeine").
		Summary("Doctor: caffeine under 200mg/day, try meditation for stress").
		CreatedAt(now.Add(-30 * 24 * time.Hour)).AccessCount(2).LastAccessed(now.Add(-20 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.15, 0.0, 0.0, 0.0, 0.0, 0.85, 0.0}).
		Add(t, eng)

	// ---- TODOs ----

	s.TodoOpen = Record("TODO: Clean out the garage this weekend. Sort into keep, donate, and trash piles.").
		Temporality("temporal").Confidence(1.0).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("todo", "home", "organizing").
		Summary("TODO: Clean garage -- keep/donate/trash").
		Importance(0.7).
		CreatedAt(now.Add(-3 * 24 * time.Hour)).AccessCount(1).LastAccessed(now.Add(-1 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.9}).
		Add(t, eng)

	s.TodoCompleted = Record("TODO: Schedule annual dental cleaning").
		Temporality("temporal").Confidence(1.0).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("todo", "health", "dental").
		Summary("DONE: Dental cleaning scheduled").
		Resolution("completed").ResolvedAt(now.Add(-5 * 24 * time.Hour)).
		ValidUntil(now.Add(-5 * 24 * time.Hour)).
		CreatedAt(now.Add(-14 * 24 * time.Hour)).AccessCount(3).LastAccessed(now.Add(-5 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0, 0.85}).
		Add(t, eng)

	s.TodoAbandoned = Record("TODO: Learn to play guitar. Bought a beginner book but never got around to it.").
		Temporality("durable").Confidence(0.8).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("todo", "hobbies", "guitar", "music").
		Summary("ABANDONED: Learn guitar -- never started").
		Resolution("abandoned").ResolvedAt(now.Add(-7 * 24 * time.Hour)).
		ValidUntil(now.Add(-7 * 24 * time.Hour)).
		CreatedAt(now.Add(-60 * 24 * time.Hour)).AccessCount(2).LastAccessed(now.Add(-7 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.1, 0.0, 0.85}).
		Add(t, eng)

	s.TodoObsolete = Record("TODO: Renew the old chat app subscription").
		Temporality("temporal").Confidence(1.0).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("todo", "work", "tools").
		Summary("OBSOLETE: Renew old chat sub -- we switched platforms").
		Resolution("obsolete").ResolvedAt(now.Add(-10 * 24 * time.Hour)).
		ValidUntil(now.Add(-10 * 24 * time.Hour)).
		CreatedAt(now.Add(-30 * 24 * time.Hour)).AccessCount(1).LastAccessed(now.Add(-10 * 24 * time.Hour)).
		Embedding([]float32{0.1, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.85}).
		Add(t, eng)

	s.TodoOpenLow = Record("TODO: Organize the photo library. Been meaning to do this for months.").
		Temporality("durable").Confidence(0.7).KnowledgeType("procedural").EpistemicStatus("well_established").
		Keywords("todo", "photos", "organizing").
		Summary("TODO: Organize photo library").
		Importance(0.2).
		CreatedAt(now.Add(-45 * 24 * time.Hour)).AccessCount(0).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.88}).
		Add(t, eng)

	// ---- Orphans (no keywords, no edges) ----

	s.Orphan1 = Record("Look into that thing Jamie mentioned about the new bakery downtown").
		Confidence(0.3).
		CreatedAt(now.Add(-8 * 24 * time.Hour)).
		Embedding([]float32{0.2, 0.2, 0.2, 0.2, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.Orphan2 = Record("'The best time to plant a tree was 20 years ago. The second best time is now.'").
		Confidence(0.5).
		CreatedAt(now.Add(-20 * 24 * time.Hour)).
		Embedding([]float32{0.1, 0.1, 0.1, 0.1, 0.1, 0.3, 0.1, 0.1}).
		Add(t, eng)

	s.Orphan3 = Record("interesting article about urban farming, saved the link somewhere").
		Confidence(0.4).
		CreatedAt(now.Add(-12 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.1, 0.3, 0.0, 0.0, 0.2, 0.0, 0.0}).
		Add(t, eng)

	s.Orphan4 = Record("password for the wifi at the coffee shop is 'latteart2024'").
		Temporality("ephemeral").Confidence(0.9).
		CreatedAt(now.Add(-30 * 24 * time.Hour)).
		Embedding([]float32{0.1, 0.0, 0.0, 0.1, 0.0, 0.0, 0.1, 0.0}).
		Add(t, eng)

	// ---- Pending (unclassified) ----

	s.Pending1 = Record("Had a great conversation about sustainable architecture. The concept of passive houses is fascinating -- minimal energy for heating/cooling.").
		Pending().
		CreatedAt(now.Add(-2 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.4, 0.0, 0.0}).
		Add(t, eng)

	s.Pending2 = Record("Meeting notes from Thursday: discussed Q3 planning, need to hire two more engineers, budget approved for new monitoring tools.").
		Pending().
		CreatedAt(now.Add(-1 * 24 * time.Hour)).
		Embedding([]float32{0.5, 0.0, 0.0, 0.0, 0.3, 0.0, 0.0, 0.0}).
		Add(t, eng)

	s.Pending3 = Record("Someone recommended a podcast about behavioral economics. Need to find the name.").
		Pending().
		CreatedAt(now.Add(-4 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.5, 0.0, 0.0}).
		Add(t, eng)

	// ---- Ephemeral/stale ----

	s.EphemeralRecent = Record("Quick note: the dry cleaner closes at 6pm on weekdays, 3pm Saturday, closed Sunday.").
		Temporality("ephemeral").Confidence(0.9).KnowledgeType("reference").EpistemicStatus("well_established").
		Keywords("errands", "dry-cleaner", "hours").
		Summary("Dry cleaner hours: weekday 6pm, Sat 3pm, Sun closed").
		CreatedAt(now.Add(-6 * time.Hour)).AccessCount(1).LastAccessed(now.Add(-2 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.3}).
		Add(t, eng)

	s.EphemeralStale = Record("Reminder: pick up the package from the front office before Friday").
		Temporality("ephemeral").Confidence(1.0).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("reminder", "errands").
		Summary("Pick up package before Friday").
		CreatedAt(now.Add(-14 * 24 * time.Hour)).
		Embedding([]float32{0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.2}).
		Add(t, eng)

	s.EphemeralMeeting = Record("Yesterday's team sync agenda: sprint review, blockers, upcoming PTO. Action: follow up on the deployment issue.").
		Temporality("ephemeral").Confidence(0.95).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("work", "meeting", "agenda").
		Summary("Yesterday sync: sprint review, blockers, PTO, follow up deployment").
		CreatedAt(now.Add(-24 * time.Hour)).AccessCount(1).LastAccessed(now.Add(-20 * time.Hour)).
		Embedding([]float32{0.5, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0}).
		Add(t, eng)

	// ---- Chunked record ----
	// Simulate a long meeting transcript split into 3 chunks.

	s.ChunkedParent = Record("Q3 planning meeting full transcript. Duration: 90 minutes. " +
		"Attendees: team leads from all squads. " +
		"Topics covered: hiring plan, budget allocation, technical debt priorities, " +
		"cross-team dependencies, and milestone dates. " +
		"Key decisions: approved two new headcount for platform squad, " +
		"allocated 20% of sprint capacity to tech debt, " +
		"set code freeze for August 15th.").
		Temporality("temporal").Confidence(0.95).KnowledgeType("episodic").EpistemicStatus("well_established").
		Keywords("work", "meeting", "planning", "Q3").
		Summary("Q3 planning: 2 new hires, 20% tech debt, code freeze Aug 15").
		Importance(0.8).
		CreatedAt(now.Add(-5 * 24 * time.Hour)).AccessCount(4).LastAccessed(now.Add(-1 * 24 * time.Hour)).
		Embedding([]float32{0.7, 0.0, 0.0, 0.0, 0.2, 0.0, 0.0, 0.0}).
		Add(t, eng)

	// Add chunk nodes manually (they have chunk_of edges to parent).
	eng.Lock()
	chunk1 := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("Hiring discussion: two new engineers for platform squad. " +
			"One senior backend, one mid-level focused on observability. " +
			"Job descriptions to be finalized by end of week. " +
			"Recruiter already has pipeline of candidates from last quarter."),
	})
	for k, v := range chunk1.Properties {
		eng.PropIdx().Add(chunk1.ID, k, v)
	}
	eng.Graph().AddEdge(chunk1.ID, s.ChunkedParent, "chunk_of", 1.0, nil)

	chunk2 := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("Tech debt priorities: the authentication middleware needs refactoring, " +
			"database connection pooling is causing intermittent timeouts, " +
			"and the logging pipeline drops messages under load. " +
			"Agreed to dedicate 20% of each sprint to addressing these."),
	})
	for k, v := range chunk2.Properties {
		eng.PropIdx().Add(chunk2.ID, k, v)
	}
	eng.Graph().AddEdge(chunk2.ID, s.ChunkedParent, "chunk_of", 1.0, nil)

	chunk3 := eng.Graph().AddNode(graph.Properties{
		"content_full": graph.StringProperty("Timeline decisions: code freeze August 15th, " +
			"QA cycle August 16-30, release candidate September 1st. " +
			"Each squad owns their own release checklist. " +
			"Cross-team integration testing window: August 10-14."),
	})
	for k, v := range chunk3.Properties {
		eng.PropIdx().Add(chunk3.ID, k, v)
	}
	eng.Graph().AddEdge(chunk3.ID, s.ChunkedParent, "chunk_of", 1.0, nil)

	s.Chunk1 = chunk1.ID
	s.Chunk2 = chunk2.ID
	s.Chunk3 = chunk3.ID

	eng.Save("test: chunks")
	eng.Unlock()

	// ---- Edges ----

	// Work cluster: internal links
	Edge(t, eng, s.WorkReorg, s.WorkNewManager, "relates_to", 0.8)
	Edge(t, eng, s.WorkReorg, s.WorkMeeting, "relates_to", 0.6)
	Edge(t, eng, s.WorkNewTool, s.WorkOldTool, "supersedes", 0.95)
	Edge(t, eng, s.WorkDeadline, s.WorkMeeting, "relates_to", 0.5)

	// Health cluster: internal links
	Edge(t, eng, s.HealthExercise, s.HealthSleep, "relates_to", 0.7)
	Edge(t, eng, s.HealthSupplement, s.HealthSleep, "contradicts", 0.6)
	Edge(t, eng, s.HealthDoctorVisit, s.HealthPrescription, "relates_to", 0.8)
	Edge(t, eng, s.PersonDoctor, s.HealthDoctorVisit, "discusses", 0.7)

	// Cooking cluster: internal links
	Edge(t, eng, s.CookingRecipe, s.CookingDinnerParty, "relates_to", 0.8)
	Edge(t, eng, s.CookingDietary, s.CookingSubstitution, "relates_to", 0.6)
	Edge(t, eng, s.CookingTechnique, s.CookingDinnerParty, "relates_to", 0.5)

	// Cross-cluster links
	Edge(t, eng, s.CookingDietary, s.HealthAllergy, "relates_to", 0.7)         // cooking + health overlap
	Edge(t, eng, s.FinanceExpense, s.WorkDeadline, "discusses", 0.4)            // finance + work overlap
	Edge(t, eng, s.TravelPassport, s.TravelTrip, "relates_to", 0.6)            // travel internal
	Edge(t, eng, s.LearnRetention, s.LearnCourse, "relates_to", 0.7)           // learning internal
	Edge(t, eng, s.TodoObsolete, s.WorkOldTool, "relates_to", 0.9)             // TODO linked to what made it obsolete
	Edge(t, eng, s.FinanceBudget, s.FinanceInvestment, "relates_to", 0.5)      // finance internal
	Edge(t, eng, s.PersonDoctor, s.HealthSleep, "discusses", 0.5)              // doctor advice about sleep
	Edge(t, eng, s.EphemeralMeeting, s.WorkReorg, "relates_to", 0.4)           // meeting references reorg

	return eng, s
}

// PopulatedEngineWithEmbeddings creates a PopulatedEngine and replaces
// the deterministic test embeddings with the provided real embeddings.
// Keys in the embeddings map are PopulatedStore field names (e.g.
// "WorkReorg", "HealthAllergy"). Records not in the map keep their
// original toy embeddings.
func PopulatedEngineWithEmbeddings(t *testing.T, embeddings map[string][]float32) (*core.Engine, *PopulatedStore) {
	t.Helper()
	eng, store := PopulatedEngine(t)

	if len(embeddings) == 0 {
		return eng, store
	}

	// Build field name -> record ID mapping via reflection.
	idMap := storeFieldIDs(store)

	eng.Lock()
	defer eng.Unlock()

	for fieldName, vec := range embeddings {
		id, ok := idMap[fieldName]
		if !ok || id == "" {
			continue
		}
		eng.SetProp(id, "embedding_full", graph.BytesProperty(float32sToBytes(vec)))
		eng.VecIdx().Remove(id)
		eng.VecIdx().Add(id, vec)
	}
	eng.Save("eval: real embeddings")
	return eng, store
}

// storeFieldIDs maps PopulatedStore field names to their string (ID) values.
func storeFieldIDs(store *PopulatedStore) map[string]string {
	m := make(map[string]string)
	v := reflect.ValueOf(store).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Type.Kind() == reflect.String {
			m[f.Name] = v.Field(i).String()
		}
	}
	return m
}

// float32sToBytes converts a float32 slice to bytes for graph storage.
func float32sToBytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		b[i*4] = byte(bits)
		b[i*4+1] = byte(bits >> 8)
		b[i*4+2] = byte(bits >> 16)
		b[i*4+3] = byte(bits >> 24)
	}
	return b
}
