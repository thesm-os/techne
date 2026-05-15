// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"strings"

	"golang.org/x/tools/go/packages"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/internal/workspace"
)

// Four narrow dependency-tracing tools that replaced the old union-shaped
// lang.go.deps. Each answers exactly one question; the agent's schema view
// shows only the fields relevant to that question.

// Callers is the lang.go.callers tool. It returns the type-checked call
// sites of a Go function or method anywhere in the workspace, with each
// result carrying the caller's location, surrounding docblock, and a
// one-line call snippet so the agent understands HOW the symbol is used
// — not just where.
//
// Prefer this over Grep for any caller search. Grep matches the string
// "Name(" and gives false positives from same-named identifiers in
// unrelated scopes, comments, and string literals; it cannot follow
// method calls through interfaces; and it has no concept of go.work.
// Callers walks the type-checked AST and only returns CallExpr nodes
// whose resolved callee identifier matches the target symbol.
//
// The full workspace is always loaded, even when input.Package is set
// — callers in module B can target a symbol in module A, and pre-
// filtering by package would hide them. In go.work mode the load is
// expanded across every use directive.
//
// By default the search is workspace-local: packages whose import path
// sits outside the workspace's own module roots are skipped before the
// finder runs. Pass IncludeExternal=true to include stdlib and external
// callers — useful when verifying that an external framework actually
// invokes a callback the workspace registered with it.
//
// Results are capped at input.Limit (default 50) and trimmed to the
// requested Detail level (summary/standard/full) so the agent can pick
// the smallest payload that answers its question.
var Callers = tool.New[lang.CallersInput, lang.DepsResult](
	"lang.go.callers",
	"PREFER OVER Grep for finding callers of a Go function. Returns type-checked call sites (Grep matches function-name strings and gives false positives from same-named identifiers in unrelated scopes). Each result includes the caller's location, surrounding docblock, and a one-line call snippet so you understand HOW the result is used. Workspace-local by default (traverses go.work modules); pass include_external=true to include stdlib and dependency callers.",
	func(ctx context.Context, in lang.CallersInput) (lang.DepsResult, error) {
		return runDepsQueryWithDetail(
			ctx,
			in.Symbol,
			in.Package,
			in.Limit,
			in.IncludeExternal,
			in.Detail,
			func(allPkgs map[string]*packages.Package, fset *token.FileSet) []lang.DepReference {
				return findCallers(in.Symbol, allPkgs)
			},
		)
	},
	tool.WithShortDescription("Find type-checked callers of a Go function or method workspace-wide"),
)

// Implementations is the lang.go.implementations tool. It returns every
// concrete type in the workspace that satisfies the named interface,
// considering both value and pointer receivers via
// types.Implements / types.NewPointer.
//
// Prefer this over Grep for finding interface implementations. Grep
// has no type information — it cannot tell which struct's method set
// satisfies a particular interface signature, and it cannot distinguish
// "implements Writer" from "defines a method called Write".
//
// By default the search is workspace-local: only packages loaded
// through workspace.Discover are considered. Pass IncludeExternal=true
// to include stdlib and dependency types — useful when verifying that a
// workspace-defined interface is satisfied by a third-party type but
// slower and louder by default. The interface's own package is always
// included even when IncludeExternal is false.
var Implementations = tool.New[lang.ImplementationsInput, lang.DepsResult](
	"lang.go.implementations",
	"PREFER OVER Grep for finding interface implementations. Uses the type checker (Grep can't tell which structs satisfy an interface) and considers both value and pointer receivers. Workspace-local by default; pass include_external=true to include stdlib and dependency implementations.",
	implementationsHandler,
	tool.WithShortDescription("Find concrete Go types that implement a given interface"),
)

