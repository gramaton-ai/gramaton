package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Prompter is the test seam for reading user input during the wizard.
// The terminal implementation (NewTerminalPrompter) reads from stdin and
// honours TTY conventions (hidden input for Secret, line-buffered input
// otherwise). Tests pass ScriptedPrompter to drive the wizard with a
// canned sequence of answers.
//
// Why an interface (not a concrete terminal reader everywhere):
//   - Unit-testing the wizard without driving a real terminal.
//   - Keeps the wizard logic independent of *os.File assumptions; the
//     wizard never calls term.IsTerminal directly, it just receives a
//     Prompter that already committed to a mode.
type Prompter interface {
	// Text reads a line, trims whitespace, and returns it. If the user
	// presses Enter without input and def is non-empty, def is
	// returned. The prompt is whatever the caller wants; Prompter does
	// not print it (the caller prints, the prompter reads). This
	// separation keeps output formatting in Writer and input reading
	// here.
	Text(def string) (string, error)

	// Secret reads a line with echo disabled (if the input is a TTY).
	// No default -- secrets are explicitly typed or skipped by pressing
	// Enter. If the stream is not a TTY (piped input), Secret falls
	// back to plain ReadString; this is acceptable because piped
	// secrets are already flowing through something that could log
	// them, and refusing to read them would break scripted --non-
	// interactive setups.
	Secret() (string, error)

	// Choice presents the options as "  [n] label" lines on the Writer
	// out-of-band (caller prints; Prompter just reads the digit). It
	// parses a 1-indexed digit, validates it's in [1, maxChoice], and
	// returns the 0-indexed result. If the user presses Enter without
	// input and def != -1, def is returned. Any other input is an
	// error the caller should surface and re-prompt.
	Choice(maxChoice, def int) (int, error)

	// YesNo reads a single [y/n] answer. defaultYes controls which
	// letter is returned on a blank Enter. Accepts y, Y, yes, YES, n,
	// N, no, NO.
	YesNo(defaultYes bool) (bool, error)
}

// ErrAborted is returned when the user explicitly aborts the wizard
// (currently: ctrl+C mid-prompt, which surfaces as io.EOF from stdin
// and we translate here for callers to handle gracefully).
var ErrAborted = errors.New("setup aborted by user")

// TerminalPrompter is the production Prompter backed by os.Stdin and
// golang.org/x/term for secret input. It is NOT safe for concurrent
// use (the wizard is single-threaded by design).
type TerminalPrompter struct {
	reader *bufio.Reader
	// stdinFD is the file descriptor for term.ReadPassword. Captured
	// at construction so tests that replace os.Stdin don't have to
	// worry about when we look up the fd.
	stdinFD int
}

// NewTerminalPrompter returns a Prompter that reads from os.Stdin.
// Secret uses term.ReadPassword when stdin is a TTY; otherwise it
// falls back to ReadString (piped input case).
func NewTerminalPrompter() *TerminalPrompter {
	return &TerminalPrompter{
		reader:  bufio.NewReader(os.Stdin),
		stdinFD: int(os.Stdin.Fd()),
	}
}

func (p *TerminalPrompter) Text(def string) (string, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

func (p *TerminalPrompter) Secret() (string, error) {
	if term.IsTerminal(p.stdinFD) {
		// Echo-disabled read. Prints no newline on submit; we add one
		// so subsequent output is cleanly aligned.
		raw, err := term.ReadPassword(p.stdinFD)
		fmt.Println()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	// Piped input fallback. The caller warns about this when relevant;
	// we just read a line.
	return p.Text("")
}

func (p *TerminalPrompter) Choice(maxChoice, def int) (int, error) {
	// Single-shot: invalid input returns an error for the caller to
	// print and re-prompt. Keeping retry policy outside Prompter lets
	// different steps choose how forgiving they want to be (e.g., the
	// LLM-key step retries silently on bad format; the MCP-client
	// confirm aborts on anything unexpected).
	line, err := p.Text("")
	if err != nil {
		return 0, err
	}
	if line == "" && def != -1 {
		return def, nil
	}
	n, convErr := strconv.Atoi(line)
	if convErr != nil || n < 1 || n > maxChoice {
		return 0, fmt.Errorf("please enter a number from 1 to %d", maxChoice)
	}
	return n - 1, nil // convert 1-indexed user input to 0-indexed
}

func (p *TerminalPrompter) YesNo(defaultYes bool) (bool, error) {
	line, err := p.Text("")
	if err != nil {
		return false, err
	}
	lower := strings.ToLower(line)
	switch lower {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		// Ambiguous input: caller should re-prompt. Returning an error
		// lets the caller show "please answer y or n" and loop without
		// us baking in the message.
		return false, errors.New("please answer y or n")
	}
}

// ScriptedPrompter is the test implementation. It yields pre-loaded
// answers in order; an out-of-answers read returns ErrAborted so tests
// fail loudly rather than hanging.
type ScriptedPrompter struct {
	Answers []string
	pos     int
}

// NewScriptedPrompter accepts the canned answer sequence.
func NewScriptedPrompter(answers ...string) *ScriptedPrompter {
	return &ScriptedPrompter{Answers: answers}
}

func (s *ScriptedPrompter) next() (string, error) {
	if s.pos >= len(s.Answers) {
		return "", ErrAborted
	}
	a := s.Answers[s.pos]
	s.pos++
	return a, nil
}

func (s *ScriptedPrompter) Text(def string) (string, error) {
	a, err := s.next()
	if err != nil {
		return "", err
	}
	if a == "" {
		return def, nil
	}
	return a, nil
}

func (s *ScriptedPrompter) Secret() (string, error) {
	return s.next()
}

func (s *ScriptedPrompter) Choice(maxChoice, def int) (int, error) {
	a, err := s.next()
	if err != nil {
		return 0, err
	}
	if a == "" && def != -1 {
		return def, nil
	}
	n, convErr := strconv.Atoi(a)
	if convErr != nil || n < 1 || n > maxChoice {
		return 0, fmt.Errorf("please enter a number from 1 to %d", maxChoice)
	}
	return n - 1, nil
}

func (s *ScriptedPrompter) YesNo(defaultYes bool) (bool, error) {
	a, err := s.next()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(a) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, errors.New("please answer y or n")
	}
}
