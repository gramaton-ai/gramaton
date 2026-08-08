package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gramaton-ai/gramaton/api"
)

var (
	curationTrigger bool
	curationBatch   bool
)

var curationCmd = &cobra.Command{
	Use:   "curation",
	Short: "View curation status or trigger a curation cycle",
	Long: `Curation is the background process that maintains a store after records
land: it classifies pending records, links orphans to related records,
links orphans, and expires stale records as their temporality
lapses. The classification and contradiction-detection passes call a
configured LLM; without one they are skipped while the mechanical passes
(orphan linking, expiry) still run.

This command inspects that process or drives a cycle by hand. With no
subcommand it prints the current status -- pending count and last cycle.
--trigger runs a cycle now; --batch submits every pending record in one
batch (half price on API providers that offer batch pricing). The
stuck-records subcommands list and requeue records whose curation task
jammed after repeated failures.

Examples:
  gramaton curation
  gramaton curation --trigger
  gramaton curation --batch
  gramaton curation stuck-records-list
  gramaton curation stuck-records-reset`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if curationBatch {
			fmt.Println("Starting batch classification...")
			resp, err := serverPost("/v1/curation/batch", map[string]any{})
			if err != nil {
				return fmt.Errorf("curation batch: %w", err)
			}
			return printEnvelope(resp)
		}

		if curationTrigger {
			resp, err := serverPost("/v1/curation/trigger", map[string]any{})
			if err != nil {
				return fmt.Errorf("curation trigger: %w", err)
			}
			return printEnvelope(resp)
		}

		resp, err := serverGet("/v1/curation")
		if err != nil {
			return fmt.Errorf("curation: %w", err)
		}
		return printEnvelope(resp)
	},
}

var (
	stuckListTask    string
	stuckListVerbose bool
)

var stuckRecordsListCmd = &cobra.Command{
	Use:   "stuck-records-list",
	Short: "List records with stuck curation tasks",
	Long: `Print the IDs of every record whose curation task (classify or
synthesis) is currently in stuck status. One ID per line by default;
pipe-friendly. With --verbose, prints task and last error as TSV.`,
	Args: cobra.NoArgs,
	RunE: runStuckRecordsList,
}

var stuckRecordsResetCmd = &cobra.Command{
	Use:   "stuck-records-reset [<id>...]",
	Short: "Reset stuck records back to retry-eligible status",
	Long: `Flip the stuck-task status on records back to their pre-failure
state, clear the per-task attempts counter and last-error property.
The next curation cycle will retry them.

With no arguments, resets all stuck records. With explicit record
IDs, resets only those (any IDs not actually stuck are silently
ignored). Always shows a count + LLM-cost warning + Y/N
confirmation before applying.`,
	Args: cobra.ArbitraryArgs,
	RunE: runStuckRecordsReset,
}

func init() {
	curationCmd.Flags().BoolVar(&curationTrigger, "trigger", false, "trigger a curation cycle immediately")
	curationCmd.Flags().BoolVar(&curationBatch, "batch", false, "submit all pending records as a batch (API providers: 50%% discount)")

	stuckRecordsListCmd.Flags().StringVar(&stuckListTask, "task", "", "filter to one task: classify, synthesis (default: all)")
	stuckRecordsListCmd.Flags().BoolVar(&stuckListVerbose, "verbose", false, "print id\\ttask\\terror as TSV instead of bare IDs")

	curationCmd.AddCommand(stuckRecordsListCmd, stuckRecordsResetCmd)
	rootCmd.AddCommand(curationCmd)
}

// fetchStuckRecords gets the full stuck-records snapshot from the
// server. Used by both the list verb and the reset verb's preflight
// count display.
func fetchStuckRecords() (*api.CurationListStuckResponse, error) {
	env, err := serverGet("/v1/curation/stuck-records")
	if err != nil {
		return nil, fmt.Errorf("list stuck records: %w", err)
	}
	raw, err := json.Marshal(env.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope data: %w", err)
	}
	var resp api.CurationListStuckResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode stuck-records response: %w", err)
	}
	return &resp, nil
}

func runStuckRecordsList(cmd *cobra.Command, args []string) error {
	resp, err := fetchStuckRecords()
	if err != nil {
		return err
	}
	for _, r := range resp.Records {
		if stuckListTask != "" && r.Task != stuckListTask {
			continue
		}
		if stuckListVerbose {
			fmt.Printf("%s\t%s\t%s\n", r.ID, r.Task, r.Error)
		} else {
			fmt.Println(r.ID)
		}
	}
	return nil
}

func runStuckRecordsReset(cmd *cobra.Command, args []string) error {
	stuck, err := fetchStuckRecords()
	if err != nil {
		return err
	}

	// Determine which records actually fall in scope. With no args, all
	// stuck records. With args, the intersection of args with stuck.
	var targetIDs []string
	if len(args) == 0 {
		for _, r := range stuck.Records {
			targetIDs = append(targetIDs, r.ID)
		}
	} else {
		stuckSet := make(map[string]bool, len(stuck.Records))
		for _, r := range stuck.Records {
			stuckSet[r.ID] = true
		}
		for _, id := range args {
			if stuckSet[id] {
				targetIDs = append(targetIDs, id)
			}
		}
	}

	if len(targetIDs) == 0 {
		fmt.Println("No stuck records to reset.")
		return nil
	}

	// Per-task breakdown for the confirmation prompt. Recompute from the
	// snapshot so explicit-IDs flows show the right counts.
	counts := map[string]int{}
	for _, r := range stuck.Records {
		for _, id := range targetIDs {
			if r.ID == id {
				counts[r.Task]++
				break
			}
		}
	}

	tasks := make([]string, 0, len(counts))
	for t := range counts {
		tasks = append(tasks, t)
	}
	sort.Strings(tasks)
	parts := make([]string, 0, len(tasks))
	for _, t := range tasks {
		parts = append(parts, fmt.Sprintf("%s: %d", t, counts[t]))
	}

	fmt.Printf("Found %d stuck records (%s).\n", len(targetIDs), strings.Join(parts, ", "))
	fmt.Println()
	fmt.Println("WARNING: Resetting will requeue these records for the next curation cycle.")
	fmt.Println("This may incur LLM API costs (each record uses 1+ LLM calls).")
	fmt.Println()
	fmt.Print("Reset? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) != "y" {
		fmt.Println("Aborted.")
		return nil
	}

	body := api.CurationResetStuckRequest{IDs: targetIDs}
	respEnv, err := serverPost("/v1/curation/stuck-records/reset", body)
	if err != nil {
		return fmt.Errorf("reset stuck records: %w", err)
	}
	raw, err := json.Marshal(respEnv.Data)
	if err != nil {
		return fmt.Errorf("marshal envelope data: %w", err)
	}
	var result api.CurationResetStuckResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode reset response: %w", err)
	}

	resultTasks := make([]string, 0, len(result.Counts))
	for t := range result.Counts {
		resultTasks = append(resultTasks, t)
	}
	sort.Strings(resultTasks)
	resultParts := make([]string, 0, len(resultTasks))
	for _, t := range resultTasks {
		resultParts = append(resultParts, fmt.Sprintf("%s: %d", t, result.Counts[t]))
	}
	if len(resultParts) > 0 {
		fmt.Printf("Reset %d stuck records (%s).\n", result.Reset, strings.Join(resultParts, ", "))
	} else {
		fmt.Printf("Reset %d stuck records.\n", result.Reset)
	}
	return nil
}