// implementationsHandler implements the lang.go.implementations RPC.
// Discovers the workspace, loads the requested package (or all packages
// if Package is empty) with type information, and walks every package's
// type scope looking for named types whose value or pointer
// instantiation implements the target interface.
//
// The localRoots set is the union of workspace module import-path
// roots, so the IncludeExternal filter can distinguish workspace-local
// types from transitive-dep types after flattenWithDeps expands the
// graph regardless of whether the caller narrowed by Package.
func implementationsHandler(ctx context.Context, in lang.ImplementationsInput) (lang.DepsResult, error) {
	out := lang.DepsResult{Symbol: in.Symbol}
	if in.Symbol == "" {
		return out, fmt.Errorf("symbol must not be empty")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return out, fmt.Errorf("getwd: %w", err)
	}
	ws, err := workspace.Discover(cwd)
	if err != nil {
		return out, fmt.Errorf("discover workspace: %w", err)
	}

	fset := token.NewFileSet()
	var patterns []string
	if in.Package != "" {
		patterns = []string{in.Package}
	}
	pkgs, err := ws.Load(ctx, depsLoadMode, patterns, workspace.WithFset(fset), workspace.WithTests())
	if err != nil {
		return out, fmt.Errorf("load packages: %w", err)
	}

	allPkgs := flattenWithDeps(pkgs)
	refs := findImplementations(in.Symbol, allPkgs, workspaceLocalRoots(ws), in.IncludeExternal)

	limit := in.Limit
	if limit <= 0 {
		limit = defaultDepsLimit
	}
	if len(refs) > limit {
		refs = refs[:limit]
		out.Truncated = true
	}
	refs = trimToDetail(refs, in.Detail)
	out.Results = refs
	out.NextActions = buildDepsNextActions(refs)
	return out, nil
}

// References is the lang.go.references tool. It returns every
// type-checked identifier reference to the target symbol in the
// workspace: reads, writes, type usages, declarations, and method
// calls through receivers — anything that resolves to the same
// types.Object.
//
// Prefer this over Grep for finding all uses of a Go identifier. Grep
// cannot disambiguate same-named identifiers in different scopes
// and misses method-on-receiver cases ("x.Foo()" where x's type has
// the target Foo). Use lang.go.callers instead when only call sites of
// a function matter — it returns the same identity check but filters
// to CallExpr nodes.
//
// By default the search is workspace-local: packages whose import path
// sits outside the workspace's own module roots are skipped before the
// finder runs. Without this default, a common identifier like "Kind"
// returns a flood of matches from protobuf, go/packages, and other
// transitive deps that share the name. Pass IncludeExternal=true when
// references in deps are genuinely what you want.
var References = tool.New[lang.ReferencesInput, lang.DepsResult](
	"lang.go.references",
	"PREFER OVER Grep for finding all uses of a Go identifier. Type-checked: returns every reference (reads, writes, type usages, declarations) including method-on-receiver cases that Grep can't disambiguate. Workspace-local by default — common names like 'Kind' or 'Name' no longer collide with protobuf/go-packages dep noise; pass include_external=true to include stdlib and dependency references. Use lang.go.callers instead when you specifically want call sites of a function.",
	func(ctx context.Context, in lang.ReferencesInput) (lang.DepsResult, error) {
		return runDepsQueryWithDetail(
			ctx,
			in.Symbol,
			in.Package,
			in.Limit,
			in.IncludeExternal,
			in.Detail,
			func(allPkgs map[string]*packages.Package, fset *token.FileSet) []lang.DepReference {
				return findReferences(in.Symbol, allPkgs)
			},
		)
	},
	tool.WithShortDescription("Find every type-checked reference to a Go identifier workspace-wide"),
)

