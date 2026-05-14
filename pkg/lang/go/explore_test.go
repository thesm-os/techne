// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
)

const testSrc = `// Package greet provides greeting utilities.
package greet

import "fmt"

// DefaultName is the fallback name when none is provided.
const DefaultName = "World"

// maxAge is the maximum supported age (unexported).
const maxAge = 150

// Greeter is the interface for anything that can greet.
type Greeter interface {
	Greet() string
}

// Person represents a person with a name and age.
type Person struct {
	// Name is the person's name.
	Name string ` + "`" + `json:"name"` + "`" + `
	// Age is the person's age.
	Age int ` + "`" + `json:"age"` + "`" + `
}

// Greet returns a greeting string for the person.
func (p *Person) Greet() string {
	return fmt.Sprintf("Hello, %s!", p.Name)
}

// NewPerson constructs a Person with default values if needed.
func NewPerson(name string, age int) *Person {
	if name == "" {
		name = DefaultName
	}
	return &Person{Name: name, Age: age}
}
`

func writeTestPackage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Write a minimal go.mod so packages.Load can resolve the module root.
	gomod := "module testpkg\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "greet.go"), []byte(testSrc), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	return dir
}

func TestExplore(t *testing.T) {
	t.Run("filter by kind and prefix", func(t *testing.T) {
		dir := writeTestPackage(t)
		t.Chdir(dir)

		t.Run("kind=func returns only functions", func(t *testing.T) {
			out := execute(t, lang.ExploreInput{Package: ".", Kind: lang.KindFunc})
			for name, meta := range out.Symbols {
				if meta.Kind != lang.KindFunc {
					t.Errorf("expected only func kind; got %s for %s", meta.Kind, name)
				}
			}
		})

		t.Run("kind=struct returns only structs", func(t *testing.T) {
			out := execute(t, lang.ExploreInput{Package: ".", Kind: lang.KindStruct})
			for name, meta := range out.Symbols {
				if meta.Kind != lang.KindStruct {
					t.Errorf("expected only struct kind; got %s for %s", meta.Kind, name)
				}
			}
		})

		t.Run("name_prefix=New filters to NewPerson", func(t *testing.T) {
			out := execute(t, lang.ExploreInput{Package: ".", NamePrefix: "New"})
			if _, ok := out.Symbols["NewPerson"]; !ok {
				t.Errorf("expected NewPerson in results; got %v", out.SymbolOrder)
			}
			for name := range out.Symbols {
				if !strings.HasPrefix(name, "New") &&
					!strings.HasPrefix(strings.SplitN(name, ".", 2)[len(strings.SplitN(name, ".", 2))-1], "New") {
					t.Errorf("unexpected %s in name_prefix=New result", name)
				}
			}
		})

		t.Run("name_suffix=Person filters to Person types and ctors", func(t *testing.T) {
			out := execute(t, lang.ExploreInput{Package: ".", NameSuffix: "Person"})
			for name := range out.Symbols {
				base := name
				if i := strings.LastIndex(base, "."); i >= 0 {
					base = base[i+1:]
				}
				if !strings.HasSuffix(base, "Person") {
					t.Errorf("unexpected %s in name_suffix=Person result", name)
				}
			}
		})

		t.Run("kind+prefix combine", func(t *testing.T) {
			out := execute(t, lang.ExploreInput{Package: ".", Kind: lang.KindFunc, NamePrefix: "New"})
			if _, ok := out.Symbols["NewPerson"]; !ok {
				t.Errorf("expected NewPerson; got %v", out.SymbolOrder)
			}
			for name, meta := range out.Symbols {
				if meta.Kind != lang.KindFunc {
					t.Errorf("expected only funcs; got %s for %s", meta.Kind, name)
				}
			}
		})
	})

	t.Run("docs mode", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeDocs})

		for name, sym := range out.Symbols {
			if sym.Kind == "" {
				t.Errorf("symbol %q: empty Kind", name)
			}
			if sym.Location == "" {
				t.Errorf("symbol %q: empty Location", name)
			}
			if sym.Signature != "" {
				t.Errorf("symbol %q: docs mode should have no Signature, got %q", name, sym.Signature)
			}
			if sym.Implementation != "" {
				t.Errorf("symbol %q: docs mode should have no Implementation", name)
			}
			if len(sym.Fields) > 0 {
				t.Errorf("symbol %q: docs mode should have no Fields", name)
			}
		}

		// PackageDoc should be populated.
		if out.PackageDoc == "" {
			t.Error("PackageDoc should be populated")
		}
	})

	t.Run("skeleton mode", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeSkeleton})

		// NewPerson should have a signature.
		np, ok := out.Symbols["NewPerson"]
		if !ok {
			t.Fatal("NewPerson not found")
		}
		if np.Signature == "" {
			t.Error("skeleton mode: NewPerson should have a Signature")
		}
		if np.Implementation != "" {
			t.Error("skeleton mode: NewPerson should have no Implementation")
		}

		// Person should have fields.
		person, ok := out.Symbols["Person"]
		if !ok {
			t.Fatal("Person not found")
		}
		if len(person.Fields) == 0 {
			t.Error("skeleton mode: Person should have Fields")
		}
	})

	t.Run("code mode", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeCode})

		np, ok := out.Symbols["NewPerson"]
		if !ok {
			t.Fatal("NewPerson not found in code mode")
		}
		if np.Implementation == "" {
			t.Error("code mode: NewPerson should have Implementation")
		}
		if !strings.Contains(np.Implementation, "func NewPerson") {
			t.Errorf("Implementation should contain func signature, got: %q", np.Implementation)
		}
	})

	t.Run("symbol filter", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{
			Package: dir,
			Mode:    lang.ModeSkeleton,
			Symbols: []string{"Person", "NewPerson"},
		})

		if _, ok := out.Symbols["Person"]; !ok {
			t.Error("Person should be present")
		}
		if _, ok := out.Symbols["NewPerson"]; !ok {
			t.Error("NewPerson should be present")
		}
		// Other symbols should be absent.
		for name := range out.Symbols {
			if name != "Person" && name != "NewPerson" {
				t.Errorf("unexpected symbol %q in filtered output", name)
			}
		}
	})

	t.Run("include private false", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeSkeleton, IncludePrivate: false})

		if _, ok := out.Symbols["maxAge"]; ok {
			t.Error("maxAge should be excluded when IncludePrivate=false")
		}
	})

	t.Run("include private true", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeSkeleton, IncludePrivate: true})

		if _, ok := out.Symbols["maxAge"]; !ok {
			t.Error("maxAge should be included when IncludePrivate=true")
		}
	})

	t.Run("symbol order", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeDocs})

		// Expected declaration order (exported only):
		// DefaultName, Greeter, Person, Person.Greet, NewPerson
		wantOrder := []string{"DefaultName", "Greeter", "Person", "Person.Greet", "NewPerson"}
		got := out.SymbolOrder
		// Filter to only the symbols we care about.
		var filtered []string
		for _, s := range got {
			if slices.Contains(wantOrder, s) {
				filtered = append(filtered, s)
			}
		}
		for i, want := range wantOrder {
			if i >= len(filtered) {
				t.Errorf("SymbolOrder: missing %q at position %d", want, i)
				continue
			}
			if filtered[i] != want {
				t.Errorf("SymbolOrder[%d]: got %q, want %q", i, filtered[i], want)
			}
		}
	})

	t.Run("package doc files imports", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeDocs})

		if out.PackageDoc == "" {
			t.Error("PackageDoc should be populated")
		}
		if len(out.Files) == 0 {
			t.Error("Files should be populated")
		}
		found := false
		for _, f := range out.Files {
			if strings.HasSuffix(f, "greet.go") || f == "greet.go" {
				found = true
			}
		}
		if !found {
			t.Errorf("Files should contain greet.go, got: %v", out.Files)
		}
		if !slices.Contains(out.Imports, "fmt") {
			t.Errorf("Imports should contain fmt, got: %v", out.Imports)
		}
	})

	t.Run("person fields", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeSkeleton})

		person, ok := out.Symbols["Person"]
		if !ok {
			t.Fatal("Person not found")
		}

		fieldMap := make(map[string]lang.FieldInfo)
		for _, f := range person.Fields {
			fieldMap[f.Name] = f
		}

		nameField, ok := fieldMap["Name"]
		if !ok {
			t.Fatal("Person.Name field not found")
		}
		if nameField.Type != "string" {
			t.Errorf("Person.Name type: got %q, want %q", nameField.Type, "string")
		}
		if !strings.Contains(nameField.Tag, `json:"name"`) {
			t.Errorf("Person.Name tag: got %q, want to contain json:\"name\"", nameField.Tag)
		}

		ageField, ok := fieldMap["Age"]
		if !ok {
			t.Fatal("Person.Age field not found")
		}
		if ageField.Type != "int" {
			t.Errorf("Person.Age type: got %q, want %q", ageField.Type, "int")
		}
		if !strings.Contains(ageField.Tag, `json:"age"`) {
			t.Errorf("Person.Age tag: got %q, want to contain json:\"age\"", ageField.Tag)
		}
	})

	t.Run("person methods", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeSkeleton})

		person, ok := out.Symbols["Person"]
		if !ok {
			t.Fatal("Person not found")
		}
		if !slices.Contains(person.Methods, "Greet") {
			t.Errorf("Person.Methods should contain Greet, got: %v", person.Methods)
		}
	})

	t.Run("greet method", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{Package: dir, Mode: lang.ModeSkeleton})

		greet, ok := out.Symbols["Person.Greet"]
		if !ok {
			t.Fatal("Person.Greet not found")
		}
		if greet.Kind != lang.KindMethod {
			t.Errorf("Person.Greet Kind: got %q, want %q", greet.Kind, lang.KindMethod)
		}
		if greet.Receiver != "*Person" {
			t.Errorf("Person.Greet Receiver: got %q, want %q", greet.Receiver, "*Person")
		}
	})

	t.Run("max output tokens truncation", func(t *testing.T) {
		dir := writeTestPackage(t)
		out := execute(t, lang.ExploreInput{
			Package:         dir,
			Mode:            lang.ModeCode,
			MaxOutputTokens: 50,
		})

		if !out.Truncated {
			t.Error("expected Truncated=true with MaxOutputTokens=50")
		}

		// With MaxOutputTokens=50 (200 chars), implementations should be cleared.
		for name, sym := range out.Symbols {
			if sym.Implementation != "" {
				t.Errorf(
					"symbol %q: Implementation should be cleared when truncated, got %d chars",
					name,
					len(sym.Implementation),
				)
			}
		}

		if len(out.NextActions) == 0 {
			t.Error("expected NextActions when truncated")
		}
	})

	t.Run("complex generic type fields", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		raw, _ := json.Marshal(lang.ExploreInput{
			Package: "./core",
			Symbols: []string{"Buffer"},
			Mode:    lang.ModeSkeleton,
		})
		result, err := golang.Explore.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		out, ok := result.(lang.ExploreOutput)
		if !ok {
			b, _ := json.Marshal(result)
			_ = json.Unmarshal(b, &out)
		}

		sym, found := out.Symbols["Buffer"]
		if !found {
			t.Fatalf("expected Buffer in symbols; got %v", symbolNames(out.Symbols))
		}
		if sym.Kind != lang.KindStruct {
			t.Errorf("Buffer.Kind: got %q, want %q", sym.Kind, lang.KindStruct)
		}
	})

	// skeleton mode must list symbols without bodies — proves the
	// token-budget claim in CLAUDE.md.
	t.Run("hardening skeleton omits bodies", func(t *testing.T) {
		dir := writeMod(t, "hardskel", map[string]string{
			"a.go": "package hardskel\n\n" +
				"// Compute returns the answer.\n" +
				"func Compute() int { return 42 + 7 - 1 }\n\n" +
				"func helper() int { return 99 }\n",
		})
		t.Chdir(dir)

		in := lang.ExploreInput{
			Package:        ".",
			Mode:           "skeleton",
			IncludePrivate: true,
		}
		raw, _ := json.Marshal(in)
		result, err := golang.Explore.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("explore execute: %v", err)
		}
		out := decodeExploreOutput(t, result)

		var combined strings.Builder
		for _, sym := range out.Symbols {
			combined.WriteString(sym.Implementation)
			combined.WriteString(sym.Signature)
		}
		if strings.Contains(combined.String(), "42 + 7 - 1") {
			t.Errorf("skeleton mode must not include function bodies; got:\n%s", combined.String())
		}
		if _, ok := out.Symbols["Compute"]; !ok {
			var keys []string
			for k := range out.Symbols {
				keys = append(keys, k)
			}
			t.Errorf("skeleton mode must surface the Compute symbol; got keys=%v", keys)
		}
	})
}

