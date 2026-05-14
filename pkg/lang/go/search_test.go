// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

func TestSearch(t *testing.T) {
	// ---- core search behavior ----

	t.Run("finds symbol across packages", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "SharedSymbol",
			MaxResults: 20,
		})

		if out.TotalMatches == 0 {
			t.Fatal("expected at least one match for SharedSymbol, got none")
		}
		pkgsSeen := make(map[string]bool)
		for _, r := range out.Results {
			if r.Symbol == "SharedSymbol" {
				pkgsSeen[r.Package] = true
			}
		}
		if len(pkgsSeen) < 2 {
			t.Errorf("expected SharedSymbol in 2 packages, found in: %v", pkgsSeen)
		}
	})

	t.Run("kind filter func", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "SharedSymbol",
			Kind:       lang.KindFunc,
			MaxResults: 20,
		})

		if len(out.Results) == 0 {
			t.Fatal("expected at least one func match for SharedSymbol")
		}
		for _, r := range out.Results {
			if r.Kind != lang.KindFunc {
				t.Errorf("expected only func results, got kind %q for %q", r.Kind, r.Symbol)
			}
		}
	})

	t.Run("kind filter struct", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "Greeter",
			Kind:       lang.KindStruct,
			MaxResults: 20,
		})

		if len(out.Results) == 0 {
			t.Fatal("expected at least one struct match for Greeter")
		}
		for _, r := range out.Results {
			if r.Kind != lang.KindStruct {
				t.Errorf("expected only struct results, got kind %q for %q", r.Kind, r.Symbol)
			}
		}
	})

	t.Run("kind filter method", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "Hello",
			Kind:       lang.KindMethod,
			MaxResults: 20,
		})

		if len(out.Results) == 0 {
			t.Fatal("expected at least one method match for Hello")
		}
		for _, r := range out.Results {
			if r.Kind != lang.KindMethod {
				t.Errorf("expected only method results, got kind %q for %q", r.Kind, r.Symbol)
			}
			// Methods should be receiver-qualified.
			if !strings.Contains(r.Symbol, ".") {
				t.Errorf("expected receiver-qualified method name (Receiver.Method), got %q", r.Symbol)
			}
		}
	})

	t.Run("exported only by default", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		// "lower" is unexported; default IncludePrivate=false should hide it.
		out := executeSearch(t, lang.SearchInput{
			Query:      "lower",
			MaxResults: 20,
		})

		for _, r := range out.Results {
			if r.Symbol == "lowerHelper" {
				t.Errorf(
					"unexported lowerHelper should not appear when IncludePrivate=false; got %v",
					searchResultNames(out.Results),
				)
			}
		}
	})

	t.Run("include private true", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{
			Query:          "lower",
			IncludePrivate: true,
			MaxResults:     20,
		})

		if !containsSymbol(out.Results, "lowerHelper") {
			t.Errorf("expected lowerHelper when IncludePrivate=true, got %v", searchResultNames(out.Results))
		}
	})

	t.Run("exact match ranks ahead", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "SharedSymbol",
			MaxResults: 20,
		})

		if len(out.Results) == 0 {
			t.Fatal("expected results")
		}
		if out.Results[0].Symbol != "SharedSymbol" {
			t.Errorf("expected exact match first, got %q", out.Results[0].Symbol)
		}
	})

	t.Run("max results caps and marks truncated", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "SharedSymbol",
			MaxResults: 1,
		})

		if len(out.Results) > 1 {
			t.Errorf("expected at most 1 result, got %d", len(out.Results))
		}
		if out.TotalMatches > 1 && !out.Truncated {
			t.Errorf("expected Truncated=true when TotalMatches (%d) > MaxResults (1)", out.TotalMatches)
		}
	})

	t.Run("next action points at explore of top result", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "SharedSymbol",
			MaxResults: 20,
		})

		if len(out.Results) == 0 {
			t.Fatal("expected results")
		}
		if len(out.NextActions) == 0 {
			t.Fatal("expected next actions")
		}
		if got, want := out.NextActions[0].Tool, "lang.go.explore"; got != want {
			t.Errorf("next action tool: got %q, want %q", got, want)
		}
		expIn, ok := out.NextActions[0].Input.(lang.ExploreInput)
		if !ok {
			t.Fatalf("expected next action input to be lang.ExploreInput, got %T", out.NextActions[0].Input)
		}
		if expIn.Package != out.Results[0].Package || len(expIn.Symbols) == 0 ||
			expIn.Symbols[0] != out.Results[0].Symbol {
			t.Errorf("next action input %+v should target top result %+v", expIn, out.Results[0])
		}
	})

	t.Run("auto explore inlines metadata when single result", func(t *testing.T) {
		// Module with exactly one symbol matching "UniqueAuto" so AutoExplore triggers.
		dir := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(dir, "go.mod"),
			[]byte("module testautoexplore\n\ngo 1.21\n"),
			0o644,
		); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(`package testautoexplore

// UniqueAutoExploreSymbol does the thing.
func UniqueAutoExploreSymbol() {}
`), 0o644); err != nil {
			t.Fatalf("write x.go: %v", err)
		}
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{
			Query:       "UniqueAutoExploreSymbol",
			AutoExplore: true,
			MaxResults:  20,
		})

		if len(out.Results) != 1 {
			t.Fatalf("expected exactly 1 result, got %d: %v", len(out.Results), searchResultNames(out.Results))
		}
		if out.Results[0].Metadata == nil {
			t.Error("expected Metadata populated when AutoExplore=true and exactly 1 result")
		}
	})

	t.Run("empty query errors", func(t *testing.T) {
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		raw, _ := json.Marshal(lang.SearchInput{Query: "  ", MaxResults: 5})
		_, err := golang.Search.Execute(t.Context(), raw)
		if err == nil {
			t.Fatal("expected error for whitespace-only query")
		}
	})

	// ---- workspace support ----

	t.Run("works across go.work modules", func(t *testing.T) {
		// Two modules under a go.work — search must find a symbol in either.
		root := t.TempDir()
		makeGoModuleAt(t, filepath.Join(root, "modA"), "example.com/a", `// AlphaSymbol is in module A.
func AlphaSymbol() {}`)
		makeGoModuleAt(t, filepath.Join(root, "modB"), "example.com/b", `// BetaSymbol is in module B.
func BetaSymbol() {}`)
		if err := os.WriteFile(
			filepath.Join(root, "go.work"),
			[]byte("go 1.21\n\nuse (\n\t./modA\n\t./modB\n)\n"),
			0o644,
		); err != nil {
			t.Fatalf("write go.work: %v", err)
		}
		cdToTempDir(t, root)

		gotA := executeSearch(t, lang.SearchInput{Query: "AlphaSymbol", MaxResults: 10})
		if !containsSymbol(gotA.Results, "AlphaSymbol") {
			t.Errorf("expected AlphaSymbol from module A; got %v", searchResultNames(gotA.Results))
		}

		gotB := executeSearch(t, lang.SearchInput{Query: "BetaSymbol", MaxResults: 10})
		if !containsSymbol(gotB.Results, "BetaSymbol") {
			t.Errorf("expected BetaSymbol from module B; got %v", searchResultNames(gotB.Results))
		}
	})

	// ---- merger behavior (gopls + doc-scorer) ----

	t.Run("both signals boost score", func(t *testing.T) {
		// A symbol whose name AND docblock both hit the query should score
		// higher than a symbol where only the docblock hits.
		dir := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(dir, "go.mod"),
			[]byte("module testmerge\n\ngo 1.21\n"),
			0o644,
		); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(`package testmerge

// CacheLookup looks up an entry in the cache.
func CacheLookup() {}

// FetchEntry retrieves an entry from the cache.
func FetchEntry() {}
`), 0o644); err != nil {
			t.Fatalf("write x.go: %v", err)
		}
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{Query: "cache", MaxResults: 10})

		// CacheLookup wins on name (gopls fuzzy + doc-scorer name match) AND
		// docblock. FetchEntry only matches via docblock. CacheLookup must rank first.
		if len(out.Results) == 0 {
			t.Fatal("expected at least one result")
		}
		if out.Results[0].Symbol != "CacheLookup" {
			t.Errorf("expected CacheLookup ranked first (name+doc match), got %q (full ranking: %v)",
				out.Results[0].Symbol, searchResultNames(out.Results))
		}
	})

	t.Run("merger dedupes same symbol", func(t *testing.T) {
		// One symbol should appear at most once in results, even when both
		// signals identify it.
		dir := makeSearchModule(t)
		cdToTempDir(t, dir)

		out := executeSearch(t, lang.SearchInput{Query: "SharedSymbol", MaxResults: 20})

		counts := make(map[string]int)
		for _, r := range out.Results {
			counts[r.Package+"|"+r.Symbol]++
		}
		for key, n := range counts {
			if n > 1 {
				t.Errorf("duplicate result for %s: count=%d", key, n)
			}
		}
	})

	// ---- complex project tests ----

	t.Run("complex finds generic type", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeSearchTyped(t, lang.SearchInput{Query: "Buffer", MaxResults: 20})

		if !containsSym(out.Results, "Buffer") {
			t.Errorf("expected to find generic type 'Buffer'; got %v", searchResultSymbols(out.Results))
		}
	})

	t.Run("complex finds method on generic receiver", func(t *testing.T) {
		requireGopls(t)
		dir := complexProject(t)
		t.Chdir(dir)

		out := executeSearchTyped(t, lang.SearchInput{Query: "Buffer.Push", MaxResults: 20, IncludePrivate: true})

		if !containsSym(out.Results, "Buffer.Push") {
			t.Errorf("expected receiver-qualified method 'Buffer.Push'; got %v", searchResultSymbols(out.Results))
		}
	})

	t.Run("complex auto explore inlines metadata", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		// AutoExplore on a unique-result query should inline full source.
		out := executeSearchTyped(t, lang.SearchInput{
			Query:       "BuildHandler", // non-existent → 0 results
			AutoExplore: true,
			MaxResults:  20,
		})
		if len(out.Results) != 0 {
			t.Errorf("expected 0 results for non-existent symbol; got %v", searchResultSymbols(out.Results))
		}

		// Now a query that should match exactly one symbol.
		out = executeSearchTyped(t, lang.SearchInput{
			Query:          "JoinNonEmpty",
			AutoExplore:    true,
			MaxResults:     20,
			IncludePrivate: true,
		})
		if len(out.Results) != 1 {
			t.Fatalf("expected exactly 1 result for unique symbol; got %v", searchResultSymbols(out.Results))
		}
		if out.Results[0].Metadata == nil {
			t.Errorf("expected Metadata populated when AutoExplore=true and exactly 1 result")
		}
	})

	t.Run("complex cgo file loads and is searchable", func(t *testing.T) {
		if os.Getenv("CGO_ENABLED") == "0" {
			t.Skip("cgo disabled")
		}
		dir := t.TempDir()
		mustWriteFileC(t, filepath.Join(dir, "go.mod"), "module ex/cgomod\n\ngo 1.21\n")
		mustWriteFileC(t, filepath.Join(dir, "cgo.go"), `package cgomod

// #include <stdint.h>
//
// static int64_t add(int64_t a, int64_t b) {
//     return a + b;
// }
import "C"

// AddInts wraps the C add function.
func AddInts(a, b int64) int64 {
	return int64(C.add(C.int64_t(a), C.int64_t(b)))
}
`)
		t.Chdir(dir)

		// Search must find the Go function defined alongside cgo without
		// erroring on the import "C" preamble.
		out := executeSearchTyped(t, lang.SearchInput{Query: "AddInts", MaxResults: 10})
		if !containsSym(out.Results, "AddInts") {
			t.Errorf("expected AddInts (in cgo file) to be searchable; got %v", searchResultSymbols(out.Results))
		}
	})

	t.Run("complex vendor not surfaced as workspace code", func(t *testing.T) {
		// A project with a vendor/ tree should not surface vendored symbols
		// as if they were module-local — refactor tools must not propose
		// rewriting vendored code, and search results should be limited to
		// workspace code by default.
		dir := t.TempDir()
		// go.mod must declare the require so vendor/modules.txt's "## explicit"
		// directive matches. Without that the toolchain errors with
		// "inconsistent vendoring".
		mustWriteFileC(t, filepath.Join(dir, "go.mod"),
			"module ex/vendored\n\ngo 1.21\n\nrequire example.com/thirdparty v0.0.0\n")
		mustWriteFileC(t, filepath.Join(dir, "main.go"), `package vendored

import "example.com/thirdparty"

func Caller() string { return thirdparty.Helper() }
`)
		// Hand-rolled vendor tree: a fake third-party module.
		mustWriteFileC(t, filepath.Join(dir, "vendor/example.com/thirdparty/lib.go"), `package thirdparty

func Helper() string { return "third" }
`)
		mustWriteFileC(t, filepath.Join(dir, "vendor/modules.txt"),
			"# example.com/thirdparty v0.0.0\n## explicit\nexample.com/thirdparty\n")
		t.Chdir(dir)

		out := executeSearchTyped(t, lang.SearchInput{Query: "Caller", IncludePrivate: true})
		if !containsSym(out.Results, "Caller") {
			t.Errorf("expected to find local Caller; got %v", searchResultSymbols(out.Results))
		}

		// Renaming the local Caller should not touch vendored files. The
		// vendor/example.com/thirdparty/lib.go file must remain unchanged
		// regardless of whether its package shows up in the loaded set.
		originalVendor := mustReadFile(t, filepath.Join(dir, "vendor/example.com/thirdparty/lib.go"))

		out2 := executeRefactorTyped(t, golang.Rename, lang.RenameInput{
			Symbol:  "Caller",
			NewName: "Driver",
		})
		if out2.Status != refactor.StatusSuccess {
			t.Fatalf("rename failed in vendored project: %+v", out2.Results)
		}
		for _, r := range out2.Results {
			if strings.Contains(r.FilePath, "vendor/") {
				t.Errorf("rename touched vendored file (must not happen): %s", r.FilePath)
			}
		}
		if got := mustReadFile(t, filepath.Join(dir, "vendor/example.com/thirdparty/lib.go")); got != originalVendor {
			t.Errorf("vendored file content changed unexpectedly:\n%s", got)
		}
	})

	t.Run("complex workspace mode finds across workspace modules", func(t *testing.T) {
		root := t.TempDir()

		// modA defines an interface.
		mustWriteFileC(t, filepath.Join(root, "modA/go.mod"), "module example.com/a\n\ngo 1.21\n")
		mustWriteFileC(t, filepath.Join(root, "modA/iface.go"), `package a

type Worker interface {
	Work() error
}
`)

		// modB depends on modA and provides an implementor.
		mustWriteFileC(t, filepath.Join(root, "modB/go.mod"),
			"module example.com/b\n\ngo 1.21\n\nrequire example.com/a v0.0.0\n\nreplace example.com/a => ../modA\n")
		mustWriteFileC(t, filepath.Join(root, "modB/impl.go"), `package b

import "example.com/a"

type LocalWorker struct{}

func (LocalWorker) Work() error { return nil }

var _ a.Worker = LocalWorker{}
`)
		mustWriteFileC(t, filepath.Join(root, "go.work"), "go 1.21\n\nuse (\n\t./modA\n\t./modB\n)\n")
		t.Chdir(root)

		// Implementations of Worker should find LocalWorker even though it's
		// in a different workspace module.
		out := executeDepsTyped(t, golang.Implementations, lang.ImplementationsInput{Symbol: "Worker"})

		if !contains(refResultSymbols(out.Results), "LocalWorker") {
			t.Errorf("workspace-mode implementations should find LocalWorker; got %v", refResultSymbols(out.Results))
		}
	})

	t.Run("complex finds init function", func(t *testing.T) {
		// init is a special name in Go — it's declared like a func but
		// can't be called directly. Search should still surface it so the
		// agent can locate package-load code without grepping.
		dir := t.TempDir()
		mustWriteFileC(t, filepath.Join(dir, "go.mod"), "module ex/initsearch\n\ngo 1.21\n")
		mustWriteFileC(t, filepath.Join(dir, "boot.go"), `package initsearch

func init() {
	// runs at package load
}
`)
		t.Chdir(dir)

		// init isn't exported, so IncludePrivate=true is required.
		out := executeSearchTyped(t, lang.SearchInput{Query: "init", IncludePrivate: true})
		if !containsSym(out.Results, "init") {
			t.Errorf("expected to find init function; got %v", searchResultSymbols(out.Results))
		}
	})

	t.Run("complex external test package searchable", func(t *testing.T) {
		requireGopls(t)
		// Symbols defined inside `package foo_test` should be findable too —
		// the test variant of the package is a real loaded package and its
		// symbols belong to the workspace.
		dir := t.TempDir()
		mustWriteFileC(t, filepath.Join(dir, "go.mod"), "module ex/extsrch\n\ngo 1.21\n")
		mustWriteFileC(t, filepath.Join(dir, "core.go"), "package extsrch\n\nfunc Public() {}\n")
		mustWriteFileC(t, filepath.Join(dir, "helpers_test.go"), `package extsrch_test

func TestHelperFunc() {} // declared in the external test package
`)
		t.Chdir(dir)

		out := executeSearchTyped(t, lang.SearchInput{Query: "TestHelperFunc", IncludePrivate: true})
		if !containsSym(out.Results, "TestHelperFunc") {
			t.Errorf("expected to find symbol declared in _test package; got %v", searchResultSymbols(out.Results))
		}
	})

	t.Run("complex build tags tagged file excluded by default", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFileC(t, filepath.Join(dir, "go.mod"), "module ex/tagged\n\ngo 1.21\n")
		// Always-included file.
		mustWriteFileC(t, filepath.Join(dir, "common.go"), `package tagged

func Always() string { return "yes" }
`)
		// Tag-gated file.
		mustWriteFileC(t, filepath.Join(dir, "tagged.go"), `//go:build mytag

package tagged

func OnlyWithTag() string { return "tagged" }
`)
		t.Chdir(dir)

		// Default search (no build flags) must NOT find tag-gated symbol.
		out := executeSearchTyped(t, lang.SearchInput{Query: "OnlyWithTag", IncludePrivate: true})
		if containsSym(out.Results, "OnlyWithTag") {
			t.Errorf("OnlyWithTag should be excluded without -tags=mytag; got %v", searchResultSymbols(out.Results))
		}

		// The default-included function must still be found.
		out = executeSearchTyped(t, lang.SearchInput{Query: "Always", IncludePrivate: true})
		if !containsSym(out.Results, "Always") {
			t.Errorf("expected to find Always; got %v", searchResultSymbols(out.Results))
		}
	})

	// ---- production / real-world tests ----

	// TestProduction_Search_FuzzyMatch verifies search keeps locating
	// symbols across realistic naming variations — proves the gopls
	// integration covers the cases agents hit constantly.
	t.Run("production fuzzy match", func(t *testing.T) {
		requireGopls(t)
		dir := writeMod(t, "prodsearch", map[string]string{
			"a.go": "package prodsearch\n\n" +
				"// CalculateOrderTotal sums line items and applies tax.\n" +
				"func CalculateOrderTotal(items []int) int { sum := 0; for _, x := range items { sum += x }; return sum }\n",
		})
		t.Chdir(dir)

		in := lang.SearchInput{Query: "calculateorder"}
		raw, _ := json.Marshal(in)
		result, err := golang.Search.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("search execute: %v", err)
		}
		out, ok := result.(lang.SearchOutput)
		if !ok {
			var probe lang.SearchOutput
			b, _ := json.Marshal(result)
			_ = json.Unmarshal(b, &probe)
			out = probe
		}
		found := false
		for _, r := range out.Results {
			if strings.Contains(r.Symbol, "CalculateOrderTotal") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("fuzzy 'calculateorder' should match CalculateOrderTotal; got %d results", len(out.Results))
		}
	})

	// passing a MaxResults higher than the actual symbol count must just
	// return everything available, not error or pad with nils.
	t.Run("real world high max results", func(t *testing.T) {
		requireGopls(t)
		dir := writeMod(t, "rwzhigh", map[string]string{
			"a.go": "package rwzhigh\n\nfunc Alpha() {}\nfunc Beta() {}\nfunc Gamma() {}\n",
		})
		t.Chdir(dir)

		in := lang.SearchInput{Query: "lpha", MaxResults: 1000}
		raw, err := jsonMarshalInput(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		result, err := golang.Search.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("search execute: %v", err)
		}
		out := decodeSearchOutput(t, result)
		if len(out.Results) == 0 {
			t.Errorf("expected at least Alpha; got 0 results")
		}
		for _, r := range out.Results {
			if r.Symbol == "" {
				t.Errorf("got an empty result; should not pad with empties")
			}
		}
	})

	// filtering search by kind (struct, interface, func, etc.) must match
	// accurately and skip other kinds.
	t.Run("real world kind filter", func(t *testing.T) {
		dir := writeMod(t, "rwxkind", map[string]string{
			"a.go": "package rwxkind\n\n" +
				"type Engine struct{}\n\n" +
				"func Engine_Helper() int { return 1 }\n\n" +
				"const EngineLimit = 10\n",
		})
		t.Chdir(dir)

		in := lang.SearchInput{Query: "engine", Kind: "struct"}
		raw, _ := json.Marshal(in)
		result, err := golang.Search.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("search execute: %v", err)
		}
		out := decodeSearchOutput(t, result)
		for _, r := range out.Results {
			if r.Kind != "struct" {
				t.Errorf("kind filter `struct` should not return %q (kind=%q)", r.Symbol, r.Kind)
			}
		}
		// And the Engine struct should be among results.
		hasEngine := false
		for _, r := range out.Results {
			if r.Symbol == "Engine" && r.Kind == "struct" {
				hasEngine = true
			}
		}
		if !hasEngine {
			t.Errorf("Engine struct should be in results; got %+v", out.Results)
		}
	})

	// ---- hardening tests ----

	// a query that matches no symbol must return an empty result set, not an error.
	t.Run("hardening finds nothing for garbage query", func(t *testing.T) {
		dir := writeMod(t, "hardsearchnoop", map[string]string{
			"a.go": "package hardsearchnoop\n\nfunc Hello() string { return \"\" }\n",
		})
		t.Chdir(dir)

		in := lang.SearchInput{Query: "qzqzqzqzqzqzqz_definitely_not_a_real_symbol"}
		raw, _ := json.Marshal(in)
		result, err := golang.Search.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("search execute: %v", err)
		}
		out := decodeSearchOutput(t, result)
		if len(out.Results) != 0 {
			t.Errorf("garbage query must return zero hits; got %d", len(out.Results))
		}
	})
}

