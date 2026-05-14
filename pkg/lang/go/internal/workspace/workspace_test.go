// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package workspace_test

import (
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/go/packages"

	"go.thesmos.sh/techne/pkg/lang/go/internal/workspace"
)

// ---- fixture helpers ----

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeModule(t *testing.T, dir, modPath string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "go.mod"), "module "+modPath+"\n\ngo 1.21\n")
}

func writeGoWork(t *testing.T, dir string, useDirs []string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("go 1.21\n\nuse (\n")
	for _, u := range useDirs {
		b.WriteString("\t" + u + "\n")
	}
	b.WriteString(")\n")
	writeFile(t, filepath.Join(dir, "go.work"), b.String())
}

// ---- Discover: happy paths ----

func TestDiscover_SingleModule(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if ws.IsGoWork() {
		t.Error("expected single-module mode, got go.work mode")
	}
	if got, want := ws.Root(), absPath(t, dir); got != want {
		t.Errorf("Root: got %q, want %q", got, want)
	}
	mods := ws.Modules()
	if len(mods) != 1 {
		t.Fatalf("expected 1 module, got %d", len(mods))
	}
	if mods[0].Path != "example.com/single" {
		t.Errorf("module path: got %q, want %q", mods[0].Path, "example.com/single")
	}
	if mods[0].Dir != absPath(t, dir) {
		t.Errorf("module dir: got %q, want %q", mods[0].Dir, absPath(t, dir))
	}
}

func TestDiscover_SingleModule_FromSubdir(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	sub := filepath.Join(dir, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	ws, err := workspace.Discover(sub)
	if err != nil {
		t.Fatalf("Discover from subdir: %v", err)
	}
	if got, want := ws.Root(), absPath(t, dir); got != want {
		t.Errorf("Root: got %q, want %q", got, want)
	}
}

func TestDiscover_GoWork(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, filepath.Join(dir, "modA"), "example.com/a")
	writeModule(t, filepath.Join(dir, "modB"), "example.com/b")
	writeGoWork(t, dir, []string{"./modA", "./modB"})

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !ws.IsGoWork() {
		t.Error("expected go.work mode")
	}
	if got, want := ws.Root(), absPath(t, dir); got != want {
		t.Errorf("Root: got %q, want %q", got, want)
	}
	paths := modulePaths(ws)
	want := []string{"example.com/a", "example.com/b"}
	if !equalSorted(paths, want) {
		t.Errorf("module paths: got %v, want %v", paths, want)
	}
}

func TestDiscover_GoWork_FromMemberSubdir(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, filepath.Join(dir, "modA"), "example.com/a")
	writeModule(t, filepath.Join(dir, "modB"), "example.com/b")
	writeGoWork(t, dir, []string{"./modA", "./modB"})
	sub := filepath.Join(dir, "modA", "internal", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	ws, err := workspace.Discover(sub)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !ws.IsGoWork() {
		t.Error("expected go.work mode (go.work above member's go.mod should win)")
	}
	if got, want := ws.Root(), absPath(t, dir); got != want {
		t.Errorf("Root: got %q, want %q", got, want)
	}
}

// ---- Discover: edge cases ----

func TestDiscover_PreservesGoWorkOrder(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, filepath.Join(dir, "modC"), "example.com/c")
	writeModule(t, filepath.Join(dir, "modA"), "example.com/a")
	writeModule(t, filepath.Join(dir, "modB"), "example.com/b")
	writeGoWork(t, dir, []string{"./modC", "./modA", "./modB"})

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := modulePaths(ws)
	want := []string{"example.com/c", "example.com/a", "example.com/b"}
	if !slices.Equal(got, want) {
		t.Errorf("module order: got %v, want %v (must preserve go.work declaration order)", got, want)
	}
}

