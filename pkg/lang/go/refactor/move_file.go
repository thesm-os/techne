// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

// MoveFileAction relocates a single Go source file into a different package
// directory and rewrites every reference project-wide so the workspace
// continues to compile.
//
// Four categories of file are affected, each with its own transformation:
//
//   - The moved file itself: package clause swapped to the destination's name
//     (preserving any `_test` suffix); bare references to symbols staying
//     behind in the source package gain the source-package qualifier (`OtherX`
//     → `srcpkg.OtherX`).
//   - Source-package siblings (files staying behind): bare references to
//     symbols that moved gain the destination-package qualifier (`X` →
//     `dstpkg.X`).
//   - Destination-package siblings (when the destination directory already has
//     files): existing qualifiers on moved symbols are dropped (`srcpkg.X` →
//     `X`).
//   - Any other importer: package qualifier on moved symbols is swapped
//     (`srcpkg.X` → `dstpkg.X`).
//
// goimports runs on every modified file via [WorkspaceTransaction.AddChange],
// so imports are added and removed automatically based on the rewritten
// qualifiers.
//
// Same-package moves take a fast path: just relocate the file (the package
// clause and references are already correct).
//
// Godoc links are scanned and rewritten in lockstep with the code edits via
// [mfScanGodocLinks], so `srcpkg.X` references in comments don't dangle.
//
// Name-collision corner case: when the source and destination packages share a
// base name, adding the destination's import alongside the source's would cause
// a Go `<name> redeclared` error. The action detects this and rewrites the
// import PATH on every other importer instead of adding a second import; the
// qualifier `name.X` in code remains correct because both packages resolve to
// the same identifier.
type MoveFileAction struct{}

// Name implements [RefactorStrategy] and returns [ActionMoveFile].
func (*MoveFileAction) Name() string { return ActionMoveFile }

func init() { RegisterAction(&MoveFileAction{}) }

