package cli

import (
	"fmt"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/curation"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/spf13/cobra"
)

var (
	backfillAuthorDryRun bool
	backfillAuthorFlag   string
)

var backfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "One-time data backfills for existing stores",
	Long: `One-time data backfills that bring existing stores up to date with
properties newer gramaton versions stamp at creation time.

Each subcommand is idempotent: rerunning against an already-current
store is a no-op.`,
}

var backfillAuthorCmd = &cobra.Command{
	Use:   "author",
	Short: "Stamp the author property onto nodes that lack it",
	Long: `Stamps the set-once author property onto existing nodes that lack
it. New nodes are attributed at creation time; this command is the
deliberate one-time mass update that brings pre-attribution stores
up to date.

The stamped value is the configured author (author.name and
author.email in config, composed git-style as "Name <email>";
gramaton init collects it), or the --author flag value verbatim when
provided. Nodes created by curation (concept and observation nodes)
are attributed to "curation" instead.

Nodes that already carry an author property are never touched
(author is set-once), so reruns are no-ops.

Refuses to run while a gramaton server is active, --dry-run
included (even a preview opens the store the server holds locked).
Stop the server first: gramaton stop.
Use --dry-run to preview changes without applying them.`,
	RunE: runBackfillAuthor,
}

func init() {
	backfillAuthorCmd.Flags().BoolVar(&backfillAuthorDryRun, "dry-run", false, "preview changes without applying")
	backfillAuthorCmd.Flags().StringVar(&backfillAuthorFlag, "author", "",
		"author value to stamp, verbatim (default: the configured author, composed as \"Name <email>\")")
	backfillCmd.AddCommand(backfillAuthorCmd)
	rootCmd.AddCommand(backfillCmd)
}

func runBackfillAuthor(cmd *cobra.Command, args []string) error {
	dir := configDir()

	// Refuse to run while the server is active to prevent concurrent
	// access to the same store. Unconditional -- a dry run opens the
	// same bbolt file, which a live server holds locked, so proceeding
	// would block indefinitely instead of failing cleanly.
	if info, err := server.ReadServerInfo(dir); err == nil && server.IsProcessAlive(info.PID) {
		return fmt.Errorf("server is running (pid %d). Stop it first: gramaton stop", info.PID)
	}

	eng, err := core.LoadEngine(dir, baseConfigDir())
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	author, err := resolveBackfillAuthor(backfillAuthorFlag, eng.Config().Author.String())
	if err != nil {
		return err
	}

	if backfillAuthorDryRun {
		plan, err := executeAuthorBackfill(eng, author, true)
		if err != nil {
			return fmt.Errorf("backfill author: %w", err)
		}
		fmt.Println("Dry run -- no changes applied.")
		fmt.Println()
		printAuthorBackfillPlan(plan, author, true)
		if plan.stampCount() == 0 {
			fmt.Println("\nNothing to backfill.")
		}
		return nil
	}

	plan, err := executeAuthorBackfill(eng, author, false)
	if err != nil {
		return fmt.Errorf("backfill author: %w", err)
	}
	printAuthorBackfillPlan(plan, author, false)
	if plan.stampCount() == 0 {
		fmt.Println("\nNothing to backfill. All nodes already carry an author.")
	} else {
		fmt.Println("\nBackfill applied and saved.")
	}
	return nil
}

// resolveBackfillAuthor picks the value to stamp: the --author flag
// verbatim when non-empty (it is the literal property value, not
// parsed), otherwise the composed configured author. Both empty is a
// user-facing error.
func resolveBackfillAuthor(flagValue, configured string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if configured != "" {
		return configured, nil
	}
	return "", fmt.Errorf("no author configured: set author.name / author.email in the gramaton config (gramaton init collects it), or pass --author")
}

// authorBackfillPlan classifies every node in the store for the
// author backfill.
type authorBackfillPlan struct {
	stampAuthor    []string // node IDs missing author, to receive the resolved value
	stampCuration  []string // curation-created node IDs missing author, to receive "curation"
	alreadyStamped int      // nodes skipped: author property already present (set-once)
	total          int      // total nodes scanned
}

// stampCount is the number of nodes the plan would write to.
func (p authorBackfillPlan) stampCount() int {
	return len(p.stampAuthor) + len(p.stampCuration)
}

// planAuthorBackfill walks every node and classifies it. Nodes that
// already carry an author property (any value) are skipped -- author
// is set-once. Curation-created nodes are identified by the node_type
// property curation stamps at creation time: "concept"
// (curation/deterministic.go concept emergence) and "observation"
// (curation/observe.go observation extraction). Everything else gets
// the resolved author value. Caller must hold an engine lock (read
// lock for a dry run; WithWriteBatch's write lock for the real run).
func planAuthorBackfill(g *graph.Graph) authorBackfillPlan {
	var p authorBackfillPlan
	it := g.NodeIterator()
	defer it.Close()
	for it.Next() {
		n := it.Node()
		p.total++
		if _, ok := n.Properties["author"]; ok {
			p.alreadyStamped++
			continue
		}
		if nt, _ := n.Properties.GetString("node_type"); nt == "concept" || nt == "observation" {
			p.stampCuration = append(p.stampCuration, n.ID)
		} else {
			p.stampAuthor = append(p.stampAuthor, n.ID)
		}
	}
	return p
}

// executeAuthorBackfill runs the backfill against an already-loaded
// engine and returns the plan describing what was (or, for a dry run,
// would be) stamped.
//
// Dry run holds the read lock for the classification pass only and
// writes nothing. The real run goes through Engine.WithWriteBatch --
// write lock, all property-index writes batched into one bbolt
// transaction (a full-store backfill would otherwise fsync per node),
// and a single Save under the "backfill author" commit message when
// anything was stamped. Mirrors the mass-write shape of
// backup/import.go. The commit carries one field-scoped backfill
// action rather than per-record actions (a whole-store backfill would
// bloat the commit with one action per node).
func executeAuthorBackfill(eng *core.Engine, author string, dryRun bool) (authorBackfillPlan, error) {
	if dryRun {
		eng.RLock()
		plan := planAuthorBackfill(eng.Graph())
		eng.RUnlock()
		return plan, nil
	}

	var plan authorBackfillPlan
	err := eng.WithWriteBatch("backfill author", func(ws *core.WriteSession) (bool, error) {
		// Two passes, following core/repair.go: collect IDs first,
		// then mutate, so the iterator never observes writes.
		plan = planAuthorBackfill(ws.Graph())
		for _, id := range plan.stampAuthor {
			ws.SetProp(id, "author", graph.StringProperty(author))
		}
		for _, id := range plan.stampCuration {
			ws.SetProp(id, "author", graph.StringProperty(curation.NodeAuthor))
		}
		if plan.stampCount() > 0 {
			ws.AddAction(graph.CommitAction{Kind: graph.ActionBackfill, Field: "author"})
		}
		return plan.stampCount() > 0, nil
	})
	return plan, err
}

func printAuthorBackfillPlan(p authorBackfillPlan, author string, dryRun bool) {
	verb := "stamped"
	if dryRun {
		verb = "would stamp"
	}
	fmt.Printf("  %s: %d nodes with author %q\n", verb, len(p.stampAuthor), author)
	fmt.Printf("  %s: %d curation-created nodes with author %q\n", verb, len(p.stampCuration), curation.NodeAuthor)
	fmt.Printf("  already stamped (skipped): %d\n", p.alreadyStamped)
	fmt.Printf("  total nodes: %d\n", p.total)
}