// Invocations is the lang.go.invocations tool. It returns call sites
// where a value typed as the named function-type symbol is invoked,
// catching constructs like
//
//	h(data)
//
// where `h` is a parameter typed as `Handler`. There is NO generic
// alternative: Grep cannot distinguish this from any other identifier
// call, and gopls does not expose this query.
//
// Use this when refactoring function-typed parameters or callbacks —
// the shape that lang.go.callers does not match because the callee
// identifier resolves to a parameter, not the type declaration.
//
// By default the search is workspace-local: packages whose import path
// sits outside the workspace's own module roots are skipped. Pass
// IncludeExternal=true when the function type itself lives in an
// external package (e.g. http.HandlerFunc) and you want invocation
// sites from across the dep graph.
var Invocations = tool.New[lang.InvocationsInput, lang.DepsResult](
	"lang.go.invocations",
	"NO GENERIC ALTERNATIVE. Finds call sites that invoke a value typed as the given function-type symbol. Catches 'h(data)' inside a func that takes 'h Handler' — Grep can't distinguish this from any other identifier call, and gopls doesn't expose this query at all. Workspace-local by default; pass include_external=true when the function type lives in an external package (e.g. http.HandlerFunc). Use when refactoring function-typed parameters or callbacks.",
	func(ctx context.Context, in lang.InvocationsInput) (lang.DepsResult, error) {
		return runDepsQueryWithDetail(
			ctx,
			in.Symbol,
			in.Package,
			in.Limit,
			in.IncludeExternal,
			in.Detail,
			func(allPkgs map[string]*packages.Package, _ *token.FileSet) []lang.DepReference {
				return findInvocations(in.Symbol, allPkgs)
			},
		)
	},
	tool.WithShortDescription("Find call sites that invoke a value typed as a given Go function type"),
)

// depsLoadMode is the package-load mode shared by all four deps
// queries. NeedSyntax/NeedTypes/NeedTypesInfo together provide enough
// for AST walking AND type-checked identity matching; NeedDeps loads
// the full import graph so workspace-external implementations can be
// discovered when requested; NeedCompiledGoFiles maps Syntax entries
// to on-disk paths for accurate Location strings.
const depsLoadMode = packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo |
	packages.NeedImports | packages.NeedName | packages.NeedDeps |
	packages.NeedFiles | packages.NeedCompiledGoFiles

// workspaceLocalRoots returns the import-path roots of every module in
// the workspace. A *packages.Package whose PkgPath equals one of these
// — or is a sub-import-path of one (root + "/") — is workspace-local;
// anything else is a transitive dep (stdlib or external module).
//
// Using module roots — rather than the set of directly-loaded
// packages — makes the predicate stable regardless of whether the
// caller passed a narrow Package pattern: a callers query rooted at one
// package still reports cross-package workspace callers and excludes
// only deps/stdlib.
func workspaceLocalRoots(ws *workspace.Workspace) []string {
	mods := ws.Modules()
	roots := make([]string, 0, len(mods))
	for _, m := range mods {
		if m.Path != "" {
			roots = append(roots, m.Path)
		}
	}
	return roots
}

// isWorkspaceLocalPkg reports whether pkgPath sits inside one of the
// workspace module roots produced by workspaceLocalRoots.
func isWorkspaceLocalPkg(pkgPath string, roots []string) bool {
	for _, root := range roots {
		if pkgPath == root || strings.HasPrefix(pkgPath, root+"/") {
			return true
		}
	}
	return false
}

// defaultDepsLimit caps deps-query results when the caller does not
// specify Limit. Chosen to be small enough that an agent can read every
// result inline (50 is roughly the cap on what fits in one screen) but
// large enough to avoid silent over-truncation for tightly-coupled
// symbols.
const defaultDepsLimit = 50