// Execute is the [RefactorStrategy] entry point. It validates inputs, resolves
// the source and destination packages, computes per-file role-specific edits,
// and stages everything for atomic commit.
//
// Stages:
//
//  1. Validate that input.File and input.TargetFile are .go paths and that the
//     target does not yet exist.
//  2. Locate the source file in the loaded packages; resolve the destination
//     package's name and import path from any existing files in its directory
//     or from the containing module's go.mod.
//  3. If source and destination are the same package, fast-path to
//     [mfRelocateSamePackage].
//  4. Collect the position-keyed sets of moved and staying symbol declarations
//     (position-based so test-augmented package variants are handled
//     correctly).
//  5. Walk every file in the workspace via [ForEachFile] (which dedupes by
//     absolute path); each file's role determines which edits to stage.
//  6. Apply each file's accumulated edits in descending order, inject any
//     needed imports, and stage the result on ws.
//  7. Build the destination file's content from the source file's modified
//     bytes; rewrite the package clause and re-run goimports.
//
// Failure modes:
//
//   - Missing or non-.go inputs — early error.
//   - Source and target are the same path — error.
//   - Target file already exists — error to avoid silent overwrite.
//   - The source file isn't in any loaded package — error.
//   - The destination directory isn't inside a known module — error.
//   - The build gate fails after edits — full rollback.
func (*MoveFileAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.File == "" {
		return fmt.Errorf("file is required (path of source .go file to move)")
	}
	if input.TargetFile == "" {
		return fmt.Errorf("target_file is required (destination .go file path)")
	}

	srcPath := input.File
	if !filepath.IsAbs(srcPath) {
		srcPath = filepath.Join(ws.ModRoot(), srcPath)
	}
	dstPath := input.TargetFile
	if !filepath.IsAbs(dstPath) {
		dstPath = filepath.Join(ws.ModRoot(), dstPath)
	}

	if filepath.Clean(srcPath) == filepath.Clean(dstPath) {
		return fmt.Errorf("source and target are the same path")
	}
	if !strings.HasSuffix(srcPath, ".go") {
		return fmt.Errorf("source must be a .go file")
	}
	if !strings.HasSuffix(dstPath, ".go") {
		return fmt.Errorf("target must be a .go file")
	}
	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("target file already exists: %s", dstPath)
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	srcPkg, srcAST, err := mfFindSourceFile(pkgs, srcPath)
	if err != nil {
		return err
	}

	srcPkgName := srcPkg.Name
	srcPkgPath := srcPkg.PkgPath

	dstDir := filepath.Dir(dstPath)
	dstPkgName, dstPkgPath, err := mfResolveDestPackage(ws.ModRoot(), pkgs, dstDir)
	if err != nil {
		return err
	}

	if dstPkgPath == srcPkgPath {
		// Same-package move — fall back to fs-style file move with no
		// reference rewriting needed beyond the file relocation itself.
		return mfRelocateSamePackage(srcPath, dstPath, ws)
	}

	movedPositions := mfCollectMovedPositions(srcPkg, srcAST)
	stayingPositions := mfCollectStayingPositions(srcPkg, srcAST)
	movedNames := mfCollectMovedSymNames(srcAST)
	stayingNames := mfCollectStayingSymNames(srcPkg, srcAST)

	changes := make(map[string]*mfFileChange)
	srcByFile := make(map[string][]byte)
	getChange := func(filePath string) *mfFileChange {
		c := changes[filePath]
		if c == nil {
			c = &mfFileChange{}
			changes[filePath] = c
		}
		return c
	}

	// ForEachFile dedupes by absolute path so the test-augmented and
	// production variants of the source package don't double-fire the
	// edit collection (which would produce `clock.clock.Clock`).
	ForEachFile(pkgs, func(p *packages.Package, f *ast.File, filePath string) {
		role := mfFileRole(filePath, srcPath, p.PkgPath, srcPkgPath, dstPkgPath)
		edits, addImports, rewriteSrcPath := mfBuildEdits(
			p,
			f,
			role,
			srcPkgName,
			dstPkgName,
			srcPkgPath,
			dstPkgPath,
			movedPositions,
			stayingPositions,
		)
		linkEdits := mfScanGodocLinks(p.Fset, f, role, srcPkgName, dstPkgName, movedNames, stayingNames)
		edits = append(edits, linkEdits...)
		if len(edits) == 0 && len(addImports) == 0 && !rewriteSrcPath {
			return
		}
		if _, loaded := srcByFile[filePath]; !loaded {
			b, readErr := os.ReadFile(filePath)
			if readErr != nil {
				return
			}
			srcByFile[filePath] = b
		}
		c := getChange(filePath)
		c.edits = append(c.edits, edits...)
		if rewriteSrcPath {
			c.rewriteSrcPath = true
		}
		for _, imp := range addImports {
			if !mfHasString(c.importsToAdd, imp) {
				c.importsToAdd = append(c.importsToAdd, imp)
			}
		}
	})

	// Stage edits for every non-source file.
	for filePath, ch := range changes {
		if filepath.Clean(filePath) == filepath.Clean(srcPath) {
			continue
		}
		original := srcByFile[filePath]
		modified := applyEdits(original, ch.edits)
		if ch.rewriteSrcPath {
			modified = mfRewriteImportPath(filePath, modified, srcPkgPath, dstPkgPath)
		}
		modified = mfInjectImports(filePath, modified, ch.importsToAdd)
		if addErr := ws.AddChange(filePath, original, modified, "updated references for move_file"); addErr != nil {
			return addErr
		}
	}

	// Build the destination file content. Apply src-file edits (which qualify
	// references to staying-behind siblings), then rewrite the package clause.
	srcOriginal, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	dstContent := srcOriginal
	var srcImports []string
	if srcCh, ok := changes[srcPath]; ok {
		dstContent = applyEdits(srcOriginal, srcCh.edits)
		srcImports = srcCh.importsToAdd
	}
	// External test files declare `package <pkg>_test`; the suffix marks
	// them as living in their own pseudo-package separate from the
	// production code in the same directory. Preserve that suffix on the
	// destination clause — without it the move silently downgrades the
	// file from an external test (compiled against the public API) to
	// internal test code.
	clauseName := dstPkgName
	if strings.HasSuffix(srcPkgName, "_test") && !strings.HasSuffix(clauseName, "_test") {
		clauseName += "_test"
	}
	dstContent = mfRewritePackageClause(dstContent, srcPkgName, clauseName)
	dstContent = mfInjectImports(dstPath, dstContent, srcImports)

	if formatted, err := imports.Process(dstPath, dstContent, nil); err == nil {
		dstContent = formatted
	}

	return ws.AddFileMove(srcPath, dstPath, dstContent,
		fmt.Sprintf("moved file from %s to %s", srcPath, dstPath))
}

