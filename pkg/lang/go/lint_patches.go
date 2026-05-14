// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"

	"go.thesmos.sh/techne/pkg/lang"
)

// generateErrcheckPatch generates a synthetic lang.SuggestedPatch for
// an errcheck issue at the given line in filePath. It parses the file
// with go/ast, locates the enclosing function and the offending
// statement, then emits an edit that either wraps a discarded-error
// call in an if-err block (ExprStmt case) or rewrites a blank-identifier
// assignment to capture and check the error (AssignStmt case).
//
// The error handler is picked contextually by resolveErrHandler: in
// test files, t.Fatal/b.Fatal; in functions returning error, a
// return-with-err statement; otherwise an explicit blank discard so
// the compiler is satisfied.
//
// Returns nil if the AST analysis cannot safely determine the correct
// fix (file unreadable, parse failure, target statement not an
// ExprStmt or AssignStmt, etc.) — the lint runner then falls back
// to reporting the issue without a patch suggestion.
func generateErrcheckPatch(filePath string, line int, isTestFile bool) *lang.SuggestedPatch {
	src, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return nil
	}

	// Find the enclosing function declaration for the given line.
	fn := findFuncAtLine(file, fset, line)
	if fn == nil || fn.Body == nil {
		return nil
	}

	// Determine the error handler based on context.
	handler := resolveErrHandler(fn, isTestFile)

	// Find the statement at the target line.
	stmt := findStmtAtLine(fn.Body.List, fset, line)
	if stmt == nil {
		return nil
	}

	switch s := stmt.(type) {
	case *ast.ExprStmt:
		return buildErrcheckPatchExpr(src, fset, s, handler, filePath)
	case *ast.AssignStmt:
		return buildErrcheckPatchAssign(src, fset, fn, s, handler, filePath)
	}

	return nil
}

// resolveErrHandler determines the appropriate error-handling
// statement for a generated errcheck patch based on the enclosing
// function's signature and whether it lives in a test file.
//
// Priority (first match wins):
//  1. Test file with *testing.T parameter → t.Fatal(err)
//  2. Test file with *testing.B parameter → b.Fatal(err)
//  3. Function returning error as its last result → return ..., err
//     (other return slots filled with zero values)
//  4. Fallback → _ = err
//
// The priority order matters: in a test function that also returns an
// error (rare but legal), the t.Fatal path is correct — propagating
// the error to the test harness is the idiomatic action.
func resolveErrHandler(fn *ast.FuncDecl, isTestFile bool) string {
	if isTestFile {
		if name, ok := findTestingParam(fn, "*testing.T"); ok {
			return name + ".Fatal(err)"
		}
		if name, ok := findTestingParam(fn, "*testing.B"); ok {
			return name + ".Fatal(err)"
		}
	}

	// Check if the function returns an error.
	if fnReturnsError(fn) {
		return buildReturnErr(fn)
	}

	return "_ = err"
}

// findTestingParam checks whether fn declares a parameter of the
// given type (e.g. "*testing.T") and returns its name with true; or
// ("", false) when no matching parameter exists. The type comparison
// goes through exprString to normalize the various AST shapes that
// spell the same type.
func findTestingParam(fn *ast.FuncDecl, typeName string) (string, bool) {
	if fn.Type == nil || fn.Type.Params == nil {
		return "", false
	}
	for _, field := range fn.Type.Params.List {
		// Stringify the type expression and compare.
		typeStr := exprString(field.Type)
		if typeStr == typeName && len(field.Names) > 0 {
			return field.Names[0].Name, true
		}
	}
	return "", false
}

// fnReturnsError reports whether the function's last return type is
// literally the builtin error type. Used to decide whether the
// generated errcheck patch can propagate the error up via
// "return ..., err" instead of dropping it. The check is intentionally
// conservative: a custom error wrapper named MyError that satisfies
// the error interface will return false — the patch generator then
// falls through to the blank-discard fallback.
func fnReturnsError(fn *ast.FuncDecl) bool {
	if fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return false
	}
	results := fn.Type.Results.List
	last := results[len(results)-1]
	return exprString(last.Type) == "error"
}

