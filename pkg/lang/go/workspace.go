// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/internal/workspace"
)

// Workspace is the lang.go.workspace tool — the canonical first call
// an agent should make when encountering a Go project. It discovers
// the enclosing Go boundary (a go.work file in multi-module mode,
// falling back to the nearest go.mod), enumerates every package
// underneath it, and returns a single flat map keyed by import path
// with just enough metadata to plan the next drilling step.
//
// Reach for this tool instead of a shell `ls internal/` followed by
// ten parallel [Explore] calls. One Workspace call returns the same
// information — package names, godoc summaries, exported-symbol
// counts — at a fraction of the round-trip cost. The natural
// follow-up is a targeted [Explore] on a single package whose godoc
// or symbol count suggests it is the next thing to read.
//
// What's excluded: vendored dependencies (anywhere under a `vendor/`
// directory) and, by default, the synthetic test-only packages that
// golang.org/x/tools/go/packages emits as `[foo/bar.test]` and
// `foo/bar_test [foo/bar.test]` variants. Set IncludeTests on the
// request to surface them. The synthetic packages are always
// collapsed into their production sibling by PkgPath so the map
// never carries duplicate entries for the same import path.
//
// Three detail modes balance verbosity against token cost. Summary
// mode emits package counts only — the right setting for workspaces
// with hundreds of packages. Standard mode (the default) adds the
// first paragraph of each package's godoc and the in-source order
// of exported symbols, which is the sweet spot for the typical
// 10–50-package codebase. Full mode additionally renders one
// human-readable line per exported symbol (signature plus the first
// sentence of its doc comment), letting the agent decide which
// symbols to explore without a follow-up round trip; the per-symbol
// cost is comparable to running [Explore] in [lang.ModeDocs] on
// every package at once, so reserve it for small or unfamiliar
// workspaces.
var Workspace = tool.New[lang.WorkspaceInput, lang.WorkspaceOutput](
	"lang.go.workspace",
	"PREFER OVER `ls` / multiple `lang.go.explore` calls when encountering a Go project for the first time. Returns every package in the workspace with its name, import path, godoc summary, and exported-symbol count in a single call — the right starting point before drilling into a specific package with explore/search.",
	workspaceHandler,
	tool.Enum("detail", lang.DetailSummary, lang.DetailStandard, lang.DetailFull),
	tool.WithShortDescription("Map every Go package in the workspace with godoc summaries and symbol counts"),
)