// trimToDetail strips fields from each DepReference based on the
// requested Detail level so the agent receives the smallest payload
// that answers its question.
//
//   - lang.DetailSummary: keeps only identity fields (Symbol, Package,
//     Location, Kind). Use when the question is "how many places".
//   - lang.DetailFull: no-op; the engine already filled every field.
//   - default / standard: drops CallerDocblock, Context, and
//     SymbolSource — the heavy forensics fields — keeping the
//     one-line CallSnippet and CallerSymbol that suffice for most
//     "who calls me" questions.
//
// The transform is in-place — same slice header, fewer populated
// fields.
func trimToDetail(refs []lang.DepReference, detail string) []lang.DepReference {
	switch detail {
	case lang.DetailSummary:
		for i := range refs {
			refs[i].CallSnippet = ""
			refs[i].CallerSymbol = ""
			refs[i].CallerDocblock = ""
			refs[i].Context = ""
			refs[i].SymbolSource = ""
		}
	case lang.DetailFull:
		// no-op: keep all fields
	default: // "" or "standard"
		for i := range refs {
			refs[i].CallerDocblock = ""
			refs[i].Context = ""
			refs[i].SymbolSource = ""
		}
	}
	return refs
}

// runDepsQueryWithDetail dispatches a deps query through runDepsQuery
// and then post-filters the result fields to the requested Detail
// level. Exists because the Detail concept is orthogonal to the
// finder closure passed by each tool — it would be wasteful to
// thread Detail into every find function only to apply the same trim
// on return.
func runDepsQueryWithDetail(
	ctx context.Context,
	symbol, packagePat string,
	limit int,
	includeExternal bool,
	detail string,
	find func(allPkgs map[string]*packages.Package, fset *token.FileSet) []lang.DepReference,
) (lang.DepsResult, error) {
	out, err := runDepsQuery(ctx, symbol, packagePat, limit, includeExternal, find)
	if err != nil {
		return out, err
	}
	out.Results = trimToDetail(out.Results, detail)
	return out, nil
}

// runDepsQuery is the shared dispatcher behind the callers, references,
// and invocations tools (implementations has its own variant because
// it also needs the localRoots set). It discovers the workspace, loads
// packages once with depsLoadMode, flattens the import graph into a
// single (path -> *Package) map, optionally trims that map to
// workspace-local packages, and invokes the supplied finder.
//
// The workspace is always loaded — the Package input is treated as a
// symbol-resolution disambiguator ("the X defined in this package, not
// the X defined elsewhere"), not a search-space limiter. Call sites of
// a modA symbol may legitimately live in modB; pre-filtering by package
// would hide them.
//
// When includeExternal is false (the default), packages outside the
// workspace's own module roots are dropped before the finder sees them.
// This is what stops a `references` query for a common identifier like
// "Kind" from drowning in matches from protobuf, go/packages, etc.
// Callers that genuinely want deps in the result set — e.g. tracing
// invocations of an `http.HandlerFunc`-typed value — pass
// includeExternal=true to keep the full import graph in play.
//
// In go.work mode a second load is performed without patterns so every
// use directive is expanded — single-package loads in go.work mode
// return only that one module's packages, missing cross-module callers.
func runDepsQuery(
	ctx context.Context,
	symbol, packagePat string,
	limit int,
	includeExternal bool,
	find func(allPkgs map[string]*packages.Package, fset *token.FileSet) []lang.DepReference,
) (lang.DepsResult, error) {
	out := lang.DepsResult{Symbol: symbol}
	if symbol == "" {
		return out, fmt.Errorf("symbol must not be empty")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return out, fmt.Errorf("getwd: %w", err)
	}
	ws, err := workspace.Discover(cwd)
	if err != nil {
		return out, fmt.Errorf("discover workspace: %w", err)
	}

	fset := token.NewFileSet()
	// Always load the full workspace. The Package input is a symbol-
	// resolution disambiguator, not a search-space limiter — call sites
	// of a modA symbol can live in modB, and pre-filtering by package
	// would hide them. Loading the workspace defaults expands `./...`
	// across every use directive in go.work mode.
	var patterns []string
	if packagePat != "" {
		patterns = []string{packagePat}
	}
	pkgs, err := ws.Load(ctx, depsLoadMode, patterns, workspace.WithFset(fset), workspace.WithTests())
	if err != nil {
		return out, fmt.Errorf("load packages: %w", err)
	}
	if packagePat != "" && ws.IsGoWork() {
		extra, err := ws.Load(ctx, depsLoadMode, nil, workspace.WithFset(fset), workspace.WithTests())
		if err == nil {
			pkgs = append(pkgs, extra...)
		}
	}

	allPkgs := flattenWithDeps(pkgs)
	if !includeExternal {
		roots := workspaceLocalRoots(ws)
		filtered := make(map[string]*packages.Package, len(allPkgs))
		for path, p := range allPkgs {
			if isWorkspaceLocalPkg(path, roots) {
				filtered[path] = p
			}
		}
		allPkgs = filtered
	}
	refs := find(allPkgs, fset)

	if limit <= 0 {
		limit = defaultDepsLimit
	}
	if len(refs) > limit {
		refs = refs[:limit]
		out.Truncated = true
	}
	out.Results = refs
	out.NextActions = buildDepsNextActions(refs)
	return out, nil
}

