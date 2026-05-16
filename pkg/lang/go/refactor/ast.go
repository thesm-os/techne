// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// FindSymbolObject locates a types.Object by name across loaded packages,
// optionally narrowed by package, file, and line.
// Returns nil when no match is found.
//
// Resolution proceeds in three phases, stopping at the first hit:
//
//  1. Package-scope lookup. For "Type.Method", looks up Type in the package's
//     universe scope, then iterates the named type's methods. For "Type.Field"
//     on a struct, falls through to a struct-field scan — agents use the same
//     dotted form for renaming a field as for a method, so we accept either.
//     For a bare identifier, returns the first matching package-scope object.
//
//  2. file+line disambiguation. When both are non-zero, scans every package's
//     TypesInfo.Defs for an identifier whose name equals symbol and whose
//     source position matches the supplied (file, line). This is the only
//     reliable way to pick out a local variable from a function body;
//     pkg-scope lookup is useless inside a function.
//
//  3. Uniqueness fallback. Scans TypesInfo.Defs across all packages for any
//     identifier named symbol; if exactly one match exists, returns it.
//     Multiple matches return nil — without a file+line hint, the result would
//     be ambiguous.
//
// pkgFilter restricts every phase's package iteration to packages whose
// PkgPath equals the filter (matching the import path the caller passed
// through Input.Package). When non-empty it is a strict filter: the
// resolver never falls through to other packages. This is what stops a
// "Default" const lookup in package priority from silently picking up a
// "Default" function declared in directive/naming/builder when those
// packages happen to be loaded too. An empty filter preserves the
// original "search every loaded package" behavior, which is the right
// default when the caller genuinely wants the workspace-wide unique
// match (the agent didn't specify a package).
func FindSymbolObject(pkgs []*packages.Package, symbol, pkgFilter, file string, line int) types.Object {
	parts := strings.SplitN(symbol, ".", 2)
	typeName := ""
	funcName := parts[0]
	if len(parts) == 2 {
		typeName = parts[0]
		funcName = parts[1]
	}

	// matches accepts a package iff either the filter is the empty /
	// wildcard form (any package matches), or names this package
	// exactly via its import path, or is a filesystem path covering
	// this package's source directory. The directory branch lets
	// callers pass either a specific package dir ("./pkg/lang/go") OR
	// a workspace root ("/abs/module-root"), and have the latter still
	// scope the lookup to the project — the right semantic for legacy
	// callers that conflate Package with "the workspace I'm refactoring
	// inside".
	//
	// Workspace-wildcard forms (`""`, `"."`, `"./..."`, `"..."`) match
	// every package — they're how the Go toolchain spells "everything
	// in the workspace", and the resolver should treat them the same
	// as an unspecified filter rather than as strict path equality.
	isWildcard := pkgFilter == "" || pkgFilter == "." || pkgFilter == "..." || pkgFilter == "./..."
	matches := func(pkg *packages.Package) bool {
		if isWildcard {
			return true
		}
		if pkg.PkgPath == pkgFilter {
			return true
		}
		if len(pkg.GoFiles) == 0 {
			return false
		}
		pkgDir := filepath.Dir(pkg.GoFiles[0])
		filtAbs, err := filepath.Abs(pkgFilter)
		if err != nil {
			return false
		}
		return pkgDir == filtAbs || strings.HasPrefix(pkgDir, filtAbs+string(filepath.Separator))
	}

	// 1. Package-scope lookup first.
	for _, pkg := range pkgs {
		if !matches(pkg) {
			continue
		}
		if pkg.Types == nil || pkg.Types.Scope() == nil {
			continue
		}

		if typeName != "" {
			typeObj := pkg.Types.Scope().Lookup(typeName)
			if typeObj == nil {
				continue
			}

			named, ok := typeObj.Type().(*types.Named)
			if !ok {
				if ptr, ok2 := typeObj.Type().Underlying().(*types.Pointer); ok2 {
					named, ok = ptr.Elem().(*types.Named)
				}
			}
			if ok {
				for method := range named.Methods() {
					if method.Name() == funcName {
						return method
					}
				}
				// Field lookup: when the underlying type is a struct, allow
				// "Type.Field" syntax to resolve to the struct field. This
				// matches the syntax used for "Type.Method" and is what
				// agents reach for when renaming or restyping a field.
				if structType, ok := named.Underlying().(*types.Struct); ok {
					for f := range structType.Fields() {
						if f.Name() == funcName {
							return f
						}
					}
				}
			}
			continue
		}

		if obj := pkg.Types.Scope().Lookup(funcName); obj != nil {
			return obj
		}
	}

	// 2. If file and line are provided: scan TypesInfo.Defs for idents at that file:line.
	if file != "" && line > 0 {
		for _, pkg := range pkgs {
			if !matches(pkg) {
				continue
			}
			if pkg.TypesInfo == nil || pkg.Fset == nil {
				continue
			}
			for ident, obj := range pkg.TypesInfo.Defs {
				if obj == nil || ident.Name != symbol {
					continue
				}
				pos := pkg.Fset.Position(ident.Pos())
				if pos.Line == line && (file == "" || strings.HasSuffix(pos.Filename, file) || pos.Filename == file) {
					return obj
				}
			}
		}
	}

	// 3. Fallback to uniqueness check — safe when no file:line provided.
	var localObj types.Object
	var localMatches int
	for _, pkg := range pkgs {
		if !matches(pkg) {
			continue
		}
		if pkg.TypesInfo == nil {
			continue
		}
		for ident, obj := range pkg.TypesInfo.Defs {
			if obj != nil && ident.Name == symbol {
				localObj = obj
				localMatches++
			}
		}
	}

	if localMatches == 1 {
		return localObj
	}

	return nil
}