func TestDiscover_GoWork_AbsoluteUsePath(t *testing.T) {
	workDir := t.TempDir()
	modDir := t.TempDir()
	writeModule(t, modDir, "example.com/abs")
	// go.work in workDir referencing modDir as an absolute path.
	writeGoWork(t, workDir, []string{absPath(t, modDir)})

	ws, err := workspace.Discover(workDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	mods := ws.Modules()
	if len(mods) != 1 {
		t.Fatalf("expected 1 module, got %d", len(mods))
	}
	if mods[0].Path != "example.com/abs" {
		t.Errorf("module path: got %q, want %q", mods[0].Path, "example.com/abs")
	}
	if mods[0].Dir != absPath(t, modDir) {
		t.Errorf("module dir: got %q, want %q", mods[0].Dir, absPath(t, modDir))
	}
}

func TestDiscover_GoWork_PartialFailureReturnsValid(t *testing.T) {
	// One use directive points at a real module, another at a nonexistent
	// directory. The valid one should still be returned (best-effort behavior).
	dir := t.TempDir()
	writeModule(t, filepath.Join(dir, "good"), "example.com/good")
	writeGoWork(t, dir, []string{"./good", "./missing"})

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: expected success with at least one valid module, got error: %v", err)
	}
	got := modulePaths(ws)
	if !slices.Equal(got, []string{"example.com/good"}) {
		t.Errorf("module paths: got %v, want %v", got, []string{"example.com/good"})
	}
}

func TestDiscover_NestedGoMod_ClosestWins(t *testing.T) {
	// outer module at dir, inner module at dir/sub. Discover from inside sub
	// should pick the inner go.mod.
	dir := t.TempDir()
	writeModule(t, dir, "example.com/outer")
	writeModule(t, filepath.Join(dir, "sub"), "example.com/inner")

	ws, err := workspace.Discover(filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	mods := ws.Modules()
	if len(mods) != 1 {
		t.Fatalf("expected 1 module, got %d: %v", len(mods), modulePaths(ws))
	}
	if mods[0].Path != "example.com/inner" {
		t.Errorf("expected closest go.mod to win; got %q, want %q", mods[0].Path, "example.com/inner")
	}
}

// ---- Discover: error paths ----

func TestDiscover_NotInModule(t *testing.T) {
	dir := t.TempDir()

	if _, err := workspace.Discover(dir); err == nil {
		t.Fatal("expected error when no go.mod or go.work exists")
	}
}

func TestDiscover_NonexistentStartDir(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "does", "not", "exist")
	if _, err := workspace.Discover(bogus); err == nil {
		t.Fatal("expected error for nonexistent start directory")
	}
}

func TestDiscover_GoWorkAllModulesMissing(t *testing.T) {
	dir := t.TempDir()
	writeGoWork(t, dir, []string{"./missing-a", "./missing-b"})

	_, err := workspace.Discover(dir)
	if err == nil {
		t.Fatal("expected error when all go.work modules are missing")
	}
	if !strings.Contains(err.Error(), "no usable modules") {
		t.Errorf("error message should mention unusable modules; got %q", err.Error())
	}
}

func TestDiscover_GoWorkEmpty(t *testing.T) {
	dir := t.TempDir()
	writeGoWork(t, dir, nil)

	if _, err := workspace.Discover(dir); err == nil {
		t.Fatal("expected error when go.work has no use directives")
	}
}