// buildReturnErr constructs the return statement for a function that
// returns error as its last result. Non-error return slots are filled
// with their zero values (from zeroValueFor) so the resulting code
// compiles cleanly. Multi-named returns are expanded so (a, b int,
// err error) produces "return 0, 0, err" rather than "return 0, err".
func buildReturnErr(fn *ast.FuncDecl) string {
	if fn.Type == nil || fn.Type.Results == nil {
		return "return err"
	}

	results := fn.Type.Results.List
	if len(results) == 1 {
		// Just `return err`.
		return "return err"
	}

	// Build a list: zero values for all fields except the last (error), then err.
	var parts []string
	for i, field := range results {
		if i == len(results)-1 {
			// Last return is error.
			parts = append(parts, "err")
		} else {
			// Expand named returns or infer zero values.
			count := 1
			if len(field.Names) > 0 {
				count = len(field.Names)
			}
			for range count {
				parts = append(parts, zeroValueFor(field.Type))
			}
		}
	}
	return "return " + strings.Join(parts, ", ")
}

// zeroValueFor returns the Go zero-value literal for a type
// expression. Handles all built-in numeric and string types
// explicitly; everything else falls through to the literal nil, which
// is the zero value for pointer, slice, map, chan, func, and
// interface types. This covers ~95% of return signatures encountered
// in production code — the residual gap (named numeric types, custom
// struct values) is intentionally left to the agent to fix manually.
func zeroValueFor(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		switch t.Name {
		case "bool":
			return "false"
		case "string":
			return `""`
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64",
			"float32", "float64", "complex64", "complex128",
			"byte", "rune":
			return "0"
		default:
			// Named type — could be a struct, interface, etc.
			return "nil"
		}
	case *ast.StarExpr, *ast.ArrayType, *ast.MapType, *ast.ChanType,
		*ast.FuncType, *ast.InterfaceType, *ast.SliceExpr:
		return "nil"
	}
	return "nil"
}

// buildErrcheckPatchExpr generates a patch that wraps a standalone
// call-expression statement in an if-err block:
//
//	Foo(bar)
//
// becomes
//
//	if err := Foo(bar); err != nil {
//		handler
//	}
//
// The wrap is then run through go/format on a synthetic
// "package p; func _() { ... }" wrapper to canonicalise whitespace
// before being emitted as the patch's NewString — this avoids
// fighting goimports when the patch is applied.
func buildErrcheckPatchExpr(
	src []byte,
	fset *token.FileSet,
	stmt *ast.ExprStmt,
	handler, filePath string,
) *lang.SuggestedPatch {
	callStart := fset.Position(stmt.Pos()).Offset
	callEnd := fset.Position(stmt.End()).Offset
	if callStart < 0 || callEnd > len(src) || callStart >= callEnd {
		return nil
	}

	indent := detectIndent(src, callStart)
	callText := strings.TrimRight(string(src[callStart:callEnd]), "\n\r\t ")

	oldString := callText
	newString := fmt.Sprintf("if err := %s; err != nil {\n%s\t%s\n%s}", callText, indent, handler, indent)

	// Re-format the new snippet for canonical whitespace.
	if formatted, err := format.Source([]byte("package p\nfunc _(){" + newString + "}")); err == nil {
		// Extract just the inner statement from the formatted output.
		inner := extractInnerStmt(string(formatted))
		if inner != "" {
			newString = inner
		}
	}

	return &lang.SuggestedPatch{
		FilePath: filePath,
		Edits: []lang.PatchEdit{
			{OldString: oldString, NewString: newString},
		},
		Reason: fmt.Sprintf(
			"errcheck: unchecked error return on call expression; wrapping with if-err block using handler %q",
			handler,
		),
		Source: "synthetic-errcheck",
	}
}

