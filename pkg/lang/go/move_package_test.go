// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

func TestMovePackage(t *testing.T) {
	t.Run("renames and rewrites importers", func(t *testing.T) {
		dir := writeMod(t, "testmovepkg", map[string]string{
			"old/old.go":   "package old\n\nfunc Hello() string { return \"hi\" }\n",
			"main/main.go": "package main\n\nimport \"testmovepkg/old\"\n\nfunc main() { _ = old.Hello() }\n",
		})
		t.Chdir(dir)

		// SourcePackage/DestPackage accept module-relative paths; this is the
		// canonical input shape (full import paths confuse mvResolveLocalPath).
		out := executeRefactor[refactor.Output](t, golang.MovePackage, lang.MovePackageInput{
			SourcePackage: "old",
			DestPackage:   "new",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		if _, err := os.Stat(filepath.Join(dir, "new")); err != nil {
			t.Errorf("expected new/ directory to exist after move: %v", err)
		}
		body := readFile(t, filepath.Join(dir, "main", "main.go"))
		if !strings.Contains(body, "testmovepkg/new") {
			t.Errorf("expected importer to reference testmovepkg/new; got:\n%s", body)
		}
	})

	t.Run("rewrites cross-package importers in complex project", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.MovePackage, lang.MovePackageInput{
			SourcePackage: "store",
			DestPackage:   "backends/store",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("move_package failed: status=%q results=%+v", out.Status, out.Results)
		}
		// api/handler.go imports example.com/cx/store — must be rewritten.
		apiBody := mustReadFile(t, filepath.Join(dir, "api/handler.go"))
		if !strings.Contains(apiBody, `"example.com/cx/backends/store"`) {
			t.Errorf("expected api/handler.go to reference new path; got:\n%s", apiBody)
		}
		if _, err := os.Stat(filepath.Join(dir, "backends/store/store.go")); err != nil {
			t.Errorf("expected backends/store/store.go to exist: %v", err)
		}
	})

	// Relocate keeps the same directory base name (and package identifier),
	// only the import path moves. Importers update the path, call sites unchanged.
	t.Run("relocate with same base name — import path updated call sites unchanged", func(t *testing.T) {
		dir := writeMod(t, "pkgrelocate", map[string]string{
			"internal/old/old.go": "package old\n\nfunc Hello() string { return \"hi\" }\n",
			"main.go":             "package pkgrelocate\n\nimport \"pkgrelocate/internal/old\"\n\nfunc Run() string { return old.Hello() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MovePackage, lang.MovePackageInput{
			SourcePackage: "internal/old",
			DestPackage:   "lib/old",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("relocate failed: status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "lib/old/old.go"))
		if !strings.Contains(moved, "package old") {
			t.Errorf("relocated package keeps the same identifier; got:\n%s", moved)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "pkgrelocate/lib/old") {
			t.Errorf("importer should reference the new path; got:\n%s", main)
		}
		if !strings.Contains(main, "old.Hello()") {
			t.Errorf("call sites should be untouched when package name is unchanged; got:\n%s", main)
		}
	})

	// Moving "internal/utils" → "internal/util" changes the dir base name,
	// so the package clause becomes `package util`. An alias is inserted on
	// the importer so `utils.Foo()` call sites keep compiling.
	t.Run("directory base name change inserts alias on importer", func(t *testing.T) {
		dir := writeMod(t, "pkgrename", map[string]string{
			"internal/utils/utils.go": "package utils\n\nfunc Help() string { return \"h\" }\n",
			"main.go":                 "package pkgrename\n\nimport \"pkgrename/internal/utils\"\n\nfunc Run() string { return utils.Help() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MovePackage, lang.MovePackageInput{
			SourcePackage: "internal/utils",
			DestPackage:   "internal/util",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("rename failed: status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "internal/util/utils.go"))
		if !strings.Contains(moved, "package util") {
			t.Errorf("moved file's package clause should become 'package util'; got:\n%s", moved)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "pkgrename/internal/util") {
			t.Errorf("importer should reference the new path; got:\n%s", main)
		}
		if !strings.Contains(main, `utils "pkgrename/internal/util"`) {
			t.Errorf("expected alias `utils \"...\"` on the importer; got:\n%s", main)
		}
	})

	// In-place rename (source == dest) must either reject or no-op — never
	// partially mutate files.
	t.Run("in-place rename (source == dest) rejects or no-ops cleanly", func(t *testing.T) {
		dir := writeMod(t, "pkginplace", map[string]string{
			"shared/old.go": "package shared\n\nfunc Run() string { return \"x\" }\n",
			"main.go":       "package pkginplace\n\nimport \"pkginplace/shared\"\n\nfunc Use() string { return shared.Run() }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "shared/old.go"))
		result, err := executeRefactorRaw(t, golang.MovePackage, lang.MovePackageInput{
			SourcePackage: "shared",
			DestPackage:   "shared",
		})
		if err != nil {
			if got := mustReadFile(t, filepath.Join(dir, "shared/old.go")); got != original {
				t.Errorf("source must remain untouched; got:\n%s", got)
			}
			t.Skipf("in-place package rename not supported (acceptable): %v", err)
		}
		out, _ := result.(refactor.Output)
		if out.Status != refactor.StatusSuccess {
			if got := mustReadFile(t, filepath.Join(dir, "shared/old.go")); got != original {
				t.Errorf("source must roll back on failure; got:\n%s", got)
			}
			t.Skipf("in-place package rename declined; status=%q", out.Status)
		}
	})
}
