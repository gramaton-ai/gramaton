package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/gramaton-ai/gramaton/backup"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/migrate"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/spf13/cobra"
)

var (
	backfillCollapseApply     bool
	backfillCollapseMinWeight float64
)

var backfillCollapseCmd = &cobra.Command{
	Use:   "collapse",
	Short: "Collapse auto-superseded records out of the live graph",
	Long: `One-off migration for stores that predate mutable records: folds
the chains auto-supersession left behind out of the live graph,
leaving one live record per fact. Each collapsed record is archived
to a JSONL file next to the store before deletion, session-segment
provenance re-points to the surviving successor, and derived
observation children are cascaded.

Selection is deliberately narrow -- an inbound supersedes edge AND
resolution=superseded AND valid_until, all three. Manual supersedes
edges on current records are reported and kept. Chains containing a
below-floor edge weight (see --min-weight) or records in collections
their successor does not share are deferred to manual review. The
command reports what remains; it never certifies the store clean.

Without --apply this prints the plan and changes nothing. With
--apply, a store backup is taken first and its path printed; restore
from that backup to roll the collapse back wholesale, or read the
JSONL archive to recover individual records.

Refuses to run while a gramaton server is active (the store is
locked). Stop the server first: gramaton stop.`,
	Args: cobra.NoArgs,
	RunE: runBackfillCollapse,
}

func init() {
	backfillCollapseCmd.Flags().BoolVar(&backfillCollapseApply, "apply", false,
		"execute the plan (default is a dry-run print)")
	backfillCollapseCmd.Flags().Float64Var(&backfillCollapseMinWeight, "min-weight", 0.92,
		"per-edge weight floor; a chain containing any edge below it is deferred to manual review")
	backfillCmd.AddCommand(backfillCollapseCmd)
}

func runBackfillCollapse(cmd *cobra.Command, args []string) error {
	if err := guardLocalStore("backfill"); err != nil {
		return err
	}
	dir := configDir()

	if info, err := server.ReadServerInfo(dir); err == nil && server.IsProcessAlive(info.PID) {
		return fmt.Errorf("server is running (pid %d). Stop it first: gramaton stop", info.PID)
	}

	eng, err := core.LoadEngine(dir, baseConfigDir())
	if err != nil {
		return fmt.Errorf("load engine: %w", err)
	}

	opts := migrate.DefaultPlanOptions()
	opts.MinWeight = backfillCollapseMinWeight
	plan := migrate.BuildPlan(eng, opts)
	printCollapsePlan(plan, opts)

	if !backfillCollapseApply {
		fmt.Println("\nDry run -- no changes applied. Re-run with --apply to execute.")
		return nil
	}
	if len(plan.Victims) == 0 {
		fmt.Println("\nNothing to collapse.")
		return nil
	}

	// Inline backup before any mutation: restore-from-backup is the
	// documented wholesale rollback.
	cfg := eng.Config()
	backupDir := cfg.Backup.Dir
	if backupDir == "" {
		backupDir = backup.DefaultBackupDir()
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	backupPath, err := backup.Create(cfg.DataDir, cfgPath, backupDir, activeStoreName())
	if err != nil {
		return fmt.Errorf("pre-collapse backup failed; nothing was changed: %w", err)
	}
	fmt.Printf("\nBackup written: %s\n", backupPath)

	archivePath := filepath.Join(cfg.DataDir,
		fmt.Sprintf("collapse-archive-%s.jsonl", time.Now().UTC().Format("20060102-150405")))
	res, err := migrate.Apply(eng, plan, archivePath)
	if err != nil {
		return fmt.Errorf("collapse apply: %w", err)
	}

	fmt.Printf("Archive written: %s\n", res.ArchivePath)
	fmt.Printf("\nCollapsed %d record(s); %d skipped (state changed since planning).\n",
		res.VictimsDeleted, res.VictimsSkipped)
	fmt.Printf("Re-pointed %d segment provenance link(s); cascaded %d observation(s).\n",
		res.SegmentsRepointed, res.ObservationsDeleted)
	if len(plan.Deferred) > 0 || len(plan.Anomalies) > 0 {
		fmt.Println("Deferred items and anomalies above remain for manual review.")
	}
	return nil
}

func printCollapsePlan(p *migrate.Plan, opts migrate.PlanOptions) {
	fmt.Printf("Collapse plan (%d supersedes edge(s) examined, weight floor %.2f)\n\n",
		p.TotalSupersedesEdges, opts.MinWeight)

	fmt.Printf("Collapse: %d record(s)\n", len(p.Victims))
	for _, v := range p.Victims {
		fmt.Printf("  %s  (weight %.3f, resolved %s)\n", v.ID, v.EdgeWeight, v.Resolution)
		fmt.Printf("    victim:    %s\n", truncatePlanText(v.ContentShort))
		fmt.Printf("    successor: %s  %s\n", v.SuccessorID, truncatePlanText(v.SuccessorContentShort))
		if len(v.Collections) > 0 {
			fmt.Printf("    collections (shared by successor): %v\n", v.Collections)
		}
		if len(v.SegmentIDs) > 0 {
			fmt.Printf("    segments to re-point: %d\n", len(v.SegmentIDs))
		}
		if len(v.ObservationIDs) > 0 {
			fmt.Printf("    observations to cascade: %d\n", len(v.ObservationIDs))
		}
	}

	const listCap = 20
	if len(p.Deferred) > 0 {
		fmt.Printf("\nDeferred to manual review: %d\n", len(p.Deferred))
		for i, d := range p.Deferred {
			if i == listCap {
				fmt.Printf("  ... and %d more\n", len(p.Deferred)-listCap)
				break
			}
			fmt.Printf("  %s  -- %s\n", d.VictimID, d.Reason)
		}
	}
	if len(p.Anomalies) > 0 {
		fmt.Printf("\nAnomalies (reported, never touched): %d\n", len(p.Anomalies))
		for i, a := range p.Anomalies {
			if i == listCap {
				fmt.Printf("  ... and %d more\n", len(p.Anomalies)-listCap)
				break
			}
			fmt.Printf("  [%s] %s  -- %s\n", a.Kind, a.NodeID, a.Detail)
		}
	}
	if len(p.ForkPairs) > 0 {
		fmt.Printf("\nFork census -- live near-duplicate pairs (manual merge-via-update material): %d\n", len(p.ForkPairs))
		for _, fp := range p.ForkPairs {
			fmt.Printf("  %.3f  %s <-> %s\n", fp.Similarity, fp.IDA, fp.IDB)
		}
	}
}

// truncatePlanText keeps plan lines readable in a terminal.
func truncatePlanText(s string) string {
	const max = 90
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
