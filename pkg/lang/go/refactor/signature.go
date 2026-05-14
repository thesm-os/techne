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

// ChangeSignatureAction modifies a function or method's parameter and return
// signature and updates every call site in the workspace to match.
//
// Four kinds of edit are supported in a single call:
//
//   - AddParams: append named parameters to the definition; insert their
//     DefaultValue at every call site. When the function has a variadic tail,
//     new args are inserted BEFORE the variadic args (so the variadic remains
//     last); otherwise they are appended.
//   - AddReturns: append return types to the definition. Call sites receive the
//     assignment expression supplied as DefaultValue — typically '_' to
//     discard.
//   - RemoveParams: strip named parameters from the definition; strip the
//     corresponding positional argument from every call site, indexed by the
//     original parameter list.
//   - Any combination of the above, applied atomically.
//
// Call-site matching is two-phase. First, [sigFindCallSites] walks every loaded
// package's TypesInfo and records the (filename, offset) of each CallExpr whose
// callee resolves to the target object. Second, [updateCallSitesInFile]
// re-parses each file and matches recorded offsets to post-parse CallExpr nodes
// by proximity (tolerance ±200 bytes) combined with a callee-name check. The
// wide tolerance is intentional: when a signature change in the same file
// shifts later call positions, the recorded offsets drift but the name check
// keeps false positives out.
//
// Variadic-tail handling is computed once from the definition AST via
// [sigVariadicInsertIndex] and applied uniformly at every call site, so a
// function with signature foo(a int, vs ...string) gets new params inserted
// between a and vs, not after vs.
//
// The transaction is atomic: the definition site and every call site stage as
// one set, and the build gate rolls them all back together if compilation
// fails.
type ChangeSignatureAction struct{}

// Name implements [RefactorStrategy] and returns [ActionChangeSignature].
func (*ChangeSignatureAction) Name() string { return ActionChangeSignature }

func init() { RegisterAction(&ChangeSignatureAction{}) }

