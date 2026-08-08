// saveactions is a static lint that fires on Save() / SaveOrLog()
// call sites in non-test Go files that don't carry at least one
// graph.CommitAction. Catches the "added a new Save site, forgot to
// emit an action" regression class -- without it, gramaton_log
// filtering by action would silently miss the new mutation surface.
//
// Scope: every non-test .go file under the working directory. Exits
// 0 if everything's clean, 1 if any violation. Wired into the
// pre-merge-check skill so it runs alongside go build / go test /
// go vet on every commit.
//
// Exemptions: a comment line `//gramaton:saveactions:exempt`
// immediately above the call site (no blank line between, but the
// pragma can be preceded by other comment lines on the same comment
// group). Use sparingly -- typical legitimate cases are setup paths
// and periodic background flushes that don't represent a logical
// mutation.
//
// Implementation: AST walk via go/parser. For each file, find every
// CallExpr whose callee is a SelectorExpr ending in "Save" or
// "SaveOrLog". Inspect the argument list: the second arg onward (or
// for batch contexts, any spread `actions...`) must be a CommitAction
// or a slice of CommitActions. If neither, look at the comment group
// immediately preceding the call statement; pass if it contains the
// pragma.
//
// USAGE: go run ./tools/lint/saveactions [path]
//
//	default path: current working directory.
//	exits 0 = clean, 1 = violations (printed to stderr).
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// pragmaExempt is the comment text that suppresses the lint at a
// specific Save call site. Match is case-sensitive and the pragma
// must appear in a comment group attached to the enclosing
// statement (or one immediately above it).
const pragmaExempt = "//gramaton:saveactions:exempt"

// saveMethodSuffixes lists the method names this lint cares about.
// Any CallExpr whose callee is a SelectorExpr with one of these as
// the .Sel.Name is considered a Save call site.
var saveMethodSuffixes = map[string]bool{
	"Save":      true,
	"SaveOrLog": true,
}

// finding describes a single violation: a Save call with no
// CommitAction args and no exemption pragma.
type finding struct {
	file string
	line int
	msg  string
}

func main() {
	flag.Parse()
	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	findings, err := walk(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "saveactions: walk %s: %v\n", root, err)
		os.Exit(2)
	}
	if len(findings) == 0 {
		os.Exit(0)
	}
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "%s:%d: %s\n", f.file, f.line, f.msg)
	}
	fmt.Fprintf(os.Stderr, "saveactions: %d violation(s)\n", len(findings))
	os.Exit(1)
}

// walk traverses root, parses every non-test Go file, and accumulates
// findings. Skips vendor/, node_modules/, and any .git tree entry.
func walk(root string) ([]finding, error) {
	var findings []finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			// vendor / node_modules / .git: standard ignore list.
			// testdata: Go convention for fixtures, not real code.
			// testutil: project convention for test-only helpers
			//   (Go scope rule says only *_test.go files are "test
			//   code" syntactically, but testutil/ packages are
			//   imported only from tests by convention; treating
			//   them as scope-of-tests matches that intent and
			//   spares per-helper exemption pragmas).
			if name == "vendor" || name == "node_modules" || name == ".git" || name == "testdata" || name == "testutil" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileFindings, err := lintFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		findings = append(findings, fileFindings...)
		return nil
	})
	return findings, err
}

// lintFile parses one Go source file and returns any violations.
// File-level errors (parse failures) bubble out as Go errors.
func lintFile(path string) ([]finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	// Pre-index every pragma-exempt comment line so we can probe by
	// line number per call without scanning the whole comment list.
	exemptLines := make(map[int]bool)
	for _, group := range f.Comments {
		for _, c := range group.List {
			if strings.TrimSpace(c.Text) == pragmaExempt {
				exemptLines[fset.Position(c.End()).Line] = true
			}
		}
	}

	var findings []finding
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isSaveCall(call) {
			return true
		}
		if hasActionArg(call) {
			return true
		}
		callLine := fset.Position(call.Pos()).Line
		// Pragma is honored when it sits on the line immediately
		// preceding the call. Mirrors //nolint convention. Allow up
		// to 2 lines of slack for a multi-line if-statement opener
		// like `if _, err := e.Save(...); err != nil { ... }` where
		// the call's position can sometimes report later than the
		// statement opener.
		if exemptLines[callLine-1] || exemptLines[callLine-2] {
			return true
		}
		findings = append(findings, finding{
			file: path,
			line: callLine,
			msg: fmt.Sprintf("%s call has no CommitAction; add one or annotate %s",
				saveMethodName(call), pragmaExempt),
		})
		return true
	})
	return findings, nil
}

// isSaveCall reports whether the call's callee resolves to a method
// named Save or SaveOrLog. Receivers are not type-checked -- this is
// a syntactic match that catches engine.Save, e.Save, ws.Save,
// a.engine.Save, s.engine.Save, etc.
func isSaveCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return saveMethodSuffixes[sel.Sel.Name]
}

// saveMethodName returns the method name for use in messages.
func saveMethodName(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return "Save"
}

// hasActionArg returns true if any argument beyond the first (the
// message) is plausibly a CommitAction. The check is intentionally
// loose: we don't have type info so we accept any expression in the
// args[1:] slice as a positive signal. The only failure case is
// `Save("msg")` with no second arg, OR `Save("msg", ...)` where the
// `...` is unrelated to actions -- in practice the only pattern we
// see is `Save("msg", ...someActionSlice)` or `Save("msg", action)`,
// both of which are valid.
func hasActionArg(call *ast.CallExpr) bool {
	return len(call.Args) >= 2
}
