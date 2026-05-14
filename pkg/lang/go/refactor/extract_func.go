// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ExtractFunctionAction extracts a contiguous range of statements out of an
// enclosing function and into a new top-level function (or method, when
// Receiver is supplied), replacing the original range with a call to the new
// function.
//
// Three non-trivial bits live in this action:
//
//   - Variable-scope analysis: identifiers declared before the extracted block
//     but used inside it become parameters; identifiers declared inside the
//     block but used after it become return values. The function's own
//     parameters and named returns count as "declared before" so they flow
//     through correctly.
//   - Type resolution: parameter and return types are resolved from go/types
//     information first ([efResolveVarType]), falling back to AST scans of
//     typed var declarations, and finally to the any type. Type information
//     from the workspace load is preferred because it correctly handles
//     generics, type aliases, and embedded types.
//   - Control-flow escape check: extracting a block that contains a labelless
//     return, break, continue, or goto would silently change semantics (a
//     return wouldn't propagate; a labelless break would target the wrong
//     loop). [efBlockEscapes] rejects these up front with a clear error rather
//     than relying on the build gate to surface them after rewriting.
//
// Cross-file extraction is supported: when TargetFile differs from File, the
// call site is updated in the source and the new function appended to the
// target (creating the target with a synthesized package clause if it didn't
// exist). Same-file extraction inserts the new function immediately after the
// enclosing function.
//
// Receiver inheritance: when the enclosing function is a method whose receiver
// is referenced inside the extracted range, the receiver is inherited verbatim
// from source (preserving generic type parameters such as a generic
// Stack-with-type-parameter receiver) unless the caller passed an explicit
// Receiver.
type ExtractFunctionAction struct{}

// Name implements [RefactorStrategy] and returns [ActionExtractFunction].
func (*ExtractFunctionAction) Name() string { return ActionExtractFunction }

func init() { RegisterAction(&ExtractFunctionAction{}) }