// Execute is the [RefactorStrategy] entry point. It locates the function's
// definition, rewrites the signature, computes per-call-site patches, and
// stages everything for atomic commit on ws.
//
// Process:
//
//  1. Locate the target object via [FindSymbolObject]; resolve its declaration
//     file via [sigLocateDefinition].
//  2. Rewrite the signature with [modifyFuncSignature], preserving variadic
//     position and existing parameter names.
//  3. Compute the removed-parameter indices and variadic insert index for use
//     at call sites.
//  4. For each call site (per [sigFindCallSites]), update the argument list
//     via [updateCallSitesInFile]. The definition file uses the
//     post-signature-change bytes so chained edits compose correctly.
//
// Failure modes:
//   - input.Symbol is empty — returns an error before touching anything.
//   - Symbol not found — returns an error naming it.
//   - Definition file cannot be located — returns an error (typically means the
//     symbol resolved to a synthetic object).
//   - Build gate fails after edits — every file rolls back; the action returns
//     the first compiler diagnostic.
//
// Valid Go syntax is the build gate's job: the action does not validate Type or
// DefaultValue at AddParameter level, so a typo there shows up as a rollback
// with the actual compiler message, not a custom error.
func (*ChangeSignatureAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.Symbol == "" {
		return fmt.Errorf("symbol is required")
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	targetObj := FindSymbolObject(pkgs, input.Symbol, input.File, input.Line)
	if targetObj == nil {
		return fmt.Errorf("symbol %q not found", input.Symbol)
	}

	defFilePath := sigLocateDefinition(pkgs, targetObj)
	if defFilePath == "" {
		return fmt.Errorf("cannot locate definition of %q", input.Symbol)
	}

	// 1. Rewrite the signature at the definition site.
	defOriginal, err := os.ReadFile(defFilePath)
	if err != nil {
		return fmt.Errorf("read definition file: %w", err)
	}

	modifiedSig, err := modifyFuncSignature(
		defOriginal,
		defFilePath,
		input.Symbol,
		input.AddParams,
		input.AddReturns,
		input.RemoveParams,
	)
	if err != nil {
		return fmt.Errorf("modify signature: %w", err)
	}

	if addErr := ws.AddChange(defFilePath, defOriginal, modifiedSig, "function signature updated"); addErr != nil {
		return addErr
	}

	// 2. Determine which parameter indices are being removed (for call-site patching).
	removedIndices := sigRemovedParamIndices(defOriginal, defFilePath, input.Symbol, input.RemoveParams)

	// 3. Determine the variadic split: index in the call site's arg list at
	// which new args must be inserted to stay before the variadic tail.
	// -1 means the function has no variadic — append at the end.
	insertBeforeIndex := sigVariadicInsertIndex(defOriginal, defFilePath, input.Symbol)

	// 4. Rewrite call sites.
	callSites := sigFindCallSites(pkgs, targetObj)
	for filePath, offsets := range callSites {
		var original []byte
		if filePath == defFilePath {
			original = modifiedSig // chain edits: use post-sig-change bytes
		} else {
			original, err = os.ReadFile(filePath)
			if err != nil {
				continue
			}
		}

		// Pass the bare function name so updateCallSitesInFile can match
		// by identifier when byte offsets have drifted (the def-file case
		// where the signature change shifted call positions).
		parts := strings.SplitN(input.Symbol, ".", 2)
		funcName := parts[len(parts)-1]
		modified, count, err := updateCallSitesInFile(
			original,
			filePath,
			offsets,
			funcName,
			input.AddParams,
			removedIndices,
			insertBeforeIndex,
		)
		if err != nil {
			return fmt.Errorf("update call sites in %s: %w", filePath, err)
		}
		if count == 0 {
			continue
		}

		if err := ws.AddChange(
			filePath,
			original,
			modified,
			fmt.Sprintf("%d call site(s) updated", count),
		); err != nil {
			return err
		}
	}

	return nil
}

// sigVariadicInsertIndex returns the 0-based index in the call site's arg
// list where new positional args must be inserted to remain BEFORE the
// variadic tail. Returns -1 when the function has no variadic.
func sigVariadicInsertIndex(src []byte, filePath, symbol string) int {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, 0)
	if err != nil {
		return -1
	}
	parts := strings.SplitN(symbol, ".", 2)
	funcName := parts[len(parts)-1]

	var targetFn *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == funcName {
			targetFn = fn
			break
		}
	}
	if targetFn == nil || targetFn.Type.Params == nil {
		return -1
	}

	idx := 0
	for _, field := range targetFn.Type.Params.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		if isEllipsisType(field.Type) {
			return idx
		}
		idx += count
	}
	return -1
}

// sigLocateDefinition returns the filename where targetObj is declared.
func sigLocateDefinition(pkgs []*packages.Package, targetObj types.Object) string {
	for _, pkg := range pkgs {
		if pkg.Fset == nil {
			continue
		}
		pos := pkg.Fset.Position(targetObj.Pos())
		if pos.Filename != "" {
			return pos.Filename
		}
	}
	return ""
}

// sigRemovedParamIndices computes the 0-based positions of params that will
// be removed.
func sigRemovedParamIndices(src []byte, filePath, symbol string, removeParams []string) []int {
	if len(removeParams) == 0 {
		return nil
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, 0)
	if err != nil {
		return nil
	}

	parts := strings.SplitN(symbol, ".", 2)
	funcName := parts[len(parts)-1]

	var targetFn *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == funcName {
			targetFn = fn
			break
		}
	}
	if targetFn == nil || targetFn.Type.Params == nil {
		return nil
	}

	removeSet := make(map[string]bool, len(removeParams))
	for _, p := range removeParams {
		removeSet[p] = true
	}

	var indices []int
	idx := 0
	for _, field := range targetFn.Type.Params.List {
		if len(field.Names) == 0 {
			idx++
			continue
		}
		for _, name := range field.Names {
			if removeSet[name.Name] {
				indices = append(indices, idx)
			}
			idx++
		}
	}
	return indices
}

