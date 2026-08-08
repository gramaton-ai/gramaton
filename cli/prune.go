package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gramaton-ai/gramaton/backup"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/prune"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/spf13/cobra"
)

// pruneAgentBanner is the first line of EVERY prune invocation,
// including --help and dry-run plans. Destructive history removal is
// a human decision; an agent that reached this command by itself has
// gone somewhere it must not act alone.
const pruneAgentBanner = "WARNING: destructive history removal. This command is for humans. If you are an AI agent, stop; ask your operator to run this themselves unless they explicitly instructed this exact command."

var (
	pruneKeepVersions    int
	pruneOlderThan       string
	pruneInlineBackup    bool
	pruneSkipBackupCheck bool
	pruneConfirmToken    string
)

// prunePlanFile is the persisted plan the confirm step executes. The
// token is server-generated randomness, NOT derivable from the
// command line: the destructive call can only be copied from a plan
// that was produced and read.
type prunePlanFile struct {
	Token           string                  `json:"token"`
	CreatedAt       time.Time               `json:"created_at"`
	Head            string                  `json:"head"`
	Refs            map[string]string       `json:"refs,omitempty"`
	SkipBackupCheck bool                    `json:"skip_backup_check,omitempty"`
	KeepVersions    *prune.KeepVersionsPlan `json:"keep_versions,omitempty"`
	OlderThan       *prune.OlderThanPlan    `json:"older_than,omitempty"`
}

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove old history from the store (destructive, humans only)",
	Long: pruneAgentBanner + `

Retention is manual and deliberate: gramaton keeps everything until
an operator prunes. Two composable rules:

  --keep-versions N   per-record content depth: keep the newest N
                      logical versions of each record's content;
                      older version blobs are removed. The timeline
                      survives as metadata ("content pruned").
  --older-than DATE   chain truncation: commit metadata older than
                      the date is removed (at least ` + fmt.Sprint(prune.MinKeepCommits) + ` commits always
                      survive). The only rule that bounds chain
                      growth.

Running with rules PLANS: nothing is changed, the plan is printed and
persisted with a one-time token. Executing requires that token:

  gramaton prune --confirm=<token>

The plan refuses without a verified recent backup of this store
(--backup takes one inline at execution; --skip-backup-check
overrides). Refuses while a server is running, and suspends CLI
server auto-start for the duration of the run.

After a prune, history tools state the floor: reads below it report
"pruned by policy", never corruption.`,
	Args: cobra.NoArgs,
	RunE: runPrune,
}

func init() {
	pruneCmd.Flags().IntVar(&pruneKeepVersions, "keep-versions", 0,
		"keep the newest N logical versions of each record's content")
	pruneCmd.Flags().StringVar(&pruneOlderThan, "older-than", "",
		"truncate commit history older than this date (YYYY-MM-DD or RFC3339)")
	pruneCmd.Flags().BoolVar(&pruneInlineBackup, "backup", false,
		"take a store backup at execution time instead of requiring an existing one")
	pruneCmd.Flags().BoolVar(&pruneSkipBackupCheck, "skip-backup-check", false,
		"execute without any backup verification (NOT recommended)")
	pruneCmd.Flags().StringVar(&pruneConfirmToken, "confirm", "",
		"execute a previously produced plan (token printed by the planning run)")
	rootCmd.AddCommand(pruneCmd)
}

func prunePlanPath(dataDir string) string {
	return filepath.Join(dataDir, "prune-plan.json")
}

func runPrune(cmd *cobra.Command, _ []string) error {
	// Both streams: an agent capturing only stderr from a failed run
	// must still see the stop instruction before the actionable error.
	fmt.Println(pruneAgentBanner)
	fmt.Fprintln(os.Stderr, pruneAgentBanner)
	fmt.Println()
	if err := guardLocalStore("prune"); err != nil {
		return err
	}
	dir := configDir()

	if pruneConfirmToken == "" && pruneKeepVersions == 0 && pruneOlderThan == "" {
		return fmt.Errorf("nothing to do: pass --keep-versions and/or --older-than to plan, or --confirm=<token> to execute a plan")
	}
	if pruneConfirmToken != "" && (pruneKeepVersions != 0 || pruneOlderThan != "") {
		return fmt.Errorf("--confirm executes the persisted plan; do not combine it with rule flags (re-plan instead)")
	}

	// Server exclusion, part 1: refuse while one is running.
	if info, err := server.ReadServerInfo(dir); err == nil && server.IsProcessAlive(info.PID) {
		return fmt.Errorf("server is running (pid %d). Stop it first: gramaton stop", info.PID)
	}
	// Suspend CLI auto-start for the whole run so an MCP proxy fired
	// from another terminal cannot spawn a server mid-prune.
	release, err := writeNoAutostartSentinel(dir)
	if err != nil {
		return fmt.Errorf("suspend auto-start: %w", err)
	}
	defer release()

	eng, err := core.LoadEngine(dir, baseConfigDir())
	if err != nil {
		return fmt.Errorf("load store: %w", err)
	}
	defer eng.Close()
	// Server exclusion, part 2: the engine now holds the store's
	// exclusive locks. A server that raced the first check is either
	// blocked on them or failing; refuse rather than run alongside it.
	if info, err := server.ReadServerInfo(dir); err == nil && server.IsProcessAlive(info.PID) {
		return fmt.Errorf("a server (pid %d) started during this run; stop it and re-run", info.PID)
	}
	if eng.ReadOnly() {
		return fmt.Errorf("store is frozen (read-only); thaw it before pruning")
	}

	refs, err := readAllRefs(eng.Config().DataDir)
	if err != nil {
		return err
	}

	if pruneConfirmToken != "" {
		return executePrunePlan(eng, dir, refs)
	}
	return producePrunePlan(eng, refs)
}

