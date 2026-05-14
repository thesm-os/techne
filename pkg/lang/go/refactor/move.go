// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/printer"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// MovePackageAction relocates an entire Go package directory to a new import
// path and rewrites every importer in the workspace to match.
//
// What moves and what stays:
//
//   - Every .go file in the source directory itself moves to the destination
//     directory; subpackages (nested directories) are NOT included — they're
//     separate Go packages with their own import paths.
//   - The package clause in moved files is updated to the destination's base
//     directory name (the new package name).
//   - External test files (`package foo_test`) keep their `_test` suffix on the
//     destination package clause so they remain external tests after the move.
//   - Every file in the workspace that imports the source path has its import
//     rewritten to the destination path. When source and destination directory
//     base names differ, an explicit import alias preserving the OLD package
//     identifier is added so call-sites like `old.Hello()` continue to compile
//     without per-file selector rewrites.
//   - Godoc links referring to the old package name in any modified file are
//     rewritten to the new name.
//
// Cross-module moves are allowed under go.work: the destination may live in a
// sibling submodule. The action does NOT edit go.mod — `require` / `replace`
// directive bookkeeping is left to `go mod tidy`, and an advisory note is
// attached telling the user to run it in both affected modules.
//
// Subdirectory disambiguation matters here: a naive prefix check would mistake
// a sibling like `directives` for being inside `directive`. The action only
// treats files whose containing directory is EXACTLY the source directory as
// part of the move; nested directories are not relocated and their package
// paths are not rewritten.
type MovePackageAction struct{}

// Name implements [RefactorStrategy] and returns [ActionMovePackage].
func (*MovePackageAction) Name() string { return ActionMovePackage }

func init() { RegisterAction(&MovePackageAction{}) }

// Execute is the [RefactorStrategy] entry point. It resolves source and
// destination paths, walks every loaded package, and stages file moves plus
// import rewrites in a single transaction.
//
// Resolution sequence:
//
//  1. [mvResolveSource] accepts the source as a full import path matching a
//     loaded package, an absolute filesystem path, or a path relative to the
//     module root.
//  2. [mvCollectModules] discovers every module that contributed at least one
//     loaded package, plus the workspace modRoot, so cross-module destinations
//     resolve correctly.
//  3. [mvResolveDest] anchors the destination against one of those modules
//     (longest-prefix match for import paths, deepest-containing-root for
//     filesystem paths).
//
// Failure modes:
//
//   - input.SourcePackage or input.DestPackage is empty — early error.
//   - The source path resolves to no loaded package or directory — error.
//   - The destination is not within any known module — error.
//   - No files were found inside the source — error.
//   - The build gate fails after edits — full rollback.
//
// On cross-module moves the action attaches a note via Transaction.AddNote
// asking the user to run `go mod tidy` in both modules. This is the one piece
// of bookkeeping the framework cannot do safely on its own (it would have to
// edit go.mod's require/replace directives, which is well outside the
// build-gate's safety net).
func (*MovePackageAction) Execute(ctx context.Context, input Input, ws Transaction) error {
	if input.SourcePackage == "" || input.DestPackage == "" {
		return fmt.Errorf("source_package and dest_package are required")
	}

	pkgs, err := ws.LoadPackages(ctx)
	if err != nil {
		return fmt.Errorf("load packages: %w", err)
	}

	// Resolve source against the loaded packages so a full import path,
	// a relative directory, or an absolute path all work — including for
	// packages in a sibling module under a go.work workspace, where the
	// source's module is not necessarily the modRoot's module.
	sourceDir, srcImportPath, err := mvResolveSource(pkgs, ws.ModRoot(), input.SourcePackage)
	if err != nil {
		return fmt.Errorf("resolve source package: %w", err)
	}
	modules := mvCollectModules(pkgs, sourceDir, ws.ModRoot())
	destDir, destImportPath, err := mvResolveDest(modules, ws.ModRoot(), input.DestPackage)
	if err != nil {
		return fmt.Errorf("resolve destination package: %w", err)
	}

	newPkgName := filepath.Base(destDir)

	filesMoved := 0

	for _, pkg := range pkgs {
		if pkg.Fset == nil {
			continue
		}
		fset := pkg.Fset

		for _, fileAST := range pkg.Syntax {
			pos := fset.Position(fileAST.Pos())
			if pos.Filename == "" {
				continue
			}
			filePath := pos.Filename

			// Exact directory match. A naive prefix check would misidentify
			// a sibling package whose name shares a prefix (`directive` vs
			// `directives`) as part of the source. Subpackages live in
			// nested directories and are separate Go packages — they are
			// not part of this move.
			isInsideSource := filepath.Dir(filePath) == sourceDir

			original, err := os.ReadFile(filePath)
			if err != nil {
				continue
			}

			changed := false

			if astutil.UsesImport(fileAST, srcImportPath) {
				astutil.RewriteImport(fset, fileAST, srcImportPath, destImportPath)
				// If the package name changes (old dir != new dir), add an explicit
				// alias so call sites (e.g. old.Hello()) continue to compile.
				oldPkgName := filepath.Base(sourceDir)
				if oldPkgName != newPkgName {
					mvFixImportAlias(fileAST, destImportPath, oldPkgName)
					mvUpdateGodocLinks(fileAST, oldPkgName, newPkgName)
				}
				changed = true
			}

			if isInsideSource {
				// External test files in the source directory carry a
				// `_test` suffix on their package clause. Preserve it on
				// the destination so they remain external tests after the
				// move — without this, files like `foo_test.go` declared
				// `package foo_test` silently become `package bar` in the
				// destination, downgrading from external to internal tests.
				target := newPkgName
				if strings.HasSuffix(fileAST.Name.Name, "_test") && !strings.HasSuffix(target, "_test") {
					target += "_test"
				}
				fileAST.Name.Name = target
				changed = true
			}

			if !changed {
				continue
			}

			var buf bytes.Buffer
			if err := printer.Fprint(&buf, fset, fileAST); err != nil {
				return fmt.Errorf("failed to rewrite AST for %s: %w", filePath, err)
			}
			modified := buf.Bytes()

			if isInsideSource {
				relPath, _ := filepath.Rel(sourceDir, filePath)
				newFilePath := filepath.Join(destDir, relPath)
				if err := ws.AddFileMove(
					filePath,
					newFilePath,
					modified,
					fmt.Sprintf("moved to %s", destImportPath),
				); err != nil {
					return err
				}
				filesMoved++
			} else {
				if err := ws.AddChange(
					filePath,
					original,
					modified,
					fmt.Sprintf("rewrote import %s → %s", srcImportPath, destImportPath),
				); err != nil {
					return err
				}
			}
		}
	}

	if filesMoved == 0 {
		return fmt.Errorf("no files found in source package %s", input.SourcePackage)
	}

	// Cross-module moves shift go.mod requirements that the tool does not
	// edit itself. Surface a note so the user knows to run `go mod tidy`
	// in the affected modules.
	srcModuleRoot := mvFindModuleRoot(sourceDir)
	destModuleRoot := mvFindModuleRoot(destDir)
	if destModuleRoot == "" {
		destModuleRoot = mvFindModuleRoot(filepath.Dir(destDir))
	}
	if srcModuleRoot != "" && destModuleRoot != "" && srcModuleRoot != destModuleRoot {
		srcModPath, _ := mvReadModulePath(srcModuleRoot)
		destModPath, _ := mvReadModulePath(destModuleRoot)
		ws.AddNote(fmt.Sprintf(
			"cross-module move: package crossed a go.mod boundary (%s → %s). Run `go mod tidy` in %s and %s to update require/replace directives.",
			srcModPath,
			destModPath,
			srcModuleRoot,
			destModuleRoot,
		))
	}

	return nil
}

