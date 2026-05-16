// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignature(t *testing.T) {
	t.Run("add parameter — all callers receive default value", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "greet/greet.go", `package greet

func Hello(name string) string {
	return "hello " + name
}
`)
		writeTestFile(t, dir, "main/main.go", `package main

import (
	"testmod.example.com/greet"
	"fmt"
)

func main() {
	fmt.Println(greet.Hello("world"))
	fmt.Println(greet.Hello("go"))
	fmt.Println(greet.Hello("test"))
}
`)

		out := runRefactor(t, dir, Input{
			Action: ActionChangeSignature,
			Symbol: "Hello",
			AddParams: []AddParameter{
				{Name: "age", Type: "int", DefaultValue: "0"},
			},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		defContent, _ := os.ReadFile(filepath.Join(dir, "greet/greet.go"))
		if !strings.Contains(string(defContent), "age int") {
			t.Error("definition missing new parameter")
		}

		mainContent, _ := os.ReadFile(filepath.Join(dir, "main/main.go"))
		count := strings.Count(string(mainContent), `Hello("world", 0)`) +
			strings.Count(string(mainContent), `Hello("go", 0)`) +
			strings.Count(string(mainContent), `Hello("test", 0)`)
		if count != 3 {
			t.Errorf("expected 3 updated call sites, found:\n%s", mainContent)
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("add return value — definition updated", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "math/math.go", `package math

func Double(x int) int {
	panic("not implemented")
}
`)
		writeTestFile(t, dir, "app/app.go", `package app

import "testmod.example.com/math"

func Run() {
	math.Double(5)
}
`)

		out := runRefactor(t, dir, Input{
			Action: ActionChangeSignature,
			Symbol: "Double",
			AddReturns: []AddReturn{
				{Type: "error", DefaultValue: "_"},
			},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		defContent, _ := os.ReadFile(filepath.Join(dir, "math/math.go"))
		if !strings.Contains(string(defContent), "error") {
			t.Error("definition missing new return type")
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("remove parameter — definition and call sites updated", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "logger/logger.go", `package logger

func Log(msg string, verbose bool) {
	_ = msg
}
`)
		writeTestFile(t, dir, "svc/svc.go", `package svc

import "testmod.example.com/logger"

func doWork() {
	logger.Log("starting", true)
	logger.Log("done", false)
}
`)

		out := runRefactor(t, dir, Input{
			Action:       ActionChangeSignature,
			Symbol:       "Log",
			RemoveParams: []string{"verbose"},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}

		defContent, _ := os.ReadFile(filepath.Join(dir, "logger/logger.go"))
		if strings.Contains(string(defContent), "verbose") {
			t.Error("definition still has removed parameter")
		}

		callerContent, _ := os.ReadFile(filepath.Join(dir, "svc/svc.go"))
		if strings.Contains(string(callerContent), "true") || strings.Contains(string(callerContent), "false") {
			t.Errorf("call site still has removed argument:\n%s", callerContent)
		}
		verifyModuleIntegrity(t, dir)
	})

	// Sibling of "remove parameter": drop a return type from the
	// signature. Mirrors the AddReturns scope contract — signature
	// only, body returns left to a follow-up edit by the agent.
	t.Run("remove return — definition updated, error type dropped", func(t *testing.T) {
		dir := setupTestModule(t)
		// `panic()` is the noreturn idiom used elsewhere in this file
		// so the test fixture compiles after the signature change
		// without needing the agent to also rewrite body returns.
		writeTestFile(t, dir, "math/math.go", `package math

func Halve(x int) (int, error) {
	panic("not implemented")
}
`)
		out := runRefactor(t, dir, Input{
			Action:        ActionChangeSignature,
			Symbol:        "Halve",
			RemoveReturns: []string{"error"},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		defContent, _ := os.ReadFile(filepath.Join(dir, "math/math.go"))
		s := string(defContent)
		if strings.Contains(s, "error") {
			t.Errorf("error return should be dropped; got:\n%s", s)
		}
		if !strings.Contains(s, "func Halve(x int) int") {
			t.Errorf("expected `func Halve(x int) int`; got:\n%s", s)
		}
		verifyModuleIntegrity(t, dir)
	})

	// Multiple returns of the same type — left-to-right consumption.
	t.Run("remove return — duplicate types consumed left-to-right", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "tup/tup.go", `package tup

func Triple(x int) (int, int, int) {
	panic("not implemented")
}
`)
		// Two "int" entries should remove the first two ints, leaving one.
		out := runRefactor(t, dir, Input{
			Action:        ActionChangeSignature,
			Symbol:        "Triple",
			RemoveReturns: []string{"int", "int"},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s: %+v", out.Status, out.Results)
		}
		defContent, _ := os.ReadFile(filepath.Join(dir, "tup/tup.go"))
		s := string(defContent)
		if !strings.Contains(s, "func Triple(x int) int") {
			t.Errorf("expected single-int return; got:\n%s", s)
		}
		verifyModuleIntegrity(t, dir)
	})

	// Non-matching remove_returns entry must error rather than silently
	// no-op — silent no-ops led to agent confusion in B2's
	// inline_constant scenario, and we want loud failures here too.
	t.Run("remove return — unmatched type returns error", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "nm/nm.go", `package nm

func Plain(x int) int {
	return x
}
`)
		_, err := Handle(t.Context(), Input{
			Action:        ActionChangeSignature,
			Package:       dir,
			Symbol:        "Plain",
			RemoveReturns: []string{"error"},
		})
		if err == nil {
			t.Fatal("expected error for unmatched remove_returns entry; got nil")
		}
		if !strings.Contains(err.Error(), "remove_returns") {
			t.Errorf("error should name remove_returns; got: %v", err)
		}
	})

	t.Run("multiple callers across packages all updated", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "core/core.go", `package core

func Process(data string) string {
	return data
}
`)
		for _, pkg := range []string{"svc1", "svc2", "svc3"} {
			writeTestFile(t, dir, pkg+"/"+pkg+".go", `package `+pkg+`

import "testmod.example.com/core"

func Run() string {
	return core.Process("data")
}
`)
		}

		out := runRefactor(t, dir, Input{
			Action: ActionChangeSignature,
			Symbol: "Process",
			AddParams: []AddParameter{
				{Name: "ctx", Type: "string", DefaultValue: `""`},
			},
		})
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		for _, pkg := range []string{"svc1", "svc2", "svc3"} {
			content, _ := os.ReadFile(filepath.Join(dir, pkg+"/"+pkg+".go"))
			if !strings.Contains(string(content), `Process("data", ""`) {
				t.Errorf("%s call site not updated:\n%s", pkg, content)
			}
		}
		verifyModuleIntegrity(t, dir)
	})

	t.Run("symbol not found returns error", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "pkg.go", "package main\n\nfunc main() {}\n")

		_, err := Handle(t.Context(), Input{
			Action:  ActionChangeSignature,
			Package: dir,
			Symbol:  "NoSuchFunc",
			AddParams: []AddParameter{
				{Name: "x", Type: "int", DefaultValue: "0"},
			},
		})
		if err == nil {
			t.Fatal("expected error for missing symbol, got nil")
		}
	})

	t.Run("dry run leaves disk untouched", func(t *testing.T) {
		dir := setupTestModule(t)
		writeTestFile(t, dir, "calc/calc.go", `package calc

func Add(a, b int) int { return a + b }
`)

		originalContent, _ := os.ReadFile(filepath.Join(dir, "calc/calc.go"))

		out, err := Handle(t.Context(), Input{
			Action:  ActionChangeSignature,
			Package: dir,
			Symbol:  "Add",
			DryRun:  true,
			AddParams: []AddParameter{
				{Name: "label", Type: "string", DefaultValue: `""`},
			},
		})
		if err != nil {
			t.Fatalf("dry run error: %v", err)
		}
		if out.Status != StatusSuccess {
			t.Fatalf("expected success, got %s", out.Status)
		}

		afterContent, _ := os.ReadFile(filepath.Join(dir, "calc/calc.go"))
		if string(afterContent) != string(originalContent) {
			t.Error("dry run wrote to disk")
		}
	})
}
