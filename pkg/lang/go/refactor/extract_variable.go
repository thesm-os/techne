// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
)

// ExtractVariableAction lifts an expression at a specified location into a
// named local variable, replacing the original expression with the new variable
// name and inserting `<name> := <expr>` on a fresh line immediately above.
//
// Three input modes determine which expression is captured:
//
//   - StartCol + EndCol both supplied: exact byte range on Line.
//   - StartCol supplied without EndCol: walks the AST and picks the smallest
//     ast.Expr whose byte range contains the column.
//   - Neither column supplied: examines the statement on Line and selects an
//     "interesting" sub-expression — if-condition, last RHS of an assignment,
//     last result of a return, the expression of an ExprStmt, or a for-loop
//     condition. The selection is deliberately conservative; it covers the
//     cases agents most often ask for without trying to be clever about chained
//     calls.
//
// The resulting source is run through go/format. If formatting fails (e.g., the
// rewrite produced syntactically valid but unparseable bytes — rare), the
// action falls through to AddChange with the unformatted version; goimports
// runs there and the build gate has the final say.
//
// Indentation of the inserted declaration is detected from the line containing
// the expression so the new line lands flush with the surrounding code. The
// transaction is atomic via Transaction.AddChange.
type ExtractVariableAction struct{}

// Name implements [RefactorStrategy] and returns [ActionExtractVariable].
func (*ExtractVariableAction) Name() string { return ActionExtractVariable }

func init() { RegisterAction(&ExtractVariableAction{}) }

// Execute is the [RefactorStrategy] entry point. It validates required inputs
// and delegates the rewrite to [doExtractVariable].
//
// Failure modes:
//
//   - input.File, input.Line, or input.VariableName missing or invalid — early
//     error.
//   - The line is not in the file — error.
//   - StartCol is beyond end-of-file — error.
//   - EndCol is not greater than StartCol — error.
//   - No extractable expression on the line in auto-detect mode — error.
//   - Expression bounds resolve to an empty or reversed range — error.
//   - The build gate fails after the edit — rollback.
//
// This is a strictly local refactor: only the target file is modified. Other
// files in the workspace are not consulted, so the action runs without the full
// packages.Load roundtrip that whole-program refactors require.
func (*ExtractVariableAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.File == "" {
		return fmt.Errorf("file is required for extract_variable")
	}
	if input.Line <= 0 {
		return fmt.Errorf("line is required for extract_variable")
	}
	if input.VariableName == "" {
		return fmt.Errorf("variable_name is required for extract_variable")
	}

	original, err := os.ReadFile(input.File)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	modified, err := doExtractVariable(original, input.File, input)
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("extracted expression on line %d into %q", input.Line, input.VariableName)
	return ws.AddChange(input.File, original, modified, msg)
}

func doExtractVariable(src []byte, filePath string, input Input) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	// Find the line start offset.
	lineStart := evFindLineStart(src, input.Line)
	if lineStart < 0 {
		return nil, fmt.Errorf("line %d not found in file", input.Line)
	}

	indent := DetectIndent(src, lineStart)

	var exprStart, exprEnd int

	if input.StartCol > 0 {
		// Column-specified extraction.
		exprStart = lineStart + input.StartCol - 1
		if exprStart > len(src) {
			return nil, fmt.Errorf("start_col %d is beyond end of file", input.StartCol)
		}
		if input.EndCol > 0 {
			exprEnd = lineStart + input.EndCol - 1
			if exprEnd > len(src) || exprEnd <= exprStart {
				return nil, fmt.Errorf("end_col %d is invalid", input.EndCol)
			}
		} else {
			// Find the smallest AST expression containing the start offset.
			node := evFindSmallestExprAt(fset, file, exprStart)
			if node == nil {
				return nil, fmt.Errorf("no expression found at line %d, col %d", input.Line, input.StartCol)
			}
			exprStart = fset.Position(node.Pos()).Offset
			exprEnd = fset.Position(node.End()).Offset
		}
	} else {
		// Line-only mode: find the "interesting" expression on that line.
		node, expr := evFindInterestingExpr(fset, file, input.Line)
		if node == nil || expr == nil {
			return nil, fmt.Errorf("no extractable expression found on line %d", input.Line)
		}
		exprStart = fset.Position(expr.Pos()).Offset
		exprEnd = fset.Position(expr.End()).Offset
		_ = node
	}

	if exprStart < 0 || exprEnd > len(src) || exprStart >= exprEnd {
		return nil, fmt.Errorf("could not determine expression bounds at line %d", input.Line)
	}

	exprText := string(src[exprStart:exprEnd])

	// Build the new variable declaration line.
	varDecl := indent + input.VariableName + " := " + exprText + "\n"

	// Replace the expression with the variable name, then insert the declaration above.
	modified := ReplaceBytes(src, exprStart, exprEnd, []byte(input.VariableName))
	modified = ReplaceBytes(modified, lineStart, lineStart, []byte(varDecl))

	// Format the result.
	formatted, err := format.Source(modified)
	if err != nil {
		// Return unformatted; AddChange + goimports will handle it.
		return modified, nil
	}
	return formatted, nil
}

