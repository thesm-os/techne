// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/internal/workspace"
)

// blast_radius is no longer a public tool. The algorithm here is invoked
// internally by verify when CompareTo is set, to narrow verification targets
// to only the packages affected by the diff.

// blastRadiusHandler computes the blast radius of a set of changed
// files: the packages that contain them, the packages that transitively
// import those, the tests that exercise the affected code, and a
// pre-filled lang.VerifyInput suitable for driving lang.go.verify.
//
// This is no longer a public tool. It runs internally from verify when
// CompareTo is set, so the cost of running verification can be narrowed
// from the whole module (slow, 10+ seconds) to just the diff's blast
// radius (sub-100ms). Two package loads are required: one with import
// metadata to compute reverse-import propagation, and one with syntax
// information and Tests=true to discover Test* functions in the
// affected set.
//
// MaxDepth defaults to 3 levels of reverse-import propagation when the
// caller does not specify; deeper traversal rarely catches additional
// breakage because most breakage manifests within 1–2 hops. The risk
// classification (Low/Medium/High/Critical) is exposed back to the
// caller so verify can present a confidence-scoped status to the agent.
func blastRadiusHandler(ctx context.Context, input lang.BlastRadiusInput) (lang.BlastRadiusOutput, error) {
	out := lang.BlastRadiusOutput{}

	maxDepth := input.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}

	cwd, err := os.Getwd()
	if err != nil {
		return out, fmt.Errorf("getwd: %w", err)
	}
	ws, err := workspace.Discover(cwd)
	if err != nil {
		return out, fmt.Errorf("discover workspace: %w", err)
	}

	pkgs, err := ws.Load(ctx, packages.NeedImports|packages.NeedDeps|packages.NeedName|packages.NeedFiles, nil)
	if err != nil {
		return out, fmt.Errorf("load packages: %w", err)
	}

	allPkgs := flattenWithDeps(pkgs)
	changedPkgs := mapChangedFilesToPackages(allPkgs, input.Files)
	affectedPkgs := propagateAffected(allPkgs, changedPkgs, input.IncludeTransitive, maxDepth)

	out.AffectedPackages = sortedKeys(affectedPkgs)
	out.RiskLevel = riskLevelFor(affectedPkgs)

	// Test discovery uses a fresh load with NeedSyntax + Tests:true.
	testPkgs, terr := ws.Load(
		ctx,
		packages.NeedSyntax|packages.NeedName|packages.NeedFiles|packages.NeedCompiledGoFiles,
		nil,
		workspace.WithTests(),
	)
	if terr == nil {
		out.CriticalTests = findTestsInPackages(testPkgs, affectedPkgs, input.Symbols)
	}

	out.SuggestedVerifyInput = &lang.VerifyInput{
		Suites:  []string{lang.SuiteTest},
		Targets: out.AffectedPackages,
		Focus:   strings.Join(out.CriticalTests, "|"),
	}
	return out, nil
}

// flattenWithDeps walks the package import graph starting from each
// root and returns every reachable package keyed by import path.
//
// When a package has internal ("package foo") test files, packages.Load
// returns it twice with identical PkgPath but different Syntax slices.
// The test-augmented variant is a superset (production files PLUS test
// files). The dedup prefers the variant with more Syntax files so
// test-file references in callers/references queries are preserved —
// without this, hits in *_test.go files would be silently dropped
// depending on iteration order.
//
// Not thread-safe: callers must not concurrently mutate roots.
func flattenWithDeps(roots []*packages.Package) map[string]*packages.Package {
	all := make(map[string]*packages.Package)
	var walk func(p *packages.Package)
	walk = func(p *packages.Package) {
		existing, seen := all[p.PkgPath]
		if seen {
			// Prefer the variant with more Syntax files (test-augmented
			// is a superset of production).
			if len(p.Syntax) > len(existing.Syntax) {
				all[p.PkgPath] = p
			}
			return
		}
		all[p.PkgPath] = p
		for _, dep := range p.Imports {
			walk(dep)
		}
	}
	for _, p := range roots {
		walk(p)
	}
	return all
}

// mapChangedFilesToPackages identifies which loaded packages contain
// any of the given changed files. Each file path is tried both as-given
// and as its absolute form so the function tolerates the various ways
// git diff and editor tooling produce paths (relative to repo root vs
// relative to module root vs absolute).
//
// Returns a set keyed by package path; an empty result means none of
// the files belongs to a loaded Go package (perhaps they are config
// files or docs).
func mapChangedFilesToPackages(allPkgs map[string]*packages.Package, files []string) map[string]bool {
	wanted := make(map[string]bool, len(files)*2)
	for _, f := range files {
		wanted[f] = true
		if abs, err := filepath.Abs(f); err == nil {
			wanted[abs] = true
		}
	}
	out := make(map[string]bool)
	for pkgPath, p := range allPkgs {
		for _, f := range p.GoFiles {
			if wanted[f] {
				out[pkgPath] = true
				break
			}
			if abs, err := filepath.Abs(f); err == nil && wanted[abs] {
				out[pkgPath] = true
				break
			}
		}
	}
	return out
}

