// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"regexp"
	"strings"

	"golang.org/x/tools/go/packages"
)

// RenameAction renames a Go symbol and rewrites every reference to it across
// the entire workspace, including cross-package and cross-module references in
// a go.work setup.
//
// The action is type-checked: it locates the target symbol via go/types rather
// than text search, which means it correctly handles method resolution,
// interface satisfaction, and shadowed identifiers without the false positives
// a regex-driven rename would produce. Two reference sources are unified into a
// single edit set:
//
//  1. Code identifiers — every TypesInfo.Defs/Uses entry whose object identity
//     matches the target, plus the position-based fallback that handles
//     test-augmented package variants (where the same declaration is loaded
//     into multiple *types.Object instances).
//  2. Godoc links — bracketed references such as the bare symbol,
//     Type-qualified, or package-qualified forms inside comment groups,
//     scanned via addCommentLinkEdits. Only links that unambiguously resolve
//     to the target are updated.
//
// References hidden inside string literals (e.g.
// reflect.ValueOf(x).MethodByName("Foo")) are NOT updated — those are runtime
// lookups invisible to static analysis. After renaming an exported method or
// field, callers should grep for MethodByName / FieldByName and update them by
// hand.
//
// Like every action, RenameAction registers itself via init() and runs against
// a [Transaction] abstraction so unit tests can use a fake workspace without
// spinning up a real Go module.
type RenameAction struct{}

// Name implements [RefactorStrategy] and returns [ActionRename].
func (*RenameAction) Name() string { return ActionRename }

func init() { RegisterAction(&RenameAction{}) }

type renamePos struct {
	offset int
	oldLen int
}

// Execute is the [RefactorStrategy] entry point. It loads the workspace's
// packages, locates input.Symbol via [FindSymbolObject], computes every
// reference site (code + godoc links), and stages the per-file edits on ws as a
// single atomic transaction.
//
// Failure modes:
//   - input.Symbol or input.NewName is empty — returns an error before touching
//     any files.
//   - The symbol is not found — returns an error naming the symbol; staged set
//     is empty so no files are written.
//   - No references are produced — returns an error rather than a silent no-op;
//     this typically means the symbol resolved but lives in code paths that the
//     workspace's packages.Load didn't pick up.
//   - The build gate fails after applying edits — ws.Commit rolls every file
//     back to its original content and the action surfaces the compiler
//     diagnostic verbatim.
//
// The new name does NOT need to be a valid Go identifier at parse time; the
// build gate catches an invalid identifier on commit. Callers should still
// validate upstream to avoid wasting a workspace-wide AST walk on an obviously
// invalid input.
func (*RenameAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.Symbol == "" || input.NewName == "" {
		return fmt.Errorf("rename requires symbol and new_name")
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	targetObj := FindSymbolObject(pkgs, input.Symbol, input.File, input.Line)
	if targetObj == nil {
		return fmt.Errorf("symbol %q not found", input.Symbol)
	}

	// targetDefFile and targetDefOffset are the canonical file+offset of the
	// target symbol's declaration. We match by both object identity AND by
	// declaration position so that test package variants (which load the same
	// source into different *token.FileSet instances, producing different
	// *types.Object pointers) are also updated when Tests:true is set.
	//
	// We resolve the declaration position using the FIRST package's fset that
	// can do so (the one FindSymbolObject used).
	targetDefFile := ""
	targetDefOffset := -1
	for _, pkg := range pkgs {
		if pkg.Fset == nil {
			continue
		}
		p := pkg.Fset.Position(targetObj.Pos())
		if p.IsValid() && p.Filename != "" {
			targetDefFile = p.Filename
			targetDefOffset = p.Offset
			break
		}
	}

	// Collect all Defs and Uses that reference targetObj.
	fileEdits := make(map[string][]renamePos)

	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}

		record := func(ident *ast.Ident, obj types.Object) {
			// Primary match: exact object identity.
			if obj == targetObj {
				pos := pkg.Fset.Position(ident.Pos())
				if pos.Filename != "" {
					fileEdits[pos.Filename] = append(fileEdits[pos.Filename], renamePos{
						offset: pos.Offset,
						oldLen: len(ident.Name),
					})
				}
				return
			}
			// Secondary match: same symbol name AND declaration resolves to the
			// same file+offset. This handles test package variants where the same
			// source file is compiled into multiple *packages.Package entries with
			// different *token.FileSet objects and therefore different token.Pos
			// integer values for the same source position.
			if obj != nil && targetDefFile != "" && ident.Name == targetObj.Name() {
				defPos := pkg.Fset.Position(obj.Pos())
				if defPos.Filename == targetDefFile && defPos.Offset == targetDefOffset {
					pos := pkg.Fset.Position(ident.Pos())
					if pos.Filename != "" {
						fileEdits[pos.Filename] = append(fileEdits[pos.Filename], renamePos{
							offset: pos.Offset,
							oldLen: len(ident.Name),
						})
					}
				}
			}
		}

		for ident, obj := range pkg.TypesInfo.Defs {
			if obj != nil {
				record(ident, obj)
			}
		}
		for ident, obj := range pkg.TypesInfo.Uses {
			record(ident, obj)
		}
	}

	if len(fileEdits) == 0 {
		return fmt.Errorf("no references found for %q", input.Symbol)
	}

	// Extend fileEdits with godoc link references ([OldName], [pkg.OldName])
	// found in comment groups across the entire workspace.
	addCommentLinkEdits(pkgs, targetObj, input.Symbol, fileEdits)

	newNameBytes := []byte(input.NewName)

	for filePath, positions := range fileEdits {
		original, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s: %w", filePath, err)
		}

		// Deduplicate offsets and sort descending so we patch back-to-front.
		rawOffsets := make([]int, 0, len(positions))
		oldLenByOffset := make(map[int]int, len(positions))
		for _, p := range positions {
			if _, dup := oldLenByOffset[p.offset]; !dup {
				rawOffsets = append(rawOffsets, p.offset)
				oldLenByOffset[p.offset] = p.oldLen
			}
		}
		sortedOffsets := SortAndDedup(rawOffsets) // returns descending

		src := make([]byte, len(original))
		copy(src, original)

		for _, off := range sortedOffsets {
			end := off + oldLenByOffset[off]
			src = ReplaceBytes(src, off, end, newNameBytes)
		}

		msg := fmt.Sprintf("renamed %d occurrence(s) of %q to %q", len(sortedOffsets), input.Symbol, input.NewName)
		if err := ws.AddChange(filePath, original, src, msg); err != nil {
			return err
		}
	}

	return nil
}