// buildDepsNextActions suggests a follow-up explore on the most
// interesting hit in the results. The top-ranked caller's package and
// symbol are pre-filled into a lang.ExploreInput so the agent's next
// turn can be a one-shot drilldown rather than building input fields
// from scratch.
func buildDepsNextActions(refs []lang.DepReference) []lang.NextAction {
	if len(refs) == 0 {
		return nil
	}
	top := refs[0]
	if top.Package == "" {
		return nil
	}
	target := top.CallerSymbol
	if target == "" {
		target = top.Symbol
	}
	return []lang.NextAction{{
		Tool:       "lang.go.explore",
		Reason:     fmt.Sprintf("Explore top result %q in package %s", target, top.Package),
		Confidence: lang.ConfidenceHigh,
		Input: lang.ExploreInput{
			Package: top.Package,
			Symbols: []string{target},
			Mode:    lang.ModeSkeleton,
		},
	}}
}

// findImplementations locates types that implement the named interface
// symbol by walking every loaded package's type scope.
//
// The algorithm has two phases:
//  1. Find the target interface by name in some loaded package's
//     type scope. The first match wins; if no interface is found,
//     return nil.
//  2. For every named type in every package, check whether either
//     the type's value form OR its pointer form implements the
//     target. The pointer-form check matters because methods declared
//     on a pointer receiver are only in the method set of *T, not T.
//
// localRoots is the slice of workspace module import-path roots
// produced by workspaceLocalRoots. When includeExternal is false,
// packages whose PkgPath is not inside one of those roots are skipped
// — except the interface's own package, which is always reported so
// that a workspace-defined interface still surfaces implementations
// declared in a stdlib/external package it's used with.
func findImplementations(
	symbol string,
	allPkgs map[string]*packages.Package,
	localRoots []string,
	includeExternal bool,
) []lang.DepReference {
	// First find the interface type by name.
	var targetIface *types.Interface
	var targetPkg string

	for _, p := range allPkgs {
		if p.Types == nil {
			continue
		}
		obj := p.Types.Scope().Lookup(symbol)
		if obj == nil {
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		iface, ok := named.Underlying().(*types.Interface)
		if !ok {
			continue
		}
		targetIface = iface
		targetPkg = p.PkgPath
		break
	}

	if targetIface == nil {
		return nil
	}

	var refs []lang.DepReference

	for _, p := range allPkgs {
		// Skip non-local packages unless requested. Workspace-local roots
		// always pass; the interface's own package always passes.
		if !includeExternal && p.PkgPath != targetPkg && !isWorkspaceLocalPkg(p.PkgPath, localRoots) {
			continue
		}
		if p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if obj == nil {
				continue
			}
			named, ok := obj.Type().(*types.Named)
			if !ok {
				continue
			}
			// Check pointer and value receiver.
			if !types.Implements(named, targetIface) && !types.Implements(types.NewPointer(named), targetIface) {
				continue
			}
			// Don't report the interface itself.
			if _, isIface := named.Underlying().(*types.Interface); isIface {
				continue
			}

			pos := obj.Pos()
			location := ""
			if p.Fset != nil && pos.IsValid() {
				position := p.Fset.Position(pos)
				location = fmt.Sprintf("%s:%d", position.Filename, position.Line)
			}

			// Get doc comment from AST.
			docblock := extractTypeDoc(name, p)

			refs = append(refs, lang.DepReference{
				Symbol:   name,
				Package:  p.PkgPath,
				Location: location,
				Kind:     lang.RelImplementor,
				Context:  docblock,
			})
		}
	}

	return refs
}

