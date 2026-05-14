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

func TestChangeType(t *testing.T) {
	t.Run("replaces type definition — compatible change builds", func(t *testing.T) {
		dir := writeMod(t, "testchtype", map[string]string{
			"a.go": "package testchtype\n\ntype ID int32\n\nfunc Use() ID { var i ID = 5; return i }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ChangeType, lang.ChangeTypeInput{
			Symbol:            "ID",
			NewTypeDefinition: "int64",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type ID int64") && !strings.Contains(body, "type ID = int64") {
			t.Errorf("expected ID redefined as int64; got:\n%s", body)
		}
	})

	t.Run("incompatible change fails build and rolls back cleanly", func(t *testing.T) {
		dir := writeMod(t, "testchtypefail", map[string]string{
			"a.go": "package testchtypefail\n\ntype ID int\n\nfunc Use() ID { var i ID = 5; return i }\n",
		})
		t.Chdir(dir)

		if _, err := executeRefactorRaw(t, golang.ChangeType, lang.ChangeTypeInput{
			Symbol:            "ID",
			NewTypeDefinition: "string",
		}); err == nil {
			t.Fatal("expected error: changing ID to string should fail to build")
		}
		body := readFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type ID int") {
			t.Errorf("expected rollback to restore 'type ID int'; got:\n%s", body)
		}
	})

	// Changing Tag from string to []byte breaks the Tag("x") conversion —
	// acceptable to fail and roll back.
	t.Run("named type with incompatible underlying — rejects or rolls back", func(t *testing.T) {
		dir := writeMod(t, "kindschtype", map[string]string{
			"a.go": "package kindschtype\n\n" +
				"type Tag string\n\n" +
				"func Make() Tag { return Tag(\"x\") }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ChangeType, lang.ChangeTypeInput{
			Symbol:            "Tag",
			NewTypeDefinition: "[]byte",
		})
		if out.Status != refactor.StatusSuccess {
			body := mustReadFile(t, filepath.Join(dir, "a.go"))
			if !strings.Contains(body, "type Tag string") {
				t.Errorf("on rollback, original definition must remain; got:\n%s", body)
			}
			return
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Tag []byte") {
			t.Errorf("type not changed; got:\n%s", body)
		}
	})

	t.Run("type referenced in type assertion — updates assertion or rolls back", func(t *testing.T) {
		dir := writeMod(t, "prodassert", map[string]string{
			"a.go": "package prodassert\n\n" +
				"type Color int\n\n" +
				"func IsColor(v any) bool { _, ok := v.(Color); return ok }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		out := executeRefactor[refactor.Output](t, golang.ChangeType, lang.ChangeTypeInput{
			Symbol:            "Color",
			NewTypeDefinition: "uint32",
		})
		if out.Status == refactor.StatusFailure {
			got := mustReadFile(t, filepath.Join(dir, "a.go"))
			if got != original {
				t.Errorf("on failure, source must roll back; got:\n%s", got)
			}
			return
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "type Color uint32") {
			t.Errorf("definition not updated; got:\n%s", body)
		}
	})

	// Rename a type referenced in a type-set constraint (~T | ~U syntax).
	// Historical regex rewriters broke on the tilde prefix.
	t.Run("type-set member in interface constraint is updated", func(t *testing.T) {
		dir := writeMod(t, "rwtypeset", map[string]string{
			"a.go": "package rwtypeset\n\n" +
				"type Score int32\n\n" +
				"type Numeric interface{ ~int | Score }\n\n" +
				"func Use[T Numeric](v T) T { return v }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.Rename, lang.RenameInput{
			Symbol:  "Score",
			NewName: "Rating",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "~int | Rating") {
			t.Errorf("type-set member not updated; got:\n%s", body)
		}
	})

	t.Run("changes type across modules in go.work workspace", func(t *testing.T) {
		twoModuleWorkspace(
			t,
			map[string]string{"types.go": "package a\n\ntype Status int\n\nconst Active Status = 1\n"},
			map[string]string{
				"main.go": "package b\n\nimport \"example.com/a\"\n\nfunc IsActive(s a.Status) bool { return s == a.Active }\n",
			},
		)

		out := executeRefactor[refactor.Output](t, golang.ChangeType, lang.ChangeTypeInput{
			Symbol:            "Status",
			NewTypeDefinition: "uint32",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		aTypes := mustReadFile(t, "modA/types.go")
		if !strings.Contains(aTypes, "type Status uint32") {
			t.Errorf("modA type not updated; got:\n%s", aTypes)
		}
	})

	// Replace a function-type with an interface and rewrite the direct
	// invocations as method calls using method_mapping.
	t.Run("function type replaced with interface — call sites rewritten via method_mapping", func(t *testing.T) {
		dir := writeMod(t, "stresschtype", map[string]string{
			"a.go": "package stresschtype\n\n" +
				"type Edge func(int) int\n\n" +
				"func ApplyEdge(e Edge, v int) int { return e(v) }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ChangeType, lang.ChangeTypeInput{
			Symbol:            "Edge",
			NewTypeDefinition: "interface{ Apply(int) int }",
			MethodMapping:     map[string]string{"__call__": "Apply"},
		})
		if out.Status != refactor.StatusSuccess {
			body := mustReadFile(t, filepath.Join(dir, "a.go"))
			if !strings.Contains(body, "type Edge func(int) int") {
				t.Errorf("on failure, source must roll back; got:\n%s", body)
			}
			return
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "interface") {
			t.Errorf("Edge should be redefined as interface; got:\n%s", body)
		}
		if !strings.Contains(body, "e.Apply(v)") {
			t.Errorf("invocation should be rewritten via method_mapping; got:\n%s", body)
		}
	})
}