// mfFileChange aggregates the byte edits and imports-to-add for a single
// file. Imports are injected via astutil.AddImport because goimports cannot
// resolve a destination package whose directory does not yet exist on disk
// at AddChange time (the move only commits at transaction commit).
type mfFileChange struct {
	edits          []fileEdit
	importsToAdd   []string
	rewriteSrcPath bool // rewrite srcPkgPath import → dstPkgPath
}

func mfHasString(xs []string, s string) bool {
	return slices.Contains(xs, s)
}

// mfRewriteImportPath parses src, rewrites an import of oldPath to
// newPath via astutil.RewriteImport, and re-serialises. Used when a
// destination package shares its name with the source package: adding
// the dst alongside the src import would collide on the package
// identifier, so the path is swapped instead and the existing
// `name.X` qualifiers in code remain valid for the destination.
// If parsing fails, returns src unchanged so the caller can still
// attempt a build (and roll back on failure).
func mfRewriteImportPath(filePath string, src []byte, oldPath, newPath string) []byte {
	if oldPath == newPath {
		return src
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return src
	}
	if !astutil.RewriteImport(fset, file, oldPath, newPath) {
		return src
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, file); err != nil {
		return src
	}
	return buf.Bytes()
}

// mfInjectImports parses src, adds any imports listed in paths via
// astutil.AddImport, and re-serialises. If parsing fails, returns src
// unchanged so the caller can still attempt a build (and roll back on
// failure).
func mfInjectImports(filePath string, src []byte, paths []string) []byte {
	if len(paths) == 0 {
		return src
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, src, parser.ParseComments)
	if err != nil {
		return src
	}
	added := false
	for _, p := range paths {
		if astutil.AddImport(fset, file, p) {
			added = true
		}
	}
	if !added {
		return src
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, file); err != nil {
		return src
	}
	return buf.Bytes()
}

// mfFindSourceFile resolves the package + AST file for srcPath.
func mfFindSourceFile(pkgs []*packages.Package, srcPath string) (*packages.Package, *ast.File, error) {
	target := filepath.Clean(srcPath)
	for _, p := range pkgs {
		for i, f := range p.Syntax {
			if i >= len(p.CompiledGoFiles) {
				continue
			}
			if filepath.Clean(p.CompiledGoFiles[i]) == target {
				return p, f, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("source file %s not found in loaded packages", srcPath)
}

// mfResolveDestPackage returns the package name and import path for dstDir.
// If dstDir contains existing Go files, their package name is used.
// Otherwise, walks UP from dstDir to find the containing module's go.mod
// and synthesizes the import path from there. This is workspace-safe:
// the containing module is whichever go.mod sits above dstDir, NOT the
// transaction's modRoot (which is the workspace root in go.work mode and
// has no go.mod).
func mfResolveDestPackage(_ string, pkgs []*packages.Package, dstDir string) (string, string, error) {
	cleanDst := filepath.Clean(dstDir)

	// Prefer the production package over an external-test pseudo-package
	// when both inhabit the same directory: the production name is what
	// qualifier insertions in moved files (e.g., `pkg.Symbol()`) should
	// use. Package-clause rewriting handles the `_test` suffix separately
	// at the call site.
	var testFallbackName, testFallbackPath string
	for _, p := range pkgs {
		for i := range p.Syntax {
			if i >= len(p.CompiledGoFiles) {
				continue
			}
			if filepath.Clean(filepath.Dir(p.CompiledGoFiles[i])) != cleanDst {
				continue
			}
			if strings.HasSuffix(p.Name, "_test") {
				if testFallbackName == "" {
					testFallbackName, testFallbackPath = p.Name, p.PkgPath
				}
				continue
			}
			return p.Name, p.PkgPath, nil
		}
	}
	if testFallbackName != "" {
		return testFallbackName, testFallbackPath, nil
	}

	moduleRoot := mfFindModuleRoot(cleanDst)
	if moduleRoot == "" {
		return "", "", fmt.Errorf("no go.mod found above destination directory %s", cleanDst)
	}
	modPath, err := mvReadModulePath(moduleRoot)
	if err != nil {
		return "", "", fmt.Errorf("read module path: %w", err)
	}
	rel, err := filepath.Rel(moduleRoot, cleanDst)
	if err != nil {
		return "", "", fmt.Errorf("destination outside module root: %w", err)
	}
	pkgPath := modPath
	if rel != "." {
		pkgPath = modPath + "/" + filepath.ToSlash(rel)
	}
	return filepath.Base(cleanDst), pkgPath, nil
}

// mfFindModuleRoot walks up from startDir looking for the nearest go.mod.
// Returns "" if none is found.
func mfFindModuleRoot(startDir string) string {
	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// mfRelocateSamePackage moves a file inside its own package. Just stages a
// rename (the package clause and references are already correct).
func mfRelocateSamePackage(srcPath, dstPath string, ws Transaction) error {
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	return ws.AddFileMove(srcPath, dstPath, src,
		fmt.Sprintf("moved %s to %s within the same package", srcPath, dstPath))
}

// posKey identifies a declaration by its source position, surviving the
// fact that Go's package loader produces a distinct *types.Object for the
// same declaration in each package variant (e.g., production vs.
// `_test`-with-internal-tests). All variants resolve to the same
// (filename, offset).
type posKey struct {
	file   string
	offset int
}

// mfCollectMovedPositions returns the position keys for every top-level
// declaration in srcAST that should move with the file.
func mfCollectMovedPositions(pkg *packages.Package, srcAST *ast.File) map[posKey]bool {
	moved := make(map[posKey]bool)
	if pkg.TypesInfo == nil || pkg.Fset == nil {
		return moved
	}
	add := func(obj types.Object) {
		if obj == nil {
			return
		}
		p := pkg.Fset.Position(obj.Pos())
		if p.Filename == "" {
			return
		}
		moved[posKey{file: p.Filename, offset: p.Offset}] = true
	}
	for _, decl := range srcAST.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			add(pkg.TypesInfo.Defs[d.Name])
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					add(pkg.TypesInfo.Defs[s.Name])
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(pkg.TypesInfo.Defs[n])
					}
				}
			}
		}
	}
	return moved
}

