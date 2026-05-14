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

func TestExtractVariable(t *testing.T) {
	t.Run("extracts if-condition expression", func(t *testing.T) {
		dir := writeMod(t, "testextvar", map[string]string{
			"a.go": `package testextvar

func Compute() int {
	if 2+3 > 4 {
		return 1
	}
	return 0
}
`,
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ExtractVariable, lang.ExtractVariableInput{
			File:         filepath.Join(dir, "a.go"),
			Line:         4,
			VariableName: "ok",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "ok :=") {
			t.Errorf("expected 'ok :=' assignment; got:\n%s", body)
		}
	})
}