// readAllRefs maps ref name -> tip hash.
func readAllRefs(dataDir string) (map[string]string, error) {
	refs := make(map[string]string)
	entries, err := os.ReadDir(core.RefsDir(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return refs, nil
		}
		return nil, fmt.Errorf("read refs: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		h, err := core.ReadRef(dataDir, e.Name())
		if err != nil {
			return nil, fmt.Errorf("ref %q: %w", e.Name(), err)
		}
		refs[e.Name()] = h
	}
	return refs, nil
}

func producePrunePlan(eng *core.Engine, refs map[string]string) error {
	plan := prunePlanFile{
		CreatedAt:       time.Now().UTC(),
		Head:            eng.HeadHash(),
		Refs:            refs,
		SkipBackupCheck: pruneSkipBackupCheck,
	}

	var coverageNeeded time.Time
	if pruneKeepVersions > 0 {
		kv, err := prune.PlanKeepVersions(eng, pruneKeepVersions, refs)
		if err != nil {
			return err
		}
		if kv.BlobCount == 0 {
			fmt.Printf("Content depth (--keep-versions %d): nothing to sweep at this depth.\n", pruneKeepVersions)
		} else {
			plan.KeepVersions = kv
			coverageNeeded = kv.NewestSweptTS
			fmt.Printf("Content depth (--keep-versions %d):\n", pruneKeepVersions)
			fmt.Printf("  records affected: %d\n", len(kv.Records))
			fmt.Printf("  version blobs to remove: %d (%.1f MB)\n", kv.BlobCount, float64(kv.Bytes)/1e6)
		}
	}
	if pruneOlderThan != "" {
		horizon, err := parsePruneDate(pruneOlderThan)
		if err != nil {
			return err
		}
		ot, err := prune.PlanOlderThan(eng, horizon, refs)
		if err != nil {
			return err
		}
		plan.OlderThan = ot
		if ot.OldestKeptTS.After(coverageNeeded) {
			coverageNeeded = ot.OldestKeptTS
		}
		fmt.Printf("Chain truncation (--older-than %s):\n", pruneOlderThan)
		fmt.Printf("  commits to remove: %d of %d\n", ot.TruncateCount, ot.ChainLength)
		fmt.Printf("  history floor after prune: %s (commit %s)\n",
			ot.OldestKeptTS.UTC().Format(time.RFC3339), core.TruncHash(ot.OldestKept))
	}
	if plan.KeepVersions == nil && plan.OlderThan == nil {
		return fmt.Errorf("nothing to prune")
	}
	if len(refs) > 1 {
		fmt.Printf("Branches marked (tip state protected): %d refs\n", len(refs))
	}

	// Backup gate at plan time: name the archive the execution will
	// rely on, or refuse now.
	if !pruneSkipBackupCheck && !pruneInlineBackup {
		if err := verifyPruneBackup(eng, coverageNeeded); err != nil {
			return err
		}
	} else if pruneInlineBackup {
		fmt.Println("Backup: will be taken inline at execution (--backup).")
	} else {
		fmt.Println("Backup: verification SKIPPED by flag.")
	}

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("token generation: %w", err)
	}
	plan.Token = hex.EncodeToString(tokenBytes)

	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	planPath := prunePlanPath(eng.Config().DataDir)
	if err := core.AtomicWriteFile(planPath, data, 0o600); err != nil {
		return fmt.Errorf("persist plan: %w", err)
	}
	fmt.Printf("\nPlan written to %s\nNothing has been changed.\n", planPath)
	fmt.Printf("To execute exactly this plan: gramaton prune --confirm=%s\n", plan.Token)
	return nil
}