// mvResolveSource locates the source package on disk and returns its
// directory plus its canonical import path. Accepts (in order):
//
//   - A full import path that matches a loaded package's PkgPath
//     (works for any module in a go.work workspace).
//   - A filesystem path (absolute or relative to modRoot) that matches
//     a loaded package's directory.
//   - A filesystem path that doesn't match any loaded package — used
//     to surface a clear error for the typo case.
//
// PkgPath matching takes precedence so callers can pass the full import
// path for an unambiguous reference, even when it lives in a sibling
// module that modRoot's go.mod doesn't own.
func mvResolveSource(pkgs []*packages.Package, modRoot, input string) (string, string, error) {
	for _, p := range pkgs {
		if p.PkgPath == input && len(p.GoFiles) > 0 {
			return filepath.Dir(p.GoFiles[0]), p.PkgPath, nil
		}
	}
	var dir string
	if filepath.IsAbs(input) {
		dir = filepath.Clean(input)
	} else {
		dir = filepath.Clean(filepath.Join(modRoot, input))
	}
	for _, p := range pkgs {
		if len(p.GoFiles) > 0 && filepath.Dir(p.GoFiles[0]) == dir {
			return dir, p.PkgPath, nil
		}
	}
	return "", "", fmt.Errorf("package %q not found (no PkgPath or directory match)", input)
}