// Execute is the [RefactorStrategy] entry point. It validates the input range,
// performs the extraction via [doExtractFunction], and stages the resulting
// per-file changes on ws.
//
// Failure modes:
//
//   - input.File or input.NewFuncName is empty — returns an error before any
//     work.
//   - Line range is invalid (zero or inverted) — returns an error.
//   - No function contains the requested range — returns an error naming the
//     line bounds.
//   - The range contains a no-extract control-flow construct (return, labelless
//     break/continue, goto) — returns a descriptive error suggesting a smaller
//     range.
//   - The build gate fails after edits — full rollback.
//
// Type information is loaded once via ws.LoadPackages and passed through to
// [doExtractFunction]; if the target file is not in any loaded package the
// action falls back to AST-only resolution, which may produce any for variables
// whose type cannot be inferred locally.
func (*ExtractFunctionAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.File == "" || input.NewFuncName == "" {
		return fmt.Errorf("file and new_func_name are required")
	}
	if input.StartLine <= 0 || input.EndLine <= 0 || input.StartLine > input.EndLine {
		return fmt.Errorf("valid start_line and end_line are required")
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	var typesInfo *types.Info
	var typesFset *token.FileSet
	var typesPkg *packages.Package

	for _, pkg := range pkgs {
		if pkg.Fset == nil || pkg.TypesInfo == nil {
			continue
		}
		for _, f := range pkg.Syntax {
			pos := pkg.Fset.Position(f.Pos())
			if pos.Filename == input.File {
				typesInfo = pkg.TypesInfo
				typesFset = pkg.Fset
				typesPkg = pkg
				break
			}
		}
		if typesInfo != nil {
			break
		}
	}

	original, err := os.ReadFile(input.File)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	modifiedCallSite, extractedFunc, enclosingFuncName, err := doExtractFunction(
		original,
		input.File,
		input,
		typesInfo,
		typesFset,
		typesPkg,
	)
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("extracted lines %d-%d into %s", input.StartLine, input.EndLine, input.NewFuncName)

	// Cross-file extraction: call site update in source, new function in target.
	if input.TargetFile != "" && input.TargetFile != input.File {
		if addErr := ws.AddChange(input.File, original, modifiedCallSite, msg); addErr != nil {
			return addErr
		}

		targetOriginal, _ := os.ReadFile(input.TargetFile)
		var newFileBuf bytes.Buffer

		if len(targetOriginal) == 0 {
			pkgName := "main"
			if typesPkg != nil {
				pkgName = typesPkg.Name
			}
			fmt.Fprintf(&newFileBuf, "package %s\n\n", pkgName)
		} else {
			newFileBuf.Write(targetOriginal)
		}

		newFileBuf.Write(extractedFunc)
		return ws.AddChange(input.TargetFile, targetOriginal, newFileBuf.Bytes(), "extracted function to new file")
	}

	// Same-file extraction: insert new function after the enclosing function.
	fset2 := token.NewFileSet()
	file2, err := parser.ParseFile(fset2, input.File, modifiedCallSite, parser.ParseComments)
	insertAfter := len(modifiedCallSite)
	if err == nil {
		for _, decl := range file2.Decls {
			if fn2, ok := decl.(*ast.FuncDecl); ok && fn2.Name.Name == enclosingFuncName {
				endOff := fset2.Position(fn2.End()).Offset
				insertAfter = FindLineEnd(modifiedCallSite, endOff)
				break
			}
		}
	}

	result := make([]byte, 0, len(modifiedCallSite)+len(extractedFunc))
	result = append(result, modifiedCallSite[:insertAfter]...)
	result = append(result, extractedFunc...)
	result = append(result, modifiedCallSite[insertAfter:]...)

	return ws.AddChange(input.File, original, result, msg)
}

func doExtractFunction(
	src []byte,
	filePath string,
	input Input,
	typesInfo *types.Info,
	typesFset *token.FileSet,
	typesPkg *packages.Package,
) ([]byte, []byte, string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return nil, nil, "", fmt.Errorf("parse error: %w", err)
	}

	var enclosingFn *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		fnStartLine := fset.Position(fn.Pos()).Line
		fnEndLine := fset.Position(fn.End()).Line
		if fnStartLine <= input.StartLine && input.EndLine <= fnEndLine {
			enclosingFn = fn
			break
		}
	}
	if enclosingFn == nil {
		return nil, nil, "", fmt.Errorf("no function found containing lines %d-%d", input.StartLine, input.EndLine)
	}

	var extractStmts []ast.Stmt
	extractBlockStart := 0
	extractBlockEnd := 0

	for _, stmt := range enclosingFn.Body.List {
		stmtStartLine := fset.Position(stmt.Pos()).Line
		stmtEndLine := fset.Position(stmt.End()).Line

		if stmtEndLine < input.StartLine {
			continue
		}
		if stmtStartLine > input.EndLine {
			break
		}

		extractStmts = append(extractStmts, stmt)
		offset := fset.Position(stmt.Pos()).Offset
		end := fset.Position(stmt.End()).Offset

		if extractBlockStart == 0 || offset < extractBlockStart {
			extractBlockStart = offset
		}
		if end > extractBlockEnd {
			extractBlockEnd = end
		}
	}

	if len(extractStmts) == 0 {
		return nil, nil, "", fmt.Errorf("no statements found in range %d-%d", input.StartLine, input.EndLine)
	}

	// Reject extracted blocks containing control-flow statements that would
	// break when lifted into a new function: a `return` would not propagate
	// to the caller, and a labelless `break`/`continue` would target the
	// caller's loop, not the new function. The build gate would catch
	// these too — but rejecting up-front gives a clearer error and avoids
	// touching the source.
	if reason := efBlockEscapes(extractStmts); reason != "" {
		return nil, nil, "", fmt.Errorf(
			"cannot extract: %s — extract a smaller range or refactor the control flow first",
			reason,
		)
	}

	declaredBefore := efCollectDeclaredBeforeWithParams(enclosingFn, enclosingFn.Body.List, extractStmts, fset)
	usedInBlock := efCollectUsedInStmts(extractStmts)
	declaredInBlock := efCollectDeclaredInStmts(extractStmts)
	usedAfter := efCollectUsedAfter(enclosingFn.Body.List, fset, input.EndLine)

	var qual types.Qualifier
	if typesPkg != nil {
		qual = implQualifierForPkg(typesPkg)
	}

	// Resolve the receiver text and the receiver variable name. If the
	// caller passed Receiver explicitly, use it. Otherwise — when the
	// enclosing function is a method whose receiver is referenced inside
	// the extracted block — inherit the enclosing method's receiver
	// verbatim from source. This preserves generic type parameters
	// (e.g. `s *Stack[T]`) without separate AST surgery.
	receiverText := input.Receiver
	if receiverText == "" && enclosingFn.Recv != nil && len(enclosingFn.Recv.List) > 0 {
		recvField := enclosingFn.Recv.List[0]
		recvName := ""
		if len(recvField.Names) > 0 {
			recvName = recvField.Names[0].Name
		}
		if recvName != "" && usedInBlock[recvName] {
			recvStart := fset.Position(recvField.Pos()).Offset
			recvEnd := fset.Position(recvField.End()).Offset
			if recvStart >= 0 && recvEnd <= len(src) && recvStart < recvEnd {
				receiverText = string(src[recvStart:recvEnd])
			}
		}
	}

	receiverVarName := ""
	if receiverText != "" {
		receiverVarName = strings.Fields(receiverText)[0]
		// The receiver is in scope for the extracted method, so it must
		// not be added as a parameter and must not be confused with an
		// outer-scope identifier.
		declaredBefore[receiverVarName] = true
	}

	var paramPairs []string
	var paramNames []string
	for name := range declaredBefore {
		if name == receiverVarName {
			continue
		}
		if usedInBlock[name] {
			typeStr := efResolveVarType(name, enclosingFn, typesInfo, typesFset, fset, qual, src)
			paramPairs = append(paramPairs, name+" "+typeStr)
			paramNames = append(paramNames, name)
		}
	}
	sort.Strings(paramPairs)
	sort.Strings(paramNames)

	type returnVar struct {
		name    string
		typeStr string
	}
	var returnVars []returnVar
	for name := range declaredInBlock {
		if usedAfter[name] {
			typeStr := efResolveVarType(name, enclosingFn, typesInfo, typesFset, fset, qual, src)
			returnVars = append(returnVars, returnVar{name: name, typeStr: typeStr})
		}
	}
	sort.Slice(returnVars, func(i, j int) bool { return returnVars[i].name < returnVars[j].name })

	var returnNames []string
	var returnTypes []string
	for _, rv := range returnVars {
		returnNames = append(returnNames, rv.name)
		returnTypes = append(returnTypes, rv.typeStr)
	}

	blockStart := FindLineStart(src, extractBlockStart)
	blockEnd := FindLineEnd(src, extractBlockEnd)
	blockText := string(src[blockStart:blockEnd])

	var newFuncBuf bytes.Buffer
	newFuncBuf.WriteString("\n")
	if receiverText != "" {
		fmt.Fprintf(&newFuncBuf, "func (%s) ", receiverText)
	} else {
		newFuncBuf.WriteString("func ")
	}
	newFuncBuf.WriteString(input.NewFuncName)
	fmt.Fprintf(&newFuncBuf, "(%s)", strings.Join(paramPairs, ", "))

	if len(returnVars) > 0 {
		if len(returnVars) == 1 {
			newFuncBuf.WriteString(" " + returnTypes[0])
		} else {
			fmt.Fprintf(&newFuncBuf, " (%s)", strings.Join(returnTypes, ", "))
		}
	}

	newFuncBuf.WriteString(" {\n")
	newFuncBuf.WriteString(blockText)
	if len(returnVars) > 0 {
		fmt.Fprintf(&newFuncBuf, "\treturn %s\n", strings.Join(returnNames, ", "))
	}
	newFuncBuf.WriteString("}\n")

	indent := DetectIndent(src, blockStart)
	var callBuf bytes.Buffer
	callBuf.WriteString(indent)
	if len(returnVars) > 0 {
		callBuf.WriteString(strings.Join(returnNames, ", ") + " := ")
	}

	if receiverText != "" {
		callBuf.WriteString(receiverVarName + ".")
	}
	callBuf.WriteString(input.NewFuncName)
	fmt.Fprintf(&callBuf, "(%s)\n", strings.Join(paramNames, ", "))

	intermediate := ReplaceBytes(src, blockStart, blockEnd, callBuf.Bytes())
	return intermediate, newFuncBuf.Bytes(), enclosingFn.Name.Name, nil
}

