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
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// MoveSymbolAction moves a named top-level symbol (func, type, const, var) from
// one file to another WITHIN THE SAME PACKAGE. Cross-package symbol moves are
// not supported here — use move_file for a whole file across packages or
// move_package for an entire directory.
//
// When the symbol is a struct type, its associated methods (every FuncDecl
// whose receiver names the type) move with it. The original doc comments and
// trailing newlines are preserved verbatim: the action extracts the
// declaration's source bytes (including its godoc doc block and trailing
// newline) and appends them to the target file. Multi-spec const/var blocks are
// moved as a unit; per-spec moves out of a block are not supported.
//
// Because the move is intra-package, no qualifier rewriting is needed at call
// sites — the symbol's identifier resolves to the same package after the move.
// The transaction's build gate still verifies the move (e.g., catches receiver
// typos in a methods-on-renamed-type follow-up).
//
// When the destination file does not yet exist, a `package <name>` clause is
// synthesized to match the source's package.
type MoveSymbolAction struct{}

// Name implements [RefactorStrategy] and returns [ActionMoveSymbol].
func (*MoveSymbolAction) Name() string { return ActionMoveSymbol }

func init() { RegisterAction(&MoveSymbolAction{}) }

// Execute is the [RefactorStrategy] entry point. It resolves the source file
// (looking up the symbol if input.File is empty), validates that source and
// target live in the same package directory, parses the source, extracts the
// declaration's byte range, removes it from the source, and appends it to the
// target.
//
// Failure modes:
//
//   - input.Symbol or input.TargetFile is empty — early error.
//   - The symbol isn't found in any loaded package — error.
//   - Source and target are in different package directories — error suggesting
//     move_file or move_package.
//   - The source file cannot be parsed — error.
//   - The symbol does not match a top-level declaration — error.
//   - The build gate fails after extraction (e.g., a forgotten reference to a
//     now-private helper) — full rollback.
//
// The source file's modified bytes have the extracted declaration replaced by a
// single blank line, and runs of three or more consecutive newlines are
// collapsed back to two by [msCollapseBlankLines] so the file stays
// gofmt-clean.
func (*MoveSymbolAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.Symbol == "" {
		return fmt.Errorf("symbol is required for move_symbol")
	}
	if input.TargetFile == "" {
		return fmt.Errorf("target_file is required for move_symbol")
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	plan, err := msPlanMove(pkgs, ws.ModRoot(), input.File, input.Symbol, input.TargetFile)
	if err != nil {
		return err
	}

	state, err := msReadSourceState(plan.sourceFile)
	if err != nil {
		return err
	}
	state, err = msApplyExtraction(state, []string{input.Symbol})
	if err != nil {
		return err
	}

	targetOriginal, _ := os.ReadFile(plan.targetFile)
	targetContent := msComposeTargetContent(targetOriginal, state.pkgName, state.extracted)

	msg := fmt.Sprintf("moved symbol %q from %s to %s", input.Symbol, plan.sourceFile, plan.targetFile)
	if err := ws.AddChange(plan.sourceFile, state.original, state.modified, msg); err != nil {
		return err
	}
	return ws.AddChange(plan.targetFile, targetOriginal, targetContent, msg)
}

// msMovePlan captures the resolved file paths for a move so single- and
// bulk-move paths share validation.
type msMovePlan struct {
	sourceFile string
	targetFile string
}

// msPlanMove resolves the source file (looking up the symbol if needed) and
// validates that the target lives in the same package directory.
func msPlanMove(pkgs []*packages.Package, modRoot, fileHint, symbol, targetFile string) (msMovePlan, error) {
	sourceFile := fileHint
	if sourceFile == "" {
		var err error
		sourceFile, err = msFindSymbolFile(pkgs, symbol)
		if err != nil {
			return msMovePlan{}, err
		}
	}

	targetPath := targetFile
	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(modRoot, targetPath)
	}

	if filepath.Clean(filepath.Dir(sourceFile)) != filepath.Clean(filepath.Dir(targetPath)) {
		return msMovePlan{}, msCrossPackageError(sourceFile, symbol, targetFile)
	}

	return msMovePlan{sourceFile: sourceFile, targetFile: targetPath}, nil
}

