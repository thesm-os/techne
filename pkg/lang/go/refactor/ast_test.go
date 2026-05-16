// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// ---------------------------------------------------------------------------
// Helpers local to ast tests (use packages.Load directly, not Handle)
// ---------------------------------------------------------------------------

func buildTestModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte("module testmod.example.com\n\ngo 1.21\n"),
		0o644,
	); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

func loadTestPackages(t *testing.T, dir string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedCompiledGoFiles,
		Dir:   dir,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Logf("package error in %s: %v", p.PkgPath, e)
		}
	}
	return pkgs
}

const coreTypesContent = `package core

type Engine struct{}

func (e *Engine) Run() {}

func NewEngine() *Engine { return &Engine{} }

type Storage interface { Save() error }

const MaxRetries = 3

var DefaultName = "test"
`

const utilContent = `package util

func Helper() string { return "help" }
`

func setupSymbolModule(t *testing.T) []*packages.Package {
	t.Helper()
	dir := buildTestModule(t, map[string]string{
		"pkg/core/types.go": coreTypesContent,
		"pkg/util/util.go":  utilContent,
	})
	return loadTestPackages(t, dir)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFindSymbolObject(t *testing.T) {
	t.Run("exported function", func(t *testing.T) {
		pkgs := setupSymbolModule(t)
		obj := FindSymbolObject(pkgs, "NewEngine", "", "", 0)
		if obj == nil {
			t.Fatal("expected non-nil object for NewEngine")
		}
		if _, ok := obj.(*types.Func); !ok {
			t.Errorf("expected *types.Func, got %T", obj)
		}
	})

	t.Run("method on struct", func(t *testing.T) {
		pkgs := setupSymbolModule(t)
		obj := FindSymbolObject(pkgs, "Engine.Run", "", "", 0)
		if obj == nil {
			t.Fatal("expected non-nil object for Engine.Run")
		}
		if _, ok := obj.(*types.Func); !ok {
			t.Errorf("expected *types.Func, got %T", obj)
		}
	})

	t.Run("interface", func(t *testing.T) {
		pkgs := setupSymbolModule(t)
		obj := FindSymbolObject(pkgs, "Storage", "", "", 0)
		if obj == nil {
			t.Fatal("expected non-nil object for Storage interface")
		}
	})

	t.Run("constant", func(t *testing.T) {
		pkgs := setupSymbolModule(t)
		obj := FindSymbolObject(pkgs, "MaxRetries", "", "", 0)
		if obj == nil {
			t.Fatal("expected non-nil object for MaxRetries")
		}
		if _, ok := obj.(*types.Const); !ok {
			t.Errorf("expected *types.Const, got %T", obj)
		}
	})

	t.Run("variable", func(t *testing.T) {
		pkgs := setupSymbolModule(t)
		obj := FindSymbolObject(pkgs, "DefaultName", "", "", 0)
		if obj == nil {
			t.Fatal("expected non-nil object for DefaultName")
		}
		if _, ok := obj.(*types.Var); !ok {
			t.Errorf("expected *types.Var, got %T", obj)
		}
	})

	t.Run("not found returns nil", func(t *testing.T) {
		pkgs := setupSymbolModule(t)
		obj := FindSymbolObject(pkgs, "NonExistent", "", "", 0)
		if obj != nil {
			t.Errorf("expected nil for unknown symbol, got %T %v", obj, obj)
		}
	})

	t.Run("local variable resolved with file+line hint", func(t *testing.T) {
		const src = `package core

func Alpha() string {
	result := "alpha"
	return result
}
`
		dir := buildTestModule(t, map[string]string{"pkg/core/local.go": src})
		pkgs := loadTestPackages(t, dir)

		absFile := filepath.Join(dir, "pkg/core/local.go")
		obj := FindSymbolObject(pkgs, "result", "", absFile, 4)
		if obj == nil {
			t.Fatal("expected non-nil object for local 'result' with file+line hint")
		}
	})

	t.Run("ambiguous local without file+line returns nil", func(t *testing.T) {
		const src = `package core

func Alpha() string {
	result := "alpha"
	return result
}

func Beta() string {
	result := "beta"
	return result
}
`
		dir := buildTestModule(t, map[string]string{"pkg/core/ambiguous.go": src})
		pkgs := loadTestPackages(t, dir)

		obj := FindSymbolObject(pkgs, "result", "", "", 0)
		if obj != nil {
			t.Errorf("expected nil for ambiguous 'result' without file+line, got %T %v", obj, obj)
		}
	})

	t.Run("ambiguous local disambiguated by file+line", func(t *testing.T) {
		const src = `package core

func Alpha() string {
	result := "alpha"
	return result
}

func Beta() string {
	result := "beta"
	return result
}
`
		dir := buildTestModule(t, map[string]string{"pkg/core/ambiguous2.go": src})
		pkgs := loadTestPackages(t, dir)
		absFile := filepath.Join(dir, "pkg/core/ambiguous2.go")

		objAlpha := FindSymbolObject(pkgs, "result", "", absFile, 4)
		if objAlpha == nil {
			t.Fatal("expected non-nil object for Alpha's 'result'")
		}
		objBeta := FindSymbolObject(pkgs, "result", "", absFile, 9)
		if objBeta == nil {
			t.Fatal("expected non-nil object for Beta's 'result'")
		}
		if objAlpha == objBeta {
			t.Error("expected Alpha and Beta 'result' to be distinct objects")
		}
	})

	t.Run("pointer-receiver method found via type name", func(t *testing.T) {
		pkgs := setupSymbolModule(t)
		obj := FindSymbolObject(pkgs, "Engine.Run", "", "", 0)
		if obj == nil {
			t.Fatal("expected non-nil for pointer-receiver method Engine.Run")
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			t.Fatalf("expected *types.Func, got %T", obj)
		}
		if fn.Name() != "Run" {
			t.Errorf("expected method name Run, got %s", fn.Name())
		}
	})
}

func TestValidateGoSource(t *testing.T) {
	t.Run("valid Go", func(t *testing.T) {
		src := []byte("package foo\nfunc F() {}\n")
		if err := ValidateGoSource("test.go", src); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("empty content returns error", func(t *testing.T) {
		err := ValidateGoSource("test.go", []byte{})
		if err == nil {
			t.Fatal("expected error for empty content, got nil")
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("expected 'empty' in error, got: %v", err)
		}
	})

	t.Run("invalid syntax returns error", func(t *testing.T) {
		err := ValidateGoSource("test.go", []byte("not valid go"))
		if err == nil {
			t.Fatal("expected error for invalid Go, got nil")
		}
	})

	t.Run("package-only declaration is valid", func(t *testing.T) {
		src := []byte("package foo\n")
		if err := ValidateGoSource("test.go", src); err != nil {
			t.Errorf("expected nil for minimal valid Go, got %v", err)
		}
	})

	t.Run("nil content returns error", func(t *testing.T) {
		err := ValidateGoSource("test.go", nil)
		if err == nil {
			t.Fatal("expected error for nil content, got nil")
		}
	})
}

func TestResolveModuleRoot(t *testing.T) {
	t.Run("go.mod in current directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := ResolveModuleRoot(dir)
		if got != dir {
			t.Errorf("want %s, got %s", dir, got)
		}
	})

	t.Run("go.mod in parent directory", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.WriteFile(filepath.Join(parent, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(parent, "sub", "dir")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		got := ResolveModuleRoot(sub)
		if got != parent {
			t.Errorf("want %s, got %s", parent, got)
		}
	})

	t.Run("no go.mod falls back to start dir", func(t *testing.T) {
		dir := t.TempDir()
		got := ResolveModuleRoot(dir)
		if got == "" {
			t.Error("expected non-empty fallback dir")
		}
	})
}

func TestExprText(t *testing.T) {
	t.Run("simple identifier", func(t *testing.T) {
		src := []byte("package p\nvar _ = foo\n")
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "p.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		var ident *ast.Ident
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "foo" {
				ident = id
			}
			return true
		})
		if ident == nil {
			t.Fatal("could not find 'foo' ident in AST")
		}
		got := ExprText(fset, src, ident)
		if got != "foo" {
			t.Errorf("want %q, got %q", "foo", got)
		}
	})

	t.Run("call expression", func(t *testing.T) {
		src := []byte("package p\nvar _ = foo(1, 2)\n")
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "p.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		var call *ast.CallExpr
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				call = c
			}
			return true
		})
		if call == nil {
			t.Fatal("could not find call expression in AST")
		}
		got := ExprText(fset, src, call)
		if got != "foo(1, 2)" {
			t.Errorf("want %q, got %q", "foo(1, 2)", got)
		}
	})

	t.Run("nil expression returns empty string", func(t *testing.T) {
		fset := token.NewFileSet()
		got := ExprText(fset, []byte("package p\n"), nil)
		if got != "" {
			t.Errorf("expected empty string for nil expr, got %q", got)
		}
	})

	t.Run("out-of-range offsets trigger format.Node fallback", func(t *testing.T) {
		src := []byte("package p\nvar _ = foo(1, 2)\n")
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "p.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		var call *ast.CallExpr
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				call = c
			}
			return true
		})
		if call == nil {
			t.Fatal("could not find call expression in AST")
		}
		got := ExprText(fset, src[:5], call)
		if got == "" {
			t.Error("expected non-empty fallback from format.Node")
		}
	})
}

