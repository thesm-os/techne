// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"go/ast"
	"path/filepath"

	"golang.org/x/tools/go/packages"
)

// FileVisitor is the callback signature consumed by [ForEachFile]. It is
// invoked once per unique source file with the containing package, the parsed
// AST, and the absolute path on disk.
//
// The path is the canonical key for accumulating per-file edits — keying by
// anything else (the *ast.File pointer, the file's index within a package)
// breaks when a file appears in multiple package variants (e.g., production and
// test-augmented).
type FileVisitor func(pkg *packages.Package, file *ast.File, path string)

// ForEachFile invokes visit exactly once per unique non-blank source file
// across pkgs, deduplicating by absolute path.
//
// The dedup is essential, not a micro-optimization. A non-test source file
// appears in BOTH the production package and the test-augmented variant when
// its package has internal (`package foo`) test files. A naive iteration over
// `pkg.Syntax` visits the same file twice; per-file edit accumulators then
// double-fire, inserting the same qualifier prefix twice at the same offset
// (e.g., producing `pkg.pkg.Sym`). This bug surfaced in move_file and motivated
// extracting this helper.
//
// Use ForEachFile any time you iterate `pkg.Syntax` to collect edits keyed by
// file path. Files whose CompiledGoFiles entry is missing or whose
// Fset/TypesInfo are nil are silently skipped — they can't be safely walked
// anyway.
func ForEachFile(pkgs []*packages.Package, visit FileVisitor) {
	seen := make(map[string]bool)
	for _, pkg := range pkgs {
		if pkg == nil || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for i, f := range pkg.Syntax {
			if i >= len(pkg.CompiledGoFiles) {
				continue
			}
			path := pkg.CompiledGoFiles[i]
			if path == "" {
				continue
			}
			path = filepath.Clean(path)
			if seen[path] {
				continue
			}
			seen[path] = true
			visit(pkg, f, path)
		}
	}
}
