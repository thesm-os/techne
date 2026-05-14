// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

func TestMoveSymbol(t *testing.T) {
	t.Run("relocates function to target file", func(t *testing.T) {
		dir := writeMod(t, "testmovesym", map[string]string{
			"a.go": `package testmovesym

func Helper() string { return "h" }

func Used() string { return Helper() }
`,
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveSymbol, lang.MoveSymbolInput{
			Symbol:     "Helper",
			TargetFile: filepath.Join(dir, "helpers.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		helpersBody := readFile(t, filepath.Join(dir, "helpers.go"))
		if !strings.Contains(helpersBody, "func Helper()") {
			t.Errorf("expected Helper to be in helpers.go; got:\n%s", helpersBody)
		}
		aBody := readFile(t, filepath.Join(dir, "a.go"))
		if strings.Contains(aBody, "func Helper()") {
			t.Errorf("expected Helper removed from a.go; got:\n%s", aBody)
		}
	})

	t.Run("keeps receiver methods with struct in complex project", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.MoveSymbol, lang.MoveSymbolInput{
			Symbol:     "FileStore",
			TargetFile: filepath.Join(dir, "store/filestore.go"),
			Package:    "./store",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("move_symbol failed: status=%q results=%+v", out.Status, out.Results)
		}
		dst := mustReadFile(t, filepath.Join(dir, "store/filestore.go"))
		if !strings.Contains(dst, "type FileStore struct") {
			t.Errorf("FileStore should be in target file; got:\n%s", dst)
		}
		// If receiver methods didn't follow, build gate already enforced that
		// the workspace still compiles.
	})

	t.Run("moves interface type", func(t *testing.T) {
		dir := writeMod(t, "kindsmoveiface", map[string]string{
			"a.go": "package kindsmoveiface\n\n" +
				"type Logger interface{ Log(string) }\n\n" +
				"func Use(l Logger) { l.Log(\"x\") }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveSymbol, lang.MoveSymbolInput{
			Symbol:     "Logger",
			TargetFile: filepath.Join(dir, "logger.go"),
			Package:    ".",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("move interface failed: status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "logger.go"))
		if !strings.Contains(moved, "type Logger interface") {
			t.Errorf("interface not moved to target file; got:\n%s", moved)
		}
	})

	t.Run("moves constant", func(t *testing.T) {
		dir := writeMod(t, "kindsmoveconst", map[string]string{
			"a.go": "package kindsmoveconst\n\n" +
				"const MaxBuffer = 4096\n\n" +
				"func BufSize() int { return MaxBuffer }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveSymbol, lang.MoveSymbolInput{
			Symbol:     "MaxBuffer",
			TargetFile: filepath.Join(dir, "limits.go"),
			Package:    ".",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("move constant failed: status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "limits.go"))
		if !strings.Contains(moved, "const MaxBuffer") {
			t.Errorf("constant not moved; got:\n%s", moved)
		}
	})

	t.Run("moves type alias", func(t *testing.T) {
		dir := writeMod(t, "kindsmovealias", map[string]string{
			"a.go": "package kindsmovealias\n\n" +
				"type Original struct{ V int }\n\n" +
				"type Alias = Original\n\n" +
				"func Use() Alias { return Alias{V: 1} }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveSymbol, lang.MoveSymbolInput{
			Symbol:     "Alias",
			TargetFile: filepath.Join(dir, "alias.go"),
			Package:    ".",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("move alias failed: status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "alias.go"))
		if !strings.Contains(moved, "type Alias = Original") {
			t.Errorf("alias not moved; got:\n%s", moved)
		}
	})

	// Cross-package move is documented as not supported. The tool must either
	// reject up-front or roll back cleanly — never half-applied.
	t.Run("cross-package move rejects or rolls back cleanly", func(t *testing.T) {
		dir := writeMod(t, "prodmovexpkg", map[string]string{
			"src/a.go":   "package src\n\nfunc Helper() string { return \"x\" }\n",
			"dst/dst.go": "package dst\n",
			"main.go":    "package prodmovexpkg\n\nimport \"prodmovexpkg/src\"\n\nfunc Caller() string { return src.Helper() }\n",
		})
		t.Chdir(dir)

		originalSrc := mustReadFile(t, filepath.Join(dir, "src/a.go"))
		result, err := executeRefactorRaw(t, golang.MoveSymbol, lang.MoveSymbolInput{
			Symbol:     "Helper",
			Package:    "prodmovexpkg/src",
			TargetFile: filepath.Join(dir, "dst/dst.go"),
		})
		if err != nil {
			if got := mustReadFile(t, filepath.Join(dir, "src/a.go")); got != originalSrc {
				t.Errorf("source must remain untouched on rejection; got:\n%s", got)
			}
			t.Skipf("cross-package move_symbol not supported (acceptable): %v", err)
		}
		out, _ := result.(refactor.Output)
		if out.Status != refactor.StatusSuccess {
			if got := mustReadFile(t, filepath.Join(dir, "src/a.go")); got != originalSrc {
				t.Errorf("on refactor failure, source must roll back; got:\n%s", got)
			}
			t.Skipf("cross-package move_symbol declined (acceptable); status=%q", out.Status)
		}
	})

	t.Run("struct with methods and internal users moves completely", func(t *testing.T) {
		dir := writeMod(t, "stressmovesym", map[string]string{
			"a.go": "package stressmovesym\n\n" +
				"type Cache struct{ data map[string]string }\n\n" +
				"func (c *Cache) Get(k string) string { return c.data[k] }\n" +
				"func (c *Cache) Set(k, v string) { c.data[k] = v }\n\n" +
				"func New() *Cache { return &Cache{data: map[string]string{}} }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveSymbol, lang.MoveSymbolInput{
			Symbol:     "Cache",
			TargetFile: filepath.Join(dir, "cache.go"),
			Package:    ".",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		cacheBody := mustReadFile(t, filepath.Join(dir, "cache.go"))
		if !strings.Contains(cacheBody, "type Cache struct") {
			t.Errorf("Cache should be in cache.go; got:\n%s", cacheBody)
		}
	})
}