// sigFindCallSites returns all call sites (as file offsets) for the given
// object.
func sigFindCallSites(pkgs []*packages.Package, targetObj types.Object) map[string][]int {
	sites := make(map[string][]int)
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		fset := pkg.Fset
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callObj types.Object
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					callObj = pkg.TypesInfo.Uses[fn]
				case *ast.SelectorExpr:
					callObj = pkg.TypesInfo.Uses[fn.Sel]
				}
				if callObj == targetObj {
					pos := fset.Position(call.Pos())
					sites[pos.Filename] = append(sites[pos.Filename], pos.Offset)
				}
				return true
			})
		}
	}
	return sites
}

// modifyFuncSignature rewrites the function signature in src.
func modifyFuncSignature(
	src []byte,
	filePath, symbol string,
	addParams []AddParameter,
	addReturns []AddReturn,
	removeParams []string,
) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	parts := strings.SplitN(symbol, ".", 2)
	funcName := parts[len(parts)-1]

	var targetFn *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == funcName {
			targetFn = fn
			break
		}
	}
	if targetFn == nil {
		return nil, fmt.Errorf("function %q not found in %s", funcName, filePath)
	}

	removeSet := make(map[string]bool, len(removeParams))
	for _, p := range removeParams {
		removeSet[p] = true
	}

	// Build the existing parameter list (keeping non-removed params).
	// A variadic parameter (...T) must remain final, so collect it
	// separately and re-append after any newly added parameters.
	var existingParams []string
	var variadicParam string
	if targetFn.Type.Params != nil {
		fields := targetFn.Type.Params.List
		for i, field := range fields {
			typeStr := ExprText(fset, src, field.Type)
			isVariadic := i == len(fields)-1 && isEllipsisType(field.Type)
			if len(field.Names) == 0 {
				if isVariadic {
					variadicParam = typeStr
				} else {
					existingParams = append(existingParams, typeStr)
				}
				continue
			}
			for _, name := range field.Names {
				if removeSet[name.Name] {
					continue
				}
				entry := name.Name + " " + typeStr
				if isVariadic {
					variadicParam = entry
				} else {
					existingParams = append(existingParams, entry)
				}
			}
		}
	}
	newParams := append([]string{}, existingParams...)
	for _, p := range addParams {
		newParams = append(newParams, p.Name+" "+p.Type)
	}
	if variadicParam != "" {
		newParams = append(newParams, variadicParam)
	}

	// Build new return list (keeping existing, appending new ones).
	var newReturns []string
	if targetFn.Type.Results != nil {
		for _, field := range targetFn.Type.Results.List {
			typeStr := ExprText(fset, src, field.Type)
			if len(field.Names) > 0 {
				for _, name := range field.Names {
					newReturns = append(newReturns, name.Name+" "+typeStr)
				}
			} else {
				newReturns = append(newReturns, typeStr)
			}
		}
	}
	for _, r := range addReturns {
		newReturns = append(newReturns, r.Type)
	}

	// Compute replacement range: from opening paren of params to end of results.
	replStart := fset.Position(targetFn.Type.Params.Opening).Offset
	replEnd := fset.Position(targetFn.Type.Params.Closing).Offset + 1
	if targetFn.Type.Results != nil && targetFn.Type.Results.NumFields() > 0 {
		replEnd = fset.Position(targetFn.Type.Results.End()).Offset
	}

	var sigBuf bytes.Buffer
	sigBuf.WriteByte('(')
	sigBuf.WriteString(strings.Join(newParams, ", "))
	sigBuf.WriteByte(')')

	if len(newReturns) > 0 {
		sigBuf.WriteByte(' ')
		if len(newReturns) == 1 {
			sigBuf.WriteString(newReturns[0])
		} else {
			sigBuf.WriteByte('(')
			sigBuf.WriteString(strings.Join(newReturns, ", "))
			sigBuf.WriteByte(')')
		}
	}

	return ReplaceBytes(src, replStart, replEnd, sigBuf.Bytes()), nil
}

