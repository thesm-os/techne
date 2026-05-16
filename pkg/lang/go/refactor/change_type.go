// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"sort"

	"golang.org/x/tools/go/packages"
)

// ChangeTypeAction replaces a type definition and rewrites every direct
// invocation of values of that type to use a named method instead. The typical
// use case is migrating a callable-typed alias (e.g., `type Handler func(...)`)
// into an interface or struct method form while keeping every existing call
// site working.
//
// The rewrite is driven by Input.MethodMapping: when the special key "__call__"
// is set, every CallExpr whose callee has the target type — recognised by
// go/types identity or by the named-type's name — is rewritten from `f(args)`
// into `f.<method>(args)` by inserting `.<method>` immediately after the callee
// expression. All other mapping keys are reserved for future use and currently
// ignored.
//
// The definition replacement and the invocation rewrites can touch the same
// file. The action collects every per-file edit in ORIGINAL-source coordinates
// and applies them in descending start order via [applyEdits] so offsets stay
// valid as splices land. Without this discipline, a defs-then-uses two-phase
// approach would drift offsets in the file containing the definition.
type ChangeTypeAction struct{}

// Name implements [RefactorStrategy] and returns [ActionChangeType].
func (*ChangeTypeAction) Name() string { return ActionChangeType }

func init() { RegisterAction(&ChangeTypeAction{}) }

// fileEdit is a byte-range substitution: src[start:end] becomes replacement.
// All offsets are with respect to the ORIGINAL source so multiple edits to
// the same file compose correctly when applied in descending order.
type fileEdit struct {
	start       int
	end         int
	replacement []byte
}

// Execute is the [RefactorStrategy] entry point. It validates required inputs,
// locates the target type via [FindSymbolObject], computes the byte-range edit
// for the type definition, optionally collects per-file invocation edits, and
// stages everything atomically.
//
// Failure modes:
//
//   - input.Symbol or input.NewTypeDefinition is empty — early error, no files
//     touched.
//   - The symbol is not found in any loaded package — error naming the symbol.
//   - The type's declaration cannot be located on disk — error naming the
//     symbol again (typically a synthetic type).
//   - The build gate fails after edits — full rollback via ws.Commit; the
//     compiler diagnostic surfaces verbatim.
//
// The new type definition is inserted verbatim. The build gate is the source of
// truth for syntactic validity — passing garbage as NewTypeDefinition results
// in a rolled-back transaction with the compiler message, not a custom error.
func (a *ChangeTypeAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.Symbol == "" {
		return fmt.Errorf("change_type requires symbol")
	}
	if input.NewTypeDefinition == "" {
		return fmt.Errorf("change_type requires new_type_definition")
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	targetObj := FindSymbolObject(pkgs, input.Symbol, input.Package, input.File, input.Line)
	if targetObj == nil {
		return fmt.Errorf("symbol %q not found", input.Symbol)
	}
	targetType := targetObj.Type()

	// Collect every byte-range edit per file, all in ORIGINAL-source
	// coordinates. Both the type-definition replacement and the
	// invocation rewrites can target the same file, so we must combine
	// them before staging — otherwise offsets from the pre-stage AST
	// drift when applied against post-stage bytes.
	editsByFile := make(map[string][]fileEdit)
	srcByFile := make(map[string][]byte)

	defFile, defEdit, err := a.findTypeDefinitionEdit(input.Symbol, input.NewTypeDefinition, pkgs)
	if err != nil {
		return err
	}
	src, err := os.ReadFile(defFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", defFile, err)
	}
	srcByFile[defFile] = src
	editsByFile[defFile] = append(editsByFile[defFile], defEdit)

	if methodName, ok := input.MethodMapping["__call__"]; ok && methodName != "" {
		invocations := a.collectInvocationEdits(input.Symbol, targetType, methodName, pkgs)
		for filePath, edits := range invocations {
			if _, ok := srcByFile[filePath]; !ok {
				b, err := os.ReadFile(filePath)
				if err != nil {
					continue
				}
				srcByFile[filePath] = b
			}
			editsByFile[filePath] = append(editsByFile[filePath], edits...)
		}
	}

	for filePath, edits := range editsByFile {
		original := srcByFile[filePath]
		modified := applyEdits(original, edits)
		msg := fmt.Sprintf("changed type %q (%d edits)", input.Symbol, len(edits))
		if err := ws.AddChange(filePath, original, modified, msg); err != nil {
			return err
		}
	}
	return nil
}

// findTypeDefinitionEdit locates the file declaring `symbol` and returns a
// byte-range edit that replaces only the type body with newTypeDef.
func (*ChangeTypeAction) findTypeDefinitionEdit(
	symbol, newTypeDef string,
	pkgs []*packages.Package,
) (string, fileEdit, error) {
	for _, pkg := range pkgs {
		if pkg.Fset == nil {
			continue
		}
		for i, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name.Name != symbol {
						continue
					}
					filePath := ""
					if i < len(pkg.CompiledGoFiles) {
						filePath = pkg.CompiledGoFiles[i]
					}
					if filePath == "" {
						return "", fileEdit{}, fmt.Errorf("could not determine file for type %q", symbol)
					}
					bodyStart := pkg.Fset.Position(ts.Type.Pos()).Offset
					bodyEnd := pkg.Fset.Position(ts.Type.End()).Offset
					return filePath, fileEdit{
						start:       bodyStart,
						end:         bodyEnd,
						replacement: []byte(newTypeDef),
					}, nil
				}
			}
		}
	}
	return "", fileEdit{}, fmt.Errorf("type declaration for %q not found in any loaded package", symbol)
}

// collectInvocationEdits walks all AST files and returns, per file, the
// byte-range edits that turn `callee(args)` into `callee.method(args)`.
// All offsets are in ORIGINAL-source coordinates.
func (*ChangeTypeAction) collectInvocationEdits(
	symbol string,
	targetType types.Type,
	methodName string,
	pkgs []*packages.Package,
) map[string][]fileEdit {
	edits := make(map[string][]fileEdit)
	insertion := []byte("." + methodName)
	seen := make(map[string]map[int]bool)

	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for i, file := range pkg.Syntax {
			filePath := ""
			if i < len(pkg.CompiledGoFiles) {
				filePath = pkg.CompiledGoFiles[i]
			}
			if filePath == "" {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				calleeType := pkg.TypesInfo.TypeOf(call.Fun)
				if calleeType == nil {
					return true
				}
				matched := types.Identical(calleeType, targetType)
				if !matched {
					if named, ok2 := calleeType.(*types.Named); ok2 {
						matched = named.Obj().Name() == symbol
					}
				}
				if !matched {
					return true
				}
				calleeEnd := pkg.Fset.Position(call.Fun.End()).Offset
				if seen[filePath] == nil {
					seen[filePath] = make(map[int]bool)
				}
				if seen[filePath][calleeEnd] {
					return true
				}
				seen[filePath][calleeEnd] = true
				edits[filePath] = append(edits[filePath], fileEdit{
					start:       calleeEnd,
					end:         calleeEnd,
					replacement: insertion,
				})
				return true
			})
		}
	}
	return edits
}

// applyEdits applies all edits to src in descending start order so earlier
// offsets remain valid. Edits with identical positions are kept in input order.
func applyEdits(src []byte, edits []fileEdit) []byte {
	sorted := make([]fileEdit, len(edits))
	copy(sorted, edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].start > sorted[j].start
	})
	result := make([]byte, len(src))
	copy(result, src)
	for _, e := range sorted {
		result = ReplaceBytes(result, e.start, e.end, e.replacement)
	}
	return result
}