// propagateAffected starts from changedPkgs and walks the reverse-import
// graph up to maxDepth levels (when includeTransitive is true). Each
// level adds packages that import any package in the current frontier;
// the traversal stops when no new packages are discovered or maxDepth
// is reached.
//
// When includeTransitive is false, the result is just the direct set of
// changed packages — useful when an agent specifically wants to scope
// verification to only the modified packages and accepts that
// downstream test failures will be discovered in a later run.
func propagateAffected(
	allPkgs map[string]*packages.Package,
	changed map[string]bool,
	includeTransitive bool,
	maxDepth int,
) map[string]bool {
	affected := make(map[string]bool, len(changed))
	for p := range changed {
		affected[p] = true
	}
	if !includeTransitive {
		return affected
	}

	reverse := buildReverseImports(allPkgs)
	frontier := make([]string, 0, len(changed))
	for p := range changed {
		frontier = append(frontier, p)
	}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, p := range frontier {
			for importer := range reverse[p] {
				if !affected[importer] {
					affected[importer] = true
					next = append(next, importer)
				}
			}
		}
		frontier = next
	}
	return affected
}

// buildReverseImports inverts the import graph: for each package, the
// set of packages that import it. Used by propagateAffected to traverse
// the "who is affected by changes here?" direction — the regular
// Imports field only answers "what does this package depend on?".
// O(packages * imports-per-package) to build, O(1) per lookup
// thereafter.
func buildReverseImports(allPkgs map[string]*packages.Package) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(allPkgs))
	for pkgPath, p := range allPkgs {
		for depPath := range p.Imports {
			if out[depPath] == nil {
				out[depPath] = make(map[string]bool)
			}
			out[depPath][pkgPath] = true
		}
	}
	return out
}

// riskLevelFor classifies blast-radius size into the lang.Risk* tiers.
//
// The thresholds are tuned for the agent UX: Low (single package) is
// the "go ahead" tier, Medium (2–4) means "review the affected
// list", High (5–9) means "be deliberate", and Critical (10+) means
// "this change is broader than you may have intended".
//
// A package whose import path contains "/core" always escalates to
// Critical regardless of count — core packages are infrastructure
// shared by every module, and even a single change there warrants
// human attention.
func riskLevelFor(affected map[string]bool) string {
	for pkg := range affected {
		if strings.Contains(pkg, "/core") || strings.HasSuffix(pkg, "/core") {
			return lang.RiskCritical
		}
	}
	count := len(affected)
	switch {
	case count >= 10:
		return lang.RiskCritical
	case count >= 5:
		return lang.RiskHigh
	case count >= 2:
		return lang.RiskMedium
	default:
		return lang.RiskLow
	}
}

// findTestsInPackages scans test packages for Test* functions in the
// affected set. If symbols is non-empty, only tests that reference at
// least one of those symbols are kept — this is how verify narrows
// from "all tests in affected packages" to "tests that exercise the
// changed symbols specifically".
//
// External test packages use the "_test" suffix in their PkgPath; the
// function trims that suffix when comparing against affectedPkgs so an
// external test for foo/bar is considered to belong to foo/bar.
// Results are deduplicated by function name and returned in sorted
// order for deterministic output.
func findTestsInPackages(pkgs []*packages.Package, affectedPkgs map[string]bool, symbols []string) []string {
	symbolSet := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		symbolSet[s] = true
	}

	var tests []string
	seen := make(map[string]bool)

	for _, p := range pkgs {
		// Test packages may use the "_test" external package suffix; map back
		// to the underlying package path to compare against the affected set.
		basePath := strings.TrimSuffix(p.PkgPath, "_test")
		if !affectedPkgs[p.PkgPath] && !affectedPkgs[basePath] {
			continue
		}

		for _, file := range p.Syntax {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || !strings.HasPrefix(fn.Name.Name, "Test") {
					continue
				}
				if len(symbolSet) > 0 && !funcReferencesSymbols(fn, symbolSet) {
					continue
				}
				if !seen[fn.Name.Name] {
					seen[fn.Name.Name] = true
					tests = append(tests, fn.Name.Name)
				}
			}
		}
	}

	sort.Strings(tests)
	return tests
}

// funcReferencesSymbols reports whether any identifier in the function's
// AST matches a name in symbols. Walks the entire AST with early
// termination on the first hit, so worst case is O(function-size) and
// best case is much faster. The shared found flag is captured by the
// Inspect closure; once true, it short-circuits all subsequent visits.
func funcReferencesSymbols(fn *ast.FuncDecl, symbols map[string]bool) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if found {
			return false
		}
		ident, ok := n.(*ast.Ident)
		if ok && symbols[ident.Name] {
			found = true
		}
		return true
	})
	return found
}

// sortedKeys returns the keys of a string-keyed map sorted in ascending
// order. Generic over the value type V so callers can use it on maps
// of any payload — the function only touches keys. Pre-sized to len(m)
// to avoid slice growth during fill.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
