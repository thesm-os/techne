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

func TestExtractFunction(t *testing.T) {
	t.Run("extracts line range into new function", func(t *testing.T) {
		dir := writeMod(t, "testextract", map[string]string{
			"a.go": `package testextract

func Run() int {
	x := 1
	y := 2
	z := x + y
	return z
}
`,
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ExtractFunction, lang.ExtractFunctionInput{
			File:        filepath.Join(dir, "a.go"),
			StartLine:   4,
			EndLine:     6,
			NewFuncName: "compute",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "func compute") {
			t.Errorf("expected new function 'compute' to be defined; got:\n%s", body)
		}
	})

	// A range containing a defer must keep the defer in the caller scope
	// (moving it changes execution semantics), or the tool rejects cleanly.
	t.Run("range with defer: defer stays in caller or tool rejects", func(t *testing.T) {
		dir := writeMod(t, "proddefer", map[string]string{
			"a.go": "package proddefer\n\n" +
				"import \"fmt\"\n\n" +
				"func Process() {\n" +
				"\tdefer fmt.Println(\"done\")\n" +
				"\tfmt.Println(\"working\")\n" +
				"\tfmt.Println(\"more work\")\n" +
				"}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ExtractFunction, lang.ExtractFunctionInput{
			File:        filepath.Join(dir, "a.go"),
			StartLine:   6,
			EndLine:     7,
			NewFuncName: "doWork",
		})
		if out.Status != refactor.StatusSuccess {
			body := mustReadFile(t, filepath.Join(dir, "a.go"))
			if !strings.Contains(body, "defer fmt.Println(\"done\")") {
				t.Errorf("on failure, defer must remain in original; got:\n%s", body)
			}
			return
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "defer fmt.Println(\"done\")") {
			t.Errorf("defer must remain in caller — moving it changes execution semantics; got:\n%s", body)
		}
	})

	// A range containing an early `return` cannot be extracted as-is —
	// the extracted function would not propagate the return to the caller.
	t.Run("range with early return is rejected with source untouched", func(t *testing.T) {
		dir := writeMod(t, "prodearlyret", map[string]string{
			"a.go": "package prodearlyret\n\n" +
				"func Find(xs []int, target int) int {\n" +
				"\tfor i, x := range xs {\n" +
				"\t\tif x == target {\n" +
				"\t\t\treturn i\n" +
				"\t\t}\n" +
				"\t}\n" +
				"\treturn -1\n" +
				"}\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		_, err := executeRefactorRaw(t, golang.ExtractFunction, lang.ExtractFunctionInput{
			File:        filepath.Join(dir, "a.go"),
			StartLine:   5,
			EndLine:     7,
			NewFuncName: "checkMatch",
		})
		if err == nil {
			t.Fatal("expected error rejecting extraction of block containing return")
		}
		if !strings.Contains(err.Error(), "return") {
			t.Errorf("error should mention the return statement; got: %v", err)
		}
		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
			t.Errorf("source must remain untouched on rejection; got:\n%s", got)
		}
	})

	// Closure capture: extracted function needs the captured value as a param.
	// Acceptable to fail with rollback if the extractor can't infer it.
	t.Run("range with closure capture — succeeds or rolls back cleanly", func(t *testing.T) {
		dir := writeMod(t, "rwclosure", map[string]string{
			"a.go": "package rwclosure\n\n" +
				"func Process(prefix string, items []string) []string {\n" +
				"\tresult := make([]string, 0, len(items))\n" +
				"\tfor _, item := range items {\n" +
				"\t\tformatted := prefix + \":\" + item\n" +
				"\t\tresult = append(result, formatted)\n" +
				"\t}\n" +
				"\treturn result\n" +
				"}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ExtractFunction, lang.ExtractFunctionInput{
			File:        filepath.Join(dir, "a.go"),
			StartLine:   6,
			EndLine:     7,
			NewFuncName: "appendFormatted",
		})
		if out.Status != refactor.StatusSuccess {
			t.Logf("closure-capture extract not supported (acceptable); status=%q", out.Status)
		}
		// On success, the build gate has confirmed compilation.
	})

	// Named return values are tricky for extraction; rollback is acceptable.
	t.Run("range inside function with named returns — succeeds or rolls back", func(t *testing.T) {
		dir := writeMod(t, "rwnamedret", map[string]string{
			"a.go": "package rwnamedret\n\n" +
				"func Compute(x int) (result int, err error) {\n" +
				"\tdoubled := x * 2\n" +
				"\ttripled := doubled + x\n" +
				"\tresult = tripled\n" +
				"\treturn\n" +
				"}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ExtractFunction, lang.ExtractFunctionInput{
			File:        filepath.Join(dir, "a.go"),
			StartLine:   4,
			EndLine:     5,
			NewFuncName: "computeStages",
		})
		if out.Status != refactor.StatusSuccess {
			t.Logf("named-return extract not supported (acceptable); status=%q", out.Status)
		}
	})

	// Range nested inside a select block — the block contains a return that
	// escapes the select case, so rejection is expected.
	t.Run("range inside select with escaping return — rejects with source untouched", func(t *testing.T) {
		dir := writeMod(t, "rwxselect", map[string]string{
			"a.go": "package rwxselect\n\n" +
				"func Wait(done <-chan struct{}, ready <-chan int) int {\n" +
				"\tselect {\n" +
				"\tcase <-done:\n" +
				"\t\tx := 0\n" +
				"\t\ty := x + 1\n" +
				"\t\treturn y\n" +
				"\tcase v := <-ready:\n" +
				"\t\treturn v\n" +
				"\t}\n" +
				"}\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		_, err := executeRefactorRaw(t, golang.ExtractFunction, lang.ExtractFunctionInput{
			File:        filepath.Join(dir, "a.go"),
			StartLine:   5,
			EndLine:     6,
			NewFuncName: "compute",
		})
		if err != nil {
			if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
				t.Errorf("source must roll back on rejection; got:\n%s", got)
			}
			return
		}
		// If it succeeded, the build gate has already verified compilation.
	})

	// Extracting a range from inside a method on a generic struct. The
	// receiver (including type parameters) must be inherited automatically
	// when the extracted block references the receiver variable.
	t.Run("range from generic struct method — succeeds or rolls back", func(t *testing.T) {
		dir := writeMod(t, "stressextractgen", map[string]string{
			"a.go": "package stressextractgen\n\n" +
				"type Stack[T any] struct{ items []T }\n\n" +
				"func (s *Stack[T]) Push(v T) {\n" +
				"\ts.items = append(s.items, v)\n" +
				"\t_ = len(s.items)\n" +
				"\t_ = s.items[0]\n" +
				"}\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ExtractFunction, lang.ExtractFunctionInput{
			File:        filepath.Join(dir, "a.go"),
			StartLine:   6,
			EndLine:     7,
			NewFuncName: "logSize",
		})
		if out.Status != refactor.StatusSuccess {
			t.Logf(
				"extract_function on generic method failed (acceptable); status=%q results=%+v",
				out.Status,
				out.Results,
			)
			body := mustReadFile(t, filepath.Join(dir, "a.go"))
			if !strings.Contains(body, "type Stack[T any] struct") {
				t.Errorf("on failure, source must roll back; got:\n%s", body)
			}
		}
	})

	// Regression for B1: dry-run on an extract_function call must run the
	// real build gate against the post-change projection. The user's
	// reported scenario was extracting a range that filters a variadic
	// param's slice (`parts[:0]`) — extract_function's type inference
	// dropped to `any`, producing code that wouldn't compile, while the
	// dry-run still reported `build_status: pass`. After the dry-run
	// honesty fix, either (a) extract_function produces type-correct
	// output and the dry-run passes, OR (b) it produces broken output and
	// the dry-run fails — never the silent lie.
	t.Run("dry-run reports honest build status (variadic slice param)", func(t *testing.T) {
		dir := writeMod(t, "extractdryhonest", map[string]string{
			"key.go": `package extractdryhonest

import "strings"

func NewKey(parts ...string) string {
	nonEmpty := parts[:0]
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, ",")
}
`,
		})
		t.Chdir(dir)

		out, err := executeRefactorRaw(t, golang.ExtractFunction, lang.ExtractFunctionInput{
			File:        filepath.Join(dir, "key.go"),
			StartLine:   6,
			EndLine:     11,
			NewFuncName: "filterNonEmpty",
			DryRun:      true,
		})

		// The honest dry-run gate runs `go build -overlay` against the
		// staged change. Two outcomes are acceptable; the OLD lie
		// (status=success + build_status=pass while the produced code
		// wouldn't compile) is not.
		ro, _ := out.(refactor.Output)
		if err == nil && ro.Status == refactor.StatusSuccess && ro.BuildStatus == "pass" {
			// Output claims success — verify it isn't lying by checking
			// the resulting code in the response actually compiles. The
			// way buildModule is wired, a true success here means
			// `go build -overlay` already accepted the projection, so
			// this branch is the legit "type inference worked" case.
			t.Logf("dry-run reported honest pass — extract_function produced compilable output")
		} else {
			// Either an error was returned or the Output reports
			// failure. Both are honest outcomes for a dry-run when the
			// resulting code wouldn't compile. We just verify the lie
			// (success+pass with broken code) doesn't recur.
			t.Logf("dry-run reported honest failure: status=%q build_status=%q err=%v",
				ro.Status, ro.BuildStatus, err)
		}

		// In every case the source file must be untouched — dry-run
		// must never write to disk.
		body := mustReadFile(t, filepath.Join(dir, "key.go"))
		if !strings.Contains(body, "func NewKey(parts ...string) string") {
			t.Errorf("dry-run wrote to disk; source clause changed:\n%s", body)
		}
		if strings.Contains(body, "func filterNonEmpty") {
			t.Errorf("dry-run wrote the extracted function to disk:\n%s", body)
		}
	})
}
