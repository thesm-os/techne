// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

func TestAddTests(t *testing.T) {
	t.Run("creates new file with auto-detected package name", func(t *testing.T) {
		dir := writeMod(t, "addtestsnew", map[string]string{
			"parser.go": "package addtestsnew\n\nfunc Parse(s string) int { return 0 }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.AddTests, lang.AddTestsInput{
			File: "parser_test.go",
			Tests: []lang.TestSpec{{
				Name:     "Parse",
				Subtests: []string{"valid input", "empty string"},
			}},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q results=%+v", out.Status, out.Results)
		}

		body := readFile(t, filepath.Join(dir, "parser_test.go"))
		if !strings.Contains(body, "package addtestsnew_test") {
			t.Errorf("expected package addtestsnew_test; got:\n%s", body)
		}
		if !strings.Contains(body, "func TestParse(t *testing.T)") {
			t.Errorf("expected TestParse function; got:\n%s", body)
		}
		for _, sub := range []string{"valid input", "empty string"} {
			if !strings.Contains(body, `t.Run("`+sub+`"`) {
				t.Errorf("expected subtest %q; got:\n%s", sub, body)
			}
		}
		// 1 top-level + 2 subtest closures each get t.Parallel().
		if strings.Count(body, "t.Parallel()") < 3 {
			t.Errorf("expected t.Parallel() in top-level and each subtest; got:\n%s", body)
		}
		if !strings.Contains(body, `t.Skip("not implemented")`) {
			t.Errorf("expected t.Skip stub; got:\n%s", body)
		}
	})

	t.Run("name normalization adds Test prefix", func(t *testing.T) {
		dir := writeMod(t, "addtestsnorm", map[string]string{
			"calc.go": "package addtestsnorm\n\nfunc Add(a, b int) int { return a + b }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.AddTests, lang.AddTestsInput{
			File:  "calc_test.go",
			Tests: []lang.TestSpec{{Name: "Add"}},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "calc_test.go"))
		if !strings.Contains(body, "func TestAdd(t *testing.T)") {
			t.Errorf("expected TestAdd (prefix added); got:\n%s", body)
		}
	})

	t.Run("generates subtests with t.Parallel and t.Skip stubs", func(t *testing.T) {
		dir := writeMod(t, "addtestssubs", map[string]string{
			"net.go": "package addtestssubs\n\nfunc Dial(addr string) error { return nil }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.AddTests, lang.AddTestsInput{
			File: "net_test.go",
			Tests: []lang.TestSpec{{
				Name:     "Dial",
				Subtests: []string{"success", "timeout", "refused"},
			}},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "net_test.go"))
		for _, sub := range []string{"success", "timeout", "refused"} {
			if !strings.Contains(body, `t.Run("`+sub+`"`) {
				t.Errorf("expected subtest %q; got:\n%s", sub, body)
			}
		}
		// Each subtest closure must also have t.Parallel().
		if strings.Count(body, "t.Parallel()") < 4 { // 1 top-level + 3 subtests
			t.Errorf("expected t.Parallel() in top-level and each subtest; got:\n%s", body)
		}
	})

	t.Run("appends to existing file", func(t *testing.T) {
		dir := writeMod(t, "addtestsappend", map[string]string{
			"store.go": "package addtestsappend\n\nfunc Get(k string) string { return \"\" }\nfunc Set(k, v string) {}\n",
			"store_test.go": `package addtestsappend_test

import "testing"

func TestGet(t *testing.T) {
	t.Parallel()
	t.Skip("not implemented")
}
`,
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.AddTests, lang.AddTestsInput{
			File: "store_test.go",
			Tests: []lang.TestSpec{{
				Name:     "Set",
				Subtests: []string{"overwrites existing", "empty value"},
			}},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "store_test.go"))
		if !strings.Contains(body, "func TestGet") {
			t.Errorf("TestGet must still be present; got:\n%s", body)
		}
		if !strings.Contains(body, "func TestSet") {
			t.Errorf("TestSet must be appended; got:\n%s", body)
		}
		for _, sub := range []string{"overwrites existing", "empty value"} {
			if !strings.Contains(body, `t.Run("`+sub+`"`) {
				t.Errorf("expected subtest %q in appended function; got:\n%s", sub, body)
			}
		}
	})

	t.Run("inserts after named function", func(t *testing.T) {
		dir := writeMod(t, "addtestsafter", map[string]string{
			"ops.go": "package addtestsafter\n\nfunc Open() error { return nil }\nfunc Close() error { return nil }\n",
			"ops_test.go": `package addtestsafter_test

import "testing"

func TestOpen(t *testing.T) {
	t.Parallel()
	t.Skip("not implemented")
}

func TestClose(t *testing.T) {
	t.Parallel()
	t.Skip("not implemented")
}
`,
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.AddTests, lang.AddTestsInput{
			File:  "ops_test.go",
			After: "TestOpen",
			Tests: []lang.TestSpec{{
				Name:     "OpenError",
				Subtests: []string{"file not found", "permission denied"},
			}},
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "ops_test.go"))
		openPos := strings.Index(body, "func TestOpen(")
		insertedPos := strings.Index(body, "func TestOpenError(")
		closePos := strings.Index(body, "func TestClose(")
		if insertedPos < openPos {
			t.Errorf("TestOpenError must appear after TestOpen; got:\n%s", body)
		}
		if closePos < insertedPos {
			t.Errorf("TestClose must appear after TestOpenError; got:\n%s", body)
		}
		for _, sub := range []string{"file not found", "permission denied"} {
			if !strings.Contains(body, `t.Run("`+sub+`"`) {
				t.Errorf("expected subtest %q in inserted function; got:\n%s", sub, body)
			}
		}
	})

	t.Run("dry run does not write to disk", func(t *testing.T) {
		dir := writeMod(t, "addtestsdry", map[string]string{
			"v.go": "package addtestsdry\n\nfunc Validate(s string) bool { return true }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.AddTests, lang.AddTestsInput{
			File:   "v_test.go",
			Tests:  []lang.TestSpec{{Name: "Validate"}},
			DryRun: true,
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q results=%+v", out.Status, out.Results)
		}
		testFile := filepath.Join(dir, "v_test.go")
		if _, err := os.Stat(testFile); err == nil {
			t.Errorf("dry run must not write v_test.go to disk")
		}
	})

	t.Run("error on missing tests field", func(t *testing.T) {
		dir := writeMod(t, "addtestserr", map[string]string{
			"x.go": "package addtestserr\n",
		})
		t.Chdir(dir)

		_, err := executeRefactorRaw(t, golang.AddTests, lang.AddTestsInput{
			File:  "x_test.go",
			Tests: nil,
		})
		if err == nil {
			t.Fatal("expected error when tests is empty")
		}
	})
}