// workspaceHandler implements the lang.go.workspace RPC. It anchors
// at the requested directory (or cwd when unspecified), discovers
// the enclosing Go boundary via the workspace package, loads every
// package under it through Workspace.Load, and walks each package's
// syntax to build per-package metadata.
//
// The handler filters vendored deps by directory prefix, collapses
// synthetic test variants by PkgPath, and respects IncludeTests
// to decide whether to load *_test.go files at all. Detail-mode
// projections are applied after counts so the same package walk
// feeds every output level.
func workspaceHandler(ctx context.Context, input lang.WorkspaceInput) (lang.WorkspaceOutput, error) {
	anchor := input.Package
	if anchor == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return lang.WorkspaceOutput{}, fmt.Errorf("getwd: %w", err)
		}
		anchor = cwd
	}

	ws, err := workspace.Discover(anchor)
	if err != nil {
		return lang.WorkspaceOutput{}, fmt.Errorf("discover workspace: %w", err)
	}

	detail := input.Detail
	if detail == "" {
		detail = lang.DetailStandard
	}

	var loadOpts []workspace.LoadOption
	fset := token.NewFileSet()
	loadOpts = append(loadOpts, workspace.WithFset(fset))
	if input.IncludeTests {
		loadOpts = append(loadOpts, workspace.WithTests())
	}

	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
		packages.NeedSyntax | packages.NeedModule
	pkgs, err := ws.Load(ctx, mode, nil, loadOpts...)
	if err != nil {
		return lang.WorkspaceOutput{}, fmt.Errorf("load packages: %w", err)
	}

	root := ws.Root()
	out := lang.WorkspaceOutput{
		Root:     root,
		IsGoWork: ws.IsGoWork(),
		Packages: map[string]lang.WorkspacePackage{},
	}
	for _, m := range ws.Modules() {
		out.Modules = append(out.Modules, lang.WorkspaceModule{Path: m.Path, Dir: relToRoot(m.Dir, root)})
	}
	sort.Slice(out.Modules, func(i, j int) bool {
		return out.Modules[i].Path < out.Modules[j].Path
	})

	// Sort packages by PkgPath so the regular variant of a test-augmented
	// package is processed before the [pkg.test] synthetic — first-write-
	// wins dedup then prefers the production entry.
	sort.Slice(pkgs, func(i, j int) bool {
		return pkgs[i].PkgPath < pkgs[j].PkgPath
	})

	for _, pkg := range pkgs {
		if pkg == nil {
			continue
		}
		cleanPath := cleanTestVariant(pkg.PkgPath)
		if cleanPath == "" {
			continue
		}
		// Skip vendored packages: anything under a vendor/ directory.
		if isVendored(pkg) {
			continue
		}
		// Skip the magic `command-line-arguments` and similar non-import-path
		// synthetic packages.
		if cleanPath == "command-line-arguments" {
			continue
		}

		entry, ok := out.Packages[cleanPath]
		if !ok {
			entry = lang.WorkspacePackage{
				ImportPath: cleanPath,
				Name:       pkg.Name,
				Dir:        relToRoot(packageDir(pkg), root),
			}
		}

		// Merge symbols from this packages.Package's syntax into entry.
		mergePackage(&entry, pkg, input, detail)
		out.Packages[cleanPath] = entry
	}

	return out, nil
}

// cleanTestVariant strips the test-variant suffix `[foo/bar.test]` from
// a packages.Package.PkgPath. Returns the empty string for synthetic
// packages that have no underlying import path (rare; defensive).
func cleanTestVariant(pkgPath string) string {
	if before, _, ok := strings.Cut(pkgPath, " ["); ok {
		return before
	}
	return pkgPath
}

// isVendored reports whether any file in the package lives under a
// `vendor/` directory. Uses CompiledGoFiles (or GoFiles as a fallback)
// because Module may be nil for non-module-aware loads.
func isVendored(pkg *packages.Package) bool {
	files := pkg.CompiledGoFiles
	if len(files) == 0 {
		files = pkg.GoFiles
	}
	for _, f := range files {
		// Normalise separators so the check works on Windows too.
		norm := filepath.ToSlash(f)
		if strings.Contains(norm, "/vendor/") {
			return true
		}
	}
	return false
}

// packageDir returns the absolute filesystem directory of a package by
// inspecting the first compiled Go file. Falls back to GoFiles when
// CompiledGoFiles is empty (rare for our load modes).
func packageDir(pkg *packages.Package) string {
	files := pkg.CompiledGoFiles
	if len(files) == 0 {
		files = pkg.GoFiles
	}
	if len(files) == 0 {
		return ""
	}
	return filepath.Dir(files[0])
}

