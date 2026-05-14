// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strings"

	"go.thesmos.sh/techne/pkg/lang"
)

// resolveTargets inspects the verification target list and, if the
// first entry is a filesystem path (absolute or relative), returns it
// as the working directory paired with the canonical "./..." pattern.
// This is necessary because `go test <abs-path>` fails when the path
// is outside the calling module, whereas `go test ./...` from inside
// the module root works correctly.
//
// Go import paths (anything containing a dot in the first path segment
// like "github.com/foo/bar") are passed through unchanged — they are
// patterns, not paths. The empty target list defaults to ("", [./...]).
//
// Returning (workDir, patterns) lets the runner set exec.Cmd.Dir
// separately from the package patterns, which is the only way to make
// relative ./... mean what the agent expects.
func resolveTargets(targets []string) (workDir string, pkgs []string) {
	if len(targets) == 0 {
		return "", []string{"./..."}
	}

	first := targets[0]

	// Go import paths containing a dot in the first path segment are patterns,
	// not filesystem paths. e.g. "github.com/foo/bar", "go.thesmos.sh/..."
	isImportPath := !strings.Contains(first, "...") &&
		strings.Contains(strings.SplitN(first, "/", 2)[0], ".")

	if !isImportPath && lang.IsFilesystemPath(first) {
		// Use the first directory as the working dir, ignore the rest for now.
		return first, []string{"./..."}
	}

	return "", targets
}

// buildGoTestReproCommand builds the exact `go test` invocation that
// an agent (or a human) can paste into a shell to reproduce a single
// test, benchmark, or fuzz target by name. Picks the right shape
// based on the parameter type:
//   - Test*  with *testing.T → `go test -run ^Name$ pkg -v`
//   - Benchmark* with *testing.B → `go test -bench ^Name$ -run ^$ -benchmem pkg`
//   - Fuzz* with *testing.F → `go test -fuzz ^Name$ pkg -fuzztime=10s`
//
// Returns the empty string when the function does not match a
// recognised entry-point shape, so the caller can elide the field
// cleanly. The pkgPath is expected to be a clean import path — the
// caller should strip test-variant suffixes before calling.
func buildGoTestReproCommand(funcName, paramType, pkgPath string) string {
	switch {
	case strings.HasPrefix(funcName, "Test") && paramType == "*testing.T":
		return fmt.Sprintf("go test -run ^%s$ %s -v", funcName, pkgPath)
	case strings.HasPrefix(funcName, "Benchmark") && paramType == "*testing.B":
		return fmt.Sprintf("go test -bench ^%s$ -run ^$ -benchmem %s", funcName, pkgPath)
	case strings.HasPrefix(funcName, "Fuzz") && paramType == "*testing.F":
		return fmt.Sprintf("go test -fuzz ^%s$ %s -fuzztime=10s", funcName, pkgPath)
	}
	return ""
}