// findCallers walks every loaded AST and records each CallExpr whose
// callee identifier matches symbol. It builds a DepReference with the
// call site Location, a snippet of the call line plus two context
// lines, the enclosing function/method name (so the agent knows WHO
// is calling), the caller's docblock when present, and a one-line
// textual context derived from extractCallContext.
//
// Matching is name-based on the resolved callee identifier or selector
// (extractCallName). This is intentionally conservative — same-named
// identifiers in unrelated scopes will all match, but the cost of
// false positives here is small because the caller's location and
// symbol typically make the disambiguation obvious to the agent.
func findCallers(symbol string, allPkgs map[string]*packages.Package) []lang.DepReference {
	var refs []lang.DepReference

	for _, p := range allPkgs {
		if p.Fset == nil {
			continue
		}
		for i, file := range p.Syntax {
			filePath := ""
			if i < len(p.CompiledGoFiles) {
				filePath = p.CompiledGoFiles[i]
			}

			src, _ := os.ReadFile(filePath)

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				// Extract the called name.
				calledName := extractCallName(call.Fun)
				if calledName != symbol {
					return true
				}

				pos := p.Fset.Position(call.Pos())
				location := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)

				callSnippet := lang.ExtractLinesFromBytes(src, pos.Line, 2)
				callerSymbol := findEnclosingFunc(file, call.Pos())
				callerDocblock := extractCallerDocblock(file, call.Pos())
				context := extractCallContext(file, call, p.Fset, callerSymbol, src)

				refs = append(refs, lang.DepReference{
					Symbol:         symbol,
					Package:        p.PkgPath,
					Location:       location,
					Kind:           lang.RelDirectCaller,
					CallSnippet:    strings.TrimSpace(callSnippet),
					CallerSymbol:   callerSymbol,
					CallerDocblock: callerDocblock,
					Context:        context,
				})
				return true
			})
		}
	}

	return refs
}

// findReferences walks every loaded AST and records each ast.Ident
// whose Name matches symbol. Captures more than findCallers: type
// usages, struct field references, variable assignments, parameter
// types — anything that resolves to the same identifier name. The
// result kind is lang.RelTypeUser to distinguish from RelDirectCaller.
//
// The broad match is intentional: agents looking for "all uses" expect
// to see declarations and references in equal measure. When only call
// sites matter, callers is the right tool.
func findReferences(symbol string, allPkgs map[string]*packages.Package) []lang.DepReference {
	var refs []lang.DepReference

	for _, p := range allPkgs {
		if p.Fset == nil {
			continue
		}
		for i, file := range p.Syntax {
			filePath := ""
			if i < len(p.CompiledGoFiles) {
				filePath = p.CompiledGoFiles[i]
			}

			src, _ := os.ReadFile(filePath)

			ast.Inspect(file, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				if ident.Name != symbol {
					return true
				}

				pos := p.Fset.Position(ident.Pos())
				location := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
				callSnippet := extractLineFromBytes(src, pos.Line)
				callerSymbol := findEnclosingFunc(file, ident.Pos())
				callerDocblock := extractCallerDocblock(file, ident.Pos())
				context := extractCallContext(file, ident, p.Fset, callerSymbol, src)

				refs = append(refs, lang.DepReference{
					Symbol:         symbol,
					Package:        p.PkgPath,
					Location:       location,
					Kind:           lang.RelTypeUser,
					CallSnippet:    strings.TrimSpace(callSnippet),
					CallerSymbol:   callerSymbol,
					CallerDocblock: callerDocblock,
					Context:        context,
				})
				return true
			})
		}
	}

	return refs
}