// mfCollectStayingPositions returns position keys for top-level
// declarations in srcPkg's other files (i.e., siblings staying behind).
// References to these from the moved file will need to be qualified with
// the source package name after the move.
func mfCollectStayingPositions(pkg *packages.Package, srcAST *ast.File) map[posKey]bool {
	staying := make(map[posKey]bool)
	if pkg.TypesInfo == nil || pkg.Fset == nil {
		return staying
	}
	add := func(obj types.Object) {
		if obj == nil {
			return
		}
		p := pkg.Fset.Position(obj.Pos())
		if p.Filename == "" {
			return
		}
		staying[posKey{file: p.Filename, offset: p.Offset}] = true
	}
	srcFile := pkg.Fset.Position(srcAST.Pos()).Filename
	for _, f := range pkg.Syntax {
		if f == srcAST {
			continue
		}
		fname := pkg.Fset.Position(f.Pos()).Filename
		if fname == srcFile {
			continue
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					add(pkg.TypesInfo.Defs[d.Name])
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						add(pkg.TypesInfo.Defs[s.Name])
					case *ast.ValueSpec:
						for _, n := range s.Names {
							add(pkg.TypesInfo.Defs[n])
						}
					}
				}
			}
		}
	}
	return staying
}

// mfFileRole classifies a file relative to the move.
type mfRole int

const (
	mfRoleSource        mfRole = iota // the file being moved
	mfRoleSrcPkgSibling               // stays in source package, not the moved file
	mfRoleDstPkgSibling               // already in destination package
	mfRoleOtherImporter               // any other package
)

func mfFileRole(filePath, srcPath, pkgPath, srcPkgPath, dstPkgPath string) mfRole {
	if filepath.Clean(filePath) == filepath.Clean(srcPath) {
		return mfRoleSource
	}
	switch pkgPath {
	case srcPkgPath:
		return mfRoleSrcPkgSibling
	case dstPkgPath:
		return mfRoleDstPkgSibling
	default:
		return mfRoleOtherImporter
	}
}

