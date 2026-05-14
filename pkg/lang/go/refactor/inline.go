// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"sort"

	"golang.org/x/tools/go/packages"
)

// InlineAction replaces every usage of a constant with its literal value,
// leaving the constant declaration in place. The intended use is to flatten a
// layer of indirection (e.g., a single-call-site `MaxRetries` that an agent
// decided no longer earns its name) without disturbing the declaration site so
// it can be deleted separately if desired.
//
// Only constants are inlined today. Variables are rejected because Go's
// evaluation semantics mean a variable's value depends on when it is read; a
// textual replacement would silently change behavior when the variable is
// mutated. Functions are rejected because inlining them correctly requires
// call-graph and effect analysis that this tool does not perform.
//
// Replacement uses the constant's exact representation as produced by
// go/constant — strings keep their quoting, numeric constants preserve their
// textual form. Package-qualified uses (`pkg.MaxRetries`) are detected and the
// entire SelectorExpr is replaced, so the resulting source does not leave
// dangling qualifiers.
//
// References hidden in string literals are NOT touched — same caveat as
// [RenameAction].
type InlineAction struct{}

// Name implements [RefactorStrategy] and returns [ActionInline].
func (*InlineAction) Name() string { return ActionInline }

func init() { RegisterAction(&InlineAction{}) }

// Execute is the [RefactorStrategy] entry point. It looks up the target symbol
// via [FindSymbolObject] and dispatches based on the resolved object's kind.
//
// Failure modes:
//
//   - input.Symbol is empty — early error.
//   - The symbol is not found — error naming it.
//   - The symbol resolves to a Var — error: variables are not supported.
//   - The symbol resolves to a Func — error: functions are not supported.
//   - The symbol resolves to something other than a Const/Var/Func — error
//     citing the dynamic type.
//   - For consts: no usages found — error rather than a silent no-op (typically
//     means the constant is dead code).
//   - The build gate fails after edits — full rollback.
//
// The constant's declaration is intentionally preserved on success. Callers
// wanting it removed should follow up with delete_file or a manual edit; in
// many cases the declaration's docstring is independently valuable and worth
// keeping.
func (a *InlineAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.Symbol == "" {
		return fmt.Errorf("inline requires symbol to identify the target")
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	targetObj := FindSymbolObject(pkgs, input.Symbol, input.File, input.Line)
	if targetObj == nil {
		return fmt.Errorf("symbol %q not found", input.Symbol)
	}

	switch obj := targetObj.(type) {
	case *types.Const:
		return a.inlineConst(input, ws, pkgs, obj)
	case *types.Var:
		return fmt.Errorf("inlining variables is not supported — use manual text replacement")
	case *types.Func:
		return fmt.Errorf("inlining functions is not supported")
	default:
		return fmt.Errorf("cannot inline symbol of type %T", targetObj)
	}
}

type inlinePos struct {
	offset int
	oldLen int
}

func (*InlineAction) inlineConst(input Input, ws Transaction, pkgs []*packages.Package, obj *types.Const) error {
	replaceValue := obj.Val().ExactString()
	replaceBytes := []byte(replaceValue)

	// Collect all Uses of the target object (not Defs — keep the definition).
	// For each use, decide the replacement range:
	//   - Bare ident `MaxRetries`        → replace `MaxRetries` only.
	//   - Qualified `pkg.MaxRetries`     → replace the whole SelectorExpr.
	// Qualified-but-not-package selectors (e.g. `myStruct.Field` matching
	// a same-named const elsewhere) cannot resolve to a const obj, so the
	// extra Sel-coverage walk is safe.
	fileEdits := make(map[string][]inlinePos)
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		// Pre-compute SelectorExpr.Sel → enclosing SelectorExpr.
		selOf := make(map[*ast.Ident]*ast.SelectorExpr)
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					selOf[sel.Sel] = sel
				}
				return true
			})
		}

		for ident, o := range pkg.TypesInfo.Uses {
			if o != obj {
				continue
			}
			pos := pkg.Fset.Position(ident.Pos())
			if pos.Filename == "" {
				continue
			}
			start := pos.Offset
			oldLen := len(ident.Name)
			if sel, ok := selOf[ident]; ok {
				if xIdent, isIdent := sel.X.(*ast.Ident); isIdent {
					if _, isPkg := pkg.TypesInfo.Uses[xIdent].(*types.PkgName); isPkg {
						xPos := pkg.Fset.Position(sel.X.Pos()).Offset
						selEnd := pkg.Fset.Position(sel.End()).Offset
						start = xPos
						oldLen = selEnd - xPos
					}
				}
			}
			fileEdits[pos.Filename] = append(fileEdits[pos.Filename], inlinePos{
				offset: start,
				oldLen: oldLen,
			})
		}
	}

	if len(fileEdits) == 0 {
		return fmt.Errorf("no usages found for %q", input.Symbol)
	}

	for filePath, positions := range fileEdits {
		original, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s: %w", filePath, err)
		}

		// Deduplicate offsets, reverse-sort for back-to-front patching.
		rawOffsets := make([]int, 0, len(positions))
		oldLenByOffset := make(map[int]int, len(positions))
		for _, p := range positions {
			if _, dup := oldLenByOffset[p.offset]; !dup {
				rawOffsets = append(rawOffsets, p.offset)
				oldLenByOffset[p.offset] = p.oldLen
			}
		}
		sort.Slice(rawOffsets, func(i, j int) bool { return rawOffsets[i] > rawOffsets[j] })

		src := make([]byte, len(original))
		copy(src, original)
		for _, off := range rawOffsets {
			end := off + oldLenByOffset[off]
			src = ReplaceBytes(src, off, end, replaceBytes)
		}

		msg := fmt.Sprintf("inlined %d occurrence(s) of %q with %s", len(rawOffsets), input.Symbol, replaceValue)
		if err := ws.AddChange(filePath, original, src, msg); err != nil {
			return err
		}
	}

	// Intentionally preserve the original constant definition.
	return nil
}
