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

	// UninstallSkipped: the surface could not be handled (missing
	// harness binary, failed enumeration, best-effort harness) and
	// needs manual attention; Detail carries the reason and, where
	// possible, the exact manual command. Skips are warnings, not
	// failures.
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

// UninstallInventory probes every uninstall surface for the selected
// harnesses without modifying anything. Present surfaces come back
// with UninstallPresent; surfaces that would be skipped or refused
// report that up front so the user sees it before confirming.
func UninstallInventory(ctx context.Context, configDir string, targets []*Harness) []UninstallResult {
	return uninstallWalk(ctx, configDir, targets, false)
}

// UninstallApply removes every uninstall surface for the selected
// harnesses. Partial failure is deliberate: a failed surface is
// reported and the walk continues, so one broken config file doesn't
// strand the rest of the cleanup.
func UninstallApply(ctx context.Context, configDir string, targets []*Harness) []UninstallResult {
	return uninstallWalk(ctx, configDir, targets, true)
}

func uninstallWalk(ctx context.Context, configDir string, targets []*Harness, apply bool) []UninstallResult {
	var results []UninstallResult
	for _, h := range targets {
		results = append(results, uninstallHarness(ctx, configDir, h, apply)...)
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

// uninstallHarness walks one harness's surfaces in a stable order:
// MCP entries, hook-config entries, rendered scripts, agent
// guidance. The non-MCP surfaces are probed first because their
// presence is the "footprint" signal deciding whether a missing
// harness binary earns a skipped-with-manual-hint MCP line (the
// harness was clearly installed here once) or a quiet not-present
// (nothing was ever installed; a second uninstall run must stay a
// clean no-op).
func uninstallHarness(ctx context.Context, configDir string, h *Harness, apply bool) []UninstallResult {
	ownPaths := hookOwnershipPaths(configDir)

	// Probe pass (never mutates).
	hookPresent := false
	var hookProbeErr error
	if h.UnwireHooks != nil {
		hookPresent, _, hookProbeErr = h.UnwireHooks(ctx, ownPaths, false)
	}

	scriptsDir := ""
	scriptsPresent := false
	// The configDir != "" guard keeps a misconfigured caller from
	// ever aiming the scripts RemoveAll at a cwd-relative "hooks/"
	// path.
	if h.HookEmbedDir != "" && configDir != "" {
		scriptsDir = filepath.Join(configDir, "hooks", h.HookEmbedDir)
		if fi, err := os.Stat(scriptsDir); err == nil && fi.IsDir() {
			scriptsPresent = true
		}
	}

	instr := probeInstructions(h)

	footprint := hookPresent || hookProbeErr != nil || scriptsPresent || instr.present

	var results []UninstallResult
	results = append(results, uninstallMCP(ctx, h, footprint, apply)...)
	if r := uninstallHookConfig(ctx, h, ownPaths, hookPresent, hookProbeErr, apply); r != nil {
		results = append(results, *r)
	}
	if r := uninstallHookScripts(h, scriptsDir, scriptsPresent, apply); r != nil {
		results = append(results, *r)
	}
	if r := uninstallInstructions(h, instr, apply); r != nil {
		results = append(results, *r)
	}
	return results
}

// uninstallMCP handles the MCP-entries surface for one harness.
// Vendor-CLI harnesses are gated on their binary being present --
// gramaton does not hand-edit configs it registered through a vendor
// CLI. Cursor (DetectBinary == "") is file-driven and always
// probed.
func uninstallMCP(ctx context.Context, h *Harness, footprint, apply bool) []UninstallResult {
	if h.ListMCPEntries == nil {
		return nil
	}

	bin := ""
	if h.DetectBinary != "" {
		p, err := exec.LookPath(h.DetectBinary)
		if err != nil {
			if !footprint {
				// No binary and no other gramaton footprint for this
				// harness: nothing was ever installed here worth a
				// warning. Report not-present so a machine that
				// never had the harness (and a re-run after a full
				// uninstall) stays a clean "nothing to remove".
				return []UninstallResult{{
					Harness: h.Name,
					Surface: "MCP entries",
					Outcome: UninstallNotPresent,
					Detail:  h.DetectBinary + " not found",
				}}
			}
			return []UninstallResult{{
				Harness: h.Name,
				Surface: "MCP entries",
				Outcome: UninstallSkipped,
				Detail: fmt.Sprintf("%s binary not found; if registered, remove manually with: %s (entries are named gramaton or gramaton-<store>)",
					h.DetectBinary, h.ManualMCPRemoveHint("<entry>")),
			}}
		}
		bin = p
	}

	entries, err := h.ListMCPEntries(ctx, bin)
	if err != nil {
		return []UninstallResult{{
			Harness: h.Name,
			Surface: "MCP entries",
			Outcome: mcpFailureOutcome(h, err),
			Detail:  fmt.Sprintf("could not enumerate: %v; remove manually with: %s", err, h.ManualMCPRemoveHint("<entry>")),
		}}
	}
	if len(entries) == 0 {
		return []UninstallResult{{Harness: h.Name, Surface: "MCP entries", Outcome: UninstallNotPresent}}
	}

	if !apply {
		var out []UninstallResult
		for _, e := range entries {
			out = append(out, UninstallResult{
				Harness: h.Name,
				Surface: fmt.Sprintf("MCP entry %q", e),
				Outcome: UninstallPresent,
				Detail:  h.ManualMCPRemoveHint(e),
			})
		}
		return out
	}

	removed, backup, err := h.RemoveMCPEntries(ctx, bin, entries)
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
		for _, e := range entries {
			if removedSet[e] {
				continue
			}
			out = append(out, UninstallResult{
				Harness: h.Name,
				Surface: fmt.Sprintf("MCP entry %q", e),
				Outcome: mcpFailureOutcome(h, err),
				Detail:  fmt.Sprintf("%v; remove manually with: %s", err, h.ManualMCPRemoveHint(e)),
			})
		}
	}
	return out
}

// mcpFailureOutcome downgrades an MCP failure to an informative skip
// for best-effort harnesses (kiro), matching the wizard's
// warn-and-continue install behavior for the same integration.
func mcpFailureOutcome(h *Harness, _ error) UninstallOutcome {
	if h.MCPBestEffort {
		return UninstallSkipped
	}
	return UninstallFailed
}

// uninstallHookConfig handles the hook-registrations surface.
// Existence-driven: the config file is edited if it exists,
// regardless of whether the harness binary survives. Returns nil for
// harnesses without hook auto-wiring (kiro) -- no surface to report.
func uninstallHookConfig(ctx context.Context, h *Harness, ownPaths []string, present bool, probeErr error, apply bool) *UninstallResult {
	if h.UnwireHooks == nil {
		return nil
	}
	surface := "hook registrations"
	if h.HookConfigPathHint != nil {
		surface = fmt.Sprintf("hook registrations in %s", h.HookConfigPathHint())
	}
	r := UninstallResult{Harness: h.Name, Surface: surface}
	if probeErr != nil {
		r.Outcome = UninstallFailed
		r.Detail = probeErr.Error()
		return &r
	}
	if !present {
		r.Outcome = UninstallNotPresent
		return &r
	}
	if !apply {
		r.Outcome = UninstallPresent
		return &r
	}
	changed, backup, err := h.UnwireHooks(ctx, ownPaths, true)
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

// uninstallHookScripts handles the rendered proxy-script directory
// <configDir>/hooks/<HookEmbedDir>/. Existence-driven.
func uninstallHookScripts(h *Harness, scriptsDir string, present, apply bool) *UninstallResult {
	if h.HookEmbedDir == "" || scriptsDir == "" {
		return nil
	}
	r := UninstallResult{Harness: h.Name, Surface: fmt.Sprintf("hook scripts at %s", scriptsDir)}
	if !present {
		r.Outcome = UninstallNotPresent
		return &r
	}
	if !apply {
		r.Outcome = UninstallPresent
		return &r
	}
	if err := os.RemoveAll(scriptsDir); err != nil {
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
//     first (install parity). If nothing but whitespace remains, the
//     file is deleted -- we created it in that case.
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

	// Best-effort .bak sibling before the rewrite, matching
	// installFencedBlock's rollback affordance.
	backupPath := p.path + ".bak"
	if werr := os.WriteFile(backupPath, raw, 0o600); werr == nil {
		r.Backup = backupPath
	}

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
