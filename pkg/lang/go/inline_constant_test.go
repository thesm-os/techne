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

func TestInlineConstant(t *testing.T) {
	t.Run("inlines integer constant at all use sites", func(t *testing.T) {
		dir := writeMod(t, "testinline", map[string]string{
			"a.go": `package testinline

const Limit = 10

func A() int { return Limit }
func B() int { return Limit + 1 }
`,
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.InlineConstant, lang.InlineConstantInput{
			Symbol: "Limit",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "func A() int { return 10 }") {
			t.Errorf("expected A's body to be inlined; got:\n%s", body)
		}
		if !strings.Contains(body, "func B() int { return 10 + 1 }") {
			t.Errorf("expected B's body to be inlined; got:\n%s", body)
		}
	})

	t.Run("rejects non-constant symbol", func(t *testing.T) {
		dir := writeMod(t, "testinlinenoconst", map[string]string{
			"a.go": "package testinlinenoconst\n\nfunc Foo() int { return 1 }\n",
		})
		t.Chdir(dir)

		if _, err := executeRefactorRaw(t, golang.InlineConstant, lang.InlineConstantInput{Symbol: "Foo"}); err == nil {
			t.Fatal("expected error: inline_constant should reject a function symbol")
		}
	})

	t.Run("inlines typed integer constant", func(t *testing.T) {
		dir := writeMod(t, "kindsinlinetyped", map[string]string{
			"a.go": "package kindsinlinetyped\n\n" +
				"const Limit int = 10\n\n" +
				"func Cap() int { return Limit }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.InlineConstant, lang.InlineConstantInput{
			Symbol: "Limit",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("inline typed const failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "return 10") {
			t.Errorf("typed const not inlined; got:\n%s", body)
		}
	})

	t.Run("inlines string constant verbatim", func(t *testing.T) {
		dir := writeMod(t, "kindsinlinestr", map[string]string{
			"a.go": "package kindsinlinestr\n\n" +
				"const Greeting = \"hello\"\n\n" +
				"func Welcome() string { return Greeting + \"!\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.InlineConstant, lang.InlineConstantInput{
			Symbol: "Greeting",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("inline string const failed: status=%q results=%+v", out.Status, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, `return "hello" + "!"`) {
			t.Errorf("string const not inlined verbatim; got:\n%s", body)
		}
	})

	// Inlining a const that is part of an iota chain must either reject
	// the operation or preserve the values of all dependent constants.
	t.Run("iota chain: rejects or preserves dependent constant values", func(t *testing.T) {
		dir := writeMod(t, "prodiota", map[string]string{
			"a.go": "package prodiota\n\n" +
				"const (\n" +
				"\tA = iota\n" +
				"\tB\n" +
				"\tC\n" +
				")\n\n" +
				"func UseB() int { return B }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		out := executeRefactor[refactor.Output](t, golang.InlineConstant, lang.InlineConstantInput{
			Symbol: "B",
		})
		if out.Status == refactor.StatusFailure {
			got := mustReadFile(t, filepath.Join(dir, "a.go"))
			if got != original {
				t.Errorf("on failure, source must roll back; got:\n%s", got)
			}
			return
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "return 1") {
			t.Errorf("if B was inlined, the literal must be 1 (B's iota value); got:\n%s", body)
		}
		if !strings.Contains(body, "const") || !strings.Contains(body, "C") {
			t.Errorf("the rest of the iota chain must remain intact; got:\n%s", body)
		}
	})

	// Inlining a bit-shifted iota value produces a literal int, or is
	// rejected — both are acceptable.
	t.Run("bit-shifted iota: inlines or rejects gracefully", func(t *testing.T) {
		dir := writeMod(t, "rwbitshift", map[string]string{
			"a.go": "package rwbitshift\n\nconst (\n\tFlagA = 1 << iota\n\tFlagB\n\tFlagC\n)\n\nfunc UseB() int { return FlagB }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.InlineConstant, lang.InlineConstantInput{
			Symbol: "FlagB",
		})
		if out.Status != refactor.StatusSuccess {
			t.Logf("inline_constant on bit-shifted iota declined (acceptable); status=%q", out.Status)
			return
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "return 2") {
			t.Errorf("FlagB inlined value should be 2; got:\n%s", body)
		}
	})

	t.Run("inlines across modules in go.work workspace", func(t *testing.T) {
		twoModuleWorkspace(
			t,
			map[string]string{"const.go": "package a\n\nconst MaxRetries = 5\n"},
			map[string]string{
				"main.go": "package b\n\nimport \"example.com/a\"\n\nfunc Limit() int { return a.MaxRetries }\n",
			},
		)

		out := executeRefactor[refactor.Output](t, golang.InlineConstant, lang.InlineConstantInput{
			Symbol: "MaxRetries",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got status=%q results=%+v", out.Status, out.Results)
		}
		bMain := mustReadFile(t, "modB/main.go")
		if !strings.Contains(bMain, "return 5") {
			t.Errorf("modB use site must be inlined; got:\n%s", bMain)
		}
	})

	t.Run("inlines constant used as array size in compile-time context", func(t *testing.T) {
		dir := writeMod(t, "stressinline", map[string]string{
			"a.go": "package stressinline\n\nconst Size = 4\n\nvar Buffer [Size]int\n\nfunc Fill(v int) { for i := 0; i < Size; i++ { Buffer[i] = v } }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.InlineConstant, lang.InlineConstantInput{
			Symbol: "Size",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf(
				"expected success — inlined const must keep array-size compile-time validity; got status=%q results=%+v",
				out.Status,
				out.Results,
			)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, `[4]int`) {
			t.Errorf("array size literal should be [4]int after inlining; got:\n%s", body)
		}
	})
}