// efBlockEscapes returns a non-empty reason when stmts contain control
// flow that would silently change meaning when extracted into a new
// function: a `return` (would not propagate to the caller), or a
// labelless `break`/`continue` (would target the caller's loop). Branches
// inside nested loops or function literals are fine — they target the
// inner construct, not the caller.
func efBlockEscapes(stmts []ast.Stmt) string {
	for _, stmt := range stmts {
		if r := efScanEscape(stmt, 0, 0); r != "" {
			return r
		}
	}
	return ""
}

// efScanEscape walks a node tracking the loop and function-literal depth.
// Children are visited only via this function (not ast.Inspect) so the
// depth context is correct without needing a stateful visitor.
func efScanEscape(n ast.Node, loopDepth, funcDepth int) string {
	switch x := n.(type) {
	case nil:
		return ""
	case *ast.FuncLit:
		// Branches inside a nested function literal target the literal
		// itself, not the enclosing function — they don't escape.
		return efScanEscape(x.Body, 0, funcDepth+1)
	case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
		// labelless break/continue inside any of these resolves locally.
		return efScanChildren(x, loopDepth+1, funcDepth)
	case *ast.ReturnStmt:
		if funcDepth == 0 {
			return "extracted block contains a return statement"
		}
		return efScanChildren(x, loopDepth, funcDepth)
	case *ast.BranchStmt:
		if x.Label == nil {
			tok := x.Tok.String()
			if (tok == "break" || tok == "continue") && loopDepth == 0 {
				return "extracted block contains a labelless " + tok + " targeting the caller's loop"
			}
			if tok == "goto" {
				return "extracted block contains a goto"
			}
		}
	}
	return efScanChildren(n, loopDepth, funcDepth)
}

