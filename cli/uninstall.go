package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gramaton-ai/gramaton/internal/setup"
	"github.com/gramaton-ai/gramaton/server"
	"github.com/gramaton-ai/gramaton/store"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	// uninstallHarnessFlag limits the run to one harness, by hook
	// embed-dir slug (claude-code, kiro, codex, cursor).
	uninstallHarnessFlag string

	// uninstallDryRun prints the inventory and changes nothing.
	uninstallDryRun bool

	// uninstallYes skips the confirmation prompt (for scripts).
	uninstallYes bool
)

// Test seams. Confirmation needs a real terminal (same rationale as
// runInit's TTY check, cli/init.go); tests fake the TTY answer and
// feed canned input instead of wrestling with pseudo-terminals. The
// process-stop seams let tests assert the reap-before-stop ordering
// and the failure accounting without signalling real processes.
var (
	uninstallStdinIsTTY           = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	uninstallInput      io.Reader = os.Stdin

	uninstallReapProxies = func(dir string, w io.Writer) (stopped, failed int) {
		return reapMCPProxies(dir, defaultProcOps(dir), w)
	}
	uninstallStopServer = stopServer
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove Gramaton's AI-harness integrations (never deletes data)",
	Long: `Removes what "gramaton init" installed into your AI harnesses
(Claude Code, kiro, Codex, Cursor): MCP registrations, hook
registrations, rendered hook scripts, and installed agent guidance.
Running gramaton servers and MCP proxies are stopped first.

Uninstall operates machine-wide: harness integrations are per-user,
not per-store, so the --store flag and GRAMATON_STORE are ignored
and every surface resolves against the base config directory.

Uninstall never deletes data: your config.yaml and every store are
left in place, and the final output names the directory to delete by
hand if you want them gone too. Hook and MCP config files get a
backup before any rewrite; user-added entries in shared files are
preserved.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUninstall(cmd.Context(), os.Stdout)
	},
}

func init() {
	uninstallCmd.Flags().StringVar(&uninstallHarnessFlag, "harness", "",
		"limit to one harness: claude-code, kiro, codex, or cursor")
	uninstallCmd.Flags().BoolVar(&uninstallDryRun, "dry-run", false,
		"print what would be stopped and removed without changing anything")
	uninstallCmd.Flags().BoolVar(&uninstallYes, "yes", false,
		"skip the confirmation prompt (for scripts)")
	rootCmd.AddCommand(uninstallCmd)
}

// storeProcs is the running-process inventory for one store: the
// live server (if any) and the count of registered MCP proxies.
type storeProcs struct {
	name    string
	info    *server.ServerInfo
	proxies int
}

// runUninstall drives the flow: inventory -> explanation ->
// confirmation -> stop processes -> remove surfaces -> report ->
// data-location footer. Partial failure keeps going and exits
// non-zero at the end.
//
// Every surface resolves from baseConfigDir(), never the
// store-resolved configDir(): harness integrations are machine-level
// (one settings.json, one hooks tree), and resolving them per-store
// would silently miss the real installs whenever --store or
// GRAMATON_STORE is set.
func runUninstall(ctx context.Context, out io.Writer) error {
	targets, err := setup.UninstallTargets(uninstallHarnessFlag)
	if err != nil {
		return err
	}
	base := baseConfigDir()

	plan, inventory := setup.UninstallInventory(ctx, base, targets)
	procs := uninstallProcessInventory(base)

	anything := len(procs) > 0
	for _, r := range inventory {
		switch r.Outcome {
		case setup.UninstallPresent, setup.UninstallSkipped, setup.UninstallFailed:
			anything = true
		}
	}
	if !anything {
		fmt.Fprintln(out, "Nothing to remove: no Gramaton harness integrations or running processes found.")
		printUninstallNotes(out, inventory)
		printUninstallFooter(out, base)
		return nil
	}

	printUninstallPlan(out, inventory, procs)

	if uninstallDryRun {
		fmt.Fprintln(out, "Dry run: nothing was stopped or removed.")
		printUninstallFooter(out, base)
		return nil
	}

	if !uninstallYes {
		if !uninstallStdinIsTTY() {
			return fmt.Errorf("confirmation required but stdin is not a terminal; re-run with --yes to proceed")
		}
		fmt.Fprint(out, "Proceed? [y/N] ")
		answer, _ := bufio.NewReader(uninstallInput).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "Aborted. Nothing was changed.")
			printUninstallFooter(out, base)
			return nil
		}
	}

	stopFailures := stopAllStoreProcesses(out, base)
	results := setup.UninstallApply(ctx, plan)
	failures := printUninstallResults(out, results) + stopFailures

	printUninstallFooter(out, base)

	if failures > 0 {
		return fmt.Errorf("uninstall finished with %d failure(s); see the report above", failures)
	}
	return nil
}

// uninstallProcessInventory surveys running gramaton processes for
// every store under base (default store included): the server
// recorded in each store's server.json (when its PID is alive) and
// the store's registered MCP proxies. Read-only by construction --
// the NoPrune listing leaves stale registry files on disk, because
// this runs on the dry-run and pre-confirmation paths where nothing
// may be mutated.
func uninstallProcessInventory(base string) []storeProcs {
	var out []storeProcs
	for _, s := range store.List(base) {
		dir := store.Resolve(base, nameForResolve(s))
		var info *server.ServerInfo
		if si, err := server.ReadServerInfo(dir); err == nil && server.IsProcessAlive(si.PID) {
			info = si
		}
		proxies := len(server.ListMCPProxiesNoPrune(dir))
		if info != nil || proxies > 0 {
			out = append(out, storeProcs{name: s.Name, info: info, proxies: proxies})
		}
	}
	return out
}

// sanitizeTerminal strips ANSI escape sequences and non-printable
// control characters from strings sourced from harness configs
// (entry names, vendor CLI output embedded in error text) before
// they reach the terminal, so a hostile or corrupted config can't
// inject cursor movement or title changes into uninstall's report.
// Multi-byte UTF-8 passes through untouched.
func sanitizeTerminal(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b { // ESC: swallow the escape sequence wholesale
			i++
			if i < len(s) {
				switch s[i] {
				case '[': // CSI: params/intermediates, final byte @-~
					i++
					for i < len(s) {
						c2 := s[i]
						i++
						if c2 >= '@' && c2 <= '~' {
							break
						}
					}
				case ']': // OSC: payload until BEL or ST (ESC \)
					i++
					for i < len(s) {
						c2 := s[i]
						i++
						if c2 == 0x07 {
							break
						}
						if c2 == 0x1b {
							if i < len(s) && s[i] == '\\' {
								i++
							}
							break
						}
					}
				default: // two-char escape (ESC c, ESC 7, ...): swallow it
					i++
				}
			}
			continue
		}
		if c < 0x20 || c == 0x7f { // other control chars (incl. \t, \n, \r)
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// printUninstallNotes prints the informational note lines (surfaces
// that could not be verified, e.g. a harness binary missing from
// PATH). Notes never affect the exit code and print on every path,
// including "nothing to remove" -- an MCP-only install whose binary
// vanished still deserves the manual-removal command.
func printUninstallNotes(out io.Writer, results []setup.UninstallResult) {
	for _, r := range results {
		if r.Outcome != setup.UninstallNote {
			continue
		}
		fmt.Fprintf(out, "  note: %s: %s\n", sanitizeTerminal(r.Harness), sanitizeTerminal(r.Detail))
	}
}

// printUninstallPlan prints the explanation plus the inventory of
// what a confirmed run will stop and remove. Only present, skipped,
// and refused surfaces are listed -- "not present" lines would bury
// the signal; note-level lines follow the list.
func printUninstallPlan(out io.Writer, inventory []setup.UninstallResult, procs []storeProcs) {
	fmt.Fprintln(out, "gramaton uninstall removes Gramaton's harness integrations: MCP")
	fmt.Fprintln(out, "registrations, hook registrations, rendered hook scripts, and")
	fmt.Fprintln(out, "installed agent guidance. Your config.yaml and all stores (your")
	fmt.Fprintln(out, "data) are NOT touched. Running gramaton servers and MCP proxies")
	fmt.Fprintln(out, "will be stopped first.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "To be removed:")

	shown := false
	lastHarness := ""
	for _, r := range inventory {
		switch r.Outcome {
		case setup.UninstallPresent, setup.UninstallSkipped, setup.UninstallFailed:
		default:
			continue
		}
		if r.Harness != lastHarness {
			fmt.Fprintf(out, "  %s:\n", r.Harness)
			lastHarness = r.Harness
		}
		switch r.Outcome {
		case setup.UninstallPresent:
			if r.Detail != "" {
				fmt.Fprintf(out, "    - %s (%s)\n", sanitizeTerminal(r.Surface), sanitizeTerminal(r.Detail))
			} else {
				fmt.Fprintf(out, "    - %s\n", sanitizeTerminal(r.Surface))
			}
		case setup.UninstallSkipped:
			fmt.Fprintf(out, "    ⚠ %s: %s\n", sanitizeTerminal(r.Surface), sanitizeTerminal(r.Detail))
		case setup.UninstallFailed:
			fmt.Fprintf(out, "    ✗ %s: %s\n", sanitizeTerminal(r.Surface), sanitizeTerminal(r.Detail))
		}
		shown = true
	}
	if !shown {
		fmt.Fprintln(out, "  (no harness integrations found)")
	}
	printUninstallNotes(out, inventory)

	if len(procs) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Running processes to stop (MCP proxies first, then the server):")
		for _, p := range procs {
			parts := []string{}
			if p.info != nil {
				parts = append(parts, fmt.Sprintf("server (pid %d)", p.info.PID))
			}
			if p.proxies > 0 {
				noun := "proxies"
				if p.proxies == 1 {
					noun = "proxy"
				}
				parts = append(parts, fmt.Sprintf("%d MCP %s", p.proxies, noun))
			}
			fmt.Fprintf(out, "  - store %s: %s\n", p.name, strings.Join(parts, ", "))
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Warning: active harness sessions (e.g. a running Claude Code")
	fmt.Fprintln(out, "session) will lose their Gramaton MCP connection and must be")
	fmt.Fprintln(out, "restarted afterwards.")
	fmt.Fprintln(out)
}

// stopAllStoreProcesses stops running gramaton processes for every
// store under base: registered MCP proxies first, then a graceful
// server shutdown -- the same load-bearing order as runStop
// (cli/stop.go). A surviving proxy auto-starts a replacement server
// on its next tool call, so inverting the order would let the very
// processes we're uninstalling resurrect the server. Returns the
// number of failures: servers that were alive but could not be
// stopped, plus gramaton proxies that survived the kill escalation.
func stopAllStoreProcesses(out io.Writer, base string) int {
	failures := 0
	for _, s := range store.List(base) {
		dir := store.Resolve(base, nameForResolve(s))
		stopped, failed := uninstallReapProxies(dir, os.Stderr)
		if stopped > 0 {
			noun := "proxies"
			if stopped == 1 {
				noun = "proxy"
			}
			fmt.Fprintf(out, "  ✓ store %s: stopped %d MCP %s\n", s.Name, stopped, noun)
		}
		if failed > 0 {
			failures += failed
			noun := "proxies"
			if failed == 1 {
				noun = "proxy"
			}
			fmt.Fprintf(out, "  ✗ store %s: %d MCP %s did not exit\n", s.Name, failed, noun)
		}
		info, err := server.ReadServerInfo(dir)
		if err != nil {
			continue // no server ever started (or already cleaned up)
		}
		if !server.IsProcessAlive(info.PID) {
			server.RemoveServerInfo(dir) // stale info; same cleanup stopServer does
			continue
		}
		if err := uninstallStopServer(dir); err != nil {
			failures++
			fmt.Fprintf(out, "  ✗ store %s: %v\n", s.Name, err)
			continue
		}
		fmt.Fprintf(out, "  ✓ store %s: server shutdown requested (pid %d)\n", s.Name, info.PID)
	}
	return failures
}

// printUninstallResults renders the per-surface report and returns
// the number of failed surfaces.
func printUninstallResults(out io.Writer, results []setup.UninstallResult) int {
	failures := 0
	lastHarness := ""
	for _, r := range results {
		if r.Harness != lastHarness {
			fmt.Fprintf(out, "%s:\n", r.Harness)
			lastHarness = r.Harness
		}
		backupNote := ""
		if r.Backup != "" {
			backupNote = fmt.Sprintf(" (backup: %s)", r.Backup)
		}
		switch r.Outcome {
		case setup.UninstallRemoved:
			if r.Detail != "" {
				fmt.Fprintf(out, "  ✓ %s: removed — %s%s\n", sanitizeTerminal(r.Surface), sanitizeTerminal(r.Detail), backupNote)
			} else {
				fmt.Fprintf(out, "  ✓ %s: removed%s\n", sanitizeTerminal(r.Surface), backupNote)
			}
		case setup.UninstallNotPresent:
			fmt.Fprintf(out, "  - %s: not present\n", sanitizeTerminal(r.Surface))
		case setup.UninstallNote:
			fmt.Fprintf(out, "  note: %s\n", sanitizeTerminal(r.Detail))
		case setup.UninstallSkipped:
			fmt.Fprintf(out, "  ⚠ %s: skipped — %s\n", sanitizeTerminal(r.Surface), sanitizeTerminal(r.Detail))
		case setup.UninstallFailed:
			failures++
			fmt.Fprintf(out, "  ✗ %s: failed — %s%s\n", sanitizeTerminal(r.Surface), sanitizeTerminal(r.Detail), backupNote)
		}
	}
	return failures
}

// printUninstallFooter prints the always-shown closing lines: where
// the untouched data lives, and how to remove the binary. Uninstall
// never deletes data, so this is the user's map for finishing the
// job by hand if that's what they want. Printed on every exit path
// -- success, dry run, abort, nothing-to-remove, partial failure.
func printUninstallFooter(out io.Writer, base string) {
	stores := store.List(base)
	names := make([]string, 0, len(stores))
	for _, s := range stores {
		names = append(names, s.Name)
	}
	fmt.Fprintln(out)
	if len(names) > 0 {
		fmt.Fprintf(out, "Gramaton data and configuration remain at %s (stores: %s).\n", base, strings.Join(names, ", "))
	} else {
		fmt.Fprintf(out, "Gramaton data and configuration remain at %s.\n", base)
	}
	fmt.Fprintln(out, "Delete that directory yourself to remove all data.")
	fmt.Fprintln(out, "The gramaton binary is not removed — remove it via the method you")
	fmt.Fprintln(out, "installed it with (for a `go install` install, delete")
	fmt.Fprintln(out, "$(go env GOPATH)/bin/gramaton).")
}
