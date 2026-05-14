// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractVariable(t *testing.T) {
	t.Run("if condition extracted by line", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "svc/svc.go", `package svc

type User struct {
	Age    int
	Active bool
}

func Check(user User) bool {
	if user.Age > 18 && user.Active {
		return true
	}
	return false
}
`)

		filePath := filepath.Join(dir, "svc/svc.go")
		out := runRefactor(t, dir, Input{
			Action:       ActionExtractVariable,
			File:         filePath,
			Line:         9,
			VariableName: "isEligible",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if !strings.Contains(s, "isEligible :=") {
			t.Error("variable declaration not found")
		}
		if !strings.Contains(s, "if isEligible {") {
			t.Error("if condition not replaced with variable")
		}
		if strings.Contains(s, "if user.Age > 18 && user.Active {") {
			t.Error("original condition still present in if statement")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("extract sub-expression with column range", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "math/math.go", `package math

func Calc(a, b int) int {
	return a + b
}
`)

		filePath := filepath.Join(dir, "math/math.go")
		out := runRefactor(t, dir, Input{
			Action:       ActionExtractVariable,
			File:         filePath,
			Line:         4,
			StartCol:     9,
			EndCol:       14,
			VariableName: "sum",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if !strings.Contains(s, "sum :=") {
			t.Error("variable declaration not found")
		}
		if !strings.Contains(s, "return sum") {
			t.Error("return not updated to use variable")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("assignment RHS becomes extracted variable", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "proc/proc.go", `package proc

func compute(a, b int) int { return a * b }

func Run(a, b, offset int) int {
	result := compute(a, b) + offset
	return result
}
`)

		filePath := filepath.Join(dir, "proc/proc.go")
		out := runRefactor(t, dir, Input{
			Action:       ActionExtractVariable,
			File:         filePath,
			Line:         6,
			VariableName: "baseResult",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		content, _ := os.ReadFile(filePath)
		if !strings.Contains(string(content), "baseResult :=") {
			t.Error("variable declaration not found")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("extracted variable matches surrounding indentation", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "indent/indent.go", `package indent

func Outer() {
	if true {
		if 1+1 == 2 {
			return
		}
	}
}
`)

		filePath := filepath.Join(dir, "indent/indent.go")
		out := runRefactor(t, dir, Input{
			Action:       ActionExtractVariable,
			File:         filePath,
			Line:         5,
			VariableName: "mathCheck",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if !strings.Contains(s, "mathCheck :=") {
			t.Error("variable declaration not found")
		}
		for l := range strings.SplitSeq(s, "\n") {
			if strings.Contains(l, "mathCheck :=") && !strings.HasPrefix(l, "\t\t") {
				t.Errorf("expected double-tab indent, got: %q", l)
			}
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("missing file returns error", func(t *testing.T) {
		_, err := Handle(t.Context(), Input{
			Action:       ActionExtractVariable,
			Line:         5,
			VariableName: "x",
		})
		if err == nil {
			t.Fatal("expected error when File is missing")
		}
		if !strings.Contains(err.Error(), "file") {
			t.Errorf("expected 'file' in error, got: %v", err)
		}
	})

	t.Run("dry run leaves disk untouched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "dry/dry.go", `package dry

func Work(n int) int {
	if n > 10 {
		return n
	}
	return 0
}
`)

		filePath := filepath.Join(dir, "dry/dry.go")
		original, _ := os.ReadFile(filePath)

		out, err := Handle(t.Context(), Input{
			Action:       ActionExtractVariable,
			Package:      dir,
			File:         filePath,
			Line:         4,
			VariableName: "bigN",
			DryRun:       true,
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

	t.Run("return value extracted into named variable", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "ret/ret.go", `package ret

func Double(n int) int {
	return n * 2
}
`)

		filePath := filepath.Join(dir, "ret/ret.go")
		out := runRefactor(t, dir, Input{
			Action:       ActionExtractVariable,
			File:         filePath,
			Line:         4,
			VariableName: "doubled",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if !strings.Contains(s, "doubled :=") {
			t.Error("variable declaration not found")
		}
		if !strings.Contains(s, "return doubled") {
			t.Error("return not updated to use variable")
		}
		verifyModuleIntegrity(t, dir)
	})
}