// mfBuildEdits walks file f and returns the byte-range edits required to
// keep it consistent after the move, the new imports to add, and a flag
// indicating the file's existing import of srcPkgPath should be rewritten
// to dstPkgPath. All offsets are in original-source coordinates; the
// caller applies edits in descending order and finalizes imports
// afterwards.
//
// The rewrite flag is only set for `mfRoleOtherImporter` files when the
// source and destination package names match. In that case, adding a
// second import alongside the existing one would collide (Go: "<name>
// redeclared in this block") because both resolve to the same package
// identifier. Rewriting the path keeps the qualifier `name.X` in code
// unchanged while pointing it at the destination package.
func mfBuildEdits(
	pkg *packages.Package,
	f *ast.File,
	role mfRole,
	srcPkgName, dstPkgName, srcPkgPath, dstPkgPath string,
	movedPositions map[posKey]bool,
	stayingPositions map[posKey]bool,
) ([]fileEdit, []string, bool) {
	if pkg.TypesInfo == nil || pkg.Fset == nil {
		return nil, nil, false
	}

	// First pass: identify Idents that are SelectorExpr.Sel.
	selOf := make(map[*ast.Ident]*ast.SelectorExpr)
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			selOf[sel.Sel] = sel
		}
		return true
	})

	var edits []fileEdit
	needSrcImport := false
	needDstImport := false
	rewriteSrcToDest := false

	ast.Inspect(f, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj := pkg.TypesInfo.Uses[ident]
		if obj == nil {
			return true
		}

		// Match by declaration position so that test-package variants of
		// the source pkg (which carry distinct *types.Object instances for
		// the same declaration) still resolve correctly.
		defPos := pkg.Fset.Position(obj.Pos())
		key := posKey{file: defPos.Filename, offset: defPos.Offset}
		isMoved := movedPositions[key]
		isStaying := stayingPositions[key]
		sel, isSel := selOf[ident]

		switch role {
		case mfRoleSource:
			// Leave moved-obj refs alone (they come along).
			// Qualify staying-in-src refs with `srcpkg.`.
			if isStaying && !isSel {
				pos := pkg.Fset.Position(ident.Pos()).Offset
				edits = append(edits, fileEdit{
					start:       pos,
					end:         pos,
					replacement: []byte(srcPkgName + "."),
				})
				needSrcImport = true
			}

		case mfRoleSrcPkgSibling:
			if isMoved && !isSel {
				pos := pkg.Fset.Position(ident.Pos()).Offset
				edits = append(edits, fileEdit{
					start:       pos,
					end:         pos,
					replacement: []byte(dstPkgName + "."),
				})
				needDstImport = true
			}

		case mfRoleDstPkgSibling:
			if isMoved && isSel && mfSelXIsPackage(pkg, sel) {
				xStart := pkg.Fset.Position(sel.X.Pos()).Offset
				selStart := pkg.Fset.Position(sel.Sel.Pos()).Offset
				edits = append(edits, fileEdit{
					start:       xStart,
					end:         selStart,
					replacement: nil,
				})
			}

		case mfRoleOtherImporter:
			if isMoved && isSel && mfSelXIsPackage(pkg, sel) {
				xIdent, _ := sel.X.(*ast.Ident)
				if xIdent == nil {
					return true
				}
				if srcPkgName == dstPkgName {
					// Source and destination share a package name. Adding
					// the dst import alongside the src import would collide
					// (`<name> redeclared in this block`); rewrite the
					// import path instead. The qualifier `<name>.X`
					// remains correct because both packages share <name>.
					rewriteSrcToDest = true
				} else {
					xStart := pkg.Fset.Position(xIdent.Pos()).Offset
					xEnd := pkg.Fset.Position(xIdent.End()).Offset
					edits = append(edits, fileEdit{
						start:       xStart,
						end:         xEnd,
						replacement: []byte(dstPkgName),
					})
					needDstImport = true
				}
			}
		}
		return true
	})

	var addImports []string
	if needSrcImport {
		addImports = append(addImports, srcPkgPath)
	}
	if needDstImport {
		addImports = append(addImports, dstPkgPath)
	}
	return edits, addImports, rewriteSrcToDest
}

// mfSelXIsPackage reports whether sel.X is a package qualifier (vs. an
// expression like `obj.Method`).
func mfSelXIsPackage(pkg *packages.Package, sel *ast.SelectorExpr) bool {
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	xObj := pkg.TypesInfo.Uses[xIdent]
	_, isPkg := xObj.(*types.PkgName)
	return isPkg
}

// mfRewritePackageClause replaces `package <oldName>` with `package <newName>`
// at the top of src. Comments between the file head and the package clause
// (e.g., build tags) are preserved.
func mfRewritePackageClause(src []byte, oldName, newName string) []byte {
	if oldName == newName {
		return src
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.PackageClauseOnly)
	if err != nil {
		// Fallback: line-based search.
		return mfRewritePackageClauseByLine(src, oldName, newName)
	}
	if file.Name == nil {
		return src
	}
	start := fset.Position(file.Name.Pos()).Offset
	end := fset.Position(file.Name.End()).Offset
	return ReplaceBytes(src, start, end, []byte(newName))
}

