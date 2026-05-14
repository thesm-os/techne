// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMovePackage(t *testing.T) {
	t.Run("basic — files moved and importers rewritten", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/old/old.go", `package old

func Hello() string { return "old" }
`)
		writeTestFile(t, dir, "main/main.go", `package main

import (
	"testmod.example.com/pkg/old"
)

func main() {
	_ = old.Hello()
}
`)

		out := runRefactor(t, dir, Input{
			Action:        ActionMovePackage,
			SourcePackage: "pkg/old",
			DestPackage:   "pkg/new",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		if _, err := os.Stat(filepath.Join(dir, "pkg/new/old.go")); err != nil {
			t.Errorf("new file not found: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "pkg/old/old.go")); err == nil {
			t.Error("old file should have been removed")
		}

		mainContent, _ := os.ReadFile(filepath.Join(dir, "main/main.go"))
		s := string(mainContent)
		if strings.Contains(s, "pkg/old") {
			t.Error("old import path still present in main.go")
		}
		if !strings.Contains(s, "pkg/new") {
			t.Error("new import path not found in main.go")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("multiple importers all rewritten", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "lib/core/core.go", `package core

func Compute(n int) int { return n * 2 }
`)
		writeTestFile(t, dir, "svc/alpha/alpha.go", `package alpha

import "testmod.example.com/lib/core"

func Run() int { return core.Compute(5) }
`)
		writeTestFile(t, dir, "svc/beta/beta.go", `package beta

import "testmod.example.com/lib/core"

func Run() int { return core.Compute(10) }
`)

		out := runRefactor(t, dir, Input{
			Action:        ActionMovePackage,
			SourcePackage: "lib/core",
			DestPackage:   "lib/engine",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		for _, relPath := range []string{"svc/alpha/alpha.go", "svc/beta/beta.go"} {
			content, _ := os.ReadFile(filepath.Join(dir, relPath))
			s := string(content)
			if strings.Contains(s, "lib/core") {
				t.Errorf("%s still contains old import path", relPath)
			}
			if !strings.Contains(s, "lib/engine") {
				t.Errorf("%s missing new import path", relPath)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "lib/engine/core.go")); err != nil {
			t.Error("moved file not found at lib/engine/core.go")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("dry run leaves disk untouched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/src/src.go", `package src

func Value() int { return 1 }
`)
		writeTestFile(t, dir, "main/main.go", `package main

import "testmod.example.com/pkg/src"

func main() { _ = src.Value() }
`)

		oldFile := filepath.Join(dir, "pkg/src/src.go")
		originalContent, _ := os.ReadFile(oldFile)

		out, err := Handle(t.Context(), Input{
			Action:        ActionMovePackage,
			Package:       dir,
			SourcePackage: "pkg/src",
			DestPackage:   "pkg/dst",
			DryRun:        true,
		})
		if err != nil {
			t.Fatalf("dry run error: %v", err)
		}
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		after, _ := os.ReadFile(oldFile)
		if string(after) != string(originalContent) {
			t.Error("dry run modified source file on disk")
		}
		if _, err := os.Stat(filepath.Join(dir, "pkg/dst/src.go")); err == nil {
			t.Error("dry run created destination file")
		}
	})

	t.Run("full import path accepted as source/dest", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/old/old.go", `package old

func Hello() string { return "old" }
`)
		writeTestFile(t, dir, "main/main.go", `package main

import "testmod.example.com/pkg/old"

func main() { _ = old.Hello() }
`)

		out := runRefactor(t, dir, Input{
			Action:        ActionMovePackage,
			SourcePackage: "testmod.example.com/pkg/old",
			DestPackage:   "testmod.example.com/pkg/new",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		if _, err := os.Stat(filepath.Join(dir, "pkg/new/old.go")); err != nil {
			t.Errorf("new file not found: %v", err)
		}
		main, _ := os.ReadFile(filepath.Join(dir, "main/main.go"))
		if !strings.Contains(string(main), "testmod.example.com/pkg/new") {
			t.Errorf("import not rewritten; got:\n%s", main)
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("go.work — source in sibling module", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, root, "go.work", "go 1.21\n\nuse (\n\t.\n\t./gen\n)\n")
		writeTestFile(t, root, "go.mod", "module example.com/root\n\ngo 1.21\n")
		writeTestFile(t, root, "main.go", `package root

import "example.com/root/gen/directive"

func Use() string { return directive.Name() }
`)
		writeTestFile(t, root, "gen/go.mod",
			"module example.com/root/gen\n\ngo 1.21\n\nreplace example.com/root => ../\n")
		writeTestFile(t, root, "gen/directive/handler.go",
			"package directive\n\nfunc Name() string { return \"d\" }\n")
		writeTestFile(t, root, "gen/suite/use.go", `package suite

import "example.com/root/gen/directive"

func Get() string { return directive.Name() }
`)

		out := runRefactor(t, root, Input{
			Action:        ActionMovePackage,
			SourcePackage: "example.com/root/gen/directive",
			DestPackage:   "example.com/root/gen/directiveparse",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		if _, err := os.Stat(filepath.Join(root, "gen/directiveparse/handler.go")); err != nil {
			t.Errorf("moved file missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "gen/directive/handler.go")); err == nil {
			t.Error("source file not removed")
		}
		use, _ := os.ReadFile(filepath.Join(root, "gen/suite/use.go"))
		if !strings.Contains(string(use), "example.com/root/gen/directiveparse") {
			t.Errorf("sibling-package importer not rewritten:\n%s", use)
		}
		mainContent, _ := os.ReadFile(filepath.Join(root, "main.go"))
		if !strings.Contains(string(mainContent), "example.com/root/gen/directiveparse") {
			t.Errorf("cross-module importer not rewritten:\n%s", mainContent)
		}
	})

	t.Run("sibling package with name prefix not mistakenly matched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/directive/d.go", "package directive\n\nfunc Name() string { return \"d\" }\n")
		writeTestFile(t, dir, "pkg/directives/dd.go", "package directives\n\nfunc Names() []string { return nil }\n")

		out := runRefactor(t, dir, Input{
			Action:        ActionMovePackage,
			SourcePackage: "pkg/directive",
			DestPackage:   "pkg/directiveparse",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		if _, err := os.Stat(filepath.Join(dir, "pkg/directiveparse/d.go")); err != nil {
			t.Errorf("expected source to move; got %v", err)
		}
		siblingBody, err := os.ReadFile(filepath.Join(dir, "pkg/directives/dd.go"))
		if err != nil {
			t.Fatalf("sibling file went missing: %v", err)
		}
		if !strings.Contains(string(siblingBody), "package directives") {
			t.Errorf("sibling package clause must not be rewritten:\n%s", siblingBody)
		}
		if _, err := os.Stat(filepath.Join(dir, "pkg/directiveparse/dd.go")); err == nil {
			t.Error("sibling file must not have been relocated to destination")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("cross-module: submodule to root module", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, root, "go.work", "go 1.21\n\nuse (\n\t.\n\t./gen\n)\n")
		writeTestFile(
			t,
			root,
			"go.mod",
			"module example.com/root\n\ngo 1.21\n\nrequire example.com/root/gen v0.0.0\n\nreplace example.com/root/gen => ./gen\n",
		)
		writeTestFile(t, root, "main.go", `package root

import "example.com/root/gen/directive"

func Use() string { return directive.Name() }
`)
		writeTestFile(t, root, "gen/go.mod",
			"module example.com/root/gen\n\ngo 1.21\n\nreplace example.com/root => ../\n")
		writeTestFile(t, root, "gen/directive/handler.go",
			"package directive\n\nfunc Name() string { return \"d\" }\n")

		out := runRefactor(t, root, Input{
			Action:        ActionMovePackage,
			SourcePackage: "example.com/root/gen/directive",
			DestPackage:   "example.com/root/directive",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		if _, err := os.Stat(filepath.Join(root, "directive/handler.go")); err != nil {
			t.Errorf("file should land in root module's directive/ dir; got %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "gen/directive/handler.go")); err == nil {
			t.Error("source file should be removed from gen module")
		}
		mainContent, _ := os.ReadFile(filepath.Join(root, "main.go"))
		if !strings.Contains(string(mainContent), "example.com/root/directive") {
			t.Errorf("importer must use new path:\n%s", mainContent)
		}
	})

	t.Run("cross-module: root module to submodule", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, root, "go.work", "go 1.21\n\nuse (\n\t.\n\t./gen\n)\n")
		writeTestFile(t, root, "go.mod", "module example.com/root\n\ngo 1.21\n")
		writeTestFile(t, root, "shared/types.go", "package shared\n\ntype T struct{}\n")
		writeTestFile(
			t,
			root,
			"gen/go.mod",
			"module example.com/root/gen\n\ngo 1.21\n\nrequire example.com/root v0.0.0\n\nreplace example.com/root => ../\n",
		)
		writeTestFile(t, root, "gen/main.go", `package gen

import "example.com/root/shared"

func Use() shared.T { return shared.T{} }
`)

		out := runRefactor(t, root, Input{
			Action:        ActionMovePackage,
			SourcePackage: "example.com/root/shared",
			DestPackage:   "example.com/root/gen/shared",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		if _, err := os.Stat(filepath.Join(root, "gen/shared/types.go")); err != nil {
			t.Errorf("file should land in submodule's shared/ dir; got %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "shared/types.go")); err == nil {
			t.Error("source file should be removed from root module")
		}
		gen, _ := os.ReadFile(filepath.Join(root, "gen/main.go"))
		if !strings.Contains(string(gen), "example.com/root/gen/shared") {
			t.Errorf("importer must use new path:\n%s", gen)
		}
	})

	t.Run("cross-module move emits go mod tidy advisory note", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, root, "go.work", "go 1.21\n\nuse (\n\t.\n\t./gen\n)\n")
		writeTestFile(t, root, "go.mod", "module example.com/root\n\ngo 1.21\n")
		writeTestFile(t, root, "shared/types.go", "package shared\n\ntype T struct{}\n")
		writeTestFile(
			t,
			root,
			"gen/go.mod",
			"module example.com/root/gen\n\ngo 1.21\n\nrequire example.com/root v0.0.0\n\nreplace example.com/root => ../\n",
		)
		writeTestFile(t, root, "gen/main.go", `package gen

import "example.com/root/shared"

func Use() shared.T { return shared.T{} }
`)

		out := runRefactor(t, root, Input{
			Action:        ActionMovePackage,
			SourcePackage: "example.com/root/shared",
			DestPackage:   "example.com/root/gen/shared",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success; got %s: %+v", out.Status, out.Results)
		}
		found := false
		for _, n := range out.Notes {
			if strings.Contains(n, "go mod tidy") && strings.Contains(n, "cross-module") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a tidy note for cross-module move; got Notes=%v", out.Notes)
		}
	})

	t.Run("same-module move emits no tidy note", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/old/old.go", "package old\n\nfunc Hello() string { return \"old\" }\n")

		out := runRefactor(t, dir, Input{
			Action:        ActionMovePackage,
			SourcePackage: "pkg/old",
			DestPackage:   "pkg/new",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success; got %s: %+v", out.Status, out.Results)
		}
		for _, n := range out.Notes {
			if strings.Contains(n, "go mod tidy") {
				t.Errorf("same-module move should not emit a tidy note; got: %q", n)
			}
		}
	})

	t.Run("godoc links in importers updated when package is renamed", func(t *testing.T) {
		// Moving lib/core (package "core") to lib/engine (package "engine").
		// An importer with a [core.Compute] godoc link must have it rewritten to
		// [engine.Compute] because the package name changed.
		dir := setupTestModule(t)
		writeTestFile(t, dir, "lib/core/core.go", "package core\n\nfunc Compute(n int) int { return n * 2 }\n")
		writeTestFile(t, dir, "svc/api/api.go", `package api

import "testmod.example.com/lib/core"

// Run calls [core.Compute] to perform computation.
func Run(n int) int { return core.Compute(n) }
`)

		out := runRefactor(t, dir, Input{
			Action:        ActionMovePackage,
			SourcePackage: "lib/core",
			DestPackage:   "lib/engine",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		api, _ := os.ReadFile(filepath.Join(dir, "svc/api/api.go"))
		s := string(api)
		if strings.Contains(s, "[core.Compute]") {
			t.Errorf("stale [core.Compute] link not updated; got:\n%s", s)
		}
		if !strings.Contains(s, "[engine.Compute]") {
			t.Errorf("expected [engine.Compute] link after rename; got:\n%s", s)
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("external test file keeps _test suffix on move", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg/old/old.go", "package old\n\nfunc Hello() string { return \"hi\" }\n")
		writeTestFile(t, dir, "pkg/old/old_test.go", `package old_test

import (
	"testing"
	"testmod.example.com/pkg/old"
)

func TestHello(t *testing.T) {
	if old.Hello() != "hi" { t.Fail() }
}
`)

		out := runRefactor(t, dir, Input{
			Action:        ActionMovePackage,
			SourcePackage: "pkg/old",
			DestPackage:   "pkg/new",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		prod, _ := os.ReadFile(filepath.Join(dir, "pkg/new/old.go"))
		if !strings.Contains(string(prod), "package new\n") {
			t.Errorf("production file should declare package new; got:\n%s", prod)
		}

		test, _ := os.ReadFile(filepath.Join(dir, "pkg/new/old_test.go"))
		if !strings.Contains(string(test), "package new_test\n") {
			t.Errorf("external test must keep _test suffix; got:\n%s", test)
		}
		if strings.Contains(string(test), "package new\n") {
			t.Errorf("external test must NOT lose _test suffix; got:\n%s", test)
		}
		verifyModuleIntegrity(t, dir)
	})
}
