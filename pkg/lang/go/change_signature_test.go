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

func TestChangeSignature(t *testing.T) {
	// Regression: when the call site is in the same file as the definition,
	// the signature edit shifted the call's byte offset past the ±5 tolerance,
	// so updateCallSitesInFile silently dropped the patch. This most often
	// hit top-level functions whose callers live nearby.
	t.Run("adds param — call site in same file as definition rewritten", func(t *testing.T) {
		dir := writeMod(t, "testchsigsamefile", map[string]string{
			"a.go": "package testchsigsamefile\n\nfunc Greet() string { return \"hi\" }\n\nfunc Caller() string { return Greet() }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ChangeSignature, lang.ChangeSignatureInput{
			Symbol: "Greet",
			AddParams: []lang.AddParameter{
				{Name: "name", Type: "string", DefaultValue: `"world"`},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, `Greet("world")`) {
			t.Errorf("regression: call site in same file as def was not rewritten with default value; got:\n%s", body)
		}
	})

	t.Run("adds parameter and rewrites cross-file call sites", func(t *testing.T) {
		dir := writeMod(t, "testsig", map[string]string{
			"a.go": "package testsig\n\nfunc Greet(name string) string { return \"hi \" + name }\n",
			"b.go": "package testsig\n\nfunc Caller() string { return Greet(\"a\") }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ChangeSignature, lang.ChangeSignatureInput{
			Symbol:    "Greet",
			AddParams: []lang.AddParameter{{Name: "title", Type: "string", DefaultValue: `""`}},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		mustContain(t, filepath.Join(dir, "b.go"), `Greet("a", "")`)
	})

	t.Run("adds param to method in complex project — middleware call site rewritten", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeRefactorTyped(t, golang.ChangeSignature, lang.ChangeSignatureInput{
			Symbol: "Handler.Process",
			AddParams: []lang.AddParameter{
				{Name: "ctx", Type: "string", DefaultValue: `""`},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("change_signature failed: status=%q results=%+v", out.Status, out.Results)
		}
		mwBody := mustReadFile(t, filepath.Join(dir, "api/middleware/logging.go"))
		if !strings.Contains(mwBody, `Process("",`) && !strings.Contains(mwBody, `Process(n, "")`) {
			t.Errorf("expected middleware call site rewritten with default \"\"; got:\n%s", mwBody)
		}
	})

	// Adding a parameter before an existing variadic must place the new param
	// before the variadic and update call sites. This is a known awkward case.
	t.Run("adds param before existing variadic — rejects or variadic stays last", func(t *testing.T) {
		dir := writeMod(t, "prodvariadic", map[string]string{
			"a.go": "package prodvariadic\n\n" +
				"func Log(format string, args ...any) { _ = format; _ = args }\n\n" +
				"func Use() { Log(\"hello %s\", \"world\") }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ChangeSignature, lang.ChangeSignatureInput{
			Symbol: "Log",
			AddParams: []lang.AddParameter{
				{Name: "level", Type: "int", DefaultValue: "0"},
			},
		})
		if out.Status != refactor.StatusSuccess {
			body := mustReadFile(t, filepath.Join(dir, "a.go"))
			if !strings.Contains(body, "func Log(format string, args ...any)") {
				t.Errorf("on failure, source must roll back; got:\n%s", body)
			}
			t.Skipf("change_signature on variadic not supported cleanly; status=%q", out.Status)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if strings.Index(body, "...any") < strings.Index(body, "level int") {
			t.Errorf("variadic must remain after positional params; got:\n%s", body)
		}
	})

	t.Run("adds param across modules in go.work workspace", func(t *testing.T) {
		twoModuleWorkspace(
			t,
			map[string]string{"api.go": "package a\n\nfunc DoIt() int { return 1 }\n"},
			map[string]string{
				"main.go": "package b\n\nimport \"example.com/a\"\n\nfunc Use() int { return a.DoIt() }\n",
			},
		)

		out := executeRefactor[refactor.Output](t, golang.ChangeSignature, lang.ChangeSignatureInput{
			Symbol: "DoIt",
			AddParams: []lang.AddParameter{
				{Name: "n", Type: "int", DefaultValue: "0"},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		bMain := mustReadFile(t, "modB/main.go")
		if !strings.Contains(bMain, "a.DoIt(0)") {
			t.Errorf("modB call site must be updated; got:\n%s", bMain)
		}
	})

	// 3 calls to the same function in the same file as the def — all three
	// call sites need the new arg. Tests the offset-drift fix.
	t.Run("three same-file call sites all rewritten despite offset drift", func(t *testing.T) {
		dir := writeMod(t, "stressmultiplecalls", map[string]string{
			"a.go": "package stressmultiplecalls\n\n" +
				"func Foo() int { return 1 }\n\n" +
				"func A() int { return Foo() + 1 }\n" +
				"func B() int { return Foo() + 2 }\n" +
				"func C() int { return Foo() + 3 }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ChangeSignature, lang.ChangeSignatureInput{
			Symbol: "Foo",
			AddParams: []lang.AddParameter{
				{Name: "n", Type: "int", DefaultValue: "0"},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		count := strings.Count(body, "Foo(0)")
		if count != 3 {
			t.Errorf("expected all 3 same-file call sites updated to Foo(0); got %d in:\n%s", count, body)
		}
	})

	// Combine add_params, remove_params, and add_returns in one operation.
	// The function body uses panic so it stays valid under any return arity.
	t.Run("combined add, remove, and add-return in single operation", func(t *testing.T) {
		dir := writeMod(t, "stresscombined", map[string]string{
			"a.go": "package stresscombined\n\n" +
				"func F(a int, b int) string { panic(\"todo\") }\n\n" +
				"func Caller() { F(1, 2) }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ChangeSignature, lang.ChangeSignatureInput{
			Symbol:       "F",
			RemoveParams: []string{"b"},
			AddParams: []lang.AddParameter{
				{Name: "name", Type: "string", DefaultValue: `"x"`},
			},
			AddReturns: []lang.AddReturn{
				{Type: "error", DefaultValue: "_"},
			},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, `func F(a int, name string) (string, error)`) {
			t.Errorf("definition shape wrong; got:\n%s", body)
		}
		if !strings.Contains(body, `F(1, "x")`) {
			t.Errorf("call site arg list wrong; got:\n%s", body)
		}
	})

	t.Run("add_returns does not inject defaults into existing return statements", func(t *testing.T) {
		t.Skip("known limitation: add_returns does not inject defaults into existing return statements")
	})
}