// findInvocations walks every loaded AST and records CallExpr nodes
// whose callee EXPRESSION has the type identity of the named
// function-type symbol. This finds sites like edge(snapshot) where
// edge is a parameter typed as EdgeFunc — distinct from findCallers
// (which matches function names) and findReferences (which matches
// all identifier uses).
//
// The matching is two-tier: types.Identical first (exact identity),
// then a name-equality fallback on the underlying named type so the
// same source loaded into two different *packages.Package entries
// (test-variant vs regular) still produces matches. Without the
// fallback, cross-variant invocations would be silently missed.
func findInvocations(symbol string, allPkgs map[string]*packages.Package) []lang.DepReference {
	// First locate the target types.Object for the symbol so we can match by type identity.
	var targetType types.Type
	for _, p := range allPkgs {
		if p.Types == nil {
			continue
		}
		obj := p.Types.Scope().Lookup(symbol)
		if obj == nil {
			continue
		}
		targetType = obj.Type()
		break
	}
	if targetType == nil {
		return nil
	}

	var refs []lang.DepReference

	for _, p := range allPkgs {
		if p.TypesInfo == nil || p.Fset == nil {
			continue
		}
		for i, file := range p.Syntax {
			filePath := ""
			if i < len(p.CompiledGoFiles) {
				filePath = p.CompiledGoFiles[i]
			}

			src, _ := os.ReadFile(filePath)

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				// Get the type of the callee expression.
				calleeType := p.TypesInfo.TypeOf(call.Fun)
				if calleeType == nil {
					return true
				}

				// Match by types.Identical or by named type name to handle cross-package fset variants.
				matched := types.Identical(calleeType, targetType)
				if !matched {
					// Fall back: compare underlying named type name — handles the case where
					// the same source is loaded into two different *packages.Package entries.
					if named, ok2 := calleeType.(*types.Named); ok2 {
						matched = named.Obj().Name() == symbol
					}
				}
				if !matched {
					return true
				}

				pos := p.Fset.Position(call.Pos())
				location := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
				callSnippet := lang.ExtractLinesFromBytes(src, pos.Line, 2)
				callerSymbol := findEnclosingFunc(file, call.Pos())
				callerDocblock := extractCallerDocblock(file, call.Pos())
				context := extractCallContext(file, call, p.Fset, callerSymbol, src)

				refs = append(refs, lang.DepReference{
					Symbol:         symbol,
					Package:        p.PkgPath,
					Location:       location,
					Kind:           "invocation",
					CallSnippet:    strings.TrimSpace(callSnippet),
					CallerSymbol:   callerSymbol,
					CallerDocblock: callerDocblock,
					Context:        context,
				})
				return true
			})
		}
	}

	return refs
}

// extractCallName attempts to get the simple name from a call
// expression's Fun node. Returns the identifier name for direct calls
// (Foo()), the selector's right-hand name for method/qualified calls
// (obj.Foo() or pkg.Foo()), or the empty string for other shapes
// (generic instantiations, anonymous func literals). The conservative
// empty return means the caller drops the hit, which is preferable to
// emitting a misleading name.
func extractCallName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// findEnclosingFunc finds the name of the function/method that contains
// pos. Methods are formatted as "(Recv).Method" so the agent can see
// both the function name and its receiver type at a glance — critical
// when the same method name appears on multiple receivers. Returns the
// empty string when pos lies outside every top-level FuncDecl (a file-
// level const block, an init function, etc.).
func findEnclosingFunc(file *ast.File, pos token.Pos) string {
	fn := findEnclosingFuncDecl(file, pos)
	if fn == nil {
		return ""
	}
	name := fn.Name.Name
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recv := formatExpr(fn.Recv.List[0].Type)
		return fmt.Sprintf("(%s).%s", recv, name)
	}
	return name
}

