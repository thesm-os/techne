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

func TestImplementInterface(t *testing.T) {
	t.Run("generates stubs for missing methods", func(t *testing.T) {
		dir := writeMod(t, "testimpl", map[string]string{
			"a.go": `package testimpl

type Greeter interface {
	Hello() string
	Goodbye() string
}

type EnglishGreeter struct{}
`,
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.ImplementInterface, lang.ImplementInterfaceInput{
			TargetStruct: "EnglishGreeter",
			Interface:    "Greeter",
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("status=%q; results=%+v", out.Status, out.Results)
		}
		body := readFile(t, filepath.Join(dir, "a.go"))
		for _, want := range []string{"Hello() string", "Goodbye() string", "EnglishGreeter"} {
			if !strings.Contains(body, want) {
				t.Errorf("expected generated code to contain %q; got:\n%s", want, body)
			}
		}
	})

	// Either the tool supports generic receiver structs cleanly or rejects
	// atomically — never half-applied.
	t.Run("generic receiver struct handled cleanly or rejected atomically", func(t *testing.T) {
		dir := writeMod(t, "hardgenimpl", map[string]string{
			"a.go": "package hardgenimpl\n\n" +
				"type Box[T any] struct{ v T }\n\n" +
				"type Stringer interface{ String() string }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		result, err := executeRefactorRaw(t, golang.ImplementInterface, lang.ImplementInterfaceInput{
			TargetStruct: "Box",
			Interface:    "Stringer",
		})
		if err != nil {
			if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
				t.Errorf("source must remain untouched on rejection; got:\n%s", got)
			}
			t.Skipf("implement_interface on generic struct not supported (acceptable): %v", err)
		}
		out, _ := result.(refactor.Output)
		if out.Status != refactor.StatusSuccess {
			if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
				t.Errorf("source must roll back on refactor failure; got:\n%s", got)
			}
			t.Skipf("implement_interface declined; status=%q", out.Status)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "func (") || !strings.Contains(body, "String()") {
			t.Errorf("expected a String() stub generated; got:\n%s", body)
		}
	})
}