// msCrossPackageError builds the actionable error returned when a
// move_symbol call is asked to move a declaration into a directory
// that belongs to a different Go package.
//
// move_symbol does not rewrite importers, so cross-package moves are
// out of scope for this tool — but the agent's correct next call
// depends on what the source file looks like:
//
//   - If the source file's only top-level declarations are the symbol
//     being moved (and, when the symbol is a type, its receiver
//     methods), the equivalent operation IS move_file: relocate the
//     whole file to the destination package and let move_file's
//     importer-rewrite pass keep the build green.
//
//   - Otherwise the source file contains other symbols the caller
//     presumably wants left in place, so the established pattern is
//     two calls: move_symbol first (extract the symbol into a new
//     file inside the source package), then move_file to relocate
//     that new file to the destination.
//
// We detect the single-symbol case by re-running msFindDeclsToMove on
// a parse of the source file and comparing the returned decls against
// the file's full Decls slice. When parsing fails or the symbol is
// not found we fall through to the multi-symbol message — it's the
// strictly safer recommendation.
func msCrossPackageError(sourceFile, symbol, targetFileHint string) error {
	if msSourceFileIsSingleSymbol(sourceFile, symbol) {
		return fmt.Errorf(
			"cross-package move not supported by move_symbol; the source file %q contains only %q, "+
				"so lang.go.move_file is the equivalent operation — point its target_file at %q and it will "+
				"relocate the file AND rewrite every importer (srcpkg.X → dstpkg.X) atomically",
			filepath.Base(sourceFile), symbol, targetFileHint,
		)
	}
	return fmt.Errorf(
		"cross-package move not supported by move_symbol; the source file %q contains other declarations beyond %q "+
			"— two-step pattern: (1) call move_symbol to extract %q into its own file inside %q, "+
			"then (2) call move_file to relocate that new file into the destination package. "+
			"For moving an entire package use lang.go.move_package instead",
		filepath.Base(sourceFile), symbol, symbol, filepath.Dir(sourceFile),
	)
}

// msSourceFileIsSingleSymbol reports whether the only top-level
// declarations in sourceFile are those msFindDeclsToMove would extract
// for symbol — i.e. the symbol itself plus, when symbol names a type,
// every method declared on that type within this same file. When true,
// a cross-package move_symbol is functionally identical to a move_file
// invocation, so the error path can recommend move_file precisely
// instead of suggesting both alternatives and leaving the agent to
// guess. Parse / I/O failures conservatively return false so the
// caller falls back to the safer multi-symbol message.
func msSourceFileIsSingleSymbol(sourceFile, symbol string) bool {
	srcBytes, err := os.ReadFile(sourceFile)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	srcAST, err := parser.ParseFile(fset, sourceFile, srcBytes, parser.ParseComments)
	if err != nil {
		return false
	}
	target := msFindDeclsToMove(srcAST, symbol)
	if len(target) == 0 {
		return false
	}
	inTarget := make(map[ast.Decl]bool, len(target))
	for _, d := range target {
		inTarget[d] = true
	}
	for _, d := range srcAST.Decls {
		if !inTarget[d] {
			return false
		}
	}
	return true
}

// msSourceState holds the working state of the source file as we extract one
// or more symbols. Each call to msApplyExtraction mutates `modified` and
// appends to `extracted`; `original` stays pinned at the on-disk bytes for
// snapshot/diff purposes.
type msSourceState struct {
	original  []byte
	modified  []byte
	extracted []string
	pkgName   string
}

// msReadSourceState parses the source file and returns a fresh state with
// `modified` initially equal to `original`.
func msReadSourceState(sourceFile string) (msSourceState, error) {
	srcBytes, err := os.ReadFile(sourceFile)
	if err != nil {
		return msSourceState{}, fmt.Errorf("read source file: %w", err)
	}
	fset := token.NewFileSet()
	srcAST, err := parser.ParseFile(fset, sourceFile, srcBytes, parser.ParseComments)
	if err != nil {
		return msSourceState{}, fmt.Errorf("parse source file: %w", err)
	}
	mod := make([]byte, len(srcBytes))
	copy(mod, srcBytes)
	return msSourceState{
		original: srcBytes,
		modified: mod,
		pkgName:  srcAST.Name.Name,
	}, nil
}

// msApplyExtraction removes the declarations for each symbol from
// state.modified (parsing the current modified bytes), appending the
// extracted source text to state.extracted in input order.
func msApplyExtraction(state msSourceState, symbols []string) (msSourceState, error) {
	for _, symbol := range symbols {
		fset := token.NewFileSet()
		ast, err := parser.ParseFile(fset, "", state.modified, parser.ParseComments)
		if err != nil {
			return state, fmt.Errorf("re-parse during extraction of %q: %w", symbol, err)
		}
		decls := msFindDeclsToMove(ast, symbol)
		if len(decls) == 0 {
			return state, fmt.Errorf("symbol %q not found in source file", symbol)
		}

		type span struct{ start, end int }
		spans := make([]span, 0, len(decls))
		for _, d := range decls {
			start, end := msDeclRange(fset, state.modified, d)
			text := string(state.modified[start:end])
			state.extracted = append(state.extracted, text)
			spans = append(spans, span{start, end})
		}

		// Remove back-to-front so earlier offsets stay valid.
		for _, v := range slices.Backward(spans) {
			state.modified = ReplaceBytes(state.modified, v.start, v.end, []byte("\n"))
		}
		state.modified = msCollapseBlankLines(state.modified)
	}

	if !msHasPackageClause(state.modified) {
		state.modified = fmt.Appendf(nil, "package %s\n", state.pkgName)
	}
	return state, nil
}