// updateCallSitesInFile patches all call sites in src:
//   - Inserts default values for added parameters. When the function has
//     a variadic tail, new args are inserted BEFORE the variadic args
//     (at insertBeforeIndex); otherwise they are appended.
//   - Removes arguments at the positions listed in removedIndices.
func updateCallSitesInFile(
	src []byte,
	filePath string,
	offsets []int,
	funcName string,
	addParams []AddParameter,
	removedIndices []int,
	insertBeforeIndex int,
) ([]byte, int, error) {
	if len(addParams) == 0 && len(removedIndices) == 0 {
		return src, 0, nil
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return nil, 0, fmt.Errorf("parse call sites: %w", err)
	}

	offsetSet := make(map[int]bool, len(offsets))
	for _, o := range offsets {
		offsetSet[o] = true
	}

	// Build a set for fast lookup of removed indices.
	removedSet := make(map[int]bool, len(removedIndices))
	for _, i := range removedIndices {
		removedSet[i] = true
	}

	type patchSite struct {
		call   *ast.CallExpr
		offset int
	}
	var sites []patchSite

	// First pass: match each pre-mod offset to its post-mod CallExpr by
	// proximity. Tolerance is intentionally wide (±200 bytes) — a single
	// signature change can insert a couple hundred chars and shift any
	// later call sites in the same file. Combined with the name check,
	// false positives are extremely unlikely.
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !sigCallMatchesName(call, funcName) {
			return true
		}
		pos := fset.Position(call.Pos())
		for off := range offsetSet {
			if absOffset(pos.Offset-off) <= 200 {
				sites = append(sites, patchSite{call: call, offset: pos.Offset})
				delete(offsetSet, off)
				break
			}
		}
		return true
	})

	if len(sites) == 0 {
		return src, 0, nil
	}

	// Process in reverse order so offsets stay valid.
	sort.Slice(sites, func(i, j int) bool {
		return sites[i].offset > sites[j].offset
	})

	result := make([]byte, len(src))
	copy(result, src)
	count := 0

	for _, site := range sites {
		call := site.call
		lparen := fset.Position(call.Lparen).Offset
		rparen := fset.Position(call.Rparen).Offset

		// Build new arg list. Walk the original args and emit new defaults
		// at the variadic split point so they remain before any variadic
		// args. When the function has no variadic, defaults are appended.
		var argParts []string
		newDefaults := make([]string, 0, len(addParams))
		for _, p := range addParams {
			newDefaults = append(newDefaults, p.DefaultValue)
		}
		insertedDefaults := false
		for i, arg := range call.Args {
			if insertBeforeIndex >= 0 && !insertedDefaults && i == insertBeforeIndex {
				argParts = append(argParts, newDefaults...)
				insertedDefaults = true
			}
			if removedSet[i] {
				continue
			}
			argStart := fset.Position(arg.Pos()).Offset
			argEnd := fset.Position(arg.End()).Offset
			argParts = append(argParts, string(src[argStart:argEnd]))
		}
		if !insertedDefaults {
			argParts = append(argParts, newDefaults...)
		}

		newArgs := strings.Join(argParts, ", ")
		result = ReplaceBytes(result, lparen+1, rparen, []byte(newArgs))
		count++
	}

	return result, count, nil
}

// isEllipsisType reports whether the field type is a variadic (...T).
func isEllipsisType(expr ast.Expr) bool {
	_, ok := expr.(*ast.Ellipsis)
	return ok
}

// sigCallMatchesName reports whether a CallExpr's callee is `name` —
// either as a bare identifier (`name(...)`) or as a selector
// (`pkg.name(...)` / `obj.name(...)`).
func sigCallMatchesName(call *ast.CallExpr, name string) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == name
	case *ast.SelectorExpr:
		return fn.Sel.Name == name
	}
	return false
}

func absOffset(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