// ValidateGoSource reports whether src is non-empty and parseable Go for the
// purposes of a refactor commit. Used by [WorkspaceTransaction.AddChange] as a
// pre-flight check so the build gate is not invoked on obviously broken input.
//
// Validation is intentionally minimal: parser.AllErrors so syntax errors
// surface even past the first one, but no type-checking. The build gate is the
// source of truth for type errors; trying to mirror its rules here would
// duplicate them imperfectly and slow down every AddChange call.
func ValidateGoSource(filePath string, src []byte) error {
	if len(src) == 0 {
		return fmt.Errorf("file would be empty")
	}
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, filePath, src, parser.AllErrors)
	return err
}

// ResolveModuleRoot walks up the directory tree from startDir looking for the
// nearest go.mod, returning the directory that contains it. If no go.mod is
// reached (e.g., startDir is outside any module), startDir is returned
// unchanged so callers always get back a valid directory.
//
// Used by [NewTransaction] and several actions to anchor module-relative file
// paths. Symlinks are followed implicitly via os.Stat; callers that need
// symlink-resolved paths should filepath.EvalSymlinks first.
func ResolveModuleRoot(startDir string) string {
	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return startDir
		}
		dir = parent
	}
}

// ExprText returns the source text for an AST expression in the simplest
// possible way: when the expression's byte range is inside src, slice it;
// otherwise fall back to go/format.Node, which re-serializes from the AST.
//
// Why the dual strategy: slicing preserves the original formatting exactly
// (whitespace, comments inside parentheses, etc.), which matters when copying
// type expressions verbatim into a generated signature. The format.Node
// fallback handles synthetic nodes whose positions don't index into src —
// typically AST nodes constructed by an earlier pass of the same refactor.
//
// Returns the empty string when expr is nil.
func ExprText(fset *token.FileSet, src []byte, expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	start := fset.Position(expr.Pos()).Offset
	end := fset.Position(expr.End()).Offset
	if start < 0 || end > len(src) || start >= end {
		var buf bytes.Buffer
		_ = format.Node(&buf, fset, expr)
		return buf.String()
	}
	return string(src[start:end])
}

// ResolveWorkDir maps an input package designator to a working directory and a
// Go command package pattern.
//
// Accepts (in priority order):
//
//   - "" or "." — current working directory, pattern "./...".
//   - Absolute path to a directory — that directory, pattern "./...".
//   - Relative path starting with "./" or "../" — joined to CWD, pattern
//     "./..." when the result exists.
//   - Anything else — treated as a module-relative path: joined to the module
//     root (from [ResolveModuleRoot]) if that directory exists; otherwise falls
//     back to CWD with the input as a literal Go pattern (e.g.,
//     "github.com/foo/bar/...").
//
// The two-return form matches the call sites in [NewTransaction], which need
// both the resolved directory (for go.mod discovery) and a pattern (for tooling
// like packages.Load).
func ResolveWorkDir(pkg string) (dir, pattern string) {
	cwd, _ := os.Getwd()
	if pkg == "" || pkg == "." {
		return cwd, "./..."
	}

	// Handle absolute paths (e.g. temp dirs in tests).
	if filepath.IsAbs(pkg) {
		if info, err := os.Stat(pkg); err == nil && info.IsDir() {
			return pkg, "./..."
		}
		return cwd, "./..."
	}

	// Handle relative paths directly (e.g. "./pkg/fs").
	if strings.HasPrefix(pkg, "./") || strings.HasPrefix(pkg, "../") {
		abs := filepath.Join(cwd, pkg)
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return abs, "./..."
		}
		return cwd, pkg
	}

	// Treat as a module-relative path: resolve from module root.
	modRoot := ResolveModuleRoot(cwd)
	abs := filepath.Join(modRoot, pkg)
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return abs, "./..."
	}

	// Fall back to cwd with the original pattern.
	return cwd, "./" + pkg + "/..."
}