// ---- helpers ----

func execute(t *testing.T, input lang.ExploreInput) lang.ExploreOutput {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	result, err := golang.Explore.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := result.(lang.ExploreOutput)
	if !ok {
		// Try marshalling/unmarshalling to handle pointer returns.
		b, _ := json.Marshal(result)
		if err2 := json.Unmarshal(b, &out); err2 != nil {
			t.Fatalf("unexpected result type %T: %v", result, err2)
		}
	}
	return out
}

// TestExploreReadSideImprovements covers the four read-side cleanups
// added to lang.go.explore: noise filters (cache hashes, stdlib
// internals), the production_only flag, the overview mode, and the
// improved path-resolution error message.
func TestExploreReadSideImprovements(t *testing.T) {
	t.Run("imports_strip_testing_internal_testdeps", func(t *testing.T) {
		dir := writeMod(t, "implfilter", map[string]string{
			"a.go": "package implfilter\n\nfunc Foo() {}\n",
			"a_test.go": "package implfilter\n\nimport \"testing\"\n\n" +
				"func TestFoo(t *testing.T) { _ = t; Foo() }\n",
		})
		t.Chdir(dir)
		out := execute(t, lang.ExploreInput{Package: "."})
		for _, imp := range out.Imports {
			if strings.HasPrefix(imp, "testing/internal/") {
				t.Errorf("imports should not contain stdlib-internal helper, got %q", imp)
			}
		}
	})

	t.Run("production_only_skips_test_files_and_symbols", func(t *testing.T) {
		dir := writeMod(t, "prodonly", map[string]string{
			"prod.go": "package prodonly\n\nfunc Service() string { return \"ok\" }\n",
			"prod_test.go": "package prodonly\n\nimport \"testing\"\n\n" +
				"func TestService(t *testing.T) { _ = Service() }\n" +
				"func BenchmarkService(b *testing.B) { _ = Service() }\n",
		})
		t.Chdir(dir)

		full := execute(t, lang.ExploreInput{Package: "."})
		if _, ok := full.Symbols["TestService"]; !ok {
			t.Errorf("default explore should include TestService; got %v", full.SymbolOrder)
		}

		prod := execute(t, lang.ExploreInput{Package: ".", ProductionOnly: true})
		if _, ok := prod.Symbols["TestService"]; ok {
			t.Errorf("ProductionOnly should exclude TestService; got %v", prod.SymbolOrder)
		}
		if _, ok := prod.Symbols["BenchmarkService"]; ok {
			t.Errorf("ProductionOnly should exclude BenchmarkService; got %v", prod.SymbolOrder)
		}
		if _, ok := prod.Symbols["Service"]; !ok {
			t.Errorf("ProductionOnly should keep production symbol Service; got %v", prod.SymbolOrder)
		}
		for _, f := range prod.Files {
			if strings.HasSuffix(f, "_test.go") {
				t.Errorf("ProductionOnly should drop *_test.go files; got %q", f)
			}
		}
	})

	t.Run("overview_mode_returns_one_line_summary_only", func(t *testing.T) {
		dir := writeMod(t, "overview", map[string]string{
			"a.go": "// Package overview is a test fixture.\npackage overview\n\n" +
				"// Greet returns a greeting for name.\nfunc Greet(name string) string { return name }\n\n" +
				"// User holds user data.\ntype User struct { Name string }\n",
		})
		t.Chdir(dir)
		out := execute(t, lang.ExploreInput{Package: ".", Mode: lang.ModeOverview})

		greet, ok := out.Symbols["Greet"]
		if !ok {
			t.Fatalf("expected Greet in overview output; got %v", out.SymbolOrder)
		}
		// Overview mode must strip rich fields and populate OneLineSummary.
		if greet.OneLineSummary == "" {
			t.Errorf("expected OneLineSummary populated in overview mode")
		}
		if greet.Signature != "" || greet.Implementation != "" ||
			greet.Docblock != "" || greet.Location != "" {
			t.Errorf("overview mode should drop Signature/Implementation/Docblock/Location; got %+v", greet)
		}
		if !strings.Contains(greet.OneLineSummary, "Greet") {
			t.Errorf("OneLineSummary should include symbol name; got %q", greet.OneLineSummary)
		}
		if !strings.Contains(greet.OneLineSummary, "greeting") {
			t.Errorf("OneLineSummary should include doc sentence; got %q", greet.OneLineSummary)
		}
	})

	t.Run("path_error_suggests_qualified_form", func(t *testing.T) {
		dir := writeMod(t, "patherr", map[string]string{
			"a.go": "package patherr\n\nfunc Foo() {}\n",
		})
		t.Chdir(dir)
		_, err := executeExploreRaw(t, lang.ExploreInput{Package: "cmd/missing"})
		if err == nil {
			t.Fatal("expected error for unqualified non-existent path")
		}
		msg := err.Error()
		if !strings.Contains(msg, "patherr/cmd/missing") {
			t.Errorf("error should suggest qualified path 'patherr/cmd/missing'; got: %v", err)
		}
	})
}

func executeExploreRaw(t *testing.T, in lang.ExploreInput) (lang.ExploreOutput, error) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err := golang.Explore.Execute(t.Context(), raw)
	if err != nil {
		return lang.ExploreOutput{}, err
	}
	out, ok := result.(lang.ExploreOutput)
	if !ok {
		b, _ := json.Marshal(result)
		if err2 := json.Unmarshal(b, &out); err2 != nil {
			t.Fatalf("unexpected result type %T: %v", result, err2)
		}
	}
	return out, nil
}
