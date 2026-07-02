package setup

import (
	"fmt"
	"os/user"
	"strings"
)

// stepIdentity is the unnumbered author-identity section that opens
// the wizard, before the fresh-vs-import branch and the numbered
// steps. Unnumbered (like askImportOrFresh and stepVerify) because
// it's a 10-second framing question, not a setup step with work
// behind it -- numbering it would inflate the "Step N of M" count
// for something that barely qualifies.
//
// The identity is git-style: a name and an email that compose into
// "Name <email>" at use time (config.AuthorConfig.String). Records
// created in the store are stamped with the composed identity; both
// fields blank means records carry no author. Persistence rides the
// wizard's existing config save in stepVerify -- this section only
// mutates w.cfg in memory, consistent with every other step.
//
// When cfg.Author is already populated (cli/init.go parses the
// --author flag into it before constructing the wizard), the prompts
// are skipped and the preset identity is reported instead. Callers
// passing a partially-populated cfg is a documented Wizard contract.
func (w *Wizard) stepIdentity() error {
	if w.cfg.Author.Name != "" || w.cfg.Author.Email != "" {
		w.writer.Blank()
		w.writer.Check(fmt.Sprintf("Author identity: %s", w.cfg.Author.String()))
		return nil
	}

	w.writer.Blank()
	w.writer.Paragraph(
		"First, who should new records be attributed to? Records",
		"created in your store carry this identity, like the author",
		"on a git commit. Both fields are optional -- leave them",
		"blank and records carry no author. Edit later in",
		"~/.gramaton/config.yaml under author:.",
	)
	w.writer.Blank()

	// Name: prefilled from the OS account so the common case is a
	// single Enter. The default is shown in the prompt text (the
	// caps-customize prompts follow the same "(default X)" shape).
	nameDef := OSAccountName()
	if nameDef != "" {
		w.writer.Prompt(fmt.Sprintf("Name (e.g. Ada Lovelace) (default %s):", nameDef))
	} else {
		w.writer.Prompt("Name (e.g. Ada Lovelace) (Enter to skip):")
	}
	name, err := w.prompter.Text(nameDef)
	if err != nil {
		return err
	}
	w.cfg.Author.Name = name

	// Email: no prefill -- the OS knows the account's name but not
	// a meaningful email, and a wrong guess silently stamped onto
	// every record is worse than a blank.
	w.writer.Prompt("Email (e.g. ada@example.com) (Enter to skip):")
	email, err := w.prompter.Text("")
	if err != nil {
		return err
	}
	w.cfg.Author.Email = email

	return nil
}

// OSAccountName returns the current OS account's human name for use
// as the default author identity: the account's full/display name
// when the platform provides one, the username otherwise, "" when
// the lookup fails. Exported because cli/init.go's non-interactive
// path applies the same fallback without a wizard.
//
// On Unix the full name comes from the GECOS field, which may carry
// comma-separated subfields (office, phone); everything after the
// first comma is dropped, matching git's handling of the same field.
func OSAccountName() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	name := u.Name
	if i := strings.IndexByte(name, ','); i >= 0 {
		name = name[:i]
	}
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	return strings.TrimSpace(u.Username)
}
