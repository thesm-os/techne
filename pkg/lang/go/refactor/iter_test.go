// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"

	"go.thesmos.sh/techne/pkg/lang/go/internal/workspace"
)

// TestForEachFile exercises the deduplicating package iterator. The
// dedup is the whole point of ForEachFile: every action that walks
// pkgs.Syntax to collect edits relies on visiting each file exactly
// once, even when go/packages returns a package both as production and
// test-augmented variant.
func TestForEachFile(t *testing.T) {
	t.Run("deduplicates across test-augmented variant", func(t *testing.T) {
		// Internal _test.go files cause packages.Load to return the package
		// twice; ForEachFile must collapse them.
		dir := t.TempDir()
		files := map[string]string{
			"go.mod":           "module example.com/x\n\ngo 1.21\n",
			"a.go":             "package x\n\nfunc A() {}\n",
			"b.go":             "package x\n\nfunc B() {}\n",
			"internal_test.go": "package x\n\nimport \"testing\"\n\nfunc TestNothing(t *testing.T) {}\n",
		}
		for rel, content := range files {
			full := filepath.Join(dir, rel)
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatalf("write %s: %v", full, err)
			}
		}

		w, err := workspace.Discover(dir)
		if err != nil {
			t.Fatalf("workspace discover: %v", err)
		}
		mode := packages.NeedSyntax | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo
		pkgs, err := w.Load(context.Background(), mode, nil, workspace.WithTests())
		if err != nil {
			t.Fatalf("load packages: %v", err)
		}

		visitsByPath := make(map[string]int)
		ForEachFile(pkgs, func(_ *packages.Package, _ *ast.File, path string) {
			visitsByPath[path]++
		})
		for path, count := range visitsByPath {
			if count != 1 {
				t.Errorf("file %s visited %d times; expected exactly 1", path, count)
			}
		}

		gotA, gotB := false, false
		for path := range visitsByPath {
			switch filepath.Base(path) {
			case "a.go":
				gotA = true
			case "b.go":
				gotB = true
			}
		}
		if !gotA || !gotB {
			t.Errorf("expected to visit a.go and b.go; got paths %v", visitsByPath)
		}
	})

	t.Run("skips packages with missing TypesInfo", func(t *testing.T) {
		// Defensive: load failures or partial packages must not nil-panic.
		pkgs := []*packages.Package{{}, nil}
		count := 0
		ForEachFile(pkgs, func(_ *packages.Package, _ *ast.File, _ string) { count++ })
		if count != 0 {
			t.Errorf("expected zero visits for malformed packages; got %d", count)
		}
	})

	t.Run("skips empty package list", func(t *testing.T) {
		// Edge case: nil pkgs slice should be a clean no-op.
		count := 0
		ForEachFile(nil, func(_ *packages.Package, _ *ast.File, _ string) { count++ })
		if count != 0 {
			t.Errorf("expected zero visits on nil pkgs; got %d", count)
		}
	})
}
