package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/backup"
)

// runImport is the Step 0 [2] branch: restore from a backup archive
// created by `gramaton backup` on another machine. Replaces the
// fresh-install Step 1 bootstrap; Steps 2-4 (LLM key, MCP, hooks)
// still run because those are per-machine by design (keys stripped
// from backups for safety; MCP+hooks live in each client's local
// config).
//
// Flow:
//  1. Prompt for archive path. Accept ~/ expansion and relative
//     paths. Normalize and validate: must exist, be a regular file.
//  2. Create the parent config directory with 0700 perms.
//     backup.Restore itself creates (and atomically swaps in) the
//     data directory via a staging sibling.
//  3. Call backup.Restore(archive, dataDir). This is atomic: on any
//     failure the existing dataDir (if any) is left intact.
//  4. Print a reembed hint. The archive carries vectors produced by
//     whatever embedding provider the source machine used. If the
//     new machine's provider has a different dimension or a different
//     model, those vectors won't match new queries. We can't auto-
//     detect this reliably without extracting the archive's
//     config.yaml (deferred to post-OSS); the reembed workflow is
//     the user's escape hatch.
//
// Cleanup stack: nothing to register here. backup.Restore is atomic
// (staging + rename), and the config directory is a shared surface
// whose existence we don't attempt to roll back (other non-wizard
// files -- models, hooks, etc. -- may live there already).
//
// A wizard interrupted after successful import but before Step 5
// commit leaves the user with:
//   - Restored data directory (live and correct)
//   - No config.yaml (Step 5 hasn't run)
//
// Re-running `gramaton init` in that state proceeds normally
// because the "already initialized" check gates on config.yaml, not
// on data/. The user can resume configuration without re-importing.
func (w *Wizard) runImport(ctx context.Context) error {
	w.writer.Blank()
	w.writer.Paragraph(
		"Where's your backup file?",
		"(Usually named gramaton-backup-<timestamp>.tar.gz)",
	)
	w.writer.Blank()
	w.writer.Prompt(">")

	raw, err := w.prompter.Text("")
	if err != nil {
		return err
	}
	if raw == "" {
		w.writer.Warn("No path entered; aborting import.")
		return nil
	}
	archivePath, err := expandUserPath(raw)
	if err != nil {
		w.writer.ErrorLine(fmt.Sprintf("Can't resolve path %q: %v", raw, err))
		return nil
	}

	// Stat before doing anything destructive. Friendly errors for
	// the common "wrong path" cases.
	info, err := os.Stat(archivePath)
	if err != nil {
		if os.IsNotExist(err) {
			w.writer.ErrorLine(fmt.Sprintf("No file at %s", archivePath))
		} else {
			w.writer.ErrorLine(fmt.Sprintf("Can't access %s: %v", archivePath, err))
		}
		return nil
	}
	if info.IsDir() {
		w.writer.ErrorLine(fmt.Sprintf("%s is a directory, not a backup archive.", archivePath))
		return nil
	}
	if !strings.HasSuffix(archivePath, ".tar.gz") {
		// Soft warning rather than hard reject: the user's archive
		// could legitimately lack the extension (renamed, stripped
		// by a file manager, etc.). Restore validates the gzip+tar
		// header itself, so a non-gzip file will error clearly
		// inside Restore.
		w.writer.Warn(fmt.Sprintf("Filename doesn't end in .tar.gz; proceeding anyway. (%s)", filepath.Base(archivePath)))
	}

	// Create the parent config directory. backup.Restore creates the
	// data directory itself via its staging + rename dance.
	if err := os.MkdirAll(w.configDir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	w.writer.Check(fmt.Sprintf("Created %s", w.configDir))

	// Heads-up before the long operation. backup.Restore reads the
	// whole archive, extracts into a staging sibling of dataDir,
	// and atomically swaps. For a typical personal store that's
	// seconds-to-a-minute; for larger stores (or slow disks) it can
	// be several minutes. The progress line below prints once at
	// start, so without this pre-warning the user sees silence
	// during the extraction and assumes the wizard hung.
	size := "unknown size"
	if info != nil {
		size = humanBytes(info.Size())
	}
	w.writer.Paragraph(
		fmt.Sprintf("About to restore %s of data. This can take a while for large", size),
		"stores -- please don't close this window or interrupt.",
	)
	w.writer.Blank()

	// Extract + swap. Atomic: on error, dataDir (if it existed) is
	// untouched. On this code path dataDir usually doesn't exist yet
	// because we're in a fresh-install wizard; Restore creates it.
	//
	// A heartbeat goroutine emits a "still working" line every 5
	// seconds so the user has live feedback that the process is
	// alive -- backup.Restore itself has no progress callback (a
	// future enhancement). The heartbeat terminates via the done
	// channel as soon as Restore returns, before any output that
	// follows.
	w.writer.ProgressStart(fmt.Sprintf("Restoring from %s", archivePath))
	heartbeatDone := w.startHeartbeat("restoring")

	restoreErr := backup.Restore(archivePath, w.cfg.DataDir)
	close(heartbeatDone)
	// Small settle delay so the heartbeat goroutine's last
	// Fprintln (if any) lands before the subsequent check line.
	// Without this, a "still working" line can race with the
	// success line below and interleave oddly.
	time.Sleep(10 * time.Millisecond)
	w.writer.ProgressEnd()

	if restoreErr != nil {
		w.writer.ErrorLine(fmt.Sprintf("Restore failed: %v", restoreErr))
		w.writer.Paragraph(
			"Your backup archive couldn't be extracted. Common causes:",
			"  * The file isn't a gramaton backup (wrong gzip/tar shape)",
			"  * The archive is corrupt or truncated",
			"  * Filesystem permissions prevent writing to the data directory",
			"",
			"The existing data directory (if any) is unchanged.",
		)
		return nil
	}
	_ = ctx
	w.writer.Check(fmt.Sprintf("Data restored to %s", w.cfg.DataDir))

	// Embedding-model warning. The archive carries vectors from the
	// source machine's embedding provider; if the new machine ends
	// up with a different dimension/model (which we don't know yet
	// -- Step 1 is skipped on the import path and defaults will be
	// applied when Step 5 saves config), searches will misbehave.
	w.writer.Blank()
	w.writer.Warn("Embedding vectors in the restored data are tied to the source machine's embedding provider.")
	w.writer.Paragraph(
		"If this new machine uses a different embedding provider (BERT",
		"vs Ollama vs a different model), run this after setup finishes:",
		"",
		"  gramaton reembed",
		"",
		"This rebuilds vectors using the new provider. The command is",
		"batched, idempotent, and safe to interrupt.",
	)

	return nil
}

// startHeartbeat spins up a goroutine that emits a "  still <label>
// (<elapsed>)" line every 5 seconds until the returned channel is
// closed. Purpose: give live UX feedback during long operations
// (backup.Restore, embed-model download) that don't expose their
// own progress callbacks. Safe because fmt.Fprintln on os.Stdout
// is concurrency-safe (file writes are locked by the kernel) and
// the wizard isn't printing elsewhere while a long op runs.
//
// Caller MUST close the returned channel when the operation ends,
// or the heartbeat goroutine leaks (it only exits on channel close).
func (w *Wizard) startHeartbeat(label string) chan struct{} {
	done := make(chan struct{})
	go func() {
		start := time.Now()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				elapsed := time.Since(start).Round(time.Second)
				// Rendered at the same indent as the ProgressUpdate
				// byte-counter so it looks intentional next to the
				// "Restoring from ..." line from ProgressStart.
				w.writer.Raw(fmt.Sprintf("    ... still %s (%s elapsed)", label, elapsed))
			}
		}
	}()
	return done
}

// expandUserPath handles the ~/ shorthand and resolves relative
// paths against the current working directory. The wizard prompt
// has to tolerate both: users drag-and-drop absolute paths, and
// users type "./backup.tar.gz" from a terminal.
func expandUserPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			p = home
		} else {
			p = filepath.Join(home, p[2:])
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return abs, nil
}
