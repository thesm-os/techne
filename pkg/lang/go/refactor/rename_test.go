// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Shared test helpers
// ---------------------------------------------------------------------------

func setupTestModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module testmod.example.com\n\ngo 1.21\n")
	return dir
}

func writeTestFile(t *testing.T, dir, path, content string) {
	t.Helper()
	abs := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

func runRefactor(t *testing.T, modRoot string, input Input) Output {
	t.Helper()
	if input.Package == "" {
		input.Package = modRoot
	}
	out, err := Handle(t.Context(), input)
	if err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	return out
}

func verifyModuleIntegrity(t *testing.T, modRoot string) {
	t.Helper()
	for _, args := range [][]string{
		{"go", "build", "./..."},
		{"go", "vet", "./..."},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = modRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRename(t *testing.T) {
	t.Run("function renamed across 3 packages", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/alpha/alpha.go", `package alpha

func ProcessBatch(items []string) int {
	return len(items)
}
`)
		writeTestFile(t, dir, "pkg/beta/beta.go", `package beta

import "testmod.example.com/pkg/alpha"

func Run() int {
	return alpha.ProcessBatch([]string{"a", "b"})
}
`)
		writeTestFile(t, dir, "pkg/gamma/gamma.go", `package gamma

import "testmod.example.com/pkg/alpha"

func Execute() int {
	return alpha.ProcessBatch(nil)
}
`)

		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "ProcessBatch",
			NewName: "HandleBatch",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		for _, path := range []string{"pkg/alpha/alpha.go", "pkg/beta/beta.go", "pkg/gamma/gamma.go"} {
			content, _ := os.ReadFile(filepath.Join(dir, path))
			if strings.Contains(string(content), "ProcessBatch") {
				t.Errorf("%s still contains old name", path)
			}
			if !strings.Contains(string(content), "HandleBatch") {
				t.Errorf("%s missing new name", path)
			}
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("method rename updates definition and call sites", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "engine/engine.go", `package engine

type Engine struct{}

func (e *Engine) Start() {}

func UseEngine() {
	e := &Engine{}
	e.Start()
}
`)

		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "Engine.Start",
			NewName: "Run",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filepath.Join(dir, "engine/engine.go"))
		s := string(content)
		if strings.Contains(s, "func (e *Engine) Start()") {
			t.Error("definition not renamed")
		}
		if !strings.Contains(s, "func (e *Engine) Run()") {
			t.Error("new definition not found")
		}
		if strings.Contains(s, "e.Start()") {
			t.Error("call site not renamed")
		}
		if !strings.Contains(s, "e.Run()") {
			t.Error("call site new name not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("interface rename updates all references", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "store/store.go", `package store

type Storage interface {
	Get(key string) string
	Put(key, val string)
}

type MemStore struct{ m map[string]string }

func (s *MemStore) Get(key string) string  { return s.m[key] }
func (s *MemStore) Put(key, val string)    { s.m[key] = val }

func Use(s Storage) {
	s.Put("k", "v")
}

func Cast(v interface{}) Storage {
	return v.(Storage)
}
`)

		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "Storage",
			NewName: "Repository",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filepath.Join(dir, "store/store.go"))
		if strings.Contains(string(content), "Storage") {
			t.Error("old interface name still present")
		}
		if !strings.Contains(string(content), "Repository") {
			t.Error("new interface name not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("constant renamed across packages", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "config/config.go", "package config\n\nconst MaxRetries = 3\n")
		writeTestFile(t, dir, "worker/worker.go", `package worker

import "testmod.example.com/config"

func attempts() int {
	return config.MaxRetries
}
`)

		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "MaxRetries",
			NewName: "RetryLimit",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		for _, path := range []string{"config/config.go", "worker/worker.go"} {
			content, _ := os.ReadFile(filepath.Join(dir, path))
			if strings.Contains(string(content), "MaxRetries") {
				t.Errorf("%s still has old constant name", path)
			}
			if !strings.Contains(string(content), "RetryLimit") {
				t.Errorf("%s missing new constant name", path)
			}
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("test file call sites updated but TestFoo name preserved", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "math/math.go", "package math\n\nfunc Double(x int) int { return x * 2 }\n")
		writeTestFile(t, dir, "math/math_test.go", `package math

import "testing"

func TestDouble(t *testing.T) {
	if Double(3) != 6 {
		t.Fatal("wrong")
	}
}
`)

		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "Double",
			NewName: "Twice",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		mathContent, _ := os.ReadFile(filepath.Join(dir, "math/math.go"))
		if strings.Contains(string(mathContent), "func Double(") {
			t.Error("math.go definition not renamed")
		}
		if !strings.Contains(string(mathContent), "func Twice(") {
			t.Error("math.go missing new definition name")
		}

		testContent, _ := os.ReadFile(filepath.Join(dir, "math/math_test.go"))
		if strings.Contains(string(testContent), "\tif Double(") {
			t.Error("math_test.go call site not renamed")
		}
		if !strings.Contains(string(testContent), "\tif Twice(") {
			t.Error("math_test.go missing updated call site")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("same name in different package — only target renamed", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/a/a.go", `package a

func Helper() string { return "a" }
`)
		writeTestFile(t, dir, "pkg/b/b.go", `package b

func Helper() string { return "b" }
`)
		writeTestFile(t, dir, "main/main.go", `package main

import (
	"testmod.example.com/pkg/a"
	"testmod.example.com/pkg/b"
	"fmt"
)

func main() {
	fmt.Println(a.Helper(), b.Helper())
}
`)

		alphaFile := filepath.Join(dir, "pkg/a/a.go")
		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "Helper",
			NewName: "Assist",
			File:    alphaFile,
			Line:    3,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		aContent, _ := os.ReadFile(filepath.Join(dir, "pkg/a/a.go"))
		bContent, _ := os.ReadFile(filepath.Join(dir, "pkg/b/b.go"))
		if strings.Contains(string(aContent), "func Helper") {
			t.Error("a.Helper not renamed")
		}
		if !strings.Contains(string(bContent), "func Helper") {
			t.Error("b.Helper should be unchanged")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("local variable with file+line disambiguation", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "util/util.go", `package util

func Compute(x int) int {
	result := x * 2
	return result
}
`)

		filePath := filepath.Join(dir, "util/util.go")
		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "result",
			NewName: "output",
			File:    filePath,
			Line:    4,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		if strings.Contains(string(content), "\tresult") {
			t.Error("old local variable name still present")
		}
		if !strings.Contains(string(content), "output") {
			t.Error("new local variable name not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	// Probe: rename a parameter of a free function. Distinct from the
	// existing "local variable with file+line disambiguation" test
	// because parameters are declared in the FuncDecl's signature, not
	// the body — exercises whether FindSymbolObject finds the
	// types.Var for a *ast.Field name.
	t.Run("free function parameter via file+line", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "fn/fn.go", `package fn

func Add(amount int) int {
	return amount + 1
}
`)
		filePath := filepath.Join(dir, "fn/fn.go")
		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "amount",
			NewName: "delta",
			File:    filePath,
			Line:    3,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		got, _ := os.ReadFile(filePath)
		if strings.Contains(string(got), "amount") {
			t.Errorf("old param name still present: %s", got)
		}
		if !strings.Contains(string(got), "func Add(delta int)") || !strings.Contains(string(got), "delta + 1") {
			t.Errorf("param and body not both rewritten: %s", got)
		}
		verifyModuleIntegrity(t, dir)
	})

	// Probe: rename a parameter of a method (receiver present).
	// Adds the receiver dimension on top of the free-function case —
	// exercises whether FindSymbolObject's position-based lookup is
	// confused by the receiver field, which is structurally similar
	// to a parameter in the AST.
	t.Run("method parameter via file+line", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "m/m.go", `package m

type Counter struct{ n int }

func (c *Counter) Add(amount int) int {
	c.n += amount
	return c.n
}
`)
		filePath := filepath.Join(dir, "m/m.go")
		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "amount",
			NewName: "delta",
			File:    filePath,
			Line:    5,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		got, _ := os.ReadFile(filePath)
		if strings.Contains(string(got), "amount") {
			t.Errorf("old param name still present: %s", got)
		}
		if !strings.Contains(string(got), "Add(delta int)") || !strings.Contains(string(got), "+= delta") {
			t.Errorf("method param + body not both rewritten: %s", got)
		}
		verifyModuleIntegrity(t, dir)
	})

	// Probe: rename a parameter whose name shadows an imported
	// package — the exact case the user hit with `kind kind.Kind`.
	// Critical correctness check: the parameter `kind` and the
	// package selector `kind.Kind` (in the type expression on the
	// same line) are different types.Object identities, so the
	// rename MUST rewrite only the param's def + body uses and
	// leave the package selector untouched. Also verify the
	// package reference outside the function (`var Default =
	// kind.Zero`) is untouched.
	t.Run("parameter shadowing imported package", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "kind/kind.go", `package kind

type Kind int

const Zero Kind = 0
`)
		writeTestFile(t, dir, "consumer/consumer.go", `package consumer

import "testmod.example.com/kind"

var Default = kind.Zero

func Process(kind kind.Kind) bool {
	return kind == 0
}
`)
		filePath := filepath.Join(dir, "consumer/consumer.go")
		out := runRefactor(t, dir, Input{
			Action:  ActionRename,
			Symbol:  "kind",
			NewName: "k",
			File:    filePath,
			Line:    7, // the func Process(kind kind.Kind) line
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		got, _ := os.ReadFile(filePath)
		gs := string(got)
		// Param signature: name → k, type expression untouched.
		if !strings.Contains(gs, "func Process(k kind.Kind)") {
			t.Errorf("expected `func Process(k kind.Kind)`; got: %s", gs)
		}
		// Body use of the param: rewritten.
		if !strings.Contains(gs, "return k == 0") {
			t.Errorf("expected `return k == 0`; got: %s", gs)
		}
		// Package reference outside the function: untouched.
		if !strings.Contains(gs, "var Default = kind.Zero") {
			t.Errorf("expected `var Default = kind.Zero` untouched; got: %s", gs)
		}
		// Import line: untouched.
		if !strings.Contains(gs, `import "testmod.example.com/kind"`) {
			t.Errorf("import line should be untouched; got: %s", gs)
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("symbol not found (no file+line) nudges toward file+line", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg.go", "package main\n\nfunc main() {}\n")

		_, err := Handle(context.Background(), Input{
			Action:  ActionRename,
			Package: dir,
			Symbol:  "NoSuchSymbol",
			NewName: "Whatever",
		})
		if err == nil {
			t.Fatal("expected error for missing symbol, got nil")
		}
		// The error must include enough of the local/parameter hint to
		// route the agent toward providing file+line on the retry. We
		// check for the key phrasing — not the full sentence — so a
		// future wording tweak doesn't have to update this test.
		msg := err.Error()
		if !strings.Contains(msg, "file + line") || !strings.Contains(msg, "defining") {
			t.Errorf("error should nudge toward file+line of the defining identifier; got: %v", err)
		}
	})

	t.Run("symbol not found (with file+line miss) hints at use-vs-def", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "fn/fn.go", `package fn

func Use() int {
	x := 1
	return x + 1
}
`)
		filePath := filepath.Join(dir, "fn/fn.go")
		// Point Line at line 5 (the `return x + 1` *use*, not the def
		// on line 4). The resolver should miss because phase 2 scans
		// Defs only — and the error should explain why.
		_, err := Handle(t.Context(), Input{
			Action:  ActionRename,
			Symbol:  "x",
			NewName: "y",
			File:    filePath,
			Line:    5,
		})
		if err == nil {
			t.Fatal("expected error when File+Line points at a use site, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "defining") || !strings.Contains(msg, "use site") {
			t.Errorf("error should explain that the line must be the definition, not a use; got: %v", err)
		}
	})

	t.Run("dry run leaves disk untouched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "greet/greet.go", `package greet

func Hello() string { return "hello" }
`)

		originalContent, _ := os.ReadFile(filepath.Join(dir, "greet/greet.go"))

		out, err := Handle(t.Context(), Input{
			Action:  ActionRename,
			Package: dir,
			Symbol:  "Hello",
			NewName: "Hi",
			DryRun:  true,
		})
		if err != nil {
			t.Fatalf("dry run error: %v", err)
		}
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		afterContent, _ := os.ReadFile(filepath.Join(dir, "greet/greet.go"))
		if string(afterContent) != string(originalContent) {
			t.Error("dry run modified file on disk")
		}
	})

	t.Run("rollback on build failure restores disk", func(t *testing.T) {
		dir := setupTestModule(t)

		filePath := filepath.Join(dir, "app.go")
		original := []byte("package main\n\nfunc main() {}\n")
		if err := os.WriteFile(filePath, original, 0o644); err != nil {
			t.Fatal(err)
		}

		ws := &WorkspaceTransaction{
			modRoot:   dir,
			dryRun:    false,
			snapshots: make(map[string][]byte),
			modified:  make(map[string][]byte),
			deletions: make(map[string]bool),
		}

		broken := []byte("package main\n\nfunc main() {\n\tvar _ = undefinedIdentifier\n}\n")
		if err := ws.AddChange(filePath, original, broken, "broken"); err != nil {
			t.Fatalf("AddChange: %v", err)
		}

		_, commitErr := ws.Commit(t.Context())
		if commitErr == nil {
			t.Fatal("expected build failure, got nil")
		}

		onDisk, _ := os.ReadFile(filePath)
		if string(onDisk) != string(original) {
			t.Error("file not rolled back after build failure")
		}
	})
}
