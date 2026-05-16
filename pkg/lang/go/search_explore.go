// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"
	"fmt"
	"strings"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
)

// SearchExplore is the lang.go.search_explore tool. It chains a Go
// symbol search with an immediate explore of the top-ranked match in a
// single call, returning both the ranked result list and the
// decoded source for the chosen symbol.
//
// Prefer this over running lang.go.search followed by lang.go.explore
// whenever the agent's intent is "find this thing and show me what it
// looks like" — the typical follow-up to a search. The combined call
// saves one turn in roughly 80% of cases. Internally it issues search
// with AutoExplore=true so the N=1 hot path is satisfied inline; for
// N>1 it picks index 0 by default (or input.Pick) and calls the
// explore handler directly.
//
// The schema returns the full ranked search list alongside the explored
// symbol so the agent can pick a different match if the top hit was
// wrong without re-running search. IncludePrivate is carried through to
// the explore call — a private-symbol search would otherwise return
// empty metadata when the user-facing default is exported-only.
//
// External `*_test` / `*.test` packages are filtered from Results by
// default because the Go toolchain cannot load them via packages.Load
// — they're only addressable through `go test`. Picking one would crash
// the explore step with "no required module provides package …_test".
// Set IncludeTests=true to surface tests when hunting for a specific
// Test*/Benchmark*/Fuzz* by name; the handler still uses a non-test
// fallback if the picked test result fails to explore.
//
// Error handling preserves partial work: if the search succeeds but the
// explore step fails (e.g. package cannot be loaded), the search half
// of the output is still returned to the agent so it can fall back to
// picking a different rank without re-querying.
var SearchExplore = tool.New[lang.SearchExploreInput, lang.SearchExploreOutput](
	"lang.go.search_explore",
	"PREFER OVER calling lang.go.search then lang.go.explore in sequence. Searches for a Go symbol and explores the top match in one call — typically saves 1 turn vs. the two-step chain. Returns the full ranked search list so the agent can pick a different match if the top one wasn't what it wanted. External *_test packages are filtered by default (they can't be loaded by the Go toolchain); pass include_tests=true when hunting for a specific Test*/Benchmark*/Fuzz* by name.",
	searchExploreHandler,
	tool.WithShortDescription("Search for a Go symbol and return the top match's source in one call"),
)

// isExternalTestPackage reports whether pkgPath is an external test
// package the Go toolchain refuses to load via packages.Load / go list.
// These come in two flavors: `pkg_test` (an external test package
// declared with `package foo_test`) and `pkg.test` (the synthetic test
// binary). Both end the search→explore chain in failure if picked, so
// we treat them as a single filter category.
func isExternalTestPackage(pkgPath string) bool {
	return strings.HasSuffix(pkgPath, "_test") || strings.HasSuffix(pkgPath, ".test")
}

// searchExploreHandler implements the lang.go.search_explore RPC.
// Delegates to searchHandler with AutoExplore=true so the N=1 case is
// resolved inline without a second package load, then — for N>1 —
// selects index input.Pick (clamped to a valid range) and runs the
// explore handler on the chosen result.
//
// When AutoExplore already populated metadata for the picked result
// (the N=1 fast path), the handler returns immediately without an
// additional load. Otherwise it invokes exploreHandler with the
// result's Package + Symbol and the requested Mode (defaulting to
// lang.ModeSkeleton).
//
// External test packages are filtered from Results when
// input.IncludeTests is false (the default). When IncludeTests is true,
// tests stay in Results but the handler still falls back to the next
// non-test rank if the picked entry fails to explore — that's the
// safety net for "I asked for tests but the picked one wasn't loadable
// anyway", which is the actual user-visible failure mode B3 reported.
//
// A failed explore step still returns the search half of the output so
// the agent can recover by picking a different rank.
func searchExploreHandler(ctx context.Context, input lang.SearchExploreInput) (lang.SearchExploreOutput, error) {
	out := lang.SearchExploreOutput{}

	// 1. Run search. AutoExplore inlines metadata for N=1 — saves work.
	maxResults := input.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	searchOut, err := searchHandler(ctx, lang.SearchInput{
		Query:          input.Query,
		Kind:           input.Kind,
		IncludePrivate: input.IncludePrivate,
		MaxResults:     maxResults,
		AutoExplore:    true,
	})
	if err != nil {
		return out, fmt.Errorf("search: %w", err)
	}

	results := searchOut.Results
	if !input.IncludeTests {
		// Strip external test packages before the agent ever sees them
		// — picking one would crash the explore step below, and
		// production-only is the documented default.
		filtered := results[:0:0]
		for _, r := range results {
			if !isExternalTestPackage(r.Package) {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	out.Results = results
	out.TotalMatches = searchOut.TotalMatches
	out.Truncated = searchOut.Truncated

	if len(results) == 0 {
		return out, nil
	}

	// 2. The "top match." Default is index 0; the agent can request a
	// different rank with Pick=N.
	idx := input.Pick
	if idx < 0 || idx >= len(results) {
		idx = 0
	}
	picked := results[idx]
	out.Selected = &picked

	// 3. If AutoExplore already populated metadata for this result (N=1
	// case), we're done — no follow-up explore needed.
	if picked.Metadata != nil {
		out.Symbols = map[string]lang.SymbolMetadata{picked.Symbol: *picked.Metadata}
		return out, nil
	}

	// 4. Otherwise call explore on the picked result. Carry IncludePrivate
	// through — if the agent searched for a private symbol, the explore
	// must also include privates or it returns empty metadata.
	mode := input.Mode
	if mode == "" {
		mode = lang.ModeSkeleton
	}
	exploreOut, err := exploreHandler(ctx, lang.ExploreInput{
		Package:        picked.Package,
		Symbols:        []string{picked.Symbol},
		Mode:           mode,
		IncludePrivate: input.IncludePrivate,
	})
	if err != nil {
		// Safety net: when IncludeTests=true and the picked entry was a
		// test package the toolchain refused to load, try the next
		// non-test rank instead of failing the whole call. This is the
		// fallback the user explicitly suggested for B3 — preserves
		// progress when the agent asked for tests but the top hit
		// wasn't loadable.
		if isExternalTestPackage(picked.Package) {
			for i := idx + 1; i < len(results); i++ {
				cand := results[i]
				if isExternalTestPackage(cand.Package) {
					continue
				}
				fallbackOut, fbErr := exploreHandler(ctx, lang.ExploreInput{
					Package:        cand.Package,
					Symbols:        []string{cand.Symbol},
					Mode:           mode,
					IncludePrivate: input.IncludePrivate,
				})
				if fbErr == nil {
					out.Selected = &cand
					out.Symbols = fallbackOut.Symbols
					return out, nil
				}
			}
		}
		// Search succeeded; explore failed and no usable fallback. Return
		// the search half so the agent can fall back to picking a
		// different match.
		return out, fmt.Errorf("explore top match %q in %s: %w", picked.Symbol, picked.Package, err)
	}
	out.Symbols = exploreOut.Symbols
	return out, nil
}
