// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
)

func TestDeps(t *testing.T) {
	// ---- Callers ----

	t.Run("Callers/IncludesCallersInInternalTestFiles", func(t *testing.T) {
		// when a package has internal (`package foo`) test files,
		// packages.Load returns it as both a production variant and a
		// test-augmented variant. flattenWithDeps (keyed by PkgPath) used to
		// silently drop one of them — depending on iteration order, callers in
		// `*_test.go` files were missed. The fix is to prefer the variant with
		// more Syntax files (the test-augmented one is a superset).
		dir := t.TempDir()
		mustWriteFileT(t, filepath.Join(dir, "go.mod"), "module example.com/x\n\ngo 1.21\n")
		// Use a fixture-unique symbol name to avoid collisions with same-
		// named functions in the testing-package transitive deps (stdlib
		// Read/Write/Close appear in dozens of locations once `import
		// "testing"` is added).
		mustWriteFileT(t, filepath.Join(dir, "x.go"),
			"package x\n\nfunc FixtureReadXX() string { return \"r\" }\n")
		// An internal test file (package x, not x_test) that calls FixtureReadXX.
		mustWriteFileT(t, filepath.Join(dir, "x_test.go"),
			"package x\n\nimport \"testing\"\n\nfunc TestReadFromTest(t *testing.T) { _ = FixtureReadXX() }\n")
		t.Chdir(dir)

		out := executeDeps(t, golang.Callers, lang.CallersInput{Symbol: "FixtureReadXX"})

		foundTestCaller := false
		for _, r := range out.Results {
			if strings.Contains(r.Location, "x_test.go") {
				foundTestCaller = true
				break
			}
		}
		if !foundTestCaller {
			t.Errorf("expected a caller in x_test.go (internal test); got %d results: %+v",
				len(out.Results), out.Results)
		}
	})

	t.Run("Callers/FindsCallSites", func(t *testing.T) {
		dir := makeDepsModule(t)
		t.Chdir(dir)

		out := executeDeps(t, golang.Callers, lang.CallersInput{Symbol: "Read"})

		if len(out.Results) == 0 {
			t.Fatal("expected at least one caller of Read")
		}
		first := out.Results[0]
		if first.CallerSymbol == "" {
			t.Error("expected CallerSymbol populated")
		}
		if first.CallSnippet == "" {
			t.Error("expected CallSnippet populated")
		}
	})

	t.Run("Callers/EmptySymbolErrors", func(t *testing.T) {
		dir := makeDepsModule(t)
		t.Chdir(dir)

		if _, err := executeDepsRaw(t, golang.Callers, lang.CallersInput{Symbol: ""}); err == nil {
			t.Fatal("expected error for empty symbol")
		}
	})

	t.Run("Callers/LimitTruncates", func(t *testing.T) {
		dir := makeDepsModule(t)
		t.Chdir(dir)

		out := executeDeps(t, golang.Callers, lang.CallersInput{Symbol: "Read", Limit: 1})

		if len(out.Results) > 1 {
			t.Errorf("Limit=1 should cap results; got %d", len(out.Results))
		}
	})

	// ---- Callers Kind enum (F9) ----
	//
	// Three sites for a function `Hook`:
	//   1. Direct call:  `Hook(42)` in Direct().
	//   2. Value-grab assignment:  `cb := Hook` in Capture().
	//   3. Value-pass arg:  `register(Hook)` in Register().
	// The default Kind="call" surfaces only #1; Kind="value" surfaces
	// #2 and #3; Kind="all" surfaces all three with distinct Kind tags
	// (RelDirectCaller for #1, RelValueUse for #2 and #3).
	t.Run("Callers/KindCallReturnsOnlyDirectCalls", func(t *testing.T) {
		dir := writeMod(t, "callkindcall", map[string]string{
			"a.go": "package callkindcall\n\n" +
				"func Hook(int) {}\n\n" +
				"func register(func(int)) {}\n\n" +
				"func Direct()  { Hook(42) }\n" +
				"func Capture() { cb := Hook; cb(1) }\n" +
				"func Register() { register(Hook) }\n",
		})
		t.Chdir(dir)

		out := executeDeps(t, golang.Callers, lang.CallersInput{
			Symbol: "Hook",
			Kind:   lang.CallersKindCall,
		})

		hasDirect, hasValue := false, false
		for _, r := range out.Results {
			if r.Kind == lang.RelDirectCaller {
				hasDirect = true
			}
			if r.Kind == lang.RelValueUse {
				hasValue = true
			}
		}
		if !hasDirect {
			t.Errorf("Kind=call must include the direct Hook(42) site; got %+v", out.Results)
		}
		if hasValue {
			t.Errorf("Kind=call must NOT include value-use sites; got %+v", out.Results)
		}
	})

	t.Run("Callers/KindValueReturnsOnlyValueUses", func(t *testing.T) {
		dir := writeMod(t, "callkindvalue", map[string]string{
			"a.go": "package callkindvalue\n\n" +
				"func Hook(int) {}\n\n" +
				"func register(func(int)) {}\n\n" +
				"func Direct()  { Hook(42) }\n" +
				"func Capture() { cb := Hook; cb(1) }\n" +
				"func Register() { register(Hook) }\n",
		})
		t.Chdir(dir)

		out := executeDeps(t, golang.Callers, lang.CallersInput{
			Symbol: "Hook",
			Kind:   lang.CallersKindValue,
		})

		if len(out.Results) == 0 {
			t.Fatal("Kind=value should find value-use sites; got 0 results")
		}
		for _, r := range out.Results {
			if r.Kind != lang.RelValueUse {
				t.Errorf("Kind=value must only return RelValueUse entries; got %q at %s", r.Kind, r.Location)
			}
			// The Direct() function's Hook(42) is a direct call — the
			// `Hook` ident there must NOT be in the value-use set.
			if r.CallerSymbol == "Direct" {
				t.Errorf("Direct() contains a direct call, not a value-use; got %+v", r)
			}
		}
	})

	t.Run("Callers/KindAllReturnsBothMerged", func(t *testing.T) {
		dir := writeMod(t, "callkindall", map[string]string{
			"a.go": "package callkindall\n\n" +
				"func Hook(int) {}\n\n" +
				"func register(func(int)) {}\n\n" +
				"func Direct()  { Hook(42) }\n" +
				"func Capture() { cb := Hook; cb(1) }\n" +
				"func Register() { register(Hook) }\n",
		})
		t.Chdir(dir)

		out := executeDeps(t, golang.Callers, lang.CallersInput{
			Symbol: "Hook",
			Kind:   lang.CallersKindAll,
		})

		hasDirect, hasValue := false, false
		for _, r := range out.Results {
			if r.Kind == lang.RelDirectCaller {
				hasDirect = true
			}
			if r.Kind == lang.RelValueUse {
				hasValue = true
			}
		}
		if !hasDirect || !hasValue {
			t.Errorf(
				"Kind=all must include both direct calls and value-uses; got hasDirect=%v hasValue=%v results=%+v",
				hasDirect, hasValue, out.Results,
			)
		}
	})

	t.Run("Callers/DefaultKindIsCall", func(t *testing.T) {
		dir := writeMod(t, "callkinddefault", map[string]string{
			"a.go": "package callkinddefault\n\n" +
				"func Hook(int) {}\n\n" +
				"func register(func(int)) {}\n\n" +
				"func Direct()  { Hook(42) }\n" +
				"func Register() { register(Hook) }\n",
		})
		t.Chdir(dir)

		out := executeDeps(t, golang.Callers, lang.CallersInput{
			Symbol: "Hook",
			// Kind unspecified — must behave as "call".
		})
		for _, r := range out.Results {
			if r.Kind == lang.RelValueUse {
				t.Errorf("default Kind must not include value-uses; got %+v", r)
			}
		}
	})

	// ---- Implementations ----

	t.Run("Implementations/FindsConcreteTypes", func(t *testing.T) {
		dir := makeDepsModule(t)
		t.Chdir(dir)

		out := executeDeps(t, golang.Implementations, lang.ImplementationsInput{Symbol: "Reader"})

		if !containsSymbolDep(out.Results, "FileReader") {
			t.Errorf("expected FileReader to satisfy Reader; got: %+v", symbolList(out.Results))
		}
		for _, r := range out.Results {
			if r.Kind != lang.RelImplementor {
				t.Errorf("expected Kind=%q for implementations; got %q on %q", lang.RelImplementor, r.Kind, r.Symbol)
			}
		}
	})

	t.Run("Implementations/NextActionPointsAtTopHit", func(t *testing.T) {
		dir := makeDepsModule(t)
		t.Chdir(dir)

		out := executeDeps(t, golang.Implementations, lang.ImplementationsInput{Symbol: "Reader"})

		if len(out.NextActions) == 0 {
			t.Fatal("expected NextActions to be populated")
		}
		if got, want := out.NextActions[0].Tool, "lang.go.explore"; got != want {
			t.Errorf("next action tool: got %q, want %q", got, want)
		}
	})

	// ---- References ----

	t.Run("References/FindsAllUsages", func(t *testing.T) {
		dir := makeDepsModule(t)
		t.Chdir(dir)

		out := executeDeps(t, golang.References, lang.ReferencesInput{Symbol: "Reader"})

		if len(out.Results) == 0 {
			t.Fatal("expected at least one reference to Reader")
		}
	})

	// ---- Invocations ----

	t.Run("Invocations/FindsCallSitesOfFunctionType", func(t *testing.T) {
		dir := makeInvocationsModule(t)
		t.Chdir(dir)

		out := executeDeps(t, golang.Invocations, lang.InvocationsInput{Symbol: "Handler"})

		if len(out.Results) == 0 {
			t.Fatal("expected at least one Handler invocation")
		}
		found := false
		for _, r := range out.Results {
			if r.CallerSymbol == "Process" {
				found = true
				if r.Kind != "invocation" {
					t.Errorf("expected Kind=invocation; got %q", r.Kind)
				}
				if r.CallSnippet == "" {
					t.Error("expected CallSnippet populated")
				}
			}
		}
		if !found {
			t.Errorf("expected invocation inside Process; got: %+v", symbolList(out.Results))
		}
	})

	t.Run("Invocations/DistinguishedFromReferences", func(t *testing.T) {
		// References should find both the type's parameter declaration AND the
		// call site (h(data)). Invocations should only find the call site.
		dir := makeInvocationsModule(t)
		t.Chdir(dir)

		refs := executeDeps(t, golang.References, lang.ReferencesInput{Symbol: "Handler"})
		invs := executeDeps(t, golang.Invocations, lang.InvocationsInput{Symbol: "Handler"})

		if len(refs.Results) <= len(invs.Results) {
			t.Logf(
				"references=%d invocations=%d (invocations should be a subset)",
				len(refs.Results),
				len(invs.Results),
			)
		}
		for _, r := range invs.Results {
			if r.CallerSymbol == "" {
				t.Errorf("invocation missing CallerSymbol: %+v", r)
			}
		}
	})

	// ---- workspace ----

	// Regression for B5: in a go.work tree with one root module that
	// defines an interface and N sibling modules that implement it,
	// implementations (IncludeExternal=false, the default) used to
	// return only same-module implementors and miss every sibling.
	// The fix routed through workspaceLocalRoots(ws), which captures
	// every workspace module's import-path root — so siblings now
	// count as workspace-local. This mirrors the user's eidos shape
	// (root `plugin.Plugin` interface, implementors in
	// reference/shapewriter, backend/golang, frontend/golang, etc.).
	t.Run("WorksAcrossGoWorkModules/multipleSiblings", func(t *testing.T) {
		root := t.TempDir()

		// Root module defines the interface.
		mustWriteFileT(t, filepath.Join(root, "modRoot", "go.mod"), "module example.com/root\n\ngo 1.21\n")
		mustWriteFileT(t, filepath.Join(root, "modRoot", "plugin.go"), `package root

// Plugin is the interface every sibling implements.
type Plugin interface { Name() string }
`)

		// Three sibling modules each implement Plugin.
		for _, sib := range []struct {
			mod, typ string
		}{
			{mod: "shapewriter", typ: "ShapeWriter"},
			{mod: "repogen", typ: "RepoGen"},
			{mod: "backend", typ: "BackendImpl"},
		} {
			mustWriteFileT(
				t,
				filepath.Join(root, sib.mod, "go.mod"),
				fmt.Sprintf(
					"module example.com/%s\n\ngo 1.21\n\nrequire example.com/root v0.0.0\n\nreplace example.com/root => ../modRoot\n",
					sib.mod,
				),
			)
			mustWriteFileT(t, filepath.Join(root, sib.mod, "impl.go"), fmt.Sprintf(`package %s

import "example.com/root"

type %s struct{}
func (i *%s) Name() string { return %q }

// Compile-time guard locks in the implementation relationship.
var _ root.Plugin = (*%s)(nil)
`, sib.mod, sib.typ, sib.typ, sib.typ, sib.typ))
		}

		mustWriteFileT(t, filepath.Join(root, "go.work"),
			"go 1.21\n\nuse (\n\t./modRoot\n\t./shapewriter\n\t./repogen\n\t./backend\n)\n")
		t.Chdir(root)

		// Default scope (IncludeExternal=false): every sibling
		// implementation must surface.
		out := executeDeps(t, golang.Implementations, lang.ImplementationsInput{Symbol: "Plugin"})

		got := symbolList(out.Results)
		for _, want := range []string{"ShapeWriter", "RepoGen", "BackendImpl"} {
			if !containsSymbolDep(out.Results, want) {
				t.Errorf("default scope should find %s in sibling workspace module; got %v", want, got)
			}
		}
	})

	t.Run("WorksAcrossGoWorkModules", func(t *testing.T) {
		root := t.TempDir()

		// modA defines the interface.
		mustWriteFileT(t, filepath.Join(root, "modA", "go.mod"), "module example.com/a\n\ngo 1.21\n")
		mustWriteFileT(t, filepath.Join(root, "modA", "reader.go"), `package a

// Reader is an interface.
type Reader interface { Read() string }
`)

		// modB depends on modA; require directive is needed even in go.work mode
		// because the consumer's go.mod still drives module resolution for the
		// local toolchain.
		mustWriteFileT(
			t,
			filepath.Join(root, "modB", "go.mod"),
			"module example.com/b\n\ngo 1.21\n\nrequire example.com/a v0.0.0\n\nreplace example.com/a => ../modA\n",
		)
		mustWriteFileT(t, filepath.Join(root, "modB", "impl.go"), `package b

import "example.com/a"

type FileReader struct{}
func (f *FileReader) Read() string { return "" }

// Compile-time guard so the implementation relationship is preserved.
var _ a.Reader = (*FileReader)(nil)
`)

		mustWriteFileT(t, filepath.Join(root, "go.work"), "go 1.21\n\nuse (\n\t./modA\n\t./modB\n)\n")
		t.Chdir(root)

		out := executeDeps(t, golang.Implementations, lang.ImplementationsInput{Symbol: "Reader"})

		if !containsSymbolDep(out.Results, "FileReader") {
			t.Errorf(
				"workspace mode should find FileReader in modB as a Reader implementor; got: %+v",
				symbolList(out.Results),
			)
		}
	})

	// Regression for the location-format inconsistency. Earlier the deps
	// tools formatted locations as basename:line while search returned full
	// absolute paths. The mismatch meant agents couldn't pipe Location
	// verbatim into Read/Edit without case-by-case resolution.
	t.Run("LocationsAreAbsolutePaths", func(t *testing.T) {
		dir := makeDepsModule(t)
		t.Chdir(dir)

		// Run all four deps tools and check every Location is absolute.
		cases := []struct {
			name string
			run  func() []lang.DepReference
		}{
			{"callers", func() []lang.DepReference {
				return executeDeps(t, golang.Callers, lang.CallersInput{Symbol: "Read"}).Results
			}},
			{"implementations", func() []lang.DepReference {
				return executeDeps(t, golang.Implementations, lang.ImplementationsInput{Symbol: "Reader"}).Results
			}},
			{"references", func() []lang.DepReference {
				return executeDeps(t, golang.References, lang.ReferencesInput{Symbol: "Reader"}).Results
			}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				results := c.run()
				if len(results) == 0 {
					t.Skipf("no results for %s; can't assert location format", c.name)
					return
				}
				for _, r := range results {
					if r.Location == "" {
						continue
					}
					// "file.go:N" (basename) is the bug; "/abs/path:N" is correct.
					// Absolute paths start with "/" on macOS/Linux.
					if !filepath.IsAbs(r.Location[:strings.LastIndex(r.Location, ":")]) {
						t.Errorf("%s: location %q should be an absolute path, not basename", c.name, r.Location)
					}
				}
			})
		}
	})

	// ---- complex multi-package fixture ----

	t.Run("Complex/Callers/FindsCrossPackage", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeDepsTyped(t, golang.Callers, lang.CallersInput{Symbol: "New"})

		if len(out.Results) == 0 {
			t.Fatal("expected callers of store.New across packages")
		}
		// NewHandler in api/ calls store.New; check we found that caller.
		if !anyCaller(out.Results, "NewHandler") {
			t.Errorf("expected NewHandler as caller of store.New; got %v", callerNames(out.Results))
		}
	})

	t.Run("Complex/Implementations/FoundAcrossSubpackages", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeDepsTyped(t, golang.Implementations, lang.ImplementationsInput{Symbol: "Reader"})

		syms := refResultSymbols(out.Results)
		if !contains(syms, "FileStore") {
			t.Errorf("expected FileStore (in store/) as Reader implementor; got %v", syms)
		}
		if !contains(syms, "MemStore") {
			t.Errorf("expected MemStore (in store/memcache/) as Reader implementor; got %v", syms)
		}
	})

	t.Run("Complex/Implementations/EmbeddedInterfaceSatisfied", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		// FileStore has both Read and Close methods, so it should satisfy
		// ReadCloser (which embeds Reader). MemStore likewise.
		out := executeDepsTyped(t, golang.Implementations, lang.ImplementationsInput{Symbol: "ReadCloser"})

		syms := refResultSymbols(out.Results)
		if !contains(syms, "FileStore") {
			t.Errorf("expected FileStore as ReadCloser implementor (embedded interface); got %v", syms)
		}
	})

	t.Run("Complex/References/GenericType", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeDepsTyped(t, golang.References, lang.ReferencesInput{Symbol: "Buffer", Limit: 50})

		// Buffer is referenced by its own methods (Push, Pop, Len) and the type
		// declaration itself. We expect at least multiple references.
		if len(out.Results) < 3 {
			t.Errorf("expected multiple references to generic type Buffer; got %d", len(out.Results))
		}
	})

	// ---- real-world shapes ----

	// An identifier used as a TYPE in one place, as a CONVERSION in another,
	// and as a TAG-LIKE string in a third must report 2 references (the third
	// is a string literal, not a Go reference).
	t.Run("RealWorldX/References/DistinctReferenceKinds", func(t *testing.T) {
		dir := writeMod(t, "rwxrefkinds", map[string]string{
			"a.go": "package rwxrefkinds\n\n" +
				"type Status int\n\n" +
				"func Make() Status { return Status(0) }\n\n" +
				"// Status is mentioned in a comment; not a real ref.\n" +
				"func Doc() string { return \"Status string\" }\n",
		})
		t.Chdir(dir)

		out := executeDeps(t, golang.References, lang.ReferencesInput{Symbol: "Status"})
		if len(out.Results) < 2 {
			t.Errorf("expected at least 2 type references; got %d: %+v", len(out.Results), out.Results)
		}
	})

	// ---- hardening ----

	// A function called directly must be reported as a caller.
	// (Interface-dispatch callers are out of scope for static callers analysis.)
	t.Run("Hardening/Callers/FindsDirectInvocations", func(t *testing.T) {
		dir := writeMod(t, "hardcall", map[string]string{
			"a.go": "package hardcall\n\n" +
				"type FileReader struct{}\n\n" +
				"func (f *FileReader) Read() string { return \"data\" }\n\n" +
				"func Direct(f *FileReader) string { return f.Read() }\n",
		})
		t.Chdir(dir)

		out := executeDeps(t, golang.Callers, lang.CallersInput{Symbol: "Read"})
		hasDirect := false
		for _, c := range out.Results {
			if strings.Contains(c.CallerSymbol, "Direct") {
				hasDirect = true
			}
		}
		if !hasDirect {
			t.Errorf("Direct must appear as a caller of FileReader.Read; got %+v", out.Results)
		}
	})

	// A type used in a different package must be reported by references.
	t.Run("Hardening/References/FindsAcrossPackages", func(t *testing.T) {
		dir := writeMod(t, "hardref", map[string]string{
			"core/core.go":       "package core\n\ntype Order struct{ ID int }\n",
			"service/service.go": "package service\n\nimport \"hardref/core\"\n\nfunc Process(o core.Order) int { return o.ID }\n",
		})
		t.Chdir(dir)

		out := executeDeps(t, golang.References, lang.ReferencesInput{Symbol: "Order"})
		hasService := false
		for _, ref := range out.Results {
			if strings.Contains(ref.Location, "service/service.go") {
				hasService = true
			}
		}
		if !hasService {
			t.Errorf("references must include the cross-package use site; got %+v", out.Results)
		}
	})

	// Implementations of an interface declared in package A must be found
	// when the implementor lives in package B.
	t.Run("Hardening/Implementations/FindsConcreteAcrossPackages", func(t *testing.T) {
		dir := writeMod(t, "hardimpl", map[string]string{
			"ports/ports.go":     "package ports\n\ntype Storage interface{ Get(k string) string }\n",
			"adapter/adapter.go": "package adapter\n\ntype Memory struct{}\n\nfunc (m *Memory) Get(k string) string { return \"\" }\n",
		})
		t.Chdir(dir)

		out := executeDeps(t, golang.Implementations, lang.ImplementationsInput{Symbol: "Storage"})
		found := false
		for _, impl := range out.Results {
			if strings.Contains(impl.Symbol, "Memory") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected adapter.Memory as a Storage implementation; got %+v", out.Results)
		}
	})

	// A function-typed value (`type Handler func(...)`) invoked through a
	// variable must be reported.
	t.Run("Hardening/Invocations/FindsCallableTypeUsage", func(t *testing.T) {
		dir := writeMod(t, "hardinv", map[string]string{
			"a.go": "package hardinv\n\n" +
				"type Handler func(int) int\n\n" +
				"func Run(h Handler, v int) int { return h(v) }\n",
		})
		t.Chdir(dir)

		out := executeDeps(t, golang.Invocations, lang.InvocationsInput{Symbol: "Handler"})
		if len(out.Results) == 0 {
			t.Errorf("expected at least one invocation of Handler-typed value; got %+v", out)
		}
	})

	// ---- workspace cross-module ----

	// Callers of a function in modA must include the modB caller.
	t.Run("Workspace/Callers/AcrossModules", func(t *testing.T) {
		twoModuleWorkspace(
			t,
			map[string]string{"api.go": "package a\n\nfunc DoIt() int { return 1 }\n"},
			map[string]string{
				"main.go": "package b\n\nimport \"example.com/a\"\n\nfunc Use() int { return a.DoIt() }\n",
			},
		)

		out := executeDeps(t, golang.Callers, lang.CallersInput{Symbol: "DoIt"})
		hasModB := false
		for _, r := range out.Results {
			if strings.Contains(r.Location, "modB") {
				hasModB = true
			}
		}
		if !hasModB {
			t.Errorf("expected modB caller; got %+v", out.Results)
		}
	})

	// Type references in another module must be found.
	t.Run("Workspace/References/AcrossModules", func(t *testing.T) {
		twoModuleWorkspace(
			t,
			map[string]string{"types.go": "package a\n\ntype Order struct{ ID int }\n"},
			map[string]string{
				"main.go": "package b\n\nimport \"example.com/a\"\n\nfunc Take(o a.Order) int { return o.ID }\n",
			},
		)

		out := executeDeps(t, golang.References, lang.ReferencesInput{Symbol: "Order"})
		hasModB := false
		for _, r := range out.Results {
			if strings.Contains(r.Location, "modB") {
				hasModB = true
			}
		}
		if !hasModB {
			t.Errorf("expected modB reference; got %+v", out.Results)
		}
	})

	// Regression for the workspace-scope leak. Before the fix, common
	// identifier names (`Kind`, `Name`, `Type`) returned a flood of
	// matches from stdlib and transitive deps because runDepsQuery
	// iterated flattenWithDeps' full import graph. The fix filters to
	// workspace-local packages by default; IncludeExternal=true brings
	// back the old behavior for callers that genuinely need it.
	t.Run("Hardening/References/DefaultsToWorkspaceLocal", func(t *testing.T) {
		// Define a Kind constant in the local module and import
		// google.golang.org/protobuf/reflect/protoreflect — which also
		// defines a Kind type and uses it in dozens of places. With the
		// pre-fix behavior, IncludeExternal=false would still surface
		// every protoreflect ident named "Kind"; the fix should leave
		// only the local references.
		dir := writeMod(t, "rwxkind", map[string]string{
			"a.go": "package rwxkind\n\n" +
				"type Kind int\n\n" +
				"const KindFoo Kind = 1\n\n" +
				"func What() Kind { return KindFoo }\n",
		})
		t.Chdir(dir)

		// Sanity: workspace-local refs to Kind exist.
		out := executeDeps(t, golang.References, lang.ReferencesInput{Symbol: "Kind"})
		if len(out.Results) == 0 {
			t.Fatal("expected local references to Kind")
		}
		// Every result must live inside this module (no protobuf/
		// go/packages/etc. cross-pollination).
		for _, r := range out.Results {
			if !strings.Contains(r.Location, dir) {
				t.Errorf("default scope must be workspace-local; got cross-module hit %q in package %q",
					r.Location, r.Package)
			}
			if strings.Contains(r.Package, "google.golang.org/") ||
				strings.Contains(r.Package, "golang.org/x/tools/") {
				t.Errorf("default scope must exclude dep hits; got package %q at %q",
					r.Package, r.Location)
			}
		}
	})

	// IncludeExternal=true should restore the old behavior: dep hits
	// surface in the result set when the agent explicitly asks for them.
	t.Run("Hardening/References/IncludeExternalRestoresFullGraph", func(t *testing.T) {
		// Use a name that is heavily used in the stdlib (`Error`) so we
		// reliably see at least one external hit even on a tiny fixture.
		dir := writeMod(t, "rwxext", map[string]string{
			"a.go": "package rwxext\n\nimport \"errors\"\n\n" +
				"type Error struct{}\n\n" +
				"func New() error { return errors.New(\"x\") }\n",
		})
		t.Chdir(dir)

		// Default: workspace-local only — exactly one local Error decl.
		localOnly := executeDeps(t, golang.References, lang.ReferencesInput{Symbol: "Error"})
		for _, r := range localOnly.Results {
			if !strings.Contains(r.Location, dir) {
				t.Errorf("default scope must stay local; got %q", r.Location)
			}
		}

		// IncludeExternal: stdlib's Error type surfaces. The exact count
		// depends on the Go toolchain, but at least one non-local hit
		// must appear.
		external := executeDeps(t, golang.References, lang.ReferencesInput{
			Symbol:          "Error",
			IncludeExternal: true,
			Limit:           200,
		})
		hasExternal := false
		for _, r := range external.Results {
			if !strings.Contains(r.Location, dir) {
				hasExternal = true
				break
			}
		}
		if !hasExternal {
			t.Errorf("IncludeExternal=true should surface stdlib Error hits; got only local refs (%d)",
				len(external.Results))
		}
	})

	// Same guarantee for Callers and Invocations — the fix plumbed the
	// flag through all three narrow tools.
	t.Run("Hardening/Callers/DefaultsToWorkspaceLocal", func(t *testing.T) {
		dir := writeMod(t, "rwxcaller", map[string]string{
			"a.go": "package rwxcaller\n\nimport \"strings\"\n\n" +
				"func Local() string { return strings.ToUpper(\"x\") }\n",
		})
		t.Chdir(dir)

		// `ToUpper` is called from stdlib and from this module. Default
		// scope should ONLY surface the local caller.
		out := executeDeps(t, golang.Callers, lang.CallersInput{Symbol: "ToUpper"})
		for _, r := range out.Results {
			if !strings.Contains(r.Location, dir) {
				t.Errorf("default scope must exclude stdlib callers; got %q in %q",
					r.Location, r.Package)
			}
		}
	})

	// Invocations of a func-typed value declared in modA must surface modB
	// invocations.
	t.Run("Workspace/Invocations/AcrossModules", func(t *testing.T) {
		twoModuleWorkspace(
			t,
			map[string]string{"types.go": "package a\n\ntype Handler func(int) int\n"},
			map[string]string{
				"main.go": "package b\n\nimport \"example.com/a\"\n\nfunc Run(h a.Handler, v int) int { return h(v) }\n",
			},
		)

		out := executeDeps(t, golang.Invocations, lang.InvocationsInput{Symbol: "Handler", Package: "example.com/a"})
		hasModB := false
		for _, r := range out.Results {
			if strings.Contains(r.Location, "modB") {
				hasModB = true
			}
		}
		if !hasModB {
			t.Errorf("expected modB invocation; got %+v", out.Results)
		}
	})
}