// minifySnippet compresses a multi-line code snippet into a single line
// by stripping boilerplate prefixes (like the "> 123 | " line-number
// gutter that golangci-lint emits) and joining non-empty trimmed lines
// with a single space.
//
// Used when surfacing lint or test failure snippets in compact
// detail levels: a five-line capture compressed to one line saves
// ~80% of the tokens while preserving the textual content.
func minifySnippet(raw string) string {
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	var parts []string
	for _, line := range lines {
		// Strip "> 123 | " or "  123 | " prefixes if present.
		if idx := strings.Index(line, "|"); idx != -1 {
			line = line[idx+1:]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

// cleanTestOutput strips standard go test boilerplate lines (=== RUN,
// === PAUSE, --- PASS, etc.) from a captured log and returns the last
// 5 meaningful lines joined with newlines.
//
// Used to extract the actual assertion failure or panic message from
// the surrounding noise that `go test -json` events produce. The last-N
// truncation preserves the bottom of the output (where the panic stack
// and error message live) rather than the top, which is typically
// uninteresting setup output.
func cleanTestOutput(logs []string) string {
	var cleaned []string
	for _, line := range logs {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" ||
			strings.HasPrefix(trimmed, "=== RUN") ||
			strings.HasPrefix(trimmed, "=== PAUSE") ||
			strings.HasPrefix(trimmed, "=== CONT") ||
			strings.HasPrefix(trimmed, "--- FAIL:") ||
			strings.HasPrefix(trimmed, "--- PASS:") {
			continue
		}
		cleaned = append(cleaned, strings.TrimRight(line, "\r\n"))
	}

	start := max(len(cleaned)-5, 0)
	return strings.TrimSpace(strings.Join(cleaned[start:], "\n"))
}

// extractEnclosingFunc reads a Go source file and returns the full
// source text of the function or method that contains the given
// 1-based line number. Returns the empty string when the file cannot
// be read, the source does not parse, or the line is not inside any
// top-level function declaration (a file-level const block, an init,
// etc.).
//
// Used by the lint and test runners to surface the entire affected
// function as forensic context in the full Detail level — invaluable
// when the issue is at the bottom of a 60-line function and the
// agent needs to see the surrounding logic to fix it.
func extractEnclosingFunc(filename string, line int) string {
	if filename == "" || line <= 0 {
		return ""
	}

	src, err := os.ReadFile(filename)
	if err != nil {
		return ""
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return ""
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		startLine := fset.Position(fn.Pos()).Line
		endLine := fset.Position(fn.End()).Line

		if line >= startLine && line <= endLine {
			startOff := fset.Position(fn.Pos()).Offset
			endOff := fset.Position(fn.End()).Offset
			if startOff >= 0 && endOff <= len(src) && startOff < endOff {
				return string(src[startOff:endOff])
			}
		}
	}

	return ""
}

// stripGoComments removes all comments from a Go source fragment
// using the AST printer. Wraps the fragment in a minimal package
// clause, parses with mode 0 (so comment nodes are NOT attached to
// the AST), then prints the declarations back without them.
//
// String literals are safely preserved because go/parser correctly
// identifies them and the printer reproduces them verbatim — a naive
// string.Replace approach would corrupt a string like "// not a
// comment".
//
// Returns the original source unchanged if parsing fails or if the
// wrapped source has no declarations, so the function is safe to
// apply unconditionally as part of a minification pipeline.
func stripGoComments(src string) string {
	wrapped := "package _minify\n\n" + src
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", wrapped, 0) // mode 0 = no comment nodes
	if err != nil {
		return src
	}
	if len(f.Decls) == 0 {
		return src
	}
	var buf bytes.Buffer
	for _, decl := range f.Decls {
		if err := printer.Fprint(&buf, fset, decl); err != nil {
			return src
		}
		buf.WriteByte('\n')
	}
	return strings.TrimSpace(buf.String())
}

// minifyGoSource collapses blank lines and trims trailing whitespace in
// a Go source fragment. Safe to apply after stripGoComments, with which
// it composes to produce maximally dense source for token-budget
// constrained outputs.
//
// The function does not touch indentation or line content (other than
// right-trimming whitespace), so the result is still valid Go and
// readable in a terminal.
func minifyGoSource(src string) string {
	lines := strings.Split(src, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return strings.Join(result, "\n")
}

// extractCodeSnippet reads a source file and returns up to ~10 lines
// of bracket-balanced context starting from the target line. The
// capture continues until brackets are balanced AND the current line
// does not end with a continuation token (`,`, `+`, `&&`, `||`), so
// multi-line statements like long function calls are captured in full
// rather than truncated mid-expression.
//
// Used to populate Issue.Snippet for lint and test failures, giving
// the agent enough context to understand a finding without reading
// the whole file. Returns the empty string when the file cannot be
// opened or the target line is out of range.
func extractCodeSnippet(filename string, targetLine int) string {
	if filename == "" || targetLine <= 0 {
		return ""
	}

	file, err := os.Open(filename)
	if err != nil {
		return ""
	}
	defer file.Close()

	var snippet strings.Builder
	scanner := bufio.NewScanner(file)
	currentLine := 1

	var openBrackets int
	capturing := false
	linesCaptured := 0
	const maxSnippetLines = 10

	for scanner.Scan() {
		line := scanner.Text()

		if currentLine == targetLine {
			capturing = true
		}

		if capturing {
			linesCaptured++

			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				if snippet.Len() > 0 {
					snippet.WriteString(" ")
				}
				snippet.WriteString(trimmed)
			}

			for _, ch := range line {
				switch ch {
				case '(', '{', '[':
					openBrackets++
				case ')', '}', ']':
					openBrackets--
				}
			}

			isContinuing := strings.HasSuffix(trimmed, ",") ||
				strings.HasSuffix(trimmed, "+") ||
				strings.HasSuffix(trimmed, "&&") ||
				strings.HasSuffix(trimmed, "||")

			if (openBrackets <= 0 && !isContinuing) || linesCaptured >= maxSnippetLines {
				break
			}
		}
		currentLine++
	}

	return snippet.String()
}