// mergePackage walks pkg.Syntax and accumulates symbol counts, source-
// order names, and (in full detail) per-symbol summary lines into
// entry. It is called once per packages.Package — for test-augmented
// packages that means twice (production then synthetic), with the
// second call simply adding any test-only symbols since the order is
// already established.
//
// Counts always reflect the active filters: IncludeTests gates whether
// *_test.go files contribute, IncludePrivate gates whether unexported
// names contribute to InternalSyms (exported never feeds InternalSyms).
func mergePackage(
	entry *lang.WorkspacePackage,
	pkg *packages.Package,
	input lang.WorkspaceInput,
	detail string,
) {
	// Pair each AST file with its compiled-file path so we can filter
	// *_test.go files when IncludeTests is false.
	type fileEntry struct {
		path string
		file *ast.File
	}
	var entries []fileEntry
	for i, f := range pkg.Syntax {
		if f == nil {
			continue
		}
		path := ""
		if i < len(pkg.CompiledGoFiles) {
			path = pkg.CompiledGoFiles[i]
		} else if pkg.Fset != nil {
			path = pkg.Fset.Position(f.Pos()).Filename
		}
		if !input.IncludeTests && strings.HasSuffix(path, "_test.go") {
			continue
		}
		entries = append(entries, fileEntry{path: path, file: f})
	}
	// Stable file order so symbol_order is reproducible.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].path < entries[j].path
	})

	seen := make(map[string]bool, len(entry.SymbolOrder))
	for _, name := range entry.SymbolOrder {
		seen[name] = true
	}

	for _, fe := range entries {
		// Capture the package doc from the first file declaring one.
		if entry.PackageDoc == "" && fe.file.Doc != nil {
			entry.PackageDoc = firstParagraph(strings.TrimSpace(fe.file.Doc.Text()))
		}

		for _, decl := range fe.file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name := workspaceFuncName(d)
				if name == "" {
					continue
				}
				exported := workspaceFuncExported(d)
				if exported {
					entry.ExportedSyms++
					if !seen[name] {
						entry.SymbolOrder = append(entry.SymbolOrder, name)
						seen[name] = true
					}
					if detail == lang.DetailFull {
						if line := funcSummaryLine(d); line != "" {
							entry.SymbolLines = append(entry.SymbolLines, line)
						}
					}
				} else if input.IncludePrivate {
					entry.InternalSyms++
				}

			case *ast.GenDecl:
				mergeGenDecl(entry, d, input, detail, seen)
			}
		}
	}

	// Strip per-detail-mode fields the caller didn't ask for. We populate
	// SymbolOrder and SymbolLines during the walk and then trim down here,
	// so the walk logic stays a single pass.
	switch detail {
	case lang.DetailSummary:
		entry.PackageDoc = ""
		entry.SymbolOrder = nil
		entry.SymbolLines = nil
	case lang.DetailStandard:
		entry.SymbolLines = nil
	}
}

// mergeGenDecl folds one ast.GenDecl into entry: every type, const, or
// var spec contributes a symbol. The doc comment used for SymbolLines
// is the spec's own when present, otherwise the parent GenDecl's
// (godoc convention).
func mergeGenDecl(
	entry *lang.WorkspacePackage,
	d *ast.GenDecl,
	input lang.WorkspaceInput,
	detail string,
	seen map[string]bool,
) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			name := s.Name.Name
			if ast.IsExported(name) {
				entry.ExportedSyms++
				if !seen[name] {
					entry.SymbolOrder = append(entry.SymbolOrder, name)
					seen[name] = true
				}
				if detail == lang.DetailFull {
					if line := typeSummaryLine(s, d); line != "" {
						entry.SymbolLines = append(entry.SymbolLines, line)
					}
				}
			} else if input.IncludePrivate {
				entry.InternalSyms++
			}
		case *ast.ValueSpec:
			kind := lang.KindConst
			if d.Tok == token.VAR {
				kind = lang.KindVar
			}
			for _, ident := range s.Names {
				name := ident.Name
				if ast.IsExported(name) {
					entry.ExportedSyms++
					if !seen[name] {
						entry.SymbolOrder = append(entry.SymbolOrder, name)
						seen[name] = true
					}
					if detail == lang.DetailFull {
						if line := valueSummaryLine(name, kind, s, d); line != "" {
							entry.SymbolLines = append(entry.SymbolLines, line)
						}
					}
				} else if input.IncludePrivate {
					entry.InternalSyms++
				}
			}
		}
	}
}