// msComposeTargetContent appends each extracted declaration to the target
// file's existing bytes, synthesizing a package clause when the target is
// new (empty bytes).
func msComposeTargetContent(targetOriginal []byte, pkgName string, extracted []string) []byte {
	var buf bytes.Buffer
	if len(targetOriginal) == 0 {
		fmt.Fprintf(&buf, "package %s\n", pkgName)
	} else {
		buf.Write(targetOriginal)
	}
	for _, text := range extracted {
		buf.WriteString("\n")
		buf.WriteString(strings.TrimRight(text, "\n"))
		buf.WriteString("\n")
	}
	return buf.Bytes()
}

// msHasPackageClause parses src for a package clause only. More accurate than
// strings.Contains("package ") which is fooled by doc comments and string
// literals containing the word "package".
func msHasPackageClause(src []byte) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.PackageClauseOnly)
	if err != nil {
		return false
	}
	return f != nil && f.Name != nil && f.Name.Name != ""
}

// msFindSymbolFile returns the absolute path of the file containing the
// top-level declaration of symbol in the loaded packages.
func msFindSymbolFile(pkgs []*packages.Package, symbol string) (string, error) {
	// Strip receiver prefix if provided (e.g. "Foo.Bar" → look for "Foo").
	baseName := symbol
	if before, _, ok := strings.Cut(symbol, "."); ok {
		baseName = before
	}

	for _, pkg := range pkgs {
		if pkg.Fset == nil || pkg.TypesInfo == nil {
			continue
		}
		for ident, obj := range pkg.TypesInfo.Defs {
			if obj == nil || ident.Name != baseName {
				continue
			}
			pos := pkg.Fset.Position(ident.Pos())
			if pos.Filename != "" {
				return pos.Filename, nil
			}
		}
	}
	return "", fmt.Errorf("could not find file containing symbol %q", symbol)
}

// msFindDeclsToMove returns the AST declarations that should move.
// For a struct type: the type decl + all methods with that receiver.
// For a function/const/var: just that declaration.
func msFindDeclsToMove(file *ast.File, symbol string) []ast.Decl {
	// Determine if this is a dotted name (e.g. "Engine.Run").
	baseName := symbol
	methodName := ""
	if before, after, ok := strings.Cut(symbol, "."); ok {
		baseName = before
		methodName = after
	}

	var result []ast.Decl

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if methodName != "" {
				// Looking for a specific method on a type.
				if d.Recv != nil && msReceiverTypeName(d) == baseName && d.Name.Name == methodName {
					result = append(result, d)
				}
			} else if d.Recv == nil && d.Name.Name == symbol {
				// Top-level function.
				result = append(result, d)
			}

		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.Name == symbol {
						result = append(result, d)
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.Name == symbol {
							result = append(result, d)
						}
					}
				}
			}
		}
	}

	// If we found a type declaration, also collect all its methods.
	if methodName == "" && len(result) == 1 {
		if _, isType := msIsTypeDecl(result[0], symbol); isType {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil {
					continue
				}
				if msReceiverTypeName(fn) == symbol {
					result = append(result, fn)
				}
			}
		}
	}

	return result
}

// msIsTypeDecl checks if a declaration is a type declaration for the given
// name.
func msIsTypeDecl(decl ast.Decl, name string) (*ast.GenDecl, bool) {
	gen, ok := decl.(*ast.GenDecl)
	if !ok {
		return nil, false
	}
	for _, spec := range gen.Specs {
		if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == name {
			return gen, true
		}
	}
	return nil, false
}

// msReceiverTypeName returns the base type name of a method receiver.
func msReceiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	field := fn.Recv.List[0]
	switch t := field.Type.(type) {
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// msDeclRange returns the byte range [start, end) for a declaration,
// extended to include any leading doc comment and trailing newline.
func msDeclRange(fset *token.FileSet, src []byte, decl ast.Decl) (int, int) {
	startOff := fset.Position(decl.Pos()).Offset
	endOff := fset.Position(decl.End()).Offset

	// Extend startOff backward to include a doc comment if present.
	var docComment *ast.CommentGroup
	switch d := decl.(type) {
	case *ast.FuncDecl:
		docComment = d.Doc
	case *ast.GenDecl:
		docComment = d.Doc
	}
	if docComment != nil {
		commentStart := fset.Position(docComment.Pos()).Offset
		if commentStart < startOff {
			startOff = commentStart
		}
	}

	// Walk back to the start of the line containing startOff.
	startOff = FindLineStart(src, startOff)

	// Extend endOff forward past the trailing newline.
	if endOff < len(src) && src[endOff] == '\n' {
		endOff++
	}

	return startOff, endOff
}

// msCollapseBlankLines replaces runs of 3+ consecutive newlines with 2
// newlines.
func msCollapseBlankLines(src []byte) []byte {
	var buf bytes.Buffer
	consecutive := 0
	for _, b := range src {
		if b == '\n' {
			consecutive++
			if consecutive <= 2 {
				buf.WriteByte(b)
			}
		} else {
			consecutive = 0
			buf.WriteByte(b)
		}
	}
	return buf.Bytes()
}