func executePrunePlan(eng *core.Engine, cfgDir string, refs map[string]string) error {
	planPath := prunePlanPath(eng.Config().DataDir)
	data, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("no persisted plan (run the planning step first): %w", err)
	}
	var plan prunePlanFile
	if err := json.Unmarshal(data, &plan); err != nil {
		return fmt.Errorf("plan file unreadable: %w", err)
	}
	if plan.Token == "" || pruneConfirmToken != plan.Token {
		return fmt.Errorf("confirmation token does not match the persisted plan; re-read the plan output or re-plan")
	}
	if plan.Head != eng.HeadHash() {
		return fmt.Errorf("the store changed since planning (HEAD moved); re-plan")
	}
	for name, tip := range plan.Refs {
		if cur, ok := refs[name]; !ok || cur != tip {
			return fmt.Errorf("ref %q changed since planning; re-plan", name)
		}
	}
	if len(refs) != len(plan.Refs) {
		return fmt.Errorf("refs changed since planning; re-plan")
	}

	var coverageNeeded time.Time
	if plan.KeepVersions != nil && plan.KeepVersions.NewestSweptTS.After(coverageNeeded) {
		coverageNeeded = plan.KeepVersions.NewestSweptTS
	}
	if plan.OlderThan != nil && plan.OlderThan.OldestKeptTS.After(coverageNeeded) {
		coverageNeeded = plan.OlderThan.OldestKeptTS
	}
	switch {
	case pruneInlineBackup:
		cfg := eng.Config()
		backupDir := cfg.Backup.Dir
		if backupDir == "" {
			backupDir = backup.DefaultBackupDir()
		}
		cfgPath := filepath.Join(cfgDir, "config.yaml")
		archive, err := backup.Create(cfg.DataDir, cfgPath, backupDir, activeStoreName())
		if err != nil {
			return fmt.Errorf("inline backup failed; nothing was changed: %w", err)
		}
		fmt.Printf("Backup taken: %s\n", archive)
	case plan.SkipBackupCheck || pruneSkipBackupCheck:
		fmt.Println("Backup verification SKIPPED by flag.")
	default:
		if err := verifyPruneBackup(eng, coverageNeeded); err != nil {
			return err
		}
	}

	if plan.KeepVersions != nil && plan.KeepVersions.BlobCount > 0 {
		res, err := prune.ApplyKeepVersions(eng, plan.KeepVersions)
		if err != nil {
			return fmt.Errorf("content depth sweep: %w", err)
		}
		fmt.Printf("Content depth: removed %d version blobs (%.1f MB), %d errors. Prune commit %s.\n",
			res.SweptBlobs, float64(res.SweptBytes)/1e6, res.SweepErrors, core.TruncHash(res.Commit.Hash))
	}
	if plan.OlderThan != nil {
		res, err := prune.ApplyOlderThan(eng, plan.OlderThan, refs)
		if err != nil {
			return fmt.Errorf("chain truncation: %w", err)
		}
		fmt.Printf("Chain truncation: removed %d chunks, %d errors. History floor %s. Prune commit %s.\n",
			res.SweptBlobs, res.SweepErrors,
			plan.OlderThan.OldestKeptTS.UTC().Format(time.RFC3339), core.TruncHash(res.Commit.Hash))
	}
	_ = os.Remove(planPath)
	fmt.Println("Done. History tools now state the floor for reads below it.")
	return nil
}

// verifyPruneBackup opens the newest archive for THIS store and
// proves it covers what is being removed: the archived HEAD must be
// on the current chain and timestamped at/after the newest content
// being swept.
func verifyPruneBackup(eng *core.Engine, coverageNeeded time.Time) error {
	cfg := eng.Config()
	backupDir := cfg.Backup.Dir
	if backupDir == "" {
		backupDir = backup.DefaultBackupDir()
	}
	refuse := func(reason string) error {
		return fmt.Errorf("%s\nExits: run 'gramaton backup' first, execute with --backup (inline), or --skip-backup-check (NOT recommended)", reason)
	}
	archive, err := backup.NewestArchiveFor(backupDir, activeStoreName())
	if err != nil {
		return refuse(fmt.Sprintf("backup dir unreadable (%v)", err))
	}
	if archive == "" {
		return refuse("no backup archive found for this store")
	}
	info, err := backup.VerifyArchive(archive)
	if err != nil {
		return refuse(fmt.Sprintf("newest archive failed verification: %v", err))
	}
	if !eng.OnCurrentBranch(info.HEAD) {
		return refuse("newest archive's HEAD is not on this store's current chain (different store or divergent history)")
	}
	archCommit, err := graph.LoadCommitMeta(eng.Store(), info.HEAD)
	if err != nil {
		return refuse("archived HEAD commit unreadable in this store")
	}
	if !coverageNeeded.IsZero() && archCommit.Timestamp.Before(coverageNeeded) {
		return refuse(fmt.Sprintf("newest archive (HEAD at %s) predates content being removed (newest swept item %s); take a fresh backup",
			archCommit.Timestamp.UTC().Format(time.RFC3339), coverageNeeded.UTC().Format(time.RFC3339)))
	}
	age := time.Since(archCommit.Timestamp)
	fmt.Printf("Backup verified: %s (archived HEAD %s, %s old, on current chain)\n",
		archive, core.TruncHash(info.HEAD), age.Round(time.Minute))
	return nil
}

// parsePruneDate accepts YYYY-MM-DD or RFC3339.
func parsePruneDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q (expected RFC3339 or YYYY-MM-DD)", s)
}