// buildErrcheckPatchAssign generates a patch that replaces a blank-
// discarded error in an assignment with a proper err binding and
// follow-up nil check:
//
//	result, _ := Foo(bar)
//
// becomes
//
//	result, err := Foo(bar)
//	if err != nil {
//		handler
//	}
//
// The assignment operator is reconciled with the surrounding scope.
// When the original used = but err is not yet in scope, the rewrite
// upgrades to := so err is declared. When the original used := but
// err and every other LHS var are already in scope, the rewrite
// downgrades to = to avoid shadowing.
//
// Returns nil for shapes the generator cannot safely handle (no
// blank identifier, no position information, etc.); the lint runner
// then surfaces the issue without a patch.
func buildErrcheckPatchAssign(
	src []byte,
	fset *token.FileSet,
	fn *ast.FuncDecl,
	stmt *ast.AssignStmt,
	handler, filePath string,
) *lang.SuggestedPatch {
	// We only handle the case where at least one LHS is the blank identifier `_`
	// in the error position (last LHS).
	blankIdent := findBlankIdentInLHS(stmt.Lhs)
	if blankIdent == nil {
		return nil
	}

	stmtStart := fset.Position(stmt.Pos()).Offset
	stmtEnd := fset.Position(stmt.End()).Offset
	if stmtStart < 0 || stmtEnd > len(src) || stmtStart >= stmtEnd {
		return nil
	}

	indent := detectIndent(src, stmtStart)
	oldString := strings.TrimRight(string(src[stmtStart:stmtEnd]), "\n\r\t ")

	// Build new string: replace `_` with `err` in the original text (last `_`).
	newAssign := replaceLastBlankWithErr(oldString)
	if newAssign == "" {
		return nil
	}

	// Reconcile the assignment operator with the surrounding scope:
	//
	//   - If the original used "=" but err is NOT in scope yet, the result
	//     would reference an undeclared identifier. Upgrade "=" to ":=" so
	//     err is declared at this site. (Common case: replacing `_ = f()`
	//     with `err := f()` in a function that previously had no err var.)
	//
	//   - If the original used ":=" but err IS already in scope and every
	//     other LHS var is too, the := would shadow rather than reuse.
	//     Downgrade ":=" to "=" to assign into the existing err.
	errInScope := isErrDeclared(fn.Body, stmt.Pos())
	switch {
	case !errInScope && stmt.Tok == token.ASSIGN && strings.Contains(newAssign, "=") && !strings.Contains(newAssign, ":="):
		newAssign = strings.Replace(newAssign, "=", ":=", 1)
	case errInScope && strings.Contains(newAssign, ":=") && allLHSVarsDeclared(fn.Body, stmt):
		newAssign = strings.Replace(newAssign, ":=", "=", 1)
	}

	newString := fmt.Sprintf("%s\n%sif err != nil {\n%s\t%s\n%s}", newAssign, indent, indent, handler, indent)

	return &lang.SuggestedPatch{
		FilePath: filePath,
		Edits: []lang.PatchEdit{
			{OldString: oldString, NewString: newString},
		},
		Reason: fmt.Sprintf(
			"errcheck: blank identifier discards error return; replaced with err and added nil check using handler %q",
			handler,
		),
		Source: "synthetic-errcheck",
	}
}

// replaceLastBlankWithErr replaces the last standalone underscore
// token in the LHS of an assignment with the identifier err. The LHS
// is identified by locating the first := or = operator and
// considering everything before it; the scan is right-to-left so that
// in cases like "_, _, _ := f()" only the trailing underscore (the
// canonical error position) is replaced. Returns the empty string
// when no replacement was made.
func replaceLastBlankWithErr(line string) string {
	// Find the last standalone `_` before `:=` or `=`.
	tokIdx := strings.Index(line, ":=")
	if tokIdx < 0 {
		tokIdx = strings.Index(line, "=")
	}
	if tokIdx < 0 {
		return ""
	}

	lhs := line[:tokIdx]
	rhs := line[tokIdx:]

	// Find the last occurrence of `_` as a standalone token in lhs.
	lastIdx := -1
	for i := len(lhs) - 1; i >= 0; i-- {
		if lhs[i] == '_' {
			// Verify it is a standalone identifier (not part of another name).
			before := i > 0 && (isIdentChar(lhs[i-1]))
			after := i+1 < len(lhs) && isIdentChar(lhs[i+1])
			if !before && !after {
				lastIdx = i
				break
			}
		}
	}

	if lastIdx < 0 {
		return ""
	}

	return lhs[:lastIdx] + "err" + lhs[lastIdx+1:] + rhs
}