func efScanChildren(n ast.Node, loopDepth, funcDepth int) string {
	var reason string
	ast.Inspect(n, func(c ast.Node) bool {
		if c == nil || c == n {
			return c == n
		}
		if reason != "" {
			return false
		}
		// Recurse into the child with the (already updated) depth context.
		// Returning false here prevents Inspect from descending further;
		// the recursive call handles its own descent with correct depth.
		if r := efScanEscape(c, loopDepth, funcDepth); r != "" {
			reason = r
		}
		return false
	})
	return reason
}

// ── Variable Scope Analysis Helpers ──────────────────────────────────────────

func efCollectDeclaredBefore(stmts, extractStmts []ast.Stmt, fset *token.FileSet) map[string]bool {
	result := make(map[string]bool)
	if len(extractStmts) == 0 {
		return result
	}
	extractStart := fset.Position(extractStmts[0].Pos()).Line

	for _, stmt := range stmts {
		if fset.Position(stmt.Pos()).Line >= extractStart {
			break
		}
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				for _, lhs := range s.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name != "_" {
						result[ident.Name] = true
					}
				}
			}
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vs.Names {
							if name.Name != "_" {
								result[name.Name] = true
							}
						}
					}
				}
			}
		case *ast.RangeStmt:
			if s.Tok == token.DEFINE {
				if ident, ok := s.Key.(*ast.Ident); ok && ident.Name != "_" {
					result[ident.Name] = true
				}
				if s.Value != nil {
					if ident, ok := s.Value.(*ast.Ident); ok && ident.Name != "_" {
						result[ident.Name] = true
					}
				}
			}
		}
	}
	return result
}