func TestDiscover_MalformedGoWork(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.work"), "this is not valid go.work syntax (((")

	if _, err := workspace.Discover(dir); err == nil {
		t.Fatal("expected error for malformed go.work")
	}
}

func TestDiscover_MalformedGoMod(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "this is not valid go.mod syntax (((")

	if _, err := workspace.Discover(dir); err == nil {
		t.Fatal("expected error for malformed go.mod")
	}
}

func TestDiscover_GoModNoModuleDirective(t *testing.T) {
	dir := t.TempDir()
	// Valid go.mod syntax but missing the module directive.
	writeFile(t, filepath.Join(dir, "go.mod"), "go 1.21\n")

	_, err := workspace.Discover(dir)
	if err == nil {
		t.Fatal("expected error for go.mod without module directive")
	}
	if !strings.Contains(err.Error(), "module directive") {
		t.Errorf("error message should mention module directive; got %q", err.Error())
	}
}

// ---- Load: happy paths ----

func TestLoad_SingleModule(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "main.go"), "package single\n")
	writeFile(t, filepath.Join(dir, "sub", "sub.go"), "package sub\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	pkgs, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := pkgPaths(pkgs)
	want := []string{"example.com/single", "example.com/single/sub"}
	if !equalSorted(got, want) {
		t.Errorf("loaded packages: got %v, want %v", got, want)
	}
}

func TestLoad_GoWork(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, filepath.Join(dir, "modA"), "example.com/a")
	writeFile(t, filepath.Join(dir, "modA", "a.go"), "package a\n")
	writeModule(t, filepath.Join(dir, "modB"), "example.com/b")
	writeFile(t, filepath.Join(dir, "modB", "b.go"), "package b\n")
	writeGoWork(t, dir, []string{"./modA", "./modB"})

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	pkgs, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := pkgPaths(pkgs)
	want := []string{"example.com/a", "example.com/b"}
	if !equalSorted(got, want) {
		t.Errorf("loaded packages: got %v, want %v", got, want)
	}
}

// ---- Load: pattern handling ----

func TestLoad_GoWork_SpecificPatternPassesThrough(t *testing.T) {
	// In workspace mode, a specific pattern (not "./...") must NOT be expanded.
	// Loading "./modA/..." should return only modA's packages.
	dir := t.TempDir()
	writeModule(t, filepath.Join(dir, "modA"), "example.com/a")
	writeFile(t, filepath.Join(dir, "modA", "a.go"), "package a\n")
	writeModule(t, filepath.Join(dir, "modB"), "example.com/b")
	writeFile(t, filepath.Join(dir, "modB", "b.go"), "package b\n")
	writeGoWork(t, dir, []string{"./modA", "./modB"})

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	pkgs, err := ws.Load(t.Context(), packages.NeedName, []string{"./modA/..."})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := pkgPaths(pkgs)
	if !equalSorted(got, []string{"example.com/a"}) {
		t.Errorf("specific pattern should load only modA; got %v", got)
	}
}

