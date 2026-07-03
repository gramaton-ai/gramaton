package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The registry-driven engine behind `gramaton uninstall` (#101):
// per harness, probe and remove the four integration surfaces
// `gramaton init` installs -- MCP registrations, hook-config
// entries, rendered hook scripts, and agent guidance. Process
// stopping, confirmation, and output rendering live in
// cli/uninstall.go; this file owns the surface mechanics and
// returns structured per-surface results.
//
// The flow is two-phase: UninstallInventory probes everything ONCE
// into an UninstallPlan (never mutating), and UninstallApply acts on
// that plan. Apply deliberately does not re-enumerate MCP entries:
// `claude mcp list` health-checks each configured stdio entry by
// spawning it, so a second enumeration after the CLI's stop step
// would re-spawn proxies against the servers just shut down -- and
// plan and apply could disagree about what exists.
//
// Uninstall NEVER deletes data: config.yaml and every store are out
// of scope by design.

// UninstallOutcome classifies one surface's result.
type UninstallOutcome string

const (
	// UninstallPresent is the inventory-only outcome: the surface
	// exists and an apply pass would remove it.
	UninstallPresent UninstallOutcome = "present"

	// UninstallRemoved: the surface existed and was removed.
	UninstallRemoved UninstallOutcome = "removed"

	// UninstallNotPresent: nothing to do for this surface.
	UninstallNotPresent UninstallOutcome = "not present"

	// UninstallNote: informational only -- the surface could not be
	// verified (the harness binary is not on PATH, so its MCP
	// registration can't be checked). Notes never affect the exit
	// code and never suppress the CLI's "nothing to remove"
	// headline; Detail carries the manual check/removal command.
	UninstallNote UninstallOutcome = "note"

	// UninstallSkipped: the surface could not be handled (failed
	// enumeration, best-effort harness) and needs manual attention;
	// Detail carries the reason and, where possible, the exact
	// manual command. Skips are warnings, not failures.
	UninstallSkipped UninstallOutcome = "skipped"

	// UninstallFailed: removal was attempted and failed, or a config
	// file was refused (unparseable). Any failed result makes the
	// CLI exit non-zero.
	UninstallFailed UninstallOutcome = "failed"
)

// UninstallResult is one per-surface outcome line.
type UninstallResult struct {
	// Harness is the registry display name ("Claude Code").
	Harness string
	// Surface is the human-readable surface description, path
	// included where one exists.
	Surface string
	// Outcome classifies what happened (or would happen).
	Outcome UninstallOutcome
	// Detail carries the skip/failure reason or extra context.
	Detail string
	// Backup is the path of the backup written before a rewrite.
	Backup string
}

// UninstallTargets resolves the CLI's --harness flag value to
// registry entries: "" selects every harness; otherwise the value
// must be a hook embed-dir slug (claude-code, kiro, codex, cursor).
// Unknown slugs error, listing the valid values.
func UninstallTargets(slug string) ([]*Harness, error) {
	if slug == "" {
		out := make([]*Harness, 0, len(harnesses))
		for i := range harnesses {
			out = append(out, &harnesses[i])
		}
		return out, nil
	}
	if h := harnessByEmbedDir(slug); h != nil {
		return []*Harness{h}, nil
	}
	var valid []string
	for _, h := range harnesses {
		if h.HookEmbedDir != "" {
			valid = append(valid, h.HookEmbedDir)
		}
	}
	return nil, fmt.Errorf("unknown harness %q (valid values: %s)", slug, strings.Join(valid, ", "))
}

// UninstallPlan carries the probed state from UninstallInventory to
// UninstallApply so the two phases cannot disagree and every vendor
// CLI is invoked exactly once per harness.
type UninstallPlan struct {
	items []harnessPlan
}

// harnessPlan is the probed state of one harness's surfaces.
type harnessPlan struct {
	h        *Harness
	ownPaths []string

	hookPresent  bool
	hookProbeErr error

	scriptsDir     string
	scriptsPresent bool

	instr instructionsProbe

	// MCP surface. mcpApplicable is false when the harness has no
	// ListMCPEntries strategy; mcpBinMissing means the vendor binary
	// wasn't found on PATH (Cursor, DetectBinary == "", is
	// file-driven and never bin-missing).
	mcpApplicable bool
	mcpBin        string
	mcpBinMissing bool
	mcpEntries    []string
	mcpListErr    error
}

// UninstallInventory probes every uninstall surface for the selected
// harnesses without modifying anything, and returns the plan a
// subsequent UninstallApply consumes plus the per-surface inventory
// results. Present surfaces come back with UninstallPresent;
// surfaces that would be skipped or refused report that up front so
// the user sees it before confirming.
func UninstallInventory(ctx context.Context, configDir string, targets []*Harness) (*UninstallPlan, []UninstallResult) {
	plan := &UninstallPlan{}
	var results []UninstallResult
	for _, h := range targets {
		plan.items = append(plan.items, probeHarness(ctx, configDir, h))
	}
	for i := range plan.items {
		results = append(results, reportHarness(ctx, &plan.items[i], false)...)
	}
	return plan, results
}

// UninstallApply removes every uninstall surface captured in the
// plan. Partial failure is deliberate: a failed surface is reported
// and the walk continues, so one broken config file doesn't strand
// the rest of the cleanup.
func UninstallApply(ctx context.Context, plan *UninstallPlan) []UninstallResult {
	var results []UninstallResult
	for i := range plan.items {
		results = append(results, reportHarness(ctx, &plan.items[i], true)...)
	}
	return results
}

// hookOwnershipPaths synthesizes the scriptPaths argument for the
// unwire strategies. isGramatonHookCommand's default-layout signal
// (the `/.gramaton/hooks/` fragment) misses hooks materialized under
// a relocated config dir (#83); a synthetic path under the active
// config dir's hooks root re-teaches it the same way the register
// path's real scriptPaths do -- any command under <configDir>/hooks/
// is ours, whichever harness subdir (or legacy flat layout) it sits
// in.
func hookOwnershipPaths(configDir string) []string {
	if configDir == "" {
		return nil
	}
	return []string{filepath.Join(configDir, "hooks", "own")}
}

// probeHarness surveys one harness's surfaces without mutating
// anything. This is the ONLY place vendor CLIs are invoked.
func probeHarness(ctx context.Context, configDir string, h *Harness) harnessPlan {
	hp := harnessPlan{h: h, ownPaths: hookOwnershipPaths(configDir)}

	if h.UnwireHooks != nil {
		hp.hookPresent, _, hp.hookProbeErr = h.UnwireHooks(ctx, hp.ownPaths, false)
	}

	// The configDir != "" guard keeps a misconfigured caller from
	// ever aiming the scripts RemoveAll at a cwd-relative "hooks/"
	// path.
	if h.HookEmbedDir != "" && configDir != "" {
		hp.scriptsDir = filepath.Join(configDir, "hooks", h.HookEmbedDir)
		if fi, err := os.Stat(hp.scriptsDir); err == nil && fi.IsDir() {
			hp.scriptsPresent = true
		}
	}

	hp.instr = probeInstructions(h)

	if h.ListMCPEntries != nil {
		hp.mcpApplicable = true
		if h.DetectBinary != "" {
			p, err := exec.LookPath(h.DetectBinary)
			if err != nil {
				hp.mcpBinMissing = true
			} else {
				hp.mcpBin = p
			}
		}
		if !hp.mcpBinMissing {
			hp.mcpEntries, hp.mcpListErr = h.ListMCPEntries(ctx, hp.mcpBin)
		}
	}
	return hp
}

// reportHarness turns one harness's probed state into per-surface
// results, applying the removals when apply is true. Surface order
// is stable: MCP entries, hook-config entries, rendered scripts,
// agent guidance.
func reportHarness(ctx context.Context, hp *harnessPlan, apply bool) []UninstallResult {
	var results []UninstallResult
	results = append(results, reportMCP(ctx, hp, apply)...)
	if r := reportHookConfig(ctx, hp, apply); r != nil {
		results = append(results, *r)
	}
	if r := reportHookScripts(hp, apply); r != nil {
		results = append(results, *r)
	}
	if r := uninstallInstructions(hp.h, hp.instr, apply); r != nil {
		results = append(results, *r)
	}
	return results
}

// reportMCP handles the MCP-entries surface for one harness from the
// probed plan. Vendor-CLI harnesses whose binary is missing get an
// informational note with the exact manual command -- always, so an
// MCP-only install (the wizard allows registering MCP while
// declining hooks and guidance) is never left without guidance just
// because its binary went away.
func reportMCP(ctx context.Context, hp *harnessPlan, apply bool) []UninstallResult {
	h := hp.h
	if !hp.mcpApplicable {
		return nil
	}
	if hp.mcpBinMissing {
		return []UninstallResult{{
			Harness: h.Name,
			Surface: "MCP entries",
			Outcome: UninstallNote,
			Detail: fmt.Sprintf("cannot check MCP registration (%s not on PATH); if registered, remove with: %s (entries are named gramaton or gramaton-<store>)",
				h.DetectBinary, h.ManualMCPRemoveHint("<entry>")),
		}}
	}
	if hp.mcpListErr != nil {
		return []UninstallResult{{
			Harness: h.Name,
			Surface: "MCP entries",
			Outcome: mcpFailureOutcome(h),
			Detail:  fmt.Sprintf("could not enumerate: %v; remove manually with: %s", hp.mcpListErr, h.ManualMCPRemoveHint("<entry>")),
		}}
	}
	if len(hp.mcpEntries) == 0 {
		return []UninstallResult{{Harness: h.Name, Surface: "MCP entries", Outcome: UninstallNotPresent}}
	}

	if !apply {
		var out []UninstallResult
		for _, e := range hp.mcpEntries {
			out = append(out, UninstallResult{
				Harness: h.Name,
				Surface: fmt.Sprintf("MCP entry %q", e),
				Outcome: UninstallPresent,
				Detail:  h.ManualMCPRemoveHint(e),
			})
		}
		return out
	}

	removed, backup, err := h.RemoveMCPEntries(ctx, hp.mcpBin, hp.mcpEntries)
	removedSet := map[string]bool{}
	var out []UninstallResult
	for _, e := range removed {
		removedSet[e] = true
		out = append(out, UninstallResult{
			Harness: h.Name,
			Surface: fmt.Sprintf("MCP entry %q", e),
			Outcome: UninstallRemoved,
			Backup:  backup,
		})
	}
	if err != nil {
		for _, e := range hp.mcpEntries {
			if removedSet[e] {
				continue
			}
			out = append(out, UninstallResult{
				Harness: h.Name,
				Surface: fmt.Sprintf("MCP entry %q", e),
				Outcome: mcpFailureOutcome(h),
				Detail:  fmt.Sprintf("%v; remove manually with: %s", err, h.ManualMCPRemoveHint(e)),
			})
		}
	}
	return out
}

// mcpFailureOutcome downgrades an MCP failure to an informative skip
// for best-effort harnesses (kiro), matching the wizard's
// warn-and-continue install behavior for the same integration.
func mcpFailureOutcome(h *Harness) UninstallOutcome {
	if h.MCPBestEffort {
		return UninstallSkipped
	}
	return UninstallFailed
}

// reportHookConfig handles the hook-registrations surface from the
// probed plan. Existence-driven: the config file is edited if it
// exists, regardless of whether the harness binary survives. Returns
// nil for harnesses without hook auto-wiring (kiro) -- no surface to
// report.
func reportHookConfig(ctx context.Context, hp *harnessPlan, apply bool) *UninstallResult {
	h := hp.h
	if h.UnwireHooks == nil {
		return nil
	}
	surface := "hook registrations"
	if h.HookConfigPathHint != nil {
		surface = fmt.Sprintf("hook registrations in %s", h.HookConfigPathHint())
	}
	r := UninstallResult{Harness: h.Name, Surface: surface}
	if hp.hookProbeErr != nil {
		r.Outcome = UninstallFailed
		r.Detail = hp.hookProbeErr.Error()
		return &r
	}
	if !hp.hookPresent {
		r.Outcome = UninstallNotPresent
		return &r
	}
	if !apply {
		r.Outcome = UninstallPresent
		return &r
	}
	changed, backup, err := h.UnwireHooks(ctx, hp.ownPaths, true)
	if err != nil {
		r.Outcome = UninstallFailed
		r.Detail = err.Error()
		r.Backup = backup
		return &r
	}
	if !changed {
		// Raced away between probe and apply; gone either way.
		r.Outcome = UninstallNotPresent
		return &r
	}
	r.Outcome = UninstallRemoved
	r.Backup = backup
	return &r
}

// reportHookScripts handles the rendered proxy-script directory
// <configDir>/hooks/<HookEmbedDir>/. Existence-driven.
func reportHookScripts(hp *harnessPlan, apply bool) *UninstallResult {
	h := hp.h
	if h.HookEmbedDir == "" || hp.scriptsDir == "" {
		return nil
	}
	r := UninstallResult{Harness: h.Name, Surface: fmt.Sprintf("hook scripts at %s", hp.scriptsDir)}
	if !hp.scriptsPresent {
		r.Outcome = UninstallNotPresent
		return &r
	}
	if !apply {
		r.Outcome = UninstallPresent
		return &r
	}
	if err := os.RemoveAll(hp.scriptsDir); err != nil {
		r.Outcome = UninstallFailed
		r.Detail = err.Error()
		return &r
	}
	r.Outcome = UninstallRemoved
	return &r
}

// instructionsProbe is the result of probing one harness's agent
// guidance surface without touching it.
type instructionsProbe struct {
	// applicable is false for harnesses with no instructions path
	// (no surface to report at all).
	applicable bool
	present    bool
	path       string
	layout     instructionsLayout
	surface    string
	err        error
}

// probeInstructions resolves the instructions path (CODEX_HOME-aware
// via instructionsPathForClient) and reports whether gramaton
// guidance is currently installed there: for fenced layouts, a fence
// begin/end pair inside the shared file; for wholeFileOwned layouts,
// the file itself.
func probeInstructions(h *Harness) instructionsProbe {
	if len(h.InstructionsRelPath) == 0 {
		return instructionsProbe{}
	}
	p := instructionsProbe{applicable: true, surface: "agent guidance"}
	path, layout, err := instructionsPathForClient(h.Name)
	if err != nil {
		p.err = err
		return p
	}
	p.path = path
	p.layout = layout

	if layout == wholeFileOwned {
		p.surface = fmt.Sprintf("guidance file %s", path)
		if _, err := os.Stat(path); err == nil {
			p.present = true
		} else if !errors.Is(err, os.ErrNotExist) {
			p.err = err
		}
		return p
	}

	p.surface = fmt.Sprintf("managed guidance block in %s", path)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return p
	}
	if err != nil {
		p.err = err
		return p
	}
	_, found, err := stripFence(raw)
	if err != nil {
		p.err = err
		return p
	}
	p.present = found
	return p
}