// isIdentChar reports whether b is a valid Go identifier continuation
// character — a letter, digit, or underscore. Used by
// replaceLastBlankWithErr to ensure the underscore it replaces is a
// standalone token, not a substring of an identifier like _a or x_.
func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// generateUnusedPatch generates a synthetic lang.SuggestedPatch for an
// unused-variable issue reported by the unused or deadcode lints.
//
// Strategy:
//   - x := expr  →  _ = expr  (explicit discard, satisfies the
//     compiler and signals to the reader that the value is
//     intentionally ignored).
//   - var x Type → the line is removed entirely.
//
// Multi-LHS short-decls ("x, y := Foo()") are not rewritten because
// the right fix depends on type information the patch generator does
// not have. Returns nil for unrecognised shapes so the lint runner
// falls back to reporting the issue without a fix.
func generateUnusedPatch(filePath string, line int, varName string) *lang.SuggestedPatch {
	if varName == "" {
		return nil
	}

	src, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}

	lines := strings.Split(string(src), "\n")
	if line < 1 || line > len(lines) {
		return nil
	}

	// Lines are 1-based.
	targetLine := lines[line-1]
	trimmed := strings.TrimSpace(targetLine)

	// Case 1: short variable declaration — replace with blank assignment.
	// Match:  <varName> := <expr>
	shortDeclPrefix := varName + " :="
	if idx := strings.Index(trimmed, shortDeclPrefix); idx == 0 {
		expr := strings.TrimSpace(trimmed[len(shortDeclPrefix):])
		// Preserve original indentation.
		indent := originalIndent(targetLine)
		newLine := indent + "_ = " + expr
		return &lang.SuggestedPatch{
			FilePath: filePath,
			Edits: []lang.PatchEdit{
				{OldString: targetLine, NewString: newLine},
			},
			Reason: fmt.Sprintf(
				"unused: variable %q declared and not used; replaced with explicit blank discard",
				varName,
			),
			Source: "synthetic-unused",
		}
	}

	// Case 2: multi-lhs short decl — e.g. "x, y := Foo()" where x or y is unused.
	// We don't attempt to rewrite multi-assignment — too risky without type info.

	// Case 3: var declaration — remove the line entirely.
	varPrefix1 := "var " + varName + " "
	varPrefix2 := "var " + varName + "\t"
	varPrefix3 := "var " + varName + "\n"
	varPrefix4 := "var " + varName + "="
	if strings.HasPrefix(trimmed, varPrefix1) ||
		strings.HasPrefix(trimmed, varPrefix2) ||
		strings.HasPrefix(trimmed, varPrefix3) ||
		strings.HasPrefix(trimmed, varPrefix4) {
		// Remove the entire line (keep the newline to avoid collapsing surrounding lines).
		return &lang.SuggestedPatch{
			FilePath: filePath,
			Edits: []lang.PatchEdit{
				{OldString: targetLine + "\n", NewString: ""},
			},
			Reason: fmt.Sprintf("unused: var declaration %q is never used; removed", varName),
			Source: "synthetic-unused",
		}
	}

	return nil
}