// workspaceFuncName returns the map key for a function or method
// declaration: `FuncName` for top-level functions,
// `Receiver.MethodName` for methods. Returns the empty string when the
// declaration has no name.
func workspaceFuncName(fn *ast.FuncDecl) string {
	if fn.Name == nil {
		return ""
	}
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recv := strings.TrimLeft(workspaceExprString(fn.Recv.List[0].Type), "*")
		// Strip type parameters from generic receivers: Foo[T] -> Foo.
		if i := strings.Index(recv, "["); i != -1 {
			recv = recv[:i]
		}
		return recv + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// workspaceFuncExported reports whether a function or method should
// count as part of a package's exported surface. A top-level function
// is exported iff its name is exported. A method is exported iff BOTH
// the method name AND its receiver type are exported — a method on an
// unexported type is unreachable from outside the package regardless
// of its own name, so reporting it inflates the exported count.
func workspaceFuncExported(fn *ast.FuncDecl) bool {
	if fn.Name == nil || !ast.IsExported(fn.Name.Name) {
		return false
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return true
	}
	recv := strings.TrimLeft(workspaceExprString(fn.Recv.List[0].Type), "*")
	if i := strings.Index(recv, "["); i != -1 {
		recv = recv[:i]
	}
	return recv != "" && ast.IsExported(recv)
}

// funcSummaryLine renders a one-line summary for a func or method:
// `Signature — first doc sentence`. Returns the empty string when the
// declaration has no doc comment (the handler never fabricates).
func funcSummaryLine(fn *ast.FuncDecl) string {
	if fn.Doc == nil {
		return ""
	}
	sentence := firstSentence(strings.TrimSpace(fn.Doc.Text()))
	if sentence == "" {
		return ""
	}
	sig := renderFuncSignature(fn)
	return capLine(sig + " — " + sentence)
}

// renderFuncSignature builds a one-line function or method signature:
// `funcName(args) returns` for funcs, `(*Recv).Method(args) returns`
// for methods. Argument names are preserved so the reader sees how
// each parameter is used inside the function.
func renderFuncSignature(fn *ast.FuncDecl) string {
	var b strings.Builder
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		b.WriteString("(")
		b.WriteString(workspaceExprString(fn.Recv.List[0].Type))
		b.WriteString(").")
	}
	if fn.Name != nil {
		b.WriteString(fn.Name.Name)
	}
	b.WriteString(renderFieldList(fn.Type.Params, true))
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		b.WriteString(" ")
		b.WriteString(renderFieldList(fn.Type.Results, false))
	}
	return b.String()
}

// renderFieldList builds a parenthesised parameter or result list. When
// withNames is true (parameters), each field renders as `name type`;
// when false (results), bare types are emitted unless the result is
// named, mirroring godoc's signature style.
func renderFieldList(fl *ast.FieldList, withNames bool) string {
	if fl == nil {
		return "()"
	}
	parens := withNames || len(fl.List) != 1 || len(fl.List[0].Names) > 0
	var parts []string
	for _, f := range fl.List {
		typ := workspaceExprString(f.Type)
		if withNames && len(f.Names) > 0 {
			names := make([]string, 0, len(f.Names))
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
			parts = append(parts, strings.Join(names, ", ")+" "+typ)
		} else if !withNames && len(f.Names) > 0 {
			names := make([]string, 0, len(f.Names))
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
			parts = append(parts, strings.Join(names, ", ")+" "+typ)
		} else {
			parts = append(parts, typ)
		}
	}
	joined := strings.Join(parts, ", ")
	if !parens {
		return joined
	}
	return "(" + joined + ")"
}

// exprString renders an ast.Expr back to Go source via go/format. The
// printer needs a FileSet for position information; we synthesise one
// because the value is purely textual.
func workspaceExprString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var buf bytes.Buffer
	fset := token.NewFileSet()
	if err := format.Node(&buf, fset, expr); err != nil {
		return fmt.Sprintf("%T", expr)
	}
	return buf.String()
}