// uninstallInstructions handles the agent-guidance surface, driven
// by the harness's InstructionsLayout:
//
//   - fencedBlockInSharedFile: strip the managed fenced region,
//     preserving everything outside it; a .bak sibling is written
//     first, and a failed backup ABORTS the surface -- same contract
//     as every other rewrite path. If nothing but whitespace remains
//     after the strip, the file is deleted (we created it in that
//     case).
//   - wholeFileOwned: delete the file; when OwnsInstructionsDir, also
//     remove its (gramaton-dedicated) directory if now empty.
func uninstallInstructions(h *Harness, p instructionsProbe, apply bool) *UninstallResult {
	if !p.applicable {
		return nil
	}
	r := UninstallResult{Harness: h.Name, Surface: p.surface}
	if p.err != nil {
		r.Outcome = UninstallFailed
		r.Detail = p.err.Error()
		return &r
	}
	if !p.present {
		r.Outcome = UninstallNotPresent
		return &r
	}
	if !apply {
		r.Outcome = UninstallPresent
		return &r
	}

	if p.layout == wholeFileOwned {
		if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			r.Outcome = UninstallFailed
			r.Detail = err.Error()
			return &r
		}
		if h.OwnsInstructionsDir {
			// Best-effort: the directory is gramaton-dedicated, but
			// user-added sibling files keep it alive (os.Remove
			// refuses non-empty directories).
			_ = os.Remove(filepath.Dir(p.path))
		}
		r.Outcome = UninstallRemoved
		return &r
	}

	raw, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		r.Outcome = UninstallNotPresent
		return &r
	}
	if err != nil {
		r.Outcome = UninstallFailed
		r.Detail = err.Error()
		return &r
	}
	remaining, found, err := stripFence(raw)
	if err != nil {
		r.Outcome = UninstallFailed
		r.Detail = err.Error()
		return &r
	}
	if !found {
		r.Outcome = UninstallNotPresent
		return &r
	}

	// .bak sibling before the rewrite (installFencedBlock's rollback
	// affordance). A failed backup aborts: we are about to mutate
	// the very file the backup protects.
	backupPath := p.path + ".bak"
	if err := os.WriteFile(backupPath, raw, 0o600); err != nil {
		r.Outcome = UninstallFailed
		r.Detail = fmt.Sprintf("write backup %s: %v (aborting before modifying the original)", backupPath, err)
		return &r
	}
	r.Backup = backupPath

	if len(bytes.TrimSpace(remaining)) == 0 {
		if err := os.Remove(p.path); err != nil {
			r.Outcome = UninstallFailed
			r.Detail = err.Error()
			return &r
		}
		r.Outcome = UninstallRemoved
		r.Detail = "file contained only the managed block; deleted"
		return &r
	}
	if err := writeAtomic(p.path, remaining, 0o600); err != nil {
		r.Outcome = UninstallFailed
		r.Detail = err.Error()
		return &r
	}
	r.Outcome = UninstallRemoved
	return &r
}