func TestLoad_GoWork_ImportPathPasses(t *testing.T) {
	// An import path pattern should pass through and resolve via go.work.
	dir := t.TempDir()
	writeModule(t, filepath.Join(dir, "modA"), "example.com/a")
	writeFile(t, filepath.Join(dir, "modA", "a.go"), "package a\n")
	writeModule(t, filepath.Join(dir, "modB"), "example.com/b")
	writeFile(t, filepath.Join(dir, "modB", "b.go"), "package b\n")
	writeGoWork(t, dir, []string{"./modA", "./modB"})

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	pkgs, err := ws.Load(t.Context(), packages.NeedName, []string{"example.com/b"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := pkgPaths(pkgs)
	if !equalSorted(got, []string{"example.com/b"}) {
		t.Errorf("import-path pattern should resolve via go.work; got %v", got)
	}
}

func TestLoad_MultiplePatterns(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/multi")
	writeFile(t, filepath.Join(dir, "x", "x.go"), "package x\n")
	writeFile(t, filepath.Join(dir, "y", "y.go"), "package y\n")
	writeFile(t, filepath.Join(dir, "z", "z.go"), "package z\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	pkgs, err := ws.Load(t.Context(), packages.NeedName, []string{"./x", "./z"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := pkgPaths(pkgs)
	want := []string{"example.com/multi/x", "example.com/multi/z"}
	if !equalSorted(got, want) {
		t.Errorf("multiple patterns: got %v, want %v", got, want)
	}
}

func TestLoad_SingleModule_SpecificPattern(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "x", "x.go"), "package x\n")
	writeFile(t, filepath.Join(dir, "y", "y.go"), "package y\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	pkgs, err := ws.Load(t.Context(), packages.NeedName, []string{"./x"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := pkgPaths(pkgs)
	if !equalSorted(got, []string{"example.com/single/x"}) {
		t.Errorf("specific pattern: got %v, want [example.com/single/x]", got)
	}
}

func TestLoad_GoWork_ModuleAtWorkspaceRoot(t *testing.T) {
	// Edge case: go.work and a go.mod in the SAME directory (use ".").
	// expandPatterns must not generate "././/..." — should pass through "./...".
	dir := t.TempDir()
	writeModule(t, dir, "example.com/root")
	writeFile(t, filepath.Join(dir, "main.go"), "package root\n")
	writeFile(t, filepath.Join(dir, "go.work"), "go 1.21\n\nuse .\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !ws.IsGoWork() {
		t.Error("expected go.work mode")
	}

	pkgs, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := pkgPaths(pkgs)
	if !equalSorted(got, []string{"example.com/root"}) {
		t.Errorf("expected to load the module at workspace root; got %v", got)
	}
}

// ---- Load: options ----

func TestLoad_WithTests(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "a.go"), "package single\n\nfunc Hello() string { return \"hi\" }\n")
	writeFile(
		t,
		filepath.Join(dir, "a_test.go"),
		"package single\n\nimport \"testing\"\n\nfunc TestHello(t *testing.T) { _ = Hello() }\n",
	)

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	withTests, err := ws.Load(t.Context(), packages.NeedName|packages.NeedFiles, nil, workspace.WithTests())
	if err != nil {
		t.Fatalf("Load with tests: %v", err)
	}
	if !hasTestVariant(withTests) {
		t.Errorf("expected a test-variant package with WithTests(); got %v", pkgPaths(withTests))
	}

	withoutTests, err := ws.Load(t.Context(), packages.NeedName|packages.NeedFiles, nil)
	if err != nil {
		t.Fatalf("Load without tests: %v", err)
	}
	if hasTestVariant(withoutTests) {
		t.Errorf("did not expect a test-variant package without WithTests(); got %v", pkgPaths(withoutTests))
	}
}

func TestLoad_WithFset_PositionsResolveInProvidedFset(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "a.go"), "package single\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	fset := token.NewFileSet()
	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes
	pkgs, err := ws.Load(t.Context(), mode, nil, workspace.WithFset(fset))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(pkgs) == 0 || len(pkgs[0].Syntax) == 0 {
		t.Fatalf("expected at least one parsed file; got %d pkgs (Syntax=%d)", len(pkgs), syntaxCount(pkgs))
	}

	// The package's parsed file positions should resolve in OUR fset, proving
	// WithFset(fset) was applied.
	pos := pkgs[0].Syntax[0].Pos()
	if fset.File(pos) == nil {
		t.Error("WithFset(fset) was not applied — position does not resolve in provided FileSet")
	}
}

func TestLoad_WithBuildFlags_AppliesTags(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	// Always-included file
	writeFile(t, filepath.Join(dir, "common.go"), "package single\n")
	// Tag-gated file: only present when -tags=mytag is set.
	writeFile(t, filepath.Join(dir, "tagged.go"), "//go:build mytag\n\npackage single\n\nvar tagged = true\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	withTag, err := ws.Load(
		t.Context(),
		packages.NeedName|packages.NeedFiles,
		nil,
		workspace.WithBuildFlags("-tags=mytag"),
	)
	if err != nil {
		t.Fatalf("Load with tag: %v", err)
	}
	if !packageHasFile(withTag, "tagged.go") {
		t.Error("expected tagged.go to be loaded when -tags=mytag is set")
	}

	withoutTag, err := ws.Load(t.Context(), packages.NeedName|packages.NeedFiles, nil)
	if err != nil {
		t.Fatalf("Load without tag: %v", err)
	}
	if packageHasFile(withoutTag, "tagged.go") {
		t.Error("expected tagged.go to be excluded when -tags=mytag is not set")
	}
}