func mfRewritePackageClauseByLine(src []byte, oldName, newName string) []byte {
	scanner := bufio.NewScanner(strings.NewReader(string(src)))
	var out strings.Builder
	target := "package " + oldName
	replaced := false
	for scanner.Scan() {
		line := scanner.Text()
		if !replaced && strings.HasPrefix(strings.TrimSpace(line), target) {
			line = strings.Replace(line, target, "package "+newName, 1)
			replaced = true
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

// mfCollectMovedSymNames returns the set of top-level non-method declaration
// names from srcAST — the symbols that will move with the file.
func mfCollectMovedSymNames(srcAST *ast.File) map[string]bool {
	names := make(map[string]bool)
	for _, decl := range srcAST.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				names[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					names[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names[n.Name] = true
					}
				}
			}
		}
	}
	return names
}

// mfCollectStayingSymNames returns the set of top-level non-method declaration
// names from srcPkg's files other than srcAST (siblings staying behind).
func mfCollectStayingSymNames(pkg *packages.Package, srcAST *ast.File) map[string]bool {
	names := make(map[string]bool)
	for _, f := range pkg.Syntax {
		if f == srcAST {
			continue
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					names[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						names[s.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range s.Names {
							names[n.Name] = true
						}
					}
				}
			}
		}
	}
	return names
}

// mfScanGodocLinks returns byte-range edits to keep godoc link references
// consistent after a file move. Each role requires a different transformation:
//
//   - mfRoleSource: [StayingName] → [srcPkgName.StayingName]
//   - mfRoleSrcPkgSibling: [MovedName] → [dstPkgName.MovedName]
//   - mfRoleDstPkgSibling: [srcPkgName.MovedName] → [MovedName]
//   - mfRoleOtherImporter: [srcPkgName.MovedName] → [dstPkgName.MovedName]
func mfScanGodocLinks(
	fset *token.FileSet,
	f *ast.File,
	role mfRole,
	srcPkgName, dstPkgName string,
	movedNames, stayingNames map[string]bool,
) []fileEdit {
	if fset == nil {
		return nil
	}
	var edits []fileEdit

	for _, cg := range f.Comments {
		for _, c := range cg.List {
			commentOff := fset.Position(c.Pos()).Offset
			text := c.Text
			for _, m := range docLinkPattern.FindAllStringSubmatchIndex(text, -1) {
				inner := text[m[2]:m[3]]
				innerStart := commentOff + m[0] + 1 // offset just past '['

				segs := strings.SplitN(inner, ".", 2)
				firstSeg := segs[0]

				switch role {
				case mfRoleSource:
					// [StayingName] or [StayingType.Method] → prepend srcPkgName.
					if stayingNames[firstSeg] {
						edits = append(edits, fileEdit{
							start:       innerStart,
							end:         innerStart,
							replacement: []byte(srcPkgName + "."),
						})
					}

				case mfRoleSrcPkgSibling:
					// [MovedName] or [MovedType.Method] → prepend dstPkgName.
					if movedNames[firstSeg] {
						edits = append(edits, fileEdit{
							start:       innerStart,
							end:         innerStart,
							replacement: []byte(dstPkgName + "."),
						})
					}

				case mfRoleDstPkgSibling:
					// [srcPkgName.MovedName] → [MovedName]
					if firstSeg == srcPkgName && len(segs) > 1 {
						restFirst := strings.SplitN(segs[1], ".", 2)[0]
						if movedNames[restFirst] {
							edits = append(edits, fileEdit{
								start:       innerStart,
								end:         innerStart + len(srcPkgName) + 1,
								replacement: nil,
							})
						}
					}

				case mfRoleOtherImporter:
					if srcPkgName == dstPkgName {
						continue
					}
					// [srcPkgName.MovedName] → [dstPkgName.MovedName]
					if firstSeg == srcPkgName && len(segs) > 1 {
						restFirst := strings.SplitN(segs[1], ".", 2)[0]
						if movedNames[restFirst] {
							edits = append(edits, fileEdit{
								start:       innerStart,
								end:         innerStart + len(srcPkgName),
								replacement: []byte(dstPkgName),
							})
						}
					}
				}
			}
		}
	}
	return edits
}