// evFindLineStart returns the byte offset of the start of the given 1-based
// line. Returns -1 if the line is not found.
func evFindLineStart(src []byte, line int) int {
	if line == 1 {
		return 0
	}
	current := 1
	for i, b := range src {
		if b == '\n' {
			current++
			if current == line {
				return i + 1
			}
		}
	}
	return -1
}

// evFindSmallestExprAt walks the AST and returns the smallest ast.Expr whose
// byte range contains the given offset.
func evFindSmallestExprAt(fset *token.FileSet, file *ast.File, offset int) ast.Expr {
	var best ast.Expr
	bestSize := -1

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		expr, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		start := fset.Position(expr.Pos()).Offset
		end := fset.Position(expr.End()).Offset
		size := end - start
		if start <= offset && offset < end {
			if bestSize < 0 || size < bestSize {
				best = expr
				bestSize = size
			}
		}
		return true
	})
	return best
}

// evFindInterestingExpr finds the statement on the given line and returns the
// "interesting" expression to extract from it, along with the statement
// itself.
func evFindInterestingExpr(fset *token.FileSet, file *ast.File, line int) (ast.Stmt, ast.Expr) {
	var result ast.Stmt
	var expr ast.Expr

	// Walk all function declarations, looking for statements on the target line.
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		evWalkStmts(fset, fn.Body.List, line, &result, &expr)
		if result != nil {
			return result, expr
		}
	}
	return nil, nil
}

func evWalkStmts(fset *token.FileSet, stmts []ast.Stmt, line int, result *ast.Stmt, expr *ast.Expr) {
	for _, stmt := range stmts {
		stmtLine := fset.Position(stmt.Pos()).Line
		if stmtLine != line {
			// Recurse into compound statements.
			switch s := stmt.(type) {
			case *ast.BlockStmt:
				evWalkStmts(fset, s.List, line, result, expr)
			case *ast.IfStmt:
				if s.Body != nil {
					evWalkStmts(fset, s.Body.List, line, result, expr)
				}
				if s.Else != nil {
					if block, ok := s.Else.(*ast.BlockStmt); ok {
						evWalkStmts(fset, block.List, line, result, expr)
					}
				}
			case *ast.ForStmt:
				if s.Body != nil {
					evWalkStmts(fset, s.Body.List, line, result, expr)
				}
			case *ast.RangeStmt:
				if s.Body != nil {
					evWalkStmts(fset, s.Body.List, line, result, expr)
				}
			case *ast.SwitchStmt:
				if s.Body != nil {
					evWalkStmts(fset, s.Body.List, line, result, expr)
				}
			case *ast.CaseClause:
				evWalkStmts(fset, s.Body, line, result, expr)
			}
			continue
		}

		// Statement is on the target line — pick the interesting expression.
		switch s := stmt.(type) {
		case *ast.IfStmt:
			*result = s
			*expr = s.Cond
		case *ast.AssignStmt:
			if len(s.Rhs) > 0 {
				*result = s
				*expr = s.Rhs[len(s.Rhs)-1]
			}
		case *ast.ReturnStmt:
			if len(s.Results) > 0 {
				*result = s
				*expr = s.Results[len(s.Results)-1]
			}
		case *ast.ExprStmt:
			*result = s
			*expr = s.X
		case *ast.ForStmt:
			if s.Cond != nil {
				*result = s
				*expr = s.Cond
			}
		}
		if *result != nil {
			return
		}
	}
}