// ---- cache behavior ----

func TestLoad_CacheReturnsSameSliceAcrossCalls(t *testing.T) {
	// When two Load calls happen with identical inputs and no file changes,
	// the cache should return the SAME slice (pointer-equal). This verifies
	// the cache fires — without it both calls would return freshly-loaded
	// (different) slices.
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "a.go"), "package single\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	first, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	second, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}

	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected at least one package; got %d / %d", len(first), len(second))
	}
	// Pointer equality of the first package proves the slice came from the
	// cache rather than being re-loaded.
	if first[0] != second[0] {
		t.Errorf("expected cached pkg pointer to be reused; got distinct pkgs")
	}
}

func TestLoad_CacheInvalidatedByFileChange(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "a.go"), "package single\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	first, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}

	// Change a file; bump its mtime explicitly to ensure the fingerprint moves.
	future := time.Now().Add(2 * time.Second)
	if chtErr := os.Chtimes(filepath.Join(dir, "a.go"), future, future); chtErr != nil {
		t.Fatalf("chtimes: %v", chtErr)
	}

	second, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected packages from both loads")
	}
	if first[0] == second[0] {
		t.Errorf("expected cache to invalidate after mtime change; got same pkg pointer")
	}
}

func TestLoad_CacheInvalidatedByFileDeletion(t *testing.T) {
	// File deletion is the canonical missed-by-mtime case: max mtime
	// doesn't decrease when a file disappears. The fingerprint must
	// also track file count to catch this.
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "a.go"), "package single\n")
	writeFile(t, filepath.Join(dir, "b.go"), "package single\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	first, err := ws.Load(t.Context(), packages.NeedName|packages.NeedFiles, nil)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}

	// Delete b.go. The max mtime is still a.go's (or b.go's, whichever
	// was newer at the time of write); deletion alone doesn't bump it.
	if rmErr := os.Remove(filepath.Join(dir, "b.go")); rmErr != nil {
		t.Fatalf("remove b.go: %v", rmErr)
	}

	second, err := ws.Load(t.Context(), packages.NeedName|packages.NeedFiles, nil)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}

	// Pointer inequality of the package proves a fresh load happened.
	// File count went from 2 to 1, so the fingerprint must have moved.
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected packages from both loads; got %d / %d", len(first), len(second))
	}
	if first[0] == second[0] {
		t.Errorf("file deletion did not invalidate cache; got same pkg pointer")
	}
}

func TestLoad_CacheInvalidatedByGoModChange(t *testing.T) {
	// go.mod is the module-resolution manifest. A change can flip
	// require-version, add a replace, or rename the module — none of
	// which touch .go files but all of which change package metadata.
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "a.go"), "package single\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	first, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}

	// Bump go.mod's mtime by re-writing (semantically identical content).
	future := time.Now().Add(2 * time.Second)
	if chtErr := os.Chtimes(filepath.Join(dir, "go.mod"), future, future); chtErr != nil {
		t.Fatalf("chtimes go.mod: %v", chtErr)
	}

	second, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}

	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected packages from both loads")
	}
	if first[0] == second[0] {
		t.Errorf("go.mod change did not invalidate cache; got same pkg pointer")
	}
}

func TestLoad_CacheInvalidatedByGoWorkChange(t *testing.T) {
	// go.work is the workspace-mode manifest. Changes to use directives
	// can add/remove modules from the workspace — must invalidate.
	dir := t.TempDir()
	writeModule(t, filepath.Join(dir, "modA"), "example.com/a")
	writeFile(t, filepath.Join(dir, "modA", "a.go"), "package a\n")
	writeGoWork(t, dir, []string{"./modA"})

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	first, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}

	// Bump go.work mtime.
	future := time.Now().Add(2 * time.Second)
	if chtErr := os.Chtimes(filepath.Join(dir, "go.work"), future, future); chtErr != nil {
		t.Fatalf("chtimes go.work: %v", chtErr)
	}

	second, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}

	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected packages from both loads")
	}
	if first[0] == second[0] {
		t.Errorf("go.work change did not invalidate cache; got same pkg pointer")
	}
}