// mvResolveDest computes the destination directory and import path. The
// destination may live in any module known to the workspace's module
// map — same module as the source, a sibling submodule, the workspace
// root module, etc. Resolution order:
//
//  1. destInput is a full import path that matches a module's path or
//     is a sub-path of one — anchor to that module.
//  2. destInput is an absolute filesystem path — find which module's
//     filesystem root contains it.
//  3. destInput is a relative filesystem path — interpret it relative
//     to ws.ModRoot(), then find the containing module.
//
// Cross-module moves are allowed; go.mod files are not edited by the
// tool, so the user is expected to run `go mod tidy` in the affected
// modules afterward to update require/replace directives.
func mvResolveDest(modules map[string]string, modRoot, destInput string) (string, string, error) {
	if root, modPath, ok := mvModuleByImportPath(modules, destInput); ok {
		rel := strings.TrimPrefix(destInput, modPath)
		rel = strings.TrimPrefix(rel, "/")
		destDir := root
		if rel != "" {
			destDir = filepath.Join(root, filepath.FromSlash(rel))
		}
		return destDir, destInput, nil
	}
	var destDir string
	if filepath.IsAbs(destInput) {
		destDir = filepath.Clean(destInput)
	} else {
		destDir = filepath.Clean(filepath.Join(modRoot, destInput))
	}
	if modPath, root, ok := mvModuleByDir(modules, destDir); ok {
		rel, _ := filepath.Rel(root, destDir)
		importPath := modPath
		if rel != "" && rel != "." {
			importPath = path.Join(modPath, filepath.ToSlash(rel))
		}
		return destDir, importPath, nil
	}
	return "", "", fmt.Errorf("destination %q is not within any module of the workspace", destInput)
}

// mvCollectModules builds a map[modulePath]moduleRoot covering every
// module that contributed at least one loaded package, plus any
// fallback directories that walk up to a go.mod (e.g., the source dir
// or the workspace's modRoot — useful when the workspace contains
// modules with no loaded packages).
func mvCollectModules(pkgs []*packages.Package, fallbackDirs ...string) map[string]string {
	modules := make(map[string]string)
	seen := make(map[string]bool)
	addRoot := func(dir string) {
		if dir == "" {
			return
		}
		root := mvFindModuleRoot(dir)
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		mp, err := mvReadModulePath(root)
		if err != nil || mp == "" {
			return
		}
		modules[mp] = root
	}
	for _, p := range pkgs {
		if len(p.GoFiles) > 0 {
			addRoot(filepath.Dir(p.GoFiles[0]))
		}
	}
	for _, d := range fallbackDirs {
		addRoot(d)
	}
	return modules
}

// mvModuleByImportPath returns the module whose path is the longest
// prefix of importPath, plus its filesystem root. The longest-prefix
// rule disambiguates nested module paths (e.g., `example.com/a` vs
// `example.com/a/sub`).
func mvModuleByImportPath(modules map[string]string, importPath string) (root, modPath string, ok bool) {
	var bestModPath, bestRoot string
	for mp, r := range modules {
		if importPath == mp || strings.HasPrefix(importPath, mp+"/") {
			if len(mp) > len(bestModPath) {
				bestModPath, bestRoot = mp, r
			}
		}
	}
	if bestModPath == "" {
		return "", "", false
	}
	return bestRoot, bestModPath, true
}

// mvModuleByDir returns the module whose filesystem root contains dir.
// When modules are nested on disk, the deepest containing root wins so
// that e.g. `<workspace>/gen/x` resolves to the `gen` submodule, not
// the workspace's root module.
func mvModuleByDir(modules map[string]string, dir string) (modPath, root string, ok bool) {
	var bestModPath, bestRoot string
	for mp, r := range modules {
		rel, err := filepath.Rel(r, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(r) > len(bestRoot) {
			bestModPath, bestRoot = mp, r
		}
	}
	if bestModPath == "" {
		return "", "", false
	}
	return bestModPath, bestRoot, true
}

// mvFindModuleRoot walks up from startDir looking for the nearest go.mod.
// Returns "" if none is found.
func mvFindModuleRoot(startDir string) string {
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

// mvFixImportAlias sets an explicit alias on the import spec for destPath
// so that callers continue to use the original package name (oldName).
func mvFixImportAlias(fileAST *ast.File, destPath, oldName string) {
	for _, decl := range fileAST.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range genDecl.Specs {
			impSpec, ok := spec.(*ast.ImportSpec)
			if !ok {
				continue
			}
			path := strings.Trim(impSpec.Path.Value, `"`)
			if path == destPath {
				// Only set an alias if there isn't one already.
				if impSpec.Name == nil {
					impSpec.Name = ast.NewIdent(oldName)
				}
			}
		}
	}
}

// mvUpdateGodocLinks replaces [oldPkgName.X] godoc link prefixes with
// [newPkgName.X] in all comment groups of fileAST. The printer emits
// comment text verbatim so in-place modification is sufficient.
func mvUpdateGodocLinks(fileAST *ast.File, oldPkgName, newPkgName string) {
	if oldPkgName == newPkgName {
		return
	}
	oldPrefix := "[" + oldPkgName + "."
	newPrefix := "[" + newPkgName + "."
	for _, cg := range fileAST.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, oldPrefix) {
				c.Text = strings.ReplaceAll(c.Text, oldPrefix, newPrefix)
			}
		}
	}
}

// mvReadModulePath parses the module path from go.mod in modRoot.
func mvReadModulePath(modRoot string) (string, error) {
	f, err := os.Open(filepath.Join(modRoot, "go.mod"))
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after), nil
		}
	}
	return "", fmt.Errorf("module directive not found in go.mod")
}