// typeSummaryLine renders a one-line summary for a type declaration:
// `Name kind — first doc sentence`. The kind word ('struct',
// 'interface', or 'type') reflects the underlying ast.TypeSpec.Type;
// aliases and named non-composite types render as 'type'.
func typeSummaryLine(s *ast.TypeSpec, parent *ast.GenDecl) string {
	doc := docFor(s.Doc, parent.Doc)
	if doc == "" {
		return ""
	}
	kind := "type"
	switch s.Type.(type) {
	case *ast.StructType:
		kind = "struct"
	case *ast.InterfaceType:
		kind = "interface"
	}
	return capLine(s.Name.Name + " " + kind + " — " + firstSentence(doc))
}

// valueSummaryLine renders a one-line summary for a const or var
// declaration. The type qualifier is included only when the value
// declares one explicitly — Go's inferred-type idiom is common enough
// that synthesising a type from RHS values would mislead more than it
// would inform.
func valueSummaryLine(name, kind string, s *ast.ValueSpec, parent *ast.GenDecl) string {
	doc := docFor(s.Doc, parent.Doc)
	if doc == "" {
		return ""
	}
	var prefix string
	if s.Type != nil {
		prefix = name + " " + kind + " " + workspaceExprString(s.Type)
	} else {
		prefix = name + " " + kind
	}
	return capLine(prefix + " — " + firstSentence(doc))
}

// docFor returns the trimmed text of the spec's own doc when present,
// otherwise the parent GenDecl's doc. Matches godoc's inheritance rule
// for grouped declarations.
func docFor(specDoc, parentDoc *ast.CommentGroup) string {
	if specDoc != nil {
		if t := strings.TrimSpace(specDoc.Text()); t != "" {
			return t
		}
	}
	if parentDoc != nil {
		return strings.TrimSpace(parentDoc.Text())
	}
	return ""
}

// firstParagraph returns the leading paragraph of a doc comment —
// everything up to the first blank line. Used for PackageDoc, where a
// single paragraph is the godoc-recommended summary and full docs are
// available via [Explore] when the agent wants the rest.
func firstParagraph(text string) string {
	if before, _, ok := strings.Cut(text, "\n\n"); ok {
		return strings.TrimSpace(before)
	}
	return text
}

// firstSentence returns the first sentence of a doc comment, defined
// as everything up to the first `.`, `?`, or `!` followed by a space
// or end-of-string. Falls back to the first line when no terminator is
// found, mirroring godoc's heuristic for synopsis extraction.
func firstSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Collapse internal newlines to spaces so a sentence that wraps
	// across lines renders cleanly.
	flat := strings.Join(strings.Fields(text), " ")
	for i := 0; i < len(flat); i++ {
		c := flat[i]
		if c != '.' && c != '?' && c != '!' {
			continue
		}
		if i+1 == len(flat) || flat[i+1] == ' ' {
			return flat[:i+1]
		}
	}
	return flat
}

// capLine truncates s to roughly maxSummaryLineLen characters with a
// trailing ellipsis. Used to keep SymbolLines visually consistent and
// to bound the token cost of [DetailFull] responses.
func capLine(s string) string {
	const maxSummaryLineLen = 120
	if len(s) <= maxSummaryLineLen {
		return s
	}
	return s[:maxSummaryLineLen-3] + "..."
}

// relToRoot returns dir expressed as a path relative to root. When dir
// equals root the empty string is returned (callers can interpret it
// as "the workspace root itself"). When dir is outside root or
// filepath.Rel errors out, the absolute path is returned verbatim as a
// safe fallback so the response is never silently wrong.
//
// Used by the workspace tool to keep response payloads compact — every
// package row repeating the absolute workspace root prefix wastes ~50
// bytes/row and obscures the structurally interesting suffix.
func relToRoot(dir, root string) string {
	if dir == "" || root == "" {
		return dir
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return dir
	}
	if rel == "." {
		return ""
	}
	return rel
}
