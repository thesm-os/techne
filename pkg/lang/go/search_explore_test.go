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
)

func TestSearchExplore(t *testing.T) {
	t.Run("top match is explored and symbols populated", func(t *testing.T) {
		dir := makeSearchExploreFixture(t)
		t.Chdir(dir)

		out := executeSearchExplore(t, lang.SearchExploreInput{Query: "Process"})

		if out.Selected == nil {
			t.Fatal("expected Selected to be populated")
		}
		if len(out.Symbols) == 0 {
			t.Fatal("expected Symbols populated for the top match")
		}
		sym, ok := out.Symbols[out.Selected.Symbol]
		if !ok {
			t.Errorf("expected Symbols[%q] populated; got keys %v", out.Selected.Symbol, mapKeys(out.Symbols))
		}
		if sym.Signature == "" {
			t.Errorf("expected signature populated in skeleton mode; got %+v", sym)
		}
	})

	t.Run("Pick selects alternate ranked result", func(t *testing.T) {
		dir := makeSearchExploreFixture(t)
		t.Chdir(dir)

		out := executeSearchExplore(t, lang.SearchExploreInput{Query: "Process", Pick: 1})

		if len(out.Results) < 2 {
			t.Skipf("only %d results; can't pick rank 1", len(out.Results))
		}
		if out.Selected == nil || out.Selected.Symbol != out.Results[1].Symbol {
			t.Errorf(
				"Pick=1 should select the second result; selected %+v vs results[1]=%+v",
				out.Selected,
				out.Results[1],
			)
		}
	})

	// Regression: search_explore was dropping IncludePrivate when calling
	// explore internally, so the inline skeleton came back empty.
	t.Run("IncludePrivate passes through to explore", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(dir, "go.mod"),
			[]byte("module ex/sepriv\n\ngo 1.21\n"),
			0o644,
		); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(`package sepriv

// privateHelper does internal work.
func privateHelper() string { return "h" }
`), 0o644); err != nil {
			t.Fatalf("write x.go: %v", err)
		}
		t.Chdir(dir)

		out := executeSearchExplore(t, lang.SearchExploreInput{
			Query:          "privateHelper",
			IncludePrivate: true,
		})

		if len(out.Results) == 0 {
			t.Fatal("expected to find privateHelper with IncludePrivate=true")
		}
		if out.Selected == nil {
			t.Fatal("expected Selected populated")
		}
		if len(out.Symbols) == 0 {
			t.Errorf(
				"regression: explore-of-top-match returned empty symbols. Was IncludePrivate dropped on the way through?",
			)
		}
		if _, ok := out.Symbols["privateHelper"]; !ok {
			t.Errorf("expected privateHelper in Symbols; got keys %v", mapKeys(out.Symbols))
		}
	})

	// Regression for B3: when the top-ranked search result lived in an
	// external `_test` package (PkgPath ending in "_test" or ".test"),
	// search_explore tried to call explore on it, which invokes
	// packages.Load / `go list` — and the toolchain refuses to load an
	// external test package by import path ("no required module provides
	// package …_test"). The whole call would error, even though there
	// were perfectly explorable non-test matches further down the list.
	//
	// Fix: filter test packages from Results before picking. Default
	// behaviour is "production only"; users who genuinely want test
	// matches opt in via IncludeTests=true (and the safety net still
	// catches a picked test that explore can't load — see the next
	// subtest).
	t.Run("test-package top match is filtered by default", func(t *testing.T) {
		dir := makeSearchExploreFixtureWithTest(t)
		t.Chdir(dir)

		// "Manifest" matches both the production `ManifestHash` and the
		// external `TestManifestHash` in the *_test package. The
		// pre-fix behaviour: search returns both, the test ranks first
		// (longer name match against the docblock scorer), search_explore
		// tries to explore it, explore fails with "package … is not in
		// std". The fix should filter the test result before picking
		// and return the production hit instead.
		out := executeSearchExplore(t, lang.SearchExploreInput{Query: "Manifest"})

		if out.Selected == nil {
			t.Fatal("expected a non-test fallback to be selected; got nil")
		}
		if strings.HasSuffix(out.Selected.Package, "_test") || strings.HasSuffix(out.Selected.Package, ".test") {
			t.Errorf("default should not pick a *_test package; got %q", out.Selected.Package)
		}
		// Results should also be filtered — agents shouldn't have to
		// re-rank around tests they can't explore.
		for _, r := range out.Results {
			if strings.HasSuffix(r.Package, "_test") || strings.HasSuffix(r.Package, ".test") {
				t.Errorf("default Results should exclude *_test packages; got %q", r.Package)
			}
		}
		if len(out.Symbols) == 0 {
			t.Errorf("expected explored symbols for the production fallback; got empty")
		}
	})

	// Opting back into tests via IncludeTests=true must restore the
	// permissive behaviour — useful when the agent is specifically
	// hunting for a test function by name.
	t.Run("IncludeTests=true restores test packages in Results", func(t *testing.T) {
		dir := makeSearchExploreFixtureWithTest(t)
		t.Chdir(dir)

		out := executeSearchExplore(t, lang.SearchExploreInput{
			Query:        "Manifest",
			IncludeTests: true,
		})

		// At least one *_test result must appear in the ranked list now.
		hasTest := false
		for _, r := range out.Results {
			if strings.HasSuffix(r.Package, "_test") || strings.HasSuffix(r.Package, ".test") {
				hasTest = true
				break
			}
		}
		if !hasTest {
			t.Errorf("IncludeTests=true should surface *_test results; got Results=%+v", out.Results)
		}
	})

	t.Run("empty results handled cleanly when query matches nothing", func(t *testing.T) {
		dir := makeSearchExploreFixture(t)
		t.Chdir(dir)

		out := executeSearchExplore(t, lang.SearchExploreInput{Query: "DefinitelyDoesNotExist_xyz_123"})

		if len(out.Results) != 0 {
			t.Errorf("expected 0 results for nonsense query; got %d", len(out.Results))
		}
		if out.Selected != nil {
			t.Errorf("expected no Selected when no results; got %+v", out.Selected)
		}
	})
}

