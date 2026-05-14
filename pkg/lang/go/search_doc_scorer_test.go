// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"os"
	"path/filepath"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
)

func TestSearchDocScorer(t *testing.T) {
	t.Run("fair scheduling query finds applyQuantum or Scheduler", func(t *testing.T) {
		dir := makeSemanticModule(t)
		t.Chdir(dir)

		out := executeSearch(t, lang.SearchInput{
			Query:          "fair scheduling",
			MaxResults:     10,
			IncludePrivate: true,
		})

		found := make(map[string]bool)
		for _, r := range out.Results {
			found[r.Symbol] = true
		}
		if !found["applyQuantum"] && !found["Scheduler"] {
			t.Errorf(
				"expected applyQuantum or Scheduler in results for 'fair scheduling', got: %v",
				scoreResultNames(out.Results),
			)
		}
	})

	t.Run("backpressure query finds HandleBackpressure", func(t *testing.T) {
		dir := makeSemanticModule(t)
		t.Chdir(dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "backpressure",
			MaxResults: 10,
		})

		if len(out.Results) == 0 {
			t.Fatal("expected at least one result for 'backpressure', got none")
		}
		if !scoreContainsSymbol(out.Results, "HandleBackpressure") {
			t.Errorf("expected HandleBackpressure in results, got: %v", scoreResultNames(out.Results))
		}
	})

	t.Run("nonsense query produces no doc-scorer matches", func(t *testing.T) {
		dir := makeSemanticModule(t)
		t.Chdir(dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "nonexistent concept xyzzy",
			MaxResults: 10,
		})

		for _, r := range out.Results {
			if r.MatchedOn == "symbol_name" || r.MatchedOn == "docblock" {
				t.Errorf("nonsense query produced a doc-scorer match: symbol=%q matched_on=%q", r.Symbol, r.MatchedOn)
			}
		}
	})

	t.Run("results include NextActions pointing to explore", func(t *testing.T) {
		dir := makeSemanticModule(t)
		t.Chdir(dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "backpressure",
			MaxResults: 10,
		})

		if len(out.Results) > 0 && len(out.NextActions) == 0 {
			t.Error("expected NextActions when results are found")
		}
		if len(out.NextActions) > 0 && out.NextActions[0].Tool != "lang.go.explore" {
			t.Errorf("expected next action tool lang.go.explore, got %q", out.NextActions[0].Tool)
		}
	})

	t.Run("MaxResults cap is respected", func(t *testing.T) {
		dir := makeSemanticModule(t)
		t.Chdir(dir)

		out := executeSearch(t, lang.SearchInput{
			Query:      "fair scheduling",
			MaxResults: 1,
		})

		if len(out.Results) > 1 {
			t.Errorf("expected at most 1 result, got %d", len(out.Results))
		}
	})

	t.Run("camelCase splitting finds symbol by word fragment", func(t *testing.T) {
		dir := makeSemanticModule(t)
		t.Chdir(dir)

		// "quantum" is part of applyQuantum — querying just "quantum" should
		// find it via either signal (gopls fuzzy or doc-scorer camelCase split).
		out := executeSearch(t, lang.SearchInput{
			Query:          "quantum",
			MaxResults:     10,
			IncludePrivate: true,
		})

		if !scoreContainsSymbol(out.Results, "applyQuantum") {
			t.Errorf("expected applyQuantum in results for query 'quantum', got: %v", scoreResultNames(out.Results))
		}
	})

	t.Run("results are sorted by score descending", func(t *testing.T) {
		dir := makeSemanticModule(t)
		t.Chdir(dir)

		out := executeSearch(t, lang.SearchInput{
			Query:          "fair scheduling",
			MaxResults:     10,
			IncludePrivate: true,
		})

		for i := 1; i < len(out.Results); i++ {
			if out.Results[i].Score > out.Results[i-1].Score {
				t.Errorf("results not sorted by score descending: position %d (score %f) > position %d (score %f)",
					i, out.Results[i].Score, i-1, out.Results[i-1].Score)
			}
		}
	})
}

// ---- helpers ----

// makeSemanticModule creates a temp Go module with symbols whose docblocks
// contain words useful for testing the doc-content scorer half of the merger.
func makeSemanticModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	if err := os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte("module testsemantics\n\ngo 1.21\n"),
		0o644,
	); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "engine.go"), []byte(`package testsemantics

// applyQuantum distributes work fairly across goroutines.
func applyQuantum() {}
`), 0o644); err != nil {
		t.Fatalf("write engine.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.go"), []byte(`package testsemantics

// HandleBackpressure slows down producers when the queue is full.
func HandleBackpressure() {}
`), 0o644); err != nil {
		t.Fatalf("write store.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(`package testsemantics

// Scheduler manages fair task distribution.
type Scheduler struct{}
`), 0o644); err != nil {
		t.Fatalf("write types.go: %v", err)
	}
	return dir
}

func scoreContainsSymbol(results []lang.SearchResult, name string) bool {
	for _, r := range results {
		if r.Symbol == name {
			return true
		}
	}
	return false
}

func scoreResultNames(results []lang.SearchResult) []string {
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Symbol
	}
	return names
}
