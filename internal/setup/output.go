package setup

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Writer is the test seam for wizard output. Everything the wizard
// prints to the user (banners, step headers, prompts, checkmarks,
// warnings, errors, progress) goes through a Writer. The terminal
// implementation writes to stdout with ANSI-safe formatting; tests
// capture output into a buffer.
//
// Why not just use fmt.Fprintln everywhere:
//   - We want consistent prefix conventions (✓ for success, ⚠ for
//     warnings, ✗ for errors) with no stray emoji elsewhere. A
//     central Writer enforces this.
//   - Tests benefit from a structured API (Check, Warn, Error) that
//     lets them assert "did the wizard report success on step N"
//     without substring-matching raw output.
//   - A future non-terminal driver (imagine a plain-HTML wizard
//     output for a desktop app) would swap the Writer impl without
//     touching step logic.
type Writer interface {
	// StepHeader prints the "Step N of M: Title" banner that frames
	// each section. The implementation should add surrounding
	// whitespace so sections are visually separated.
	StepHeader(n, of int, title string)

	// Section prints a free-standing header used outside the
	// numbered steps (Welcome, Verification, Next steps).
	Section(title string)

	// Paragraph prints one or more lines of prose with indentation
	// matching the checkmark/warning prefix width. Used for the
	// plain-English explanations above each prompt.
	Paragraph(lines ...string)

	// Prompt prints the final inline prompt that immediately
	// precedes input. No trailing newline -- the user types after.
	Prompt(text string)

	// Check prints "  ✓ <msg>" for a completed sub-step.
	Check(msg string)

	// Warn prints "  ⚠ <msg>" for a non-fatal issue. Use when we
	// continued past a minor hiccup; use Error for fatal stops.
	Warn(msg string)

	// ErrorLine prints "  ✗ <msg>" for a fatal failure within a
	// step. Named ErrorLine (not Error) to avoid shadowing the
	// builtin error type in method receivers.
	ErrorLine(msg string)

	// Blank prints a blank line. Used to separate prose from
	// prompts within a step.
	Blank()

	// Raw prints a line verbatim, no indentation or prefix. Used for
	// feature-map tables, example commands, and the end-of-wizard
	// next-steps block where explicit layout is needed.
	Raw(line string)

	// ProgressStart signals the beginning of a long operation (like
	// an embedding-model download). The implementation can choose to
	// show a spinner, a byte counter, or nothing; the wizard only
	// cares that it's told when the op starts and ends.
	ProgressStart(label string)

	// ProgressUpdate reports incremental progress (bytes or count).
	// Total may be zero if the total isn't known up front.
	ProgressUpdate(done, total int64)

	// ProgressEnd closes out the progress display.
	ProgressEnd()
}

// TerminalWriter is the production Writer that renders to an
// io.Writer (usually os.Stdout). ANSI styling is deliberately minimal:
// the checkmark/warning/error prefixes are plain-ASCII-plus-UTF8 (not
// ANSI colour codes) so output is readable when piped to a log file
// or redirected by an IDE. If richer styling is desired later, the
// prefixes can be upgraded in one place here.
type TerminalWriter struct {
	out io.Writer
}

// NewTerminalWriter returns a TerminalWriter that writes to os.Stdout.
// Tests and alternate drivers can construct one directly with a
// different writer via the NewWriter helper.
func NewTerminalWriter() *TerminalWriter {
	return &TerminalWriter{out: os.Stdout}
}

// NewWriter is for tests and alternate frontends that need to inject
// a specific io.Writer (e.g., a bytes.Buffer to capture output).
func NewWriter(out io.Writer) *TerminalWriter {
	return &TerminalWriter{out: out}
}

func (w *TerminalWriter) StepHeader(n, of int, title string) {
	fmt.Fprintln(w.out)
	fmt.Fprintf(w.out, "Step %d of %d: %s\n", n, of, title)
	fmt.Fprintln(w.out)
}

func (w *TerminalWriter) Section(title string) {
	fmt.Fprintln(w.out)
	fmt.Fprintln(w.out, title)
	fmt.Fprintln(w.out)
}

func (w *TerminalWriter) Paragraph(lines ...string) {
	for _, line := range lines {
		// Two-space indent matches the checkmark prefix width so
		// paragraphs sit flush-left with the ✓/⚠/✗ lines below them.
		// Multi-line paragraphs (strings containing \n) are
		// re-indented so pasted blocks render cleanly.
		//
		// Empty sub-lines (either `""` in the variadic args or a
		// `\n\n` inside a single string) print as a bare blank line
		// with no trailing whitespace. Otherwise a 2-space indent
		// on a visually-empty line leaves invisible trailing
		// whitespace that shows up in git diffs and IDE whitespace
		// highlighters.
		for _, sub := range strings.Split(line, "\n") {
			if sub == "" {
				fmt.Fprintln(w.out)
				continue
			}
			fmt.Fprintf(w.out, "  %s\n", sub)
		}
	}
}

func (w *TerminalWriter) Prompt(text string) {
	// No trailing newline: input follows on the same visible line.
	fmt.Fprintf(w.out, "  %s ", text)
}

func (w *TerminalWriter) Check(msg string) {
	fmt.Fprintf(w.out, "  ✓ %s\n", msg)
}

func (w *TerminalWriter) Warn(msg string) {
	fmt.Fprintf(w.out, "  ⚠ %s\n", msg)
}

func (w *TerminalWriter) ErrorLine(msg string) {
	fmt.Fprintf(w.out, "  ✗ %s\n", msg)
}

func (w *TerminalWriter) Blank() {
	fmt.Fprintln(w.out)
}

func (w *TerminalWriter) Raw(line string) {
	fmt.Fprintln(w.out, line)
}

func (w *TerminalWriter) ProgressStart(label string) {
	// A minimal progress representation. Rewriting the current line
	// would require carriage-return games that fight with terminal
	// buffering and pipes. For now, print the label and let Update
	// append byte counts on new lines (verbose but portable). If we
	// later want a real progress bar, upgrade here.
	fmt.Fprintf(w.out, "  %s ...\n", label)
}

func (w *TerminalWriter) ProgressUpdate(done, total int64) {
	if total > 0 {
		fmt.Fprintf(w.out, "    %s / %s\n", humanBytes(done), humanBytes(total))
	} else {
		fmt.Fprintf(w.out, "    %s\n", humanBytes(done))
	}
}

func (w *TerminalWriter) ProgressEnd() {
	// Intentional no-op in the line-at-a-time implementation. Kept
	// in the interface so a future fancy-bar implementation has a
	// place to hang its cleanup.
}

// humanBytes formats a byte count as a human-readable string
// (e.g., "130.0 MB"). Follows the same decimal-SI convention
// HuggingFace and Ollama use in their download output.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