func TestLoad_CacheInvalidatedByGoSumChange(t *testing.T) {
	// go.sum drift is rare in pure source refactors but can happen when
	// dependencies are vendored/un-vendored or version-bumped. Catch it
	// via manifest mtime.
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "a.go"), "package single\n")
	// Hand-create a go.sum so we have something to bump.
	writeFile(t, filepath.Join(dir, "go.sum"), "")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	first, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}

	future := time.Now().Add(2 * time.Second)
	if chtErr := os.Chtimes(filepath.Join(dir, "go.sum"), future, future); chtErr != nil {
		t.Fatalf("chtimes go.sum: %v", chtErr)
	}

	second, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected packages from both loads")
	}
	if first[0] == second[0] {
		t.Errorf("go.sum change did not invalidate cache; got same pkg pointer")
	}
}

func TestLoad_CacheInvalidatedByNewFile(t *testing.T) {
	// Adding a brand-new file (not just modifying existing) — confirm
	// both fileCount tracking and the max-mtime path catch it.
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")
	writeFile(t, filepath.Join(dir, "a.go"), "package single\n")

	ws, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	first, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}

	// Add a new file.
	writeFile(t, filepath.Join(dir, "b.go"), "package single\n")
	// Make sure mtime is in the future to avoid same-tick races.
	future := time.Now().Add(2 * time.Second)
	if chtErr := os.Chtimes(filepath.Join(dir, "b.go"), future, future); chtErr != nil {
		t.Fatalf("chtimes b.go: %v", chtErr)
	}

	second, err := ws.Load(t.Context(), packages.NeedName, nil)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected packages from both loads")
	}
	if first[0] == second[0] {
		t.Errorf("new file did not invalidate cache; got same pkg pointer")
	}
}

func TestDiscover_ReturnsSameWorkspaceForRepeatedCalls(t *testing.T) {
	// Discover memoizes by canonical root: two calls to Discover with the
	// same start dir should return the SAME *Workspace. This is what makes
	// the per-Workspace Load cache effective across tools.
	dir := t.TempDir()
	writeModule(t, dir, "example.com/single")

	ws1, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover 1: %v", err)
	}
	ws2, err := workspace.Discover(dir)
	if err != nil {
		t.Fatalf("Discover 2: %v", err)
	}
	if ws1 != ws2 {
		t.Errorf("expected identical *Workspace from Discover; got distinct instances")
	}
}

// ---- assertion helpers ----

func absPath(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", p, err)
	}
	return a
}

func modulePaths(ws *workspace.Workspace) []string {
	out := make([]string, 0, len(ws.Modules()))
	for _, m := range ws.Modules() {
		out = append(out, m.Path)
	}
	return out
}

func pkgPaths(pkgs []*packages.Package) []string {
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p.PkgPath)
	}
	return out
}

func hasTestVariant(pkgs []*packages.Package) bool {
	for _, p := range pkgs {
		// packages.Load with Tests=true returns the test variant with the
		// ".test" or "_test" suffix in ID, or with extra GoFiles that include
		// _test.go files.
		if strings.HasSuffix(p.ID, ".test") || strings.Contains(p.ID, "_test") || strings.Contains(p.ID, "[") {
			return true
		}
	}
	return false
}

func syntaxCount(pkgs []*packages.Package) int {
	n := 0
	for _, p := range pkgs {
		n += len(p.Syntax)
	}
	return n
}

func packageHasFile(pkgs []*packages.Package, basename string) bool {
	for _, p := range pkgs {
		for _, f := range p.GoFiles {
			if filepath.Base(f) == basename {
				return true
			}
		}
	}
	return false
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