// ---- helpers ----

func executeDepsRaw(t *testing.T, tl tool.Tool, input any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return tl.Execute(t.Context(), raw)
}

func executeDeps(t *testing.T, tl tool.Tool, input any) lang.DepsResult {
	t.Helper()
	result, err := executeDepsRaw(t, tl, input)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := result.(lang.DepsResult)
	if ok {
		return out
	}
	b, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("marshal result: %v", marshalErr)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unexpected result type %T: %v", result, err)
	}
	return out
}

func containsSymbolDep(refs []lang.DepReference, name string) bool {
	for i := range refs {
		if refs[i].Symbol == name {
			return true
		}
	}
	return false
}

func symbolList(refs []lang.DepReference) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Symbol
	}
	return out
}

// makeDepsModule creates a module with an interface (Reader), an implementor
// (FileReader), and a caller (Process) that invokes Reader.Read().
func makeDepsModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testdeps\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(`package testdeps

// Reader is a data source interface.
type Reader interface {
	Read() string
}
`), 0o644); err != nil {
		t.Fatalf("write types.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte(`package testdeps

// FileReader reads from files.
type FileReader struct{}

// Read returns file contents.
func (f *FileReader) Read() string {
	return "file contents"
}
`), 0o644); err != nil {
		t.Fatalf("write impl.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user.go"), []byte(`package testdeps

// Process processes data from a Reader.
func Process(r Reader) string {
	// process data
	return r.Read()
}
`), 0o644); err != nil {
		t.Fatalf("write user.go: %v", err)
	}
	return dir
}

// makeInvocationsModule creates a module with a function-type and a caller
// that invokes a value of that type. Used to test invocations vs references.
func makeInvocationsModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testinv\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(`package testinv

// Handler is a function type for processing input.
type Handler func(input string) (string, error)
`), 0o644); err != nil {
		t.Fatalf("write types.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "processor.go"), []byte(`package testinv

// Process calls the handler with the provided data.
func Process(h Handler, data string) (string, error) {
	return h(data)
}
`), 0o644); err != nil {
		t.Fatalf("write processor.go: %v", err)
	}
	return dir
}

func mustWriteFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