// ---- helpers ----

func executeSearchExplore(t *testing.T, in lang.SearchExploreInput) lang.SearchExploreOutput {
	t.Helper()
	raw, _ := json.Marshal(in)
	result, err := golang.SearchExplore.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("search_explore execute: %v", err)
	}
	if v, ok := result.(lang.SearchExploreOutput); ok {
		return v
	}
	var out lang.SearchExploreOutput
	b, _ := json.Marshal(result)
	_ = json.Unmarshal(b, &out)
	return out
}

// makeSearchExploreFixtureWithTest creates a module whose search hits
// for the query "Manifest" come from BOTH a production package and an
// external `_test` package. The external test package is the exact
// shape that used to crash search_explore (PkgPath ends in `_test`,
// which the Go toolchain refuses to load via `go list`/`packages.Load`).
//
// Both names share the "Manifest" prefix so search reliably surfaces
// both, regardless of which scorer ranks higher; the assertion is on
// filter behaviour, not on relative ranking.
func makeSearchExploreFixtureWithTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte("module ex/seonlytest\n\ngo 1.21\n"),
		0o644,
	); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	// Production code: a name match for "Manifest".
	if err := os.WriteFile(filepath.Join(dir, "core.go"), []byte(`package seonlytest

// ManifestHash returns the production manifest hash.
func ManifestHash(input string) string { return input }
`), 0o644); err != nil {
		t.Fatalf("write core.go: %v", err)
	}
	// External test package: matches "Manifest" by name too. `package
	// seonlytest_test` makes this an external test package whose
	// PkgPath ends in `_test` — the toolchain refuses to load it by
	// import path because external test packages are only addressable
	// through `go test`.
	if err := os.WriteFile(filepath.Join(dir, "core_test.go"), []byte(`package seonlytest_test

import "testing"

// TestManifestHash verifies the manifest hash scenario.
func TestManifestHash(t *testing.T) { _ = t }
`), 0o644); err != nil {
		t.Fatalf("write core_test.go: %v", err)
	}
	return dir
}

// makeSearchExploreFixture creates a module with multiple symbols whose
// names start with "Process" so search has multiple ranked results.
func makeSearchExploreFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte("module ex/searchexp\n\ngo 1.21\n"),
		0o644,
	); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core.go"), []byte(`package searchexp

// Process is the primary entrypoint.
func Process(input string) (string, error) { return input, nil }

// ProcessBatch handles multiple inputs.
func ProcessBatch(inputs []string) []string { return inputs }

// Processor wraps process state.
type Processor struct{}

func (p *Processor) Run() {}
`), 0o644); err != nil {
		t.Fatalf("write core.go: %v", err)
	}
	return dir
}

func mapKeys(m map[string]lang.SymbolMetadata) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
