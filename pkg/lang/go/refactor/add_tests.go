// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

// Package refactor implements the strategy registry behind every lang.go.*
// refactoring action. Each action lives in its own file, registers itself
// via init(), and runs against a Transaction abstraction so the framework
// can stage edits, run the build gate, and roll back on failure.
package refactor

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// AddTestsAction scaffolds Go test functions in a target test file. Each test
// gets a t.Parallel() call up front; tests without subtests get a single
// t.Skip("not implemented") body, while tests with subtests get one t.Run per
// subtest, each itself wired with t.Parallel() + t.Skip. The output is always a
// compilable, immediately runnable test file — the agent can write the
// assertions on a subsequent pass.
//
// Three insertion modes:
//
//   - Target file does not exist: a new file is created with package clause,
//     the testing import, and every requested test in input order. The package
//     name is detected from production .go files in the directory (with the
//     "_test" suffix appended per external-test convention), falling back to
//     `<dirname>_test` if no production sources exist.
//   - input.After is set: the named function (with "Test" prefix added if
//     missing) is located and the new tests are inserted immediately after it.
//     Useful for keeping related tests together.
//   - Default: the new tests are appended at the end of the file.
//
// The "Test" prefix is added automatically to TestSpec.Name when it doesn't
// already start with Test/Benchmark/Fuzz, so callers can pass short names
// ("Handler") and get the right Go testing identifier ("TestHandler").
type AddTestsAction struct{}

// Name implements [RefactorStrategy] and returns [ActionAddTests].
func (*AddTestsAction) Name() string { return ActionAddTests }

func init() { RegisterAction(&AddTestsAction{}) }

// Execute is the [RefactorStrategy] entry point. It resolves the target file
// path, decides between new-file / insert-after / append modes based on input,
// and stages the result on ws.
//
// Failure modes:
//
//   - input.Tests is empty — early error.
//   - input.TargetFile is empty — early error.
//   - input.After is set but the named function is not found — error.
//   - Parser failure on the existing file (insert-after mode) — error.
//   - Build gate fails after the edit — full rollback.
//
// The context argument is unused: file IO happens at transaction commit time,
// dwarfed by the build gate's runtime. Callers wanting an early abort should
// cancel the surrounding tool invocation instead.
func (*AddTestsAction) Execute(_ context.Context, input Input, ws Transaction) error {
	if len(input.Tests) == 0 {
		return fmt.Errorf("tests: at least one test spec is required")
	}
	if input.TargetFile == "" {
		return fmt.Errorf("target_file is required")
	}

	filePath := input.TargetFile
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(ws.ModRoot(), filePath)
	}

	original, readErr := os.ReadFile(filePath)
	fileExists := readErr == nil

	var modified []byte
	if !fileExists {
		dir := filepath.Dir(filePath)
		pkgName, err := atDetectPackageName(dir)
		if err != nil {
			pkgName = filepath.Base(dir) + "_test"
		}
		modified = atBuildNewFile(pkgName, input.Tests)
	} else if input.After != "" {
		var err error
		modified, err = atInsertAfter(original, input.After, input.Tests)
		if err != nil {
			return fmt.Errorf("insert after %q: %w", input.After, err)
		}
	} else {
		modified = atAppend(original, input.Tests)
	}

	msg := fmt.Sprintf("add %d test function(s)", len(input.Tests))
	return ws.AddChange(filePath, original, modified, msg)
}

// atDetectPackageName reads production .go files in dir to find the package
// name, then appends "_test" per external test file convention.
func atDetectPackageName(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, src, parser.PackageClauseOnly)
		if err != nil || f.Name == nil {
			continue
		}
		return f.Name.Name + "_test", nil
	}
	return "", fmt.Errorf("no Go source files found in %s", dir)
}

// atBuildNewFile generates a complete test file with package clause, import,
// and all scaffolded test functions.
func atBuildNewFile(pkgName string, specs []TestSpec) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "package %s\n\nimport \"testing\"\n\n", pkgName)
	buf.Write(atGenFuncsText(specs))
	return buf.Bytes()
}

// atAppend appends scaffolded functions to the end of an existing file.
func atAppend(src []byte, specs []TestSpec) []byte {
	funcs := atGenFuncsText(specs)
	trimmed := bytes.TrimRight(src, "\n\t ")
	var buf bytes.Buffer
	buf.Grow(len(trimmed) + len(funcs) + 3)
	buf.Write(trimmed)
	buf.WriteString("\n\n")
	buf.Write(funcs)
	return buf.Bytes()
}

// atInsertAfter parses src, finds the function named afterName (normalizing
// the "Test" prefix if needed), and inserts the new functions immediately
// after it.
func atInsertAfter(src []byte, afterName string, specs []TestSpec) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse file: %w", err)
	}
	normalized := atNormalizeTestName(afterName)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name != afterName && fn.Name.Name != normalized {
			continue
		}
		endOffset := fset.File(fn.End()).Offset(fn.End())
		funcs := atGenFuncsText(specs)
		var buf bytes.Buffer
		buf.Grow(len(src) + len(funcs) + 2)
		buf.Write(src[:endOffset])
		buf.WriteString("\n\n")
		buf.Write(funcs)
		buf.Write(src[endOffset:])
		return buf.Bytes(), nil
	}
	return nil, fmt.Errorf("function %q not found in file", afterName)
}

// atGenFuncsText renders all specs as contiguous function source text.
func atGenFuncsText(specs []TestSpec) []byte {
	var buf bytes.Buffer
	for i, s := range specs {
		if i > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(atGenFunc(s))
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// atGenFunc renders a single test function with t.Parallel() and either
// t.Skip() (no subtests) or t.Run subtests each with t.Parallel() + t.Skip().
func atGenFunc(spec TestSpec) string {
	name := atNormalizeTestName(spec.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "func %s(t *testing.T) {\n", name)
	b.WriteString("\tt.Parallel()\n")
	if len(spec.Subtests) == 0 {
		b.WriteString("\tt.Skip(\"not implemented\")\n")
	} else {
		for _, sub := range spec.Subtests {
			fmt.Fprintf(&b, "\tt.Run(%q, func(t *testing.T) {\n", sub)
			b.WriteString("\t\tt.Parallel()\n")
			b.WriteString("\t\tt.Skip(\"not implemented\")\n")
			b.WriteString("\t})\n")
		}
	}
	b.WriteString("}")
	return b.String()
}

// atNormalizeTestName ensures the name starts with "Test", "Benchmark", or
// "Fuzz"; otherwise prepends "Test".
func atNormalizeTestName(name string) string {
	if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Fuzz") {
		return name
	}
	return "Test" + name
}