// addCommentLinkEdits scans every Go file in pkgs for godoc links of the form
// [OldName] (same-package files) or [pkgName.OldName] (cross-package files)
// that refer to the renamed symbol, and appends the corresponding byte-level
// edits to fileEdits so they are applied atomically with the code renames.
//
// Only links that unambiguously resolve to targetObj are updated:
//   - same-package: inner link text must equal the canonical symbol form
//     (e.g., "Foo" or "Engine.Run")
//   - cross-package: inner link text must equal "pkgName.Foo" or
//     "pkgName.Engine.Run"
func addCommentLinkEdits(
	pkgs []*packages.Package,
	targetObj types.Object,
	inputSymbol string,
	fileEdits map[string][]renamePos,
) {
	if targetObj.Pkg() == nil {
		return
	}

	// Canonical "old symbol" in godoc link form.
	// For a method, derive "ReceiverType.MethodName" from type info so that
	// a caller who passed just "Run" still matches "[Engine.Run]" links.
	oldSymbol := inputSymbol
	if fn, ok := targetObj.(*types.Func); ok {
		if sig, ok2 := fn.Type().(*types.Signature); ok2 && sig.Recv() != nil {
			recv := sig.Recv().Type()
			if ptr, ok3 := recv.(*types.Pointer); ok3 {
				recv = ptr.Elem()
			}
			if named, ok3 := recv.(*types.Named); ok3 {
				oldSymbol = named.Obj().Name() + "." + targetObj.Name()
			}
		}
	}

	oldShortName := oldSymbol
	if i := strings.LastIndex(oldSymbol, "."); i >= 0 {
		oldShortName = oldSymbol[i+1:]
	}

	targetPkgPath := targetObj.Pkg().Path()
	targetPkgName := targetObj.Pkg().Name()

	// Matches [OldName], [Qualifier.OldName], [pkg.Qualifier.OldName], etc.
	// The short name must be the final segment (immediately before ']').
	linkRe := regexp.MustCompile(`\[([A-Za-z_]\w*\.)*` + regexp.QuoteMeta(oldShortName) + `\]`)

	seen := make(map[string]bool)
	for _, pkg := range pkgs {
		if pkg.Fset == nil {
			continue
		}
		// Test package variants have paths like "pkg/path [pkg/path.test]".
		isSamePkg := pkg.PkgPath == targetPkgPath ||
			strings.HasPrefix(pkg.PkgPath, targetPkgPath+" ")

		for _, syntax := range pkg.Syntax {
			filePath := pkg.Fset.File(syntax.Pos()).Name()
			if seen[filePath] || !strings.HasSuffix(filePath, ".go") {
				continue
			}
			seen[filePath] = true

			src, err := os.ReadFile(filePath)
			if err != nil {
				continue
			}

			for _, cg := range syntax.Comments {
				for _, c := range cg.List {
					commentOff := pkg.Fset.Position(c.Pos()).Offset
					text := c.Text

					for _, m := range linkRe.FindAllStringIndex(text, -1) {
						fullLink := text[m[0]:m[1]]
						inner := fullLink[1 : len(fullLink)-1] // strip [ and ]

						// Disambiguation: only update links that unambiguously
						// resolve to this symbol.
						if isSamePkg {
							if inner != oldSymbol {
								continue
							}
						} else {
							if inner != targetPkgName+"."+oldSymbol {
								continue
							}
						}

						// Byte offset of oldShortName within the file.
						// oldShortName occupies the last len(oldShortName) bytes
						// of inner, i.e. just before the closing ']'.
						nameOffInText := m[0] + (len(fullLink) - 1 - len(oldShortName))
						fileOff := commentOff + nameOffInText
						end := fileOff + len(oldShortName)
						if end > len(src) || string(src[fileOff:end]) != oldShortName {
							continue
						}

						fileEdits[filePath] = append(fileEdits[filePath], renamePos{
							offset: fileOff,
							oldLen: len(oldShortName),
						})
					}
				}
			}
		}
	}
}