// extractVarName attempts to extract a variable name from a linter
// message. Handles three canonical message shapes emitted by go vet
// and the unused linter:
//
//   - "<name> declared and not used"
//   - "declared but not used: <name>"
//   - "`<name>` is unused"
//
// Returns the empty string when no shape matches or the extracted
// name is not a simple identifier. The conservative empty return
// means the patch generator skips the issue rather than emitting a
// broken patch.
func extractVarName(message string) string {
	// Pattern 1: "<name> declared and not used"
	if idx := strings.Index(message, " declared and not used"); idx > 0 {
		name := message[:idx]
		// Strip any leading punctuation or quotes.
		name = strings.Trim(name, `"' `)
		if isSimpleIdent(name) {
			return name
		}
	}

	// Pattern 2: "declared but not used: <name>"
	if _, after, ok := strings.Cut(message, "declared but not used: "); ok {
		name := strings.TrimSpace(after)
		name = strings.Trim(name, `"' `)
		if isSimpleIdent(name) {
			return name
		}
	}

	// Pattern 3: "`<name>` is unused"
	if strings.Contains(message, "is unused") {
		start := strings.Index(message, "`")
		end := strings.LastIndex(message, "`")
		if start >= 0 && end > start {
			name := message[start+1 : end]
			if isSimpleIdent(name) {
				return name
			}
		}
	}

	return ""
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// findFuncAtLine returns the innermost top-level *ast.FuncDecl in file
// whose source range contains the given 1-based line number, or nil
// if no top-level function matches. "Innermost" matters when two
// functions are reported on overlapping ranges (rare but possible with
// macro-generated source) — the narrower range is the correct match
// for a per-line patch.
func findFuncAtLine(file *ast.File, fset *token.FileSet, line int) *ast.FuncDecl {
	var best *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		start := fset.Position(fn.Pos()).Line
		end := fset.Position(fn.End()).Line
		if line >= start && line <= end {
			// Prefer the innermost (narrowest range).
			if best == nil {
				best = fn
			} else {
				bestStart := fset.Position(best.Pos()).Line
				bestEnd := fset.Position(best.End()).Line
				if (end - start) < (bestEnd - bestStart) {
					best = fn
				}
			}
		}
	}
	return best
}

// findStmtAtLine returns the first statement in stmts whose source
// position covers the given 1-based line, or nil if none matches. It
// searches only the top-level statement list (not nested blocks) to
// avoid returning a block statement for a line that actually sits
// several levels deep — the patch generators only know how to
// rewrite a flat statement.
func findStmtAtLine(stmts []ast.Stmt, fset *token.FileSet, line int) ast.Stmt {
	for _, stmt := range stmts {
		startLine := fset.Position(stmt.Pos()).Line
		endLine := fset.Position(stmt.End()).Line
		if line >= startLine && line <= endLine {
			return stmt
		}
	}
	return nil
}

// exprString returns a simplified string representation of a type
// expression suitable for comparison (e.g. "*testing.T", "error",
// "int"). Handles the AST shapes that appear in real-world parameter
// and return types: Ident, StarExpr, SelectorExpr, ArrayType, MapType,
// InterfaceType, and Ellipsis. Returns the empty string for shapes
// outside that set so callers know they cannot reason about the type.
func exprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.Ellipsis:
		return "..." + exprString(t.Elt)
	}
	return ""
}

// isSimpleIdent reports whether s is a valid, non-empty Go
// identifier (letters, digits, underscores, no leading digit). Used
// by extractVarName to guard against extracting garbage from a malformed
// linter message — only well-formed identifiers are returned.
func isSimpleIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '_' {
				return false
			}
		} else if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// originalIndent returns the leading whitespace of a source line
// (spaces or tabs only). Used when generating replacement lines that
// must match the surrounding file's indentation style — mixing tabs
// and spaces would cause gofmt to rewrite the patch on the next
// format pass.
func originalIndent(line string) string {
	var sb strings.Builder
	for _, c := range line {
		if c != ' ' && c != '\t' {
			break
		}
		sb.WriteRune(c)
	}
	return sb.String()
}

// extractInnerStmt extracts the inner statement text from a synthetic
// formatted Go source like "package p\nfunc _(){ <stmt> }". Used to
// canonicalise patch NewString values after running them through
// go/format — we wrap, format, then unwrap to get a properly-indented
// statement that does not need a second formatting pass at
// apply-time. Returns the empty string when the wrapper braces
// cannot be located.
func extractInnerStmt(src string) string {
	// Find the opening brace of the function body.
	open := strings.Index(src, "{")
	close := strings.LastIndex(src, "}")
	if open < 0 || close <= open {
		return ""
	}
	inner := strings.TrimSpace(src[open+1 : close])
	return inner
}
