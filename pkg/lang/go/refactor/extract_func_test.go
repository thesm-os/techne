// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractFunction(t *testing.T) {
	t.Run("basic — range extracted into free function", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "calc/calc.go", `package calc

func Add(a, b int) int {
	sum := a + b
	return sum
}
`)

		filePath := filepath.Join(dir, "calc/calc.go")
		out := runRefactor(t, dir, Input{
			Action:      ActionExtractFunction,
			File:        filePath,
			StartLine:   4,
			EndLine:     4,
			NewFuncName: "computeSum",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if !strings.Contains(s, "func computeSum(") {
			t.Error("extracted function not found")
		}
		if !strings.Contains(s, "computeSum(") {
			t.Error("call site not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("method receiver — extracted function keeps receiver", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "svc/svc.go", `package svc

type Service struct{ val int }

func (s *Service) Process() int {
	result := s.val * 2
	return result
}
`)

		filePath := filepath.Join(dir, "svc/svc.go")
		out := runRefactor(t, dir, Input{
			Action:      ActionExtractFunction,
			File:        filePath,
			StartLine:   6,
			EndLine:     6,
			NewFuncName: "doubleVal",
			Receiver:    "s *Service",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if !strings.Contains(s, "func (s *Service) doubleVal(") {
			t.Error("receiver method not found")
		}
		if !strings.Contains(s, "s.doubleVal(") {
			t.Error("receiver call site not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("multiple outer-scope params become parameters", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "math/math.go", `package math

func Compute(x, y, z int) int {
	total := x + y + z
	return total
}
`)

		filePath := filepath.Join(dir, "math/math.go")
		out := runRefactor(t, dir, Input{
			Action:      ActionExtractFunction,
			File:        filePath,
			StartLine:   4,
			EndLine:     4,
			NewFuncName: "sumThree",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		if !strings.Contains(string(content), "func sumThree(") {
			t.Error("extracted function not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("declared var used after extraction becomes return value", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "proc/proc.go", `package proc

func Run(n int) int {
	doubled := n * 2
	return doubled
}
`)

		filePath := filepath.Join(dir, "proc/proc.go")
		out := runRefactor(t, dir, Input{
			Action:      ActionExtractFunction,
			File:        filePath,
			StartLine:   4,
			EndLine:     4,
			NewFuncName: "doubleN",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if !strings.Contains(s, "return doubled") {
			t.Error("extracted function missing return")
		}
		if !strings.Contains(s, "doubled :=") {
			t.Error("call site missing := assignment")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("no params needed — block uses only literals", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "hello/hello.go", `package hello

import "fmt"

func Greet() {
	fmt.Println("hello world")
}
`)

		filePath := filepath.Join(dir, "hello/hello.go")
		out := runRefactor(t, dir, Input{
			Action:      ActionExtractFunction,
			File:        filePath,
			StartLine:   6,
			EndLine:     6,
			NewFuncName: "printGreeting",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if !strings.Contains(s, "func printGreeting()") {
			t.Error("extracted function not found")
		}
		if !strings.Contains(s, "printGreeting()") {
			t.Error("call site not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("cross-file extraction — function lands in target file", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "app/app.go", `package app

func Run(n int) int {
	result := n + 1
	return result
}
`)

		srcFile := filepath.Join(dir, "app/app.go")
		dstFile := filepath.Join(dir, "app/helpers.go")

		out := runRefactor(t, dir, Input{
			Action:      ActionExtractFunction,
			File:        srcFile,
			StartLine:   4,
			EndLine:     4,
			NewFuncName: "increment",
			TargetFile:  dstFile,
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		srcContent, _ := os.ReadFile(srcFile)
		dstContent, _ := os.ReadFile(dstFile)
		if !strings.Contains(string(srcContent), "increment(") {
			t.Error("call site not found in source file")
		}
		if !strings.Contains(string(dstContent), "func increment(") {
			t.Error("extracted function not found in target file")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("invalid line range returns error", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg.go", "package main\n\nfunc main() {}\n")
		filePath := filepath.Join(dir, "pkg.go")

		_, err := Handle(t.Context(), Input{
			Action:      ActionExtractFunction,
			Package:     dir,
			File:        filePath,
			StartLine:   5,
			EndLine:     3,
			NewFuncName: "foo",
		})
		if err == nil {
			t.Fatal("expected error for start > end, got nil")
		}

		_, err = Handle(t.Context(), Input{
			Action:      ActionExtractFunction,
			Package:     dir,
			File:        filePath,
			StartLine:   999,
			EndLine:     999,
			NewFuncName: "foo",
		})
		if err == nil {
			t.Fatal("expected error for out-of-range lines, got nil")
		}
	})

	t.Run("dry run leaves disk untouched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "dry/dry.go", `package dry

func Work(x int) int {
	y := x * 3
	return y
}
`)

		filePath := filepath.Join(dir, "dry/dry.go")
		original, _ := os.ReadFile(filePath)

		out, err := Handle(t.Context(), Input{
			Action:      ActionExtractFunction,
			Package:     dir,
			File:        filePath,
			StartLine:   4,
			EndLine:     4,
			NewFuncName: "triple",
			DryRun:      true,
		})
		if err != nil {
			t.Fatalf("dry run error: %v", err)
		}
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		after, _ := os.ReadFile(filePath)
		if string(after) != string(original) {
			t.Error("dry run modified file on disk")
		}
	})
}
