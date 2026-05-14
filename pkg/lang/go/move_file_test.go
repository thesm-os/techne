// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

func TestMoveFile(t *testing.T) {
	t.Run("preserves self import aliases", func(t *testing.T) {
		// The moved file has aliased imports of stdlib packages — those must
		// remain in place.
		dir := writeMod(t, "rwxmvalias", map[string]string{
			"util/u.go": "package util\n\n" +
				"import x \"strings\"\n\n" +
				"func Upper(s string) string { return x.ToUpper(s) }\n",
			"main.go": "package rwxmvalias\n\nimport \"rwxmvalias/util\"\n\nfunc Use() string { return util.Upper(\"hi\") }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "helpers/u.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "helpers/u.go"))
		if !strings.Contains(moved, `x "strings"`) || !strings.Contains(moved, "x.ToUpper(s)") {
			t.Errorf("source-file aliased imports must survive the move; got:\n%s", moved)
		}
	})

	t.Run("into consumer package drops qualifier and unused import", func(t *testing.T) {
		// Moving foo/foo.go into bar/ where bar.go is the only consumer of
		// foo. The refactor should strip foo's qualifier from bar.go and
		// remove the now-unused import, leaving the workspace compiling
		// without foo's source dir.
		dir := writeMod(t, "rwzcyc", map[string]string{
			"foo/foo.go": "package foo\n\nfunc Helper() string { return \"h\" }\n",
			"bar/bar.go": "package bar\n\nimport \"rwzcyc/foo\"\n\nfunc Use() string { return foo.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "foo/foo.go"),
			TargetFile: filepath.Join(dir, "bar/foo.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		bar := mustReadFile(t, filepath.Join(dir, "bar/bar.go"))
		if !strings.Contains(bar, "return Helper()") {
			t.Errorf("bar.go must drop the foo qualifier; got:\n%s", bar)
		}
		if strings.Contains(bar, "rwzcyc/foo") {
			t.Errorf("bar.go must drop the now-unused foo import; got:\n%s", bar)
		}
	})

	t.Run("workspace within same module", func(t *testing.T) {
		// Moving a file inside one workspace module must work like a
		// single-module move (no cross-module concerns).
		twoModuleWorkspace(t,
			map[string]string{
				"util/util.go": "package util\n\nfunc Helper() string { return \"h\" }\n",
				"main.go":      "package a\n\nimport \"example.com/a/util\"\n\nfunc Use() string { return util.Helper() }\n",
			},
			map[string]string{"main.go": "package b\n"},
		)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join("modA", "util/util.go"),
			TargetFile: filepath.Join("modA", "helpers/util.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		main := mustReadFile(t, "modA/main.go")
		if !strings.Contains(main, "example.com/a/helpers") || !strings.Contains(main, "helpers.Helper") {
			t.Errorf("modA importer must use new path; got:\n%s", main)
		}
	})

	t.Run("workspace cross-module rejects or succeeds without corruption", func(t *testing.T) {
		// Moving a file from modA to modB is a cross-module move. Either it
		// works (rewriting cross-module imports) or it rejects with a clean
		// error. What it must NOT do: corrupt source.
		twoModuleWorkspace(
			t,
			map[string]string{"util.go": "package a\n\nfunc Helper() string { return \"h\" }\n"},
			map[string]string{
				"consumer.go": "package b\n\nimport \"example.com/a\"\n\nfunc Use() string { return a.Helper() }\n",
			},
		)

		originalA := mustReadFile(t, "modA/util.go")
		originalB := mustReadFile(t, "modB/consumer.go")
		result, err := executeRefactorRaw(t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join("modA", "util.go"),
			TargetFile: filepath.Join("modB", "util.go"),
		})
		if err != nil {
			// Cross-module move rejected — both files must be untouched.
			if got := mustReadFile(t, "modA/util.go"); got != originalA {
				t.Errorf("modA must roll back; got:\n%s", got)
			}
			if got := mustReadFile(t, "modB/consumer.go"); got != originalB {
				t.Errorf("modB must roll back; got:\n%s", got)
			}
			t.Logf("cross-module move rejected (acceptable): %v", err)
			return
		}
		out, _ := result.(refactor.Output)
		if out.Status == refactor.StatusFailure {
			if got := mustReadFile(t, "modA/util.go"); got != originalA {
				t.Errorf("modA must roll back on failure; got:\n%s", got)
			}
			t.Logf("cross-module move declined; status=%q", out.Status)
			return
		}
		// If it succeeded, the destination must exist with correct module path
		// imports in the (now relocated) consumer.
		if _, statErr := os.Stat("modB/util.go"); statErr != nil {
			t.Errorf("destination should exist on success; got err=%v", statErr)
		}
		bConsumer := mustReadFile(t, "modB/consumer.go")
		// Since Helper is now in modB itself, the import of example.com/a
		// should be gone and the call should be unqualified.
		if strings.Contains(bConsumer, "a.Helper") {
			t.Errorf("modB call site must drop qualifier when symbol moved into modB; got:\n%s", bConsumer)
		}
	})

	t.Run("build tag preserved", func(t *testing.T) {
		// A //go:build constraint at the head of the file must remain after
		// the move.
		tag := "//go:build " + runtime.GOOS + "\n\n"
		dir := writeMod(t, "mfhardbuild", map[string]string{
			"util/u.go": tag + "package util\n\nfunc Helper() string { return \"h\" }\n",
			"main.go":   "package mfhardbuild\n\nimport \"mfhardbuild/util\"\n\nfunc Use() string { return util.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "helpers/u.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "helpers/u.go"))
		if !strings.HasPrefix(moved, "//go:build "+runtime.GOOS) {
			t.Errorf("//go:build directive must remain at file head; got:\n%s", moved)
		}
	})

	t.Run("destination dir not present is created", func(t *testing.T) {
		// Target dir is multiple segments deep and doesn't exist on disk yet
		// — the move must create the hierarchy.
		dir := writeMod(t, "mfharddir", map[string]string{
			"util/u.go": "package util\n\nfunc Helper() string { return \"h\" }\n",
			"main.go":   "package mfharddir\n\nimport \"mfharddir/util\"\n\nfunc Use() string { return util.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "internal/lib/strings/u.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		if _, err := os.Stat(filepath.Join(dir, "internal/lib/strings/u.go")); err != nil {
			t.Errorf("destination hierarchy not created: %v", err)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "strings.Helper()") {
			t.Errorf("importer must use new pkg name; got:\n%s", main)
		}
	})

	t.Run("external test package rewritten to new qualifier", func(t *testing.T) {
		// Moving a file from `util` while an external test package `util_test`
		// references it via `util.Helper`. The external test must rewrite to
		// `helpers.Helper`.
		dir := writeMod(t, "mfhardxtest", map[string]string{
			"util/u.go":      "package util\n\nfunc Helper() string { return \"h\" }\n",
			"util/x_test.go": "package util_test\n\nimport (\n\t\"testing\"\n\t\"mfhardxtest/util\"\n)\n\nfunc TestHelper(t *testing.T) {\n\tif util.Helper() != \"h\" { t.Fail() }\n}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "helpers/u.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "util/x_test.go"))
		if !strings.Contains(body, "helpers.Helper()") {
			t.Errorf("external test must use new qualifier; got:\n%s", body)
		}
	})

	t.Run("external test file preserves _test package suffix", func(t *testing.T) {
		// Moving a `_test.go` file declared `package foo_test` (external
		// test) to a sibling directory must keep the `_test` package-clause
		// suffix. Otherwise the file silently switches from external to
		// internal test code.
		dir := writeMod(t, "mfhardxtsuffix", map[string]string{
			"foo/foo.go":      "package foo\n\nfunc Hello() string { return \"hi\" }\n",
			"foo/foo_test.go": "package foo_test\n\nimport (\n\t\"testing\"\n\t\"mfhardxtsuffix/foo\"\n)\n\nfunc TestHello(t *testing.T) {\n\tif foo.Hello() != \"hi\" { t.Fail() }\n}\n",
			"bar/bar.go":      "package bar\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "foo/foo_test.go"),
			TargetFile: filepath.Join(dir, "bar/foo_test.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "bar/foo_test.go"))
		if !strings.Contains(moved, "package bar_test") {
			t.Errorf("destination clause must preserve _test suffix; got:\n%s", moved)
		}
		if strings.Contains(moved, "package bar\n") || strings.Contains(moved, "package bar\t") {
			t.Errorf("destination clause must NOT lose _test suffix; got:\n%s", moved)
		}
	})

	t.Run("generic function call site requalified", func(t *testing.T) {
		// File declares a generic function and the importer uses it with type
		// instantiation.
		dir := writeMod(t, "mfhardgen", map[string]string{
			"util/u.go": "package util\n\nfunc Map[T, U any](xs []T, f func(T) U) []U {\n\tys := make([]U, len(xs))\n\tfor i, x := range xs { ys[i] = f(x) }\n\treturn ys\n}\n",
			"main.go":   "package mfhardgen\n\nimport \"mfhardgen/util\"\n\nfunc Use() []int { return util.Map([]int{1, 2}, func(x int) int { return x * 2 }) }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "fp/u.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "fp.Map(") {
			t.Errorf("generic function call must use new pkg qualifier; got:\n%s", main)
		}
	})

	t.Run("init function moves with file", func(t *testing.T) {
		// File with init() — should move with the rest. init() runs at
		// destination-pkg import time, not source's.
		dir := writeMod(t, "mfhardinit", map[string]string{
			"util/u.go": "package util\n\nvar Counter int\n\nfunc init() { Counter = 42 }\n",
			"main.go":   "package mfhardinit\n\nimport \"mfhardinit/util\"\n\nfunc Get() int { return util.Counter }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "config/u.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "config/u.go"))
		if !strings.Contains(moved, "func init()") {
			t.Errorf("init() must come along; got:\n%s", moved)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "config.Counter") {
			t.Errorf("var reference must be requalified; got:\n%s", main)
		}
	})

	t.Run("internal test pkg does not produce duplicate qualifier edits", func(t *testing.T) {
		// A package with an internal `_test.go` file (declared `package
		// <pkg>`, not `<pkg>_test`) is loaded twice by
		// golang.org/x/tools/go/packages — once as the production variant
		// and once as the test-augmented variant. Without per-file
		// deduplication, every non-test sibling gets the qualifier prefix
		// inserted twice at the same offset, producing `dst.dst.Sym`.
		dir := writeMod(t, "mfharddup", map[string]string{
			"util/clock.go":         "package util\n\ntype Clock interface{ Now() int64 }\n",
			"util/sibling.go":       "package util\n\ntype Holder struct {\n\tclock Clock\n}\n",
			"util/internal_test.go": "package util\n\nimport \"testing\"\n\nfunc TestNothing(t *testing.T) {}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/clock.go"),
			TargetFile: filepath.Join(dir, "clock/clock.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		sibling := mustReadFile(t, filepath.Join(dir, "util/sibling.go"))
		if !strings.Contains(sibling, "clock.Clock") {
			t.Errorf("sibling must reference moved type via single qualifier; got:\n%s", sibling)
		}
		if strings.Contains(sibling, "clock.clock.") {
			t.Errorf("qualifier must not be inserted twice (test-augmented pkg variant); got:\n%s", sibling)
		}
	})

	t.Run("multiple importers all rewritten consistently", func(t *testing.T) {
		// Three different packages each import the source pkg and use a
		// moved symbol. All three must be rewritten consistently.
		dir := writeMod(t, "mfhardimps", map[string]string{
			"util/u.go":  "package util\n\nfunc Helper() string { return \"h\" }\n",
			"a/a.go":     "package a\n\nimport \"mfhardimps/util\"\n\nfunc UseA() string { return util.Helper() }\n",
			"b/b.go":     "package b\n\nimport \"mfhardimps/util\"\n\nfunc UseB() string { return util.Helper() }\n",
			"sub/c/c.go": "package c\n\nimport \"mfhardimps/util\"\n\nfunc UseC() string { return util.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "helpers/u.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		for _, p := range []string{"a/a.go", "b/b.go", "sub/c/c.go"} {
			body := mustReadFile(t, filepath.Join(dir, p))
			if !strings.Contains(body, "helpers.Helper()") {
				t.Errorf("%s must use new package qualifier; got:\n%s", p, body)
			}
			if strings.Contains(body, "util.Helper") {
				t.Errorf("%s still references old qualifier; got:\n%s", p, body)
			}
		}
	})

	t.Run("nonexistent source file rejected up front", func(t *testing.T) {
		// Source file path doesn't exist. Tool must reject up-front, leaving
		// disk untouched.
		dir := writeMod(t, "mfhardnone", map[string]string{
			"util/u.go": "package util\n\nfunc Helper() string { return \"h\" }\n",
		})
		t.Chdir(dir)

		_, err := executeRefactorRaw(t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/does_not_exist.go"),
			TargetFile: filepath.Join(dir, "helpers/u.go"),
		})
		if err == nil {
			t.Fatal("expected error for missing source file")
		}
		if _, statErr := os.Stat(filepath.Join(dir, "helpers/u.go")); !os.IsNotExist(statErr) {
			t.Errorf("destination must not exist after failed move; got err=%v", statErr)
		}
	})

	t.Run("same package name rewrites import path not adds duplicate", func(t *testing.T) {
		// When the source and destination package directories share a package
		// name (e.g., `internal/bindings` → `bindings`, both `package
		// bindings`), the tool must rewrite the existing import path on every
		// importer instead of adding the destination import alongside the
		// source. Adding both would collide on the identifier.
		dir := writeMod(t, "mfhardsamepkg", map[string]string{
			"internal/bindings/bindings.go": "package bindings\n\nfunc Hello() string { return \"hi\" }\n",
			"consumer/c.go": `package consumer

import "mfhardsamepkg/internal/bindings"

func Use() string { return bindings.Hello() }
`,
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "internal/bindings/bindings.go"),
			TargetFile: filepath.Join(dir, "bindings/bindings.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		consumer := mustReadFile(t, filepath.Join(dir, "consumer/c.go"))
		if !strings.Contains(consumer, `"mfhardsamepkg/bindings"`) {
			t.Errorf("consumer must import the new path; got:\n%s", consumer)
		}
		if strings.Contains(consumer, `"mfhardsamepkg/internal/bindings"`) {
			t.Errorf("consumer must NOT retain the old import; got:\n%s", consumer)
		}
	})

	t.Run("self-referencing decls remain bare after move", func(t *testing.T) {
		// File declares two functions where one calls the other. After the
		// move both must compile (they're in the same destination pkg now).
		dir := writeMod(t, "mfhardself", map[string]string{
			"util/u.go": "package util\n\nfunc Inner() string { return \"x\" }\nfunc Outer() string { return Inner() + \"!\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "helpers/u.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "helpers/u.go"))
		// Inner() reference must remain bare — it moved with Outer.
		if !strings.Contains(moved, "return Inner() + ") {
			t.Errorf("self-references between moved decls must remain bare; got:\n%s", moved)
		}
		// Must not have qualified them with helpers.
		if strings.Contains(moved, "helpers.Inner") {
			t.Errorf("regression: self-reference must NOT be qualified; got:\n%s", moved)
		}
	})

	t.Run("struct with methods moves together", func(t *testing.T) {
		// Moving a file containing both a struct type and its methods.
		// External value-method invocations must compile after the move
		// because the constructor and the type are co-located.
		dir := writeMod(t, "mfhardstruct", map[string]string{
			"util/cache.go": "package util\n\n" +
				"type Cache struct{ data map[string]string }\n\n" +
				"func (c *Cache) Get(k string) string { return c.data[k] }\n" +
				"func (c *Cache) Set(k, v string) { c.data[k] = v }\n\n" +
				"func NewCache() *Cache { return &Cache{data: map[string]string{}} }\n",
			"main.go": "package mfhardstruct\n\nimport \"mfhardstruct/util\"\n\nfunc Use() string { c := util.NewCache(); c.Set(\"k\", \"v\"); return c.Get(\"k\") }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/cache.go"),
			TargetFile: filepath.Join(dir, "cache/cache.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "cache/cache.go"))
		if !strings.Contains(moved, "type Cache struct") || !strings.Contains(moved, "func (c *Cache) Get") {
			t.Errorf("type and methods must move together; got:\n%s", moved)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "cache.NewCache()") {
			t.Errorf("constructor call should use new package; got:\n%s", main)
		}
		// Method invocations through values stay as-is — they're properties of
		// the value's type, not of a package qualifier.
		if !strings.Contains(main, ".Set(\"k\", \"v\")") || !strings.Contains(main, ".Get(\"k\")") {
			t.Errorf("method invocations on values must remain unchanged; got:\n%s", main)
		}
	})

	t.Run("symbol collision with destination triggers rollback", func(t *testing.T) {
		// Moved file declares `Helper` and the destination package already
		// has `Helper`. The build gate must catch the collision and roll back.
		dir := writeMod(t, "mfhardcollide", map[string]string{
			"util/u.go":      "package util\n\nfunc Helper() string { return \"u\" }\n",
			"helpers/aux.go": "package helpers\n\nfunc Helper() string { return \"h\" }\n",
		})
		t.Chdir(dir)

		originalSrc := mustReadFile(t, filepath.Join(dir, "util/u.go"))
		originalAux := mustReadFile(t, filepath.Join(dir, "helpers/aux.go"))
		_, err := executeRefactorRaw(t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "helpers/u.go"),
		})
		if err == nil {
			t.Fatal("expected build failure due to duplicate Helper declaration")
		}
		// Both originals must still be on disk — atomic rollback.
		if got := mustReadFile(t, filepath.Join(dir, "util/u.go")); got != originalSrc {
			t.Errorf("source must roll back; got:\n%s", got)
		}
		if got := mustReadFile(t, filepath.Join(dir, "helpers/aux.go")); got != originalAux {
			t.Errorf("destination sibling must roll back; got:\n%s", got)
		}
		// Destination file must not exist.
		if _, err := os.Stat(filepath.Join(dir, "helpers/u.go")); !os.IsNotExist(err) {
			t.Errorf("destination should not have been written; got err=%v", err)
		}
	})

	t.Run("internal test file symbol requalified after source move", func(t *testing.T) {
		// Moving a non-test file from a package that has an internal
		// `_test.go` file. The internal test must have been rewritten too —
		// Helper is now in helpers pkg, so the bare reference needs
		// qualification. The build gate enforces correctness.
		dir := writeMod(t, "mfhardtest", map[string]string{
			"util/u.go":      "package util\n\nfunc Helper() string { return \"h\" }\n",
			"util/u_test.go": "package util\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) {\n\tif Helper() != \"h\" { t.Fail() }\n}\n",
		})
		t.Chdir(dir)

		// Move the test file to a sibling directory keeping it as same-package
		// is not really sensible — _test files can't move across packages.
		// Instead test that we can move a non-test file from a package that
		// has a test file.
		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/u.go"),
			TargetFile: filepath.Join(dir, "helpers/u.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		testBody := mustReadFile(t, filepath.Join(dir, "util/u_test.go"))
		if !strings.Contains(testBody, "helpers.Helper()") {
			t.Errorf("test file must qualify the moved symbol; got:\n%s", testBody)
		}
	})

	t.Run("across packages single symbol", func(t *testing.T) {
		// File with one exported func is moved into a brand-new package
		// directory; the only importer must switch from `oldpkg.Helper()` to
		// `newpkg.Helper()`.
		dir := writeMod(t, "movefilea", map[string]string{
			"util/helper.go": "package util\n\nfunc Helper() string { return \"h\" }\n",
			"main.go":        "package movefilea\n\nimport \"movefilea/util\"\n\nfunc Use() string { return util.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/helper.go"),
			TargetFile: filepath.Join(dir, "helpers/helper.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}

		// Source file gone.
		if _, err := os.Stat(filepath.Join(dir, "util/helper.go")); !os.IsNotExist(err) {
			t.Errorf("source file should be gone; got err=%v", err)
		}
		// Destination file exists with new package clause.
		moved := mustReadFile(t, filepath.Join(dir, "helpers/helper.go"))
		if !strings.Contains(moved, "package helpers") {
			t.Errorf("destination must have new package clause; got:\n%s", moved)
		}
		// Importer rewritten.
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "movefilea/helpers") {
			t.Errorf("importer should reference new package path; got:\n%s", main)
		}
		if !strings.Contains(main, "helpers.Helper()") {
			t.Errorf("call site should use new package name; got:\n%s", main)
		}
		if strings.Contains(main, "util.Helper") || strings.Contains(main, "movefilea/util") {
			t.Errorf("old references must be gone; got:\n%s", main)
		}
	})

	t.Run("across packages multiple symbols", func(t *testing.T) {
		// Two exported symbols in the same file, both must be referenced from
		// the new package after the move.
		dir := writeMod(t, "movefileb", map[string]string{
			"util/util.go": "package util\n\nfunc A() int { return 1 }\nfunc B() int { return 2 }\n",
			"main.go":      "package movefileb\n\nimport \"movefileb/util\"\n\nfunc Use() int { return util.A() + util.B() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/util.go"),
			TargetFile: filepath.Join(dir, "math/util.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "math.A()") || !strings.Contains(main, "math.B()") {
			t.Errorf("both call sites must use new package name; got:\n%s", main)
		}
	})

	t.Run("sibling in source pkg qualified with new pkg name", func(t *testing.T) {
		// When the moved file's symbol is referenced (unqualified) from
		// another file in the source pkg, the sibling file must qualify the
		// reference with the new pkg name.
		dir := writeMod(t, "movefilec", map[string]string{
			"util/helper.go":   "package util\n\nfunc Helper() string { return \"h\" }\n",
			"util/internal.go": "package util\n\nfunc UseHelper() string { return Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/helper.go"),
			TargetFile: filepath.Join(dir, "helpers/helper.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		sibling := mustReadFile(t, filepath.Join(dir, "util/internal.go"))
		if !strings.Contains(sibling, "helpers.Helper()") {
			t.Errorf("sibling must qualify the now-moved symbol; got:\n%s", sibling)
		}
		if !strings.Contains(sibling, "movefilec/helpers") {
			t.Errorf("sibling must import the destination package; got:\n%s", sibling)
		}
	})

	t.Run("moved file qualifies staying-behind sibling reference", func(t *testing.T) {
		// When the moved file references a symbol that stays behind in the
		// source pkg, the moved file (now in the destination pkg) must
		// qualify with the source pkg name.
		dir := writeMod(t, "movefiled", map[string]string{
			"util/helper.go":   "package util\n\nfunc Helper() string { return Format(\"h\") }\n",
			"util/internal.go": "package util\n\nfunc Format(s string) string { return \"<\" + s + \">\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/helper.go"),
			TargetFile: filepath.Join(dir, "helpers/helper.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		moved := mustReadFile(t, filepath.Join(dir, "helpers/helper.go"))
		if !strings.Contains(moved, "util.Format(") {
			t.Errorf("moved file must qualify staying-behind sibling; got:\n%s", moved)
		}
		if !strings.Contains(moved, "movefiled/util") {
			t.Errorf("moved file must import source pkg; got:\n%s", moved)
		}
	})

	t.Run("destination sibling drops qualifier when symbol moves in", func(t *testing.T) {
		// When the destination pkg already has files referencing
		// `srcpkg.Sym`, those qualifiers must be dropped after the move
		// (Sym is now local).
		dir := writeMod(t, "movefilee", map[string]string{
			"util/helper.go": "package util\n\nfunc Helper() string { return \"h\" }\n",
			"core/core.go":   "package core\n\nimport \"movefilee/util\"\n\nfunc Use() string { return util.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/helper.go"),
			TargetFile: filepath.Join(dir, "core/helper.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		dst := mustReadFile(t, filepath.Join(dir, "core/core.go"))
		if !strings.Contains(dst, "return Helper()") {
			t.Errorf("destination sibling must drop the qualifier; got:\n%s", dst)
		}
		if strings.Contains(dst, "util.Helper") {
			t.Errorf("destination sibling must not still qualify; got:\n%s", dst)
		}
	})

	t.Run("rejects existing target file", func(t *testing.T) {
		// target_file already exists → reject with rollback.
		dir := writeMod(t, "movefilef", map[string]string{
			"util/helper.go":    "package util\n\nfunc Helper() string { return \"h\" }\n",
			"helpers/aux.go":    "package helpers\n\nfunc Aux() string { return \"a\" }\n",
			"helpers/helper.go": "package helpers\n\nfunc PreExisting() string { return \"x\" }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "util/helper.go"))
		preExisting := mustReadFile(t, filepath.Join(dir, "helpers/helper.go"))
		_, err := executeRefactorRaw(t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/helper.go"),
			TargetFile: filepath.Join(dir, "helpers/helper.go"),
		})
		if err == nil {
			t.Fatal("expected error rejecting move into existing target file")
		}
		if got := mustReadFile(t, filepath.Join(dir, "util/helper.go")); got != original {
			t.Errorf("source file must be untouched; got:\n%s", got)
		}
		if got := mustReadFile(t, filepath.Join(dir, "helpers/helper.go")); got != preExisting {
			t.Errorf("pre-existing target must be untouched; got:\n%s", got)
		}
	})

	t.Run("same package relocate is plain rename", func(t *testing.T) {
		// Moving inside the same package — the tool falls through to a plain
		// file rename without rewriting references.
		dir := writeMod(t, "movefileg", map[string]string{
			"util/old.go": "package util\n\nfunc Helper() string { return \"h\" }\n",
			"main.go":     "package movefileg\n\nimport \"movefileg/util\"\n\nfunc Use() string { return util.Helper() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/old.go"),
			TargetFile: filepath.Join(dir, "util/new.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		if _, err := os.Stat(filepath.Join(dir, "util/old.go")); !os.IsNotExist(err) {
			t.Errorf("old path should be gone; got err=%v", err)
		}
		moved := mustReadFile(t, filepath.Join(dir, "util/new.go"))
		if !strings.Contains(moved, "package util") {
			t.Errorf("package clause unchanged; got:\n%s", moved)
		}
		main := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(main, "util.Helper()") {
			t.Errorf("importer must remain unchanged; got:\n%s", main)
		}
	})

	t.Run("dry run previews without writing", func(t *testing.T) {
		// dry_run must not modify any file on disk.
		dir := writeMod(t, "movefileh", map[string]string{
			"util/helper.go": "package util\n\nfunc Helper() string { return \"h\" }\n",
			"main.go":        "package movefileh\n\nimport \"movefileh/util\"\n\nfunc Use() string { return util.Helper() }\n",
		})
		t.Chdir(dir)

		originalSrc := mustReadFile(t, filepath.Join(dir, "util/helper.go"))
		originalMain := mustReadFile(t, filepath.Join(dir, "main.go"))

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/helper.go"),
			TargetFile: filepath.Join(dir, "helpers/helper.go"),
			DryRun:     true,
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("dry-run should report success; got status=%q", out.Status)
		}
		if got := mustReadFile(t, filepath.Join(dir, "util/helper.go")); got != originalSrc {
			t.Errorf("dry-run must leave source intact; got:\n%s", got)
		}
		if got := mustReadFile(t, filepath.Join(dir, "main.go")); got != originalMain {
			t.Errorf("dry-run must leave importer intact; got:\n%s", got)
		}
		if _, err := os.Stat(filepath.Join(dir, "helpers/helper.go")); !os.IsNotExist(err) {
			t.Errorf("dry-run must not create destination; got err=%v", err)
		}
	})

	t.Run("godoc links updated across all roles", func(t *testing.T) {
		dir := writeMod(t, "mfgodoc", map[string]string{
			// File being moved — has a link to a symbol staying in the source pkg.
			"util/moved.go": "package util\n\n" +
				"// Moved uses [Staying] for formatting.\n" +
				"func Moved() string { return Staying() }\n",
			// Staying sibling — has a link to the moved symbol.
			"util/staying.go": "package util\n\n" +
				"// Staying is called by [Moved].\n" +
				"func Staying() string { return \"s\" }\n",
			// Other importer — has a qualified link to the moved symbol.
			"consumer/c.go": "package consumer\n\nimport \"mfgodoc/util\"\n\n" +
				"// C calls [util.Moved] to do work.\n" +
				"func C() string { return util.Moved() }\n",
			// Destination sibling — has a qualified link; qualifier becomes redundant.
			"helpers/existing.go": "package helpers\n\nimport \"mfgodoc/util\"\n\n" +
				"// Existing delegates to [util.Moved].\n" +
				"func Existing() string { return util.Moved() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.MoveFile, lang.MoveFileInput{
			File:       filepath.Join(dir, "util/moved.go"),
			TargetFile: filepath.Join(dir, "helpers/moved.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}

		// mfRoleSource: [Staying] → [util.Staying]
		movedFile := mustReadFile(t, filepath.Join(dir, "helpers/moved.go"))
		if !strings.Contains(movedFile, "[util.Staying]") {
			t.Errorf("moved file: [Staying] must become [util.Staying]; got:\n%s", movedFile)
		}

		// mfRoleSrcPkgSibling: [Moved] → [helpers.Moved]
		stayingFile := mustReadFile(t, filepath.Join(dir, "util/staying.go"))
		if strings.Contains(stayingFile, "[Moved]") {
			t.Errorf("staying sibling: stale bare [Moved] link not updated; got:\n%s", stayingFile)
		}
		if !strings.Contains(stayingFile, "[helpers.Moved]") {
			t.Errorf("staying sibling: expected [helpers.Moved] link; got:\n%s", stayingFile)
		}

		// mfRoleOtherImporter: [util.Moved] → [helpers.Moved]
		consumerFile := mustReadFile(t, filepath.Join(dir, "consumer/c.go"))
		if strings.Contains(consumerFile, "[util.Moved]") {
			t.Errorf("consumer: stale [util.Moved] link not updated; got:\n%s", consumerFile)
		}
		if !strings.Contains(consumerFile, "[helpers.Moved]") {
			t.Errorf("consumer: expected [helpers.Moved] link; got:\n%s", consumerFile)
		}

		// mfRoleDstPkgSibling: [util.Moved] → [Moved]
		dstSiblingFile := mustReadFile(t, filepath.Join(dir, "helpers/existing.go"))
		if strings.Contains(dstSiblingFile, "[util.Moved]") {
			t.Errorf("dst sibling: stale [util.Moved] link not updated; got:\n%s", dstSiblingFile)
		}
		if !strings.Contains(dstSiblingFile, "[Moved]") {
			t.Errorf("dst sibling: expected bare [Moved] link; got:\n%s", dstSiblingFile)
		}
	})
}