// ---- helpers ----

// executeSearch invokes the Search tool and unmarshals the result.
func executeSearch(t *testing.T, input lang.SearchInput) lang.SearchOutput {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	result, err := golang.Search.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := result.(lang.SearchOutput)
	if !ok {
		b, _ := json.Marshal(result)
		if err2 := json.Unmarshal(b, &out); err2 != nil {
			t.Fatalf("unexpected result type %T: %v", result, err2)
		}
	}
	return out
}

// makeSearchModule creates a temp Go module with two packages each defining
// SharedSymbol, plus a Greeter struct/method, plus an unexported lowerHelper.
// Used by the core search behavior tests.
func makeSearchModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testsearch\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	alpha := filepath.Join(dir, "alpha")
	if err := os.MkdirAll(alpha, 0o755); err != nil {
		t.Fatalf("mkdir alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(alpha, "alpha.go"), []byte(`package alpha

// SharedSymbol is defined in the alpha package.
func SharedSymbol() string { return "alpha" }

// Greeter says hi.
type Greeter struct{ Name string }

// Hello returns a greeting.
func (g *Greeter) Hello() string { return "hello " + g.Name }

// lowerHelper is an unexported helper.
func lowerHelper() {}
`), 0o644); err != nil {
		t.Fatalf("write alpha.go: %v", err)
	}

	beta := filepath.Join(dir, "beta")
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatalf("mkdir beta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beta, "beta.go"), []byte(`package beta

// SharedSymbol is defined in the beta package.
func SharedSymbol() int { return 42 }
`), 0o644); err != nil {
		t.Fatalf("write beta.go: %v", err)
	}

	return dir
}

// makeGoModuleAt writes a minimal Go module at dir with the given module path
// and a single source file containing body (within `package <last segment>`).
func makeGoModuleAt(t *testing.T, dir, modulePath, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte("module "+modulePath+"\n\ngo 1.21\n"),
		0o644,
	); err != nil {
		t.Fatalf("write go.mod in %s: %v", dir, err)
	}
	parts := strings.Split(modulePath, "/")
	pkgName := parts[len(parts)-1]
	src := "package " + pkgName + "\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write main.go in %s: %v", dir, err)
	}
}

// cdToTempDir changes the working directory to dir for the duration of the test.
func cdToTempDir(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
}

// containsSymbol reports whether any result in results has the given symbol name.
func containsSymbol(results []lang.SearchResult, name string) bool {
	for _, r := range results {
		if r.Symbol == name {
			return true
		}
	}
	return false
}

// searchResultNames returns the symbol names from a slice of SearchResults.
func searchResultNames(results []lang.SearchResult) []string {
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Symbol
	}
	return names
}
