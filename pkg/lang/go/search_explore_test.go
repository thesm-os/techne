// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"encoding/json"
	"os"
	"path/filepath"
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