// efCollectDeclaredBeforeWithParams is like efCollectDeclaredBefore but also
// includes the enclosing function's parameters and named return values.
func efCollectDeclaredBeforeWithParams(
	fn *ast.FuncDecl,
	stmts, extractStmts []ast.Stmt,
	fset *token.FileSet,
) map[string]bool {
	result := efCollectDeclaredBefore(stmts, extractStmts, fset)

	if fn.Type != nil && fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				if name.Name != "_" {
					result[name.Name] = true
				}
			}
		}
	}

	if fn.Type != nil && fn.Type.Results != nil {
		for _, field := range fn.Type.Results.List {
			for _, name := range field.Names {
				if name.Name != "_" {
					result[name.Name] = true
				}
			}
		}
	}

	return result
}

func efCollectUsedInStmts(stmts []ast.Stmt) map[string]bool {
	result := make(map[string]bool)
	for _, stmt := range stmts {
		ast.Inspect(stmt, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok {
				result[ident.Name] = true
			}
			return true
		})
	}
	return result
}

func efCollectDeclaredInStmts(stmts []ast.Stmt) map[string]bool {
	result := make(map[string]bool)
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				for _, lhs := range s.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name != "_" {
						result[ident.Name] = true
					}
				}
			}
		case *ast.DeclStmt:
			if gen, ok := s.Decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vs.Names {
							if name.Name != "_" {
								result[name.Name] = true
							}
						}
					}
				}
			}
		}
	}
	return result
}

func efCollectUsedAfter(stmts []ast.Stmt, fset *token.FileSet, endLine int) map[string]bool {
	result := make(map[string]bool)
	for _, stmt := range stmts {
		if fset.Position(stmt.End()).Line <= endLine {
			continue
		}
		ast.Inspect(stmt, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok {
				result[ident.Name] = true
			}
			return true
		})
	}
	return result
}

func efResolveVarType(
	name string,
	fn *ast.FuncDecl,
	typesInfo *types.Info,
	typesFset, localFset *token.FileSet,
	qual types.Qualifier,
	src []byte,
) string {
	// Prefer go/types information when available.
	if typesInfo != nil && typesFset != nil {
		for ident, obj := range typesInfo.Defs {
			if obj == nil || ident.Name != name {
				continue
			}
			if fn != nil && localFset != nil {
				objPos := typesFset.Position(ident.Pos())
				fnStart := localFset.Position(fn.Pos()).Line
				fnEnd := localFset.Position(fn.End()).Line
				if objPos.Line < fnStart || objPos.Line > fnEnd {
					continue
				}
			}
			return types.TypeString(obj.Type(), qual)
		}
	}

	// Fallback: scan the function AST for a typed var declaration.
	if fn != nil {
		var found string
		ast.Inspect(fn, func(n ast.Node) bool {
			if found != "" {
				return false
			}
			if s, ok := n.(*ast.DeclStmt); ok {
				if gen, ok := s.Decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
					for _, spec := range gen.Specs {
						if vs, ok := spec.(*ast.ValueSpec); ok && vs.Type != nil {
							for _, n2 := range vs.Names {
								if n2.Name == name {
									found = ExprText(localFset, src, vs.Type)
									return false
								}
							}
						}
					}
				}
			}
			return true
		})
		if found != "" {
			return found
		}
	}

	// Last resort: use any.
	return "any"
}
