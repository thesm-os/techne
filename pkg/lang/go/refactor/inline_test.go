// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInline(t *testing.T) {
	t.Run("integer constant — all usages replaced, definition preserved", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "config/config.go", `package config

const MaxRetries = 3

func Attempts() int {
	return MaxRetries
}

func Double() int {
	return MaxRetries * 2
}

func Triple() int {
	return MaxRetries * 3
}
`)

		filePath := filepath.Join(dir, "config/config.go")
		out := runRefactor(t, dir, Input{Action: ActionInline, Symbol: "MaxRetries"})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if strings.Contains(s, "return MaxRetries") {
			t.Error("usage of MaxRetries not inlined in Attempts()")
		}
		if strings.Contains(s, "MaxRetries * 2") {
			t.Error("usage of MaxRetries not inlined in Double()")
		}
		if strings.Contains(s, "MaxRetries * 3") {
			t.Error("usage of MaxRetries not inlined in Triple()")
		}
		if !strings.Contains(s, "const MaxRetries = 3") {
			t.Error("const definition should be preserved")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("string constant — literal substituted at use sites", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "greet/greet.go", `package greet

import "fmt"

const Greeting = "hello"

func Greet() {
	fmt.Println(Greeting)
}

func ShoutGreet() string {
	return Greeting + "!!!"
}
`)

		filePath := filepath.Join(dir, "greet/greet.go")
		out := runRefactor(t, dir, Input{Action: ActionInline, Symbol: "Greeting"})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(filePath)
		s := string(content)
		if strings.Contains(s, "Println(Greeting)") {
			t.Error("Greeting not inlined in Greet()")
		}
		if strings.Contains(s, "Greeting + ") {
			t.Error("Greeting not inlined in ShoutGreet()")
		}
		if !strings.Contains(s, `"hello"`) {
			t.Error("literal string not present")
		}
		if !strings.Contains(s, `const Greeting = "hello"`) {
			t.Error("const definition should be preserved")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("multi-file same package — sibling file uses updated", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "config/constants.go", "package config\n\nconst Timeout = 30\n")
		writeTestFile(t, dir, "config/logic.go", `package config

func IsExpired(elapsed int) bool {
	return elapsed > Timeout
}

func IsDouble(elapsed int) bool {
	return elapsed > Timeout*2
}
`)

		logicFile := filepath.Join(dir, "config/logic.go")
		out := runRefactor(t, dir, Input{Action: ActionInline, Symbol: "Timeout"})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		content, _ := os.ReadFile(logicFile)
		s := string(content)
		if strings.Contains(s, "Timeout") {
			t.Error("Timeout usage not inlined in logic.go")
		}
		if !strings.Contains(s, "30") {
			t.Error("literal value 30 not found in logic.go")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("variable not supported — descriptive error", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "vars/vars.go", "package vars\n\nvar Count = 5\n\nfunc Get() int { return Count }\n")

		_, err := Handle(t.Context(), Input{
			Action:  ActionInline,
			Package: dir,
			Symbol:  "Count",
		})
		if err == nil {
			t.Fatal("expected error for variable inline, got nil")
		}
		if !strings.Contains(err.Error(), "not supported") && !strings.Contains(err.Error(), "variable") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("function not supported — descriptive error", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(
			t,
			dir,
			"fn/fn.go",
			"package fn\n\nfunc Helper() int { return 42 }\n\nfunc Use() int { return Helper() }\n",
		)

		_, err := Handle(t.Context(), Input{
			Action:  ActionInline,
			Package: dir,
			Symbol:  "Helper",
		})
		if err == nil {
			t.Fatal("expected error for function inline, got nil")
		}
		if !strings.Contains(err.Error(), "not supported") && !strings.Contains(err.Error(), "function") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	// Regression for B2: the `Package` arg was being silently dropped by
	// the symbol resolver. Reproducer mirrors the user's eidos scenario:
	// `Default` is declared as a const in priority/ and as a function in
	// three other packages. Calling inline_constant with the const's
	// package path used to pick up one of the functions instead and bail
	// with "inlining functions is not supported".
	t.Run("Package arg disambiguates same-named symbols across packages", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "priority/priority.go", `package priority

const Default = 5

func Get() int { return Default }
`)
		writeTestFile(t, dir, "directive/directive.go", "package directive\n\nfunc Default() int { return 1 }\n")
		writeTestFile(t, dir, "naming/naming.go", "package naming\n\nfunc Default() int { return 2 }\n")
		writeTestFile(t, dir, "builder/builder.go", "package builder\n\nfunc Default() int { return 3 }\n")
		t.Chdir(dir)

		// Resolve the priority package by import path (the form the
		// user passed). Without the package filter the resolver would
		// hit one of the functions first and refuse the inline.
		out := runRefactor(t, dir, Input{
			Action:  ActionInline,
			Symbol:  "Default",
			Package: "testmod.example.com/priority",
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success when Package narrows the lookup; got %s: %+v", out.Status, out.Results)
		}
		content, _ := os.ReadFile(filepath.Join(dir, "priority/priority.go"))
		s := string(content)
		// Definition preserved; only Use replaced with literal 5.
		if !strings.Contains(s, "const Default = 5") {
			t.Errorf("expected const definition preserved; got:\n%s", s)
		}
		if !strings.Contains(s, "return 5") {
			t.Errorf("expected Use inlined to literal 5; got:\n%s", s)
		}
		// Functions in sibling packages must be untouched.
		for _, pkg := range []string{"directive", "naming", "builder"} {
			body, _ := os.ReadFile(filepath.Join(dir, pkg, pkg+".go"))
			if !strings.Contains(string(body), "func Default()") {
				t.Errorf("%s.Default function should be untouched; got:\n%s", pkg, body)
			}
		}
		verifyModuleIntegrity(t, dir)
	})

	// Negative companion: when the named symbol does not exist in the
	// supplied package, the resolver MUST NOT silently fall through to
	// matches in other packages. The user explicitly noted "never
	// disregard a supplied package".
	t.Run("Package arg is honored — symbol absent from package returns error", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "a/a.go", "package a\n\nconst Pi = 3\n\nfunc Use() int { return Pi }\n")
		writeTestFile(t, dir, "b/b.go", "package b\n\nconst Pi = 4\n")
		t.Chdir(dir)

		_, err := Handle(t.Context(), Input{
			Action:  ActionInline,
			Symbol:  "Pi",
			Package: "testmod.example.com/b", // Pi exists here but has no Uses
		})
		if err == nil {
			t.Fatal("expected error: Pi has no Uses inside package b; resolver must not fall back to package a")
		}
	})

	t.Run("dry run leaves disk untouched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "dry/dry.go", `package dry

const Pi = 3

func Area(r int) int {
	return Pi * r * r
}
`)

		filePath := filepath.Join(dir, "dry/dry.go")
		original, _ := os.ReadFile(filePath)

		out, err := Handle(t.Context(), Input{
			Action:  ActionInline,
			Package: dir,
			Symbol:  "Pi",
			DryRun:  true,
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