func TestResolveWorkDir(t *testing.T) {
	t.Run("empty package string", func(t *testing.T) {
		dir, pattern := ResolveWorkDir("")
		if dir == "" {
			t.Error("expected non-empty dir for empty package")
		}
		if pattern != "./..." {
			t.Errorf("want %q, got %q", "./...", pattern)
		}
	})

	t.Run("dot package", func(t *testing.T) {
		dir, pattern := ResolveWorkDir(".")
		if dir == "" {
			t.Error("expected non-empty dir for '.' package")
		}
		if pattern != "./..." {
			t.Errorf("want %q, got %q", "./...", pattern)
		}
	})

	t.Run("absolute path to existing directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		dir, pattern := ResolveWorkDir(tmpDir)
		if dir != tmpDir {
			t.Errorf("want %s, got %s", tmpDir, dir)
		}
		if pattern != "./..." {
			t.Errorf("want %q, got %q", "./...", pattern)
		}
	})

	t.Run("absolute path to nonexistent directory falls back to cwd", func(t *testing.T) {
		nonexistent := filepath.Join(t.TempDir(), "does_not_exist")
		cwd, _ := os.Getwd()
		dir, pattern := ResolveWorkDir(nonexistent)
		if dir != cwd {
			t.Errorf("want cwd %s, got %s", cwd, dir)
		}
		if pattern != "./..." {
			t.Errorf("want %q, got %q", "./...", pattern)
		}
	})

	t.Run("absolute subdirectory path", func(t *testing.T) {
		tmpDir := t.TempDir()
		subDir := filepath.Join(tmpDir, "mysubpkg")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dir, pattern := ResolveWorkDir(subDir)
		if dir == "" {
			t.Error("expected non-empty dir for absolute subdir path")
		}
		if pattern != "./..." {
			t.Errorf("want %q, got %q", "./...", pattern)
		}
	})

	t.Run("module-relative path that does not exist falls back to cwd", func(t *testing.T) {
		cwd, _ := os.Getwd()
		dir, pattern := ResolveWorkDir("pkg/nonexistent/subpkg")
		if dir != cwd {
			t.Errorf("want cwd %s, got %s", cwd, dir)
		}
		if !strings.HasPrefix(pattern, "./") {
			t.Errorf("expected pattern to start with './', got %q", pattern)
		}
	})
}
