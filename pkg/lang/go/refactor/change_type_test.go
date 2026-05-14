// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeChangeTypeModule creates a temp module with a Handler function type
// and a Process function that invokes a Handler-typed parameter.
func makeChangeTypeModule(t *testing.T) string {
	t.Helper()
	dir := setupTestModule(t)
	writeTestFile(t, dir, "types.go", `package testmod

// Handler is a function type for processing input.
type Handler func(input string) (string, error)
`)
	writeTestFile(t, dir, "processor.go", `package testmod

// Process calls the handler with the provided data.
func Process(h Handler, data string) (string, error) {
	result, err := h(data)
	return result, err
}
`)
	return dir
}

// TestChangeType exercises the change_type action — replacing a type
// definition and (optionally) rewriting direct-call invocations to
// method calls via MethodMapping["__call__"].
func TestChangeType(t *testing.T) {
	t.Run("replaces definition and rewrites invocations", func(t *testing.T) {
		dir := makeChangeTypeModule(t)

		out := runRefactor(t, dir, Input{
			Action:            ActionChangeType,
			Symbol:            "Handler",
			NewTypeDefinition: "interface {\n\tExecute(input string) (string, error)\n}",
			MethodMapping:     map[string]string{"__call__": "Execute"},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		typesContent := mustReadFileT(t, filepath.Join(dir, "types.go"))
		if strings.Contains(typesContent, "func(input string)") {
			t.Error("types.go: old function type still present")
		}
		if !strings.Contains(typesContent, "interface") || !strings.Contains(typesContent, "Execute") {
			t.Errorf("types.go: new interface definition missing\n%s", typesContent)
		}

		procContent := mustReadFileT(t, filepath.Join(dir, "processor.go"))
		if strings.Contains(procContent, "h(data)") {
			t.Error("processor.go: old invocation h(data) still present")
		}
		if !strings.Contains(procContent, "h.Execute(data)") {
			t.Errorf("processor.go: rewritten invocation not found\n%s", procContent)
		}

		verifyModuleIntegrity(t, dir)
	})

	t.Run("dry run leaves disk untouched but reports planned changes", func(t *testing.T) {
		dir := makeChangeTypeModule(t)
		originalTypes := mustReadFileT(t, filepath.Join(dir, "types.go"))
		originalProc := mustReadFileT(t, filepath.Join(dir, "processor.go"))

		out := runRefactor(t, dir, Input{
			Action:            ActionChangeType,
			Symbol:            "Handler",
			NewTypeDefinition: "interface {\n\tExecute(input string) (string, error)\n}",
			MethodMapping:     map[string]string{"__call__": "Execute"},
			DryRun:            true,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}
		if mustReadFileT(t, filepath.Join(dir, "types.go")) != originalTypes {
			t.Error("dry run modified types.go on disk")
		}
		if mustReadFileT(t, filepath.Join(dir, "processor.go")) != originalProc {
			t.Error("dry run modified processor.go on disk")
		}
		if out.FilesModified == 0 {
			t.Error("expected FilesModified > 0 in dry run output")
		}
	})

	t.Run("no method mapping leaves call sites intact", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "types.go", "package testmod\n\ntype Converter func(s string) string\n")
		writeTestFile(t, dir, "use.go", `package testmod

func Apply(c Converter, s string) string {
	return c(s)
}
`)
		out := runRefactor(t, dir, Input{
			Action:            ActionChangeType,
			Symbol:            "Converter",
			NewTypeDefinition: "func(s string) string",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		body := mustReadFileT(t, filepath.Join(dir, "use.go"))
		if strings.Contains(body, ".Transform(") || strings.Contains(body, ".Execute(") {
			t.Errorf("unexpected method rewrite with no __call__ mapping:\n%s", body)
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("multiple invocations in the same file are all rewritten", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "types.go", "package testmod\n\ntype Transformer func(s string) string\n")
		writeTestFile(t, dir, "apply.go", `package testmod

func Apply(tf Transformer, s string) string {
	first := tf(s)
	return tf(first)
}
`)
		out := runRefactor(t, dir, Input{
			Action:            ActionChangeType,
			Symbol:            "Transformer",
			NewTypeDefinition: "interface {\n\tTransform(s string) string\n}",
			MethodMapping:     map[string]string{"__call__": "Transform"},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		body := mustReadFileT(t, filepath.Join(dir, "apply.go"))
		if strings.Contains(body, "tf(") {
			t.Error("old invocation tf(...) still present")
		}
		if got := strings.Count(body, "tf.Transform("); got != 2 {
			t.Errorf("expected 2 tf.Transform( rewrites, got %d\n%s", got, body)
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("rejects unknown symbol", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg.go", "package testmod\n\nfunc Foo() {}\n")
		_, err := Handle(context.Background(), Input{
			Action:            ActionChangeType,
			Package:           dir,
			Symbol:            "NoSuchType",
			NewTypeDefinition: "interface{}",
		})
		if err == nil {
			t.Fatal("expected error for missing symbol, got nil")
		}
	})

	// Added coverage: cross-package invocations must be rewritten too.
	t.Run("rewrites invocations in other packages", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "core/handler.go", `package core

type Hook func(s string) string
`)
		writeTestFile(t, dir, "consumer/use.go", `package consumer

import "testmod.example.com/core"

func Run(h core.Hook, s string) string {
	return h(s)
}
`)
		out := runRefactor(t, dir, Input{
			Action:            ActionChangeType,
			Symbol:            "Hook",
			NewTypeDefinition: "interface { Apply(s string) string }",
			MethodMapping:     map[string]string{"__call__": "Apply"},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		body := mustReadFileT(t, filepath.Join(dir, "consumer/use.go"))
		if !strings.Contains(body, "h.Apply(s)") {
			t.Errorf("cross-package invocation not rewritten:\n%s", body)
		}
		verifyModuleIntegrity(t, dir)
	})

	// Added coverage: a type used as a struct field stays compatible
	// when only the underlying signature changes (no MethodMapping).
	t.Run("struct field of the type still compiles after definition swap", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "types.go", "package testmod\n\ntype Cb func() error\n")
		writeTestFile(t, dir, "owner.go", `package testmod

type Worker struct {
	OnDone Cb
}

func New() *Worker { return &Worker{OnDone: func() error { return nil }} }
`)
		out := runRefactor(t, dir, Input{
			Action:            ActionChangeType,
			Symbol:            "Cb",
			NewTypeDefinition: "func() error",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		verifyModuleIntegrity(t, dir)
	})
}

// mustReadFileT reads a file or fails the test. Local helper since the
// refactor package's _test.go files share this pattern.
func mustReadFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