// findEnclosingFuncDecl returns the *ast.FuncDecl whose source range
// contains pos, or nil if no top-level function declaration matches.
// Used by findEnclosingFunc and extractCallerDocblock to attribute call
// sites to their owning function. Skips body-less function declarations
// (interface methods, assembly stubs) since they cannot enclose a
// statement.
func findEnclosingFuncDecl(file *ast.File, pos token.Pos) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Body == nil {
			continue
		}
		if pos >= fn.Pos() && pos <= fn.End() {
			return fn
		}
	}
	return nil
}

// extractCallerDocblock returns the doc comment of the function
// containing pos. Used to enrich call-site results with the caller's
// intent in the standard and full Detail levels — a docblock can
// signal "this is the retry path" or "called from the hot loop" in
// ways the raw call snippet cannot. Returns the empty string when no
// docblock is present so the caller can elide the field cleanly.
func extractCallerDocblock(file *ast.File, pos token.Pos) string {
	fn := findEnclosingFuncDecl(file, pos)
	if fn == nil || fn.Doc == nil {
		return ""
	}
	return strings.TrimSpace(fn.Doc.Text())
}

// extractCallContext infers a one-line contextual reason for a call
// site by checking three sources in priority order:
//  1. A comment on the same or previous line — "// retry once" is the
//     most precise signal an author can leave.
//  2. The enclosing function's docblock — less precise but always
//     present when a function is documented.
//  3. The source line itself, prefixed with "called at: " — a
//     fallback that at least shows the agent the surrounding
//     expression.
//
// Returns the empty string only when all three sources are missing,
// which is rare in well-documented code.
func extractCallContext(
	file *ast.File,
	node ast.Node,
	fset *token.FileSet,
	callerSymbol string,
	src []byte,
) string {
	pos := fset.Position(node.Pos())

	// 1. Check for a comment on the same or previous line.
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			cPos := fset.Position(c.Pos())
			if cPos.Line == pos.Line || cPos.Line == pos.Line-1 {
				text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), "/*"))
				text = strings.TrimSuffix(text, "*/")
				text = strings.TrimSpace(text)
				if text != "" {
					return text
				}
			}
		}
	}

	// 2. Use enclosing function's doc comment.
	if callerSymbol != "" {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Doc != nil && strings.HasSuffix(callerSymbol, fn.Name.Name) {
				return strings.TrimSpace(fn.Doc.Text())
			}
		}
	}

	// 3. Infer from surrounding code: look at the line itself.
	line := strings.TrimSpace(extractLineFromBytes(src, pos.Line))
	if line != "" {
		return "called at: " + line
	}

	return ""
}

// extractLineFromBytes returns the content of the given 1-based line
// number from src, or the empty string for an out-of-range request.
// The split is performed on a string conversion because callers typically
// want a string back and converting once is cheaper than scanning bytes
// repeatedly across multiple calls (the result is short-lived).
func extractLineFromBytes(src []byte, lineNum int) string {
	if len(src) == 0 || lineNum <= 0 {
		return ""
	}
	lines := strings.Split(string(src), "\n")
	if lineNum > len(lines) {
		return ""
	}
	return lines[lineNum-1]
}

// extractTypeDoc looks up the doc comment for a named type in the
// package's AST. Falls back to the enclosing GenDecl's doc when the
// TypeSpec itself has none, matching the godoc convention for grouped
// type declarations. Returns the empty string when the named type is
// not found or has no doc.
func extractTypeDoc(name string, p *packages.Package) string {
	for _, file := range p.Syntax {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if ts.Name.Name != name {
					continue
				}
				if ts.Doc != nil {
					return strings.TrimSpace(ts.Doc.Text())
				}
				if gd.Doc != nil {
					return strings.TrimSpace(gd.Doc.Text())
				}
			}
		}
	}
	return ""
}
