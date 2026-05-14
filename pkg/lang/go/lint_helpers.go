// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"bytes"
	"go/ast"
	"go/token"
	"slices"
	"strings"
)

// AST + byte-level helpers used by lint_patches.go to synthesize the
// errcheck/unused auto-fix patches that lang.go.verify suggests.

// findBlankIdentInLHS returns the rightmost blank identifier (the
// underscore _) in an assignment's left-hand side, or nil if none.
// Errors in Go are conventionally the last return value, so the
// right-to-left scan finds the canonical error-discard case first —
// "result, _ := f()" yields the trailing underscore, not a leading
// one in a multi-value bind.
//
// Used by the errcheck patch generator to identify which LHS slot to
// rewrite when converting a blank-discarded error to an explicit
// check.
func findBlankIdentInLHS(lhs []ast.Expr) *ast.Ident {
	for _, v := range slices.Backward(lhs) {
		if ident, ok := v.(*ast.Ident); ok && ident.Name == "_" {
			return ident
		}
	}
	return nil
}

// isErrDeclared reports whether a variable named "err" is declared
// anywhere in funcBody at a position before beforePos. Walks only the
// top-level statement list; an err declared in a nested block does
// not count as in-scope for an outer statement.
//
// The errcheck patch generator uses this to decide whether a rewrite
// must use := (declaring err for the first time) or = (reusing the
// existing binding) — picking the wrong operator either shadows the
// original err or references an undeclared identifier.
func isErrDeclared(funcBody *ast.BlockStmt, beforePos token.Pos) bool {
	for _, stmt := range funcBody.List {
		if stmt.Pos() >= beforePos {
			break
		}
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				for _, lhs := range s.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "err" {
						return true
					}
				}
			}
		case *ast.DeclStmt:
			if genDecl, ok := s.Decl.(*ast.GenDecl); ok && genDecl.Tok == token.VAR {
				for _, spec := range genDecl.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vs.Names {
							if name.Name == "err" {
								return true
							}
						}
					}
				}
			}
		}
	}
	return false
}

// allLHSVarsDeclared reports whether every non-blank identifier in
// the LHS of stmt was already declared before stmt's position in
// funcBody. Returns false if any LHS expression is not a plain
// identifier — we cannot safely transform anything more complex.
//
// Used by the errcheck rewriter to decide between := and =: when err
// is in scope AND every other LHS var is too, := would shadow the
// existing bindings, so the operator must downgrade to =.
func allLHSVarsDeclared(funcBody *ast.BlockStmt, stmt *ast.AssignStmt) bool {
	for _, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok {
			return false
		}
		if ident.Name == "_" {
			continue
		}
		if !isVarDeclared(funcBody, ident.Name, stmt.Pos()) {
			return false
		}
	}
	return true
}

// isVarDeclared reports whether varName is declared via short-var-decl
// (:=) or a var block at any position before beforePos in funcBody.
// The traversal is linear over the function's top-level statements;
// nested-block declarations are not considered because they would be
// out of scope at beforePos anyway.
func isVarDeclared(funcBody *ast.BlockStmt, varName string, beforePos token.Pos) bool {
	for _, stmt := range funcBody.List {
		if stmt.Pos() >= beforePos {
			break
		}
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				for _, lhs := range s.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name == varName {
						return true
					}
				}
			}
		case *ast.DeclStmt:
			if genDecl, ok := s.Decl.(*ast.GenDecl); ok && genDecl.Tok == token.VAR {
				for _, spec := range genDecl.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vs.Names {
							if name.Name == varName {
								return true
							}
						}
					}
				}
			}
		}
	}
	return false
}

// findLineStart returns the byte offset of the start of the line
// containing pos. Used by detectIndent to anchor the indentation scan;
// separated out so the same lookup can be reused without re-scanning
// the full source. Returns 0 for a position at or before the start of
// src.
func findLineStart(src []byte, pos int) int {
	if pos <= 0 {
		return 0
	}
	idx := bytes.LastIndexByte(src[:pos], '\n')
	if idx < 0 {
		return 0
	}
	return idx + 1
}

// detectIndent returns the leading whitespace (tabs or spaces) of the
// line at the given byte offset. Used by the errcheck and unused patch
// generators to preserve the file's existing indentation style when
// emitting multi-line replacement text — a rewrite that drops the
// indent would corrupt the surrounding scope.
func detectIndent(src []byte, offset int) string {
	lineStart := findLineStart(src, offset)
	var sb strings.Builder
	for i := lineStart; i < len(src); i++ {
		c := src[i]
		if c != '\t' && c != ' ' {
			break
		}
		sb.WriteByte(c)
	}
	return sb.String()
}
