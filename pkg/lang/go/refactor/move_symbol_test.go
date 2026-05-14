// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMoveSymbol(t *testing.T) {
	t.Run("function moved to new file in same package", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "app/main.go", `package app

func Run() int {
	return Helper() + 1
}

func Helper() int {
	return 42
}
`)

		srcFile := filepath.Join(dir, "app/main.go")
		dstFile := filepath.Join(dir, "app/helpers.go")

		out := runRefactor(t, dir, Input{
			Action:     ActionMoveSymbol,
			Symbol:     "Helper",
			File:       srcFile,
			TargetFile: dstFile,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		srcContent, _ := os.ReadFile(srcFile)
		dstContent, _ := os.ReadFile(dstFile)
		if strings.Contains(string(srcContent), "func Helper()") {
			t.Error("Helper still present in source file")
		}
		if !strings.Contains(string(dstContent), "func Helper()") {
			t.Error("Helper not found in target file")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("struct and its methods move together", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "eng/engine.go", `package eng

type Engine struct {
	Speed int
}

func (e *Engine) Start() string {
	return "started"
}

func (e *Engine) Stop() string {
	return "stopped"
}

func NewEngine(speed int) *Engine {
	return &Engine{Speed: speed}
}
`)

		srcFile := filepath.Join(dir, "eng/engine.go")
		dstFile := filepath.Join(dir, "eng/engine_impl.go")

		out := runRefactor(t, dir, Input{
			Action:     ActionMoveSymbol,
			Symbol:     "Engine",
			File:       srcFile,
			TargetFile: dstFile,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		dstContent, _ := os.ReadFile(dstFile)
		s := string(dstContent)
		if !strings.Contains(s, "type Engine struct") {
			t.Error("Engine struct not found in target file")
		}
		if !strings.Contains(s, "func (e *Engine) Start()") {
			t.Error("Start method not found in target file")
		}
		if !strings.Contains(s, "func (e *Engine) Stop()") {
			t.Error("Stop method not found in target file")
		}

		srcContent, _ := os.ReadFile(srcFile)
		if strings.Contains(string(srcContent), "type Engine struct") {
			t.Error("Engine struct still in source file")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("constant moved to new file", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "cfg/config.go", `package cfg

const MaxRetries = 3

func Retries() int {
	return MaxRetries
}
`)

		srcFile := filepath.Join(dir, "cfg/config.go")
		dstFile := filepath.Join(dir, "cfg/constants.go")

		out := runRefactor(t, dir, Input{
			Action:     ActionMoveSymbol,
			Symbol:     "MaxRetries",
			File:       srcFile,
			TargetFile: dstFile,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		dstContent, _ := os.ReadFile(dstFile)
		if !strings.Contains(string(dstContent), "MaxRetries") {
			t.Error("MaxRetries not found in target file")
		}

		srcContent, _ := os.ReadFile(srcFile)
		if strings.Contains(string(srcContent), "const MaxRetries") {
			t.Error("MaxRetries still present as const in source file")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("target file created with correct package header when missing", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "util/util.go", `package util

func Greet(name string) string {
	return "hello " + name
}

func Farewell(name string) string {
	return "goodbye " + name
}
`)

		srcFile := filepath.Join(dir, "util/util.go")
		dstFile := filepath.Join(dir, "util/farewell.go")

		if _, err := os.Stat(dstFile); err == nil {
			t.Fatal("target file should not exist before the test")
		}

		out := runRefactor(t, dir, Input{
			Action:     ActionMoveSymbol,
			Symbol:     "Farewell",
			File:       srcFile,
			TargetFile: dstFile,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		dstContent, _ := os.ReadFile(dstFile)
		s := string(dstContent)
		if !strings.Contains(s, "package util") {
			t.Error("package declaration missing from created target file")
		}
		if !strings.Contains(s, "func Farewell(") {
			t.Error("Farewell not found in target file")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("appends to existing target file", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "lib/lib.go", `package lib

func Alpha() int { return 1 }

func Beta() int { return 2 }
`)
		writeTestFile(t, dir, "lib/extra.go", `package lib

func Gamma() int { return 3 }
`)

		srcFile := filepath.Join(dir, "lib/lib.go")
		dstFile := filepath.Join(dir, "lib/extra.go")

		out := runRefactor(t, dir, Input{
			Action:     ActionMoveSymbol,
			Symbol:     "Beta",
			File:       srcFile,
			TargetFile: dstFile,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		dstContent, _ := os.ReadFile(dstFile)
		s := string(dstContent)
		if !strings.Contains(s, "func Gamma()") {
			t.Error("Gamma should still be in the target file")
		}
		if !strings.Contains(s, "func Beta()") {
			t.Error("Beta should have been appended to the target file")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("cross-package move returns descriptive error", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkga/a.go", "package pkga\n\nfunc Func() int { return 1 }\n")
		writeTestFile(t, dir, "pkgb/b.go", "package pkgb\n")

		_, err := Handle(t.Context(), Input{
			Action:     ActionMoveSymbol,
			Package:    dir,
			Symbol:     "Func",
			File:       filepath.Join(dir, "pkga/a.go"),
			TargetFile: filepath.Join(dir, "pkgb/b.go"),
		})
		if err == nil {
			t.Fatal("expected error for cross-package move")
		}
		if !strings.Contains(err.Error(), "cross-package") {
			t.Errorf("expected 'cross-package' in error, got: %v", err)
		}
	})

	t.Run("doc comments preserved in moved symbol", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "doc/doc.go", `package doc

// Compute performs a critical computation.
// It is important.
func Compute(n int) int {
	return n * n
}

func Other() {}
`)

		srcFile := filepath.Join(dir, "doc/doc.go")
		dstFile := filepath.Join(dir, "doc/compute.go")

		out := runRefactor(t, dir, Input{
			Action:     ActionMoveSymbol,
			Symbol:     "Compute",
			File:       srcFile,
			TargetFile: dstFile,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		dstContent, _ := os.ReadFile(dstFile)
		s := string(dstContent)
		if !strings.Contains(s, "// Compute performs a critical computation.") {
			t.Error("doc comment not preserved in target file")
		}
		if !strings.Contains(s, "func Compute(") {
			t.Error("Compute function not in target file")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("dry run leaves disk untouched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "dry/dry.go", `package dry

func MoveMe() int { return 99 }

func StayHere() int { return 0 }
`)

		srcFile := filepath.Join(dir, "dry/dry.go")
		dstFile := filepath.Join(dir, "dry/moved.go")
		original, _ := os.ReadFile(srcFile)

		out, err := Handle(t.Context(), Input{
			Action:     ActionMoveSymbol,
			Package:    dir,
			Symbol:     "MoveMe",
			File:       srcFile,
			TargetFile: dstFile,
			DryRun:     true,
		})
		if err != nil {
			t.Fatalf("dry run error: %v", err)
		}
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		after, _ := os.ReadFile(srcFile)
		if string(after) != string(original) {
			t.Error("dry run modified source file on disk")
		}
		if _, err := os.Stat(dstFile); err == nil {
			t.Error("dry run created target file on disk")
		}
	})
}
