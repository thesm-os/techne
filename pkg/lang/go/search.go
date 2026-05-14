// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"
	"fmt"
	"go/ast"
	"os"
	"sort"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/internal/workspace"
)

// Search is the lang.go.search tool. It locates Go symbols across the
// workspace by name (fuzzy-matched against the symbol's identifier and
// receiver type) and by intent (word-overlap against the symbol's
// docblock and the package's name context).
//
// Prefer this over Grep for any Go-symbol lookup. Grep matches
// byte-level text — it returns false positives from same-named
// identifiers in different scopes, misses references hidden behind
// method calls, cannot tell whether a hit is the declaration or a use,
// and has no concept of the workspace (go.work). Search is type-aware:
// results are real declarations the type checker resolved, with kind,
// receiver, package, and absolute file:line baked in.
//
// The search merges two backends in parallel:
//   - gopls workspace_symbol with the fuzzy matcher, when the gopls
//     binary is on PATH. This handles tolerant name matching
//     ("HTTPRq" → HTTPRequest) using the same ranker that powers IDE
//     "Go to Symbol" navigation.
//   - An in-process doc-content scorer that weights query words against
//     symbol-name, docblock, and package-name tokens. Always runs as a
//     fallback when gopls is absent, and contributes additional
//     doc-driven hits when both backends run.
//
// Results are deduplicated by (Package, Symbol), with the higher of the
// two per-signal scores winning. When a symbol appears in BOTH signals,
// a fixed bonus is added — a name-and-intent match is stronger evidence
// than either alone. Output is sorted by combined Score descending;
// Score is in [0.0, 1.0] and rounded to three decimals for stable JSON.
// The top metadataTier results retain their Signature; lower hits drop
// the signature to keep doc listings dense.
//
// When AutoExplore=true and exactly one result is returned, the handler
// also runs explore on it and inlines the full SymbolMetadata, saving
// a follow-up call.
var Search = tool.New[lang.SearchInput, lang.SearchOutput](
	"lang.go.search",
	"PREFER OVER Grep for any Go symbol lookup. Returns type-checked symbols (no false positives from same-named identifiers in different scopes), workspace-wide (handles go.work, which Grep cannot), and matches doc-comment intent — so it works even when you don't know the exact symbol name yet. Merges fuzzy name matching (gopls) with a doc-content scorer in one ranked list. Examples: 'foldPatch', 'HTTPRq', 'how backpressure is handled'. AGENT HINT: Use auto_explore=true to receive full source inline when exactly one match is returned.",
	searchHandler,
	tool.Enum(
		"kind",
		lang.KindFunc,
		lang.KindMethod,
		lang.KindStruct,
		lang.KindInterface,
		lang.KindType,
		lang.KindConst,
		lang.KindVar,
		lang.KindAll,
	),
	tool.WithShortDescription("Find Go symbols by fuzzy name or doc-intent across the workspace"),
)

const (
	// metadataTier is the number of top-ranked results that retain their
	// full Signature in the output. Lower-ranked results drop the signature
	// so the output stays dense; agents browsing a long match list usually
	// only need detail on the top few hits.
	metadataTier = 3

	// goplsBothSignalsBonus is added to a symbol's combined score when it
	// appears in BOTH gopls and the doc-scorer. Boosts genuine name+intent
	// matches over name-only or intent-only matches. Tuned to be small
	// relative to a max single-signal score so a strong single-signal
	// result can still outrank a weak both-signals one.
	goplsBothSignalsBonus = 0.1
)

// searchLoadMode is the mode passed to packages.Load for the doc
// scorer. NeedSyntax exposes the AST for declaration walking;
// NeedCompiledGoFiles pairs each AST file with its on-disk path so
// results can render correct Location strings; NeedName and NeedImports
// supply the package-name word context used by the scorer.
const searchLoadMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
	packages.NeedSyntax | packages.NeedImports

// searchHandler implements the lang.go.search RPC. Discovers the
// workspace from the current directory, loads every package once with
// Tests=true so test symbols are searchable, then dispatches gopls and
// the doc scorer in parallel goroutines and merges the results.
//
// gopls failures are non-fatal: a missing gopls binary or a non-zero
// exit fall through to a doc-scorer-only ranking. This keeps the tool
// useful in CI containers and on machines where gopls has not been
// bootstrapped, at the cost of fuzzy-name tolerance.
//
// Returns an error only on truly fatal conditions: empty query, getwd
// failure, workspace discovery failure, or package-load failure. All
// other degradations (no gopls, no doc hits, etc.) yield an empty
// Results slice with TotalMatches=0.
func searchHandler(ctx context.Context, input lang.SearchInput) (lang.SearchOutput, error) {
	out := lang.SearchOutput{}
	if strings.TrimSpace(input.Query) == "" {
		return out, fmt.Errorf("query must not be empty")
	}

	maxResults := input.MaxResults
	if maxResults <= 0 {
		maxResults = 20
	}

	cwd, err := os.Getwd()
	if err != nil {
		return out, fmt.Errorf("getwd: %w", err)
	}

	ws, err := workspace.Discover(cwd)
	if err != nil {
		return out, fmt.Errorf("discover workspace: %w", err)
	}

	pkgs, err := ws.Load(ctx, searchLoadMode, nil, workspace.WithTests())
	if err != nil {
		return out, fmt.Errorf("load packages: %w", err)
	}

	fileToPkg := buildFileToPkgMap(pkgs)

	// Run gopls and doc-scorer in parallel — they're independent and the
	// gopls subprocess startup is the dominant latency source.
	var (
		wg         sync.WaitGroup
		goplsHits  []goplsHit
		docResults []lang.SearchResult
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Errors are non-fatal — fall through to doc-scorer-only.
		hits, _ := queryGopls(ctx, ws.Root(), input.Query)
		goplsHits = hits
	}()
	go func() {
		defer wg.Done()
		docResults = runDocScorer(pkgs, input.Query, maxResults*4) // overshoot; merger trims
	}()
	wg.Wait()

	merged := mergeSignals(goplsHits, fileToPkg, docResults)
	merged = filterMerged(merged, input.Kind, input.IncludePrivate)
	sortMerged(merged)

	out.TotalMatches = len(merged)
	out.WorkspaceVersion = lang.WorkspaceVersion()
	if out.TotalMatches > maxResults {
		out.Truncated = true
		merged = merged[:maxResults]
	}

	for i, m := range merged {
		r := m.result
		if i >= metadataTier {
			r.Signature = ""
		}
		out.Results = append(out.Results, r)
	}

	if len(out.Results) > 0 {
		top := out.Results[0]
		out.NextActions = []lang.NextAction{{
			Tool:       "lang.go.explore",
			Reason:     fmt.Sprintf("Explore top search result %q in package %s", top.Symbol, top.Package),
			Confidence: lang.ConfidenceHigh,
			Input:      lang.ExploreInput{Package: top.Package, Symbols: []string{top.Symbol}, Mode: lang.ModeSkeleton},
		}}
	}

	if input.AutoExplore && len(out.Results) == 1 {
		top := out.Results[0]
		exploreOut, exploreErr := exploreHandler(ctx, lang.ExploreInput{
			Package: top.Package, Symbols: []string{top.Symbol}, Mode: lang.ModeCode,
		})
		if exploreErr == nil {
			if meta, ok := exploreOut.Symbols[top.Symbol]; ok {
				out.Results[0].Metadata = &meta
			}
		}
	}

	return out, nil
}

// mergedResult carries provenance information so the ranker can compute
// a combined score and the bonus for hits found by both signals. Each
// field records where the hit came from and the raw per-signal score so
// the folding logic in combinedScore stays auditable.
//
// goplsRank is the 0-based position in gopls's own ranked output —
// smaller is better. docScore is the doc-scorer's normalized [0,1]
// score for this symbol.
type mergedResult struct {
	result    lang.SearchResult
	fromGopls bool
	goplsRank int // 0-based; smaller is better
	fromDoc   bool
	docScore  float64
}

// mergeSignals combines gopls hits and doc-scorer results into one list
// keyed by (Package, Symbol). When the same symbol appears in both, the
// merged record records both provenance flags so the score combiner can
// apply the both-signals bonus and prefer the higher of the two raw
// scores.
//
// gopls hits are processed first so the input rank ordering is
// preserved as ties; doc-scorer-only hits are appended after. Hits whose
// File is outside the loaded workspace are dropped (cannot render a
// Package), as are hits whose gopls kind we do not surface (fields,
// type parameters, etc.).
func mergeSignals(goplsHits []goplsHit, fileToPkg map[string]string, doc []lang.SearchResult) []mergedResult {
	// Index doc-scorer results by (Package, Symbol) for O(1) overlap lookup.
	docIdx := make(map[string]lang.SearchResult, len(doc))
	for _, r := range doc {
		docIdx[mergeKey(r.Package, r.Symbol)] = r
	}

	var merged []mergedResult
	seen := make(map[string]struct{}, len(goplsHits)+len(doc))

	// gopls hits first — preserves rank ordering for the score combiner.
	for rank, h := range goplsHits {
		kind := normalizeGoplsKind(h.Kind)
		if kind == "" {
			continue // skip Field/TypeParameter/etc. — not surfaced
		}
		pkg := fileToPkg[h.File]
		if pkg == "" {
			continue // file outside loaded workspace; can't render a Package
		}
		key := mergeKey(pkg, h.SymbolName)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		r := lang.SearchResult{
			Symbol:   h.SymbolName,
			Kind:     kind,
			Package:  pkg,
			Location: fmt.Sprintf("%s:%d", h.File, h.Line),
		}
		m := mergedResult{result: r, fromGopls: true, goplsRank: rank}
		if dr, ok := docIdx[key]; ok {
			m.fromDoc = true
			m.docScore = dr.Score
			m.result.Docblock = dr.Docblock
			m.result.MatchedOn = dr.MatchedOn
		}
		merged = append(merged, m)
	}

	// Doc-scorer-only hits.
	for _, r := range doc {
		key := mergeKey(r.Package, r.Symbol)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, mergedResult{
			result: r, fromDoc: true, docScore: r.Score,
		})
	}
	return merged
}

// mergeKey builds the (package, symbol) deduplication key. The pipe
// separator is chosen because it cannot appear in a Go package path or
// an identifier, eliminating ambiguity without a regex check.
func mergeKey(pkg, symbol string) string { return pkg + "|" + symbol }

// filterMerged applies the kind and visibility filters from the input
// to an already-merged result set. Uses the in-place slice trick (out
// := merged[:0]) so no allocation is required for the common case
// where most results pass.
//
// The "all" kind and the empty string both disable the kind filter so
// agents need not normalize their input.
func filterMerged(merged []mergedResult, kind string, includePrivate bool) []mergedResult {
	wantAll := kind == "" || kind == lang.KindAll
	out := merged[:0]
	for _, m := range merged {
		if !wantAll && m.result.Kind != kind {
			continue
		}
		if !includePrivate && !isSymbolExported(m.result.Symbol) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// isSymbolExported reports whether a symbol name (possibly
// receiver-qualified like "Greeter.Hello") is exported. The trailing
// component after the last dot determines the answer, matching how
// go/ast.IsExported treats identifiers. The empty string is treated as
// not exported — we never want to surface a malformed name.
func isSymbolExported(name string) bool {
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return false
	}
	return ast.IsExported(name)
}

// sortMerged orders results by combined score descending, with stable
// tie-breaks on (Package, Symbol) so two runs with identical inputs
// produce identical output (important for snapshot tests and for the
// NextActions field that picks index 0 as the suggested explore
// target).
//
// Scores are computed once into a parallel slice to avoid recomputing
// the score on every comparison — sort.SliceStable can invoke Less
// many times per pair. After sorting, the Result.Score is written
// through roundScore so the JSON output is stable across runs.
func sortMerged(merged []mergedResult) {
	scores := make([]float64, len(merged))
	for i := range merged {
		scores[i] = combinedScore(merged[i])
	}
	indices := make([]int, len(merged))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(a, b int) bool {
		if scores[indices[a]] != scores[indices[b]] {
			return scores[indices[a]] > scores[indices[b]]
		}
		ar, br := merged[indices[a]].result, merged[indices[b]].result
		if ar.Package != br.Package {
			return ar.Package < br.Package
		}
		return ar.Symbol < br.Symbol
	})
	tmp := make([]mergedResult, len(merged))
	for newIdx, oldIdx := range indices {
		tmp[newIdx] = merged[oldIdx]
		tmp[newIdx].result.Score = roundScore(scores[oldIdx])
	}
	copy(merged, tmp)
}

// combinedScore folds the gopls rank and doc-scorer score into a single
// [0,1] number.
//
//   - gopls contribution: rank-decaying as 1/(1+rank) — the top hit
//     (rank 0) gets 1.0, rank 1 gets 0.5, rank 2 gets 0.333… This
//     rewards being in the top of gopls's fuzzy ranking and decays
//     gracefully past the top few.
//   - doc-scorer contribution: its own already-normalized score.
//   - Both-signals: the larger of the two contributions plus a fixed
//     bonus (goplsBothSignalsBonus), capped at 1.0.
//
// Clamping at 1.0 matters for the schema's documented score range —
// agents may inspect Score numerically and assume it stays in bounds.
func combinedScore(m mergedResult) float64 {
	var s float64
	if m.fromGopls {
		s = 1.0 / (1.0 + float64(m.goplsRank))
	}
	if m.fromDoc && m.docScore > s {
		s = m.docScore
	}
	if m.fromGopls && m.fromDoc {
		s += goplsBothSignalsBonus
	}
	if s > 1.0 {
		s = 1.0
	}
	return s
}

// roundScore rounds a float to three decimal places so JSON output is
// stable across runs and unaffected by float-drift artifacts that
// creep in when the same inputs produce slightly different bit
// patterns under different goroutine schedules. Manual rounding avoids
// importing math.Round here.
func roundScore(s float64) float64 {
	return float64(int(s*1000+0.5)) / 1000
}

// buildFileToPkgMap maps each Go source file to its containing
// package's import path. When packages.Load runs with Tests=true the
// same file may appear under both the regular package and its test
// variant; the two-pass walk lets the clean (non-test-variant) path
// win so result Locations always carry the real package identifier.
//
// The map is consulted by the gopls-hit merger to translate a hit's
// file path into a Package field — hits whose file is not in the
// workspace are dropped, because we cannot render a package for them.
func buildFileToPkgMap(pkgs []*packages.Package) map[string]string {
	m := make(map[string]string, len(pkgs)*4)
	addFiles := func(p *packages.Package, pkgPath string) {
		for _, f := range p.CompiledGoFiles {
			if _, ok := m[f]; !ok {
				m[f] = pkgPath
			}
		}
		for _, f := range p.GoFiles {
			if _, ok := m[f]; !ok {
				m[f] = pkgPath
			}
		}
	}
	// First pass: clean (non-test-variant) packages take precedence.
	for _, p := range pkgs {
		if isTestVariant(p) {
			continue
		}
		addFiles(p, p.PkgPath)
	}
	// Second pass: fill in any files that only appear in test variants.
	for _, p := range pkgs {
		if !isTestVariant(p) {
			continue
		}
		addFiles(p, cleanPkgPath(p.PkgPath))
	}
	return m
}

// isTestVariant reports whether a package is the synthetic test
// variant produced when packages.Load runs with Tests=true. Two shapes
// are possible: "foo/bar [foo/bar.test]" (the suffixed PkgPath) and
// "foo/bar.test" (the .test ID). Both forms are checked because
// different loader paths produce different representations.
func isTestVariant(p *packages.Package) bool {
	return strings.Contains(p.PkgPath, " [") || strings.HasSuffix(p.ID, ".test")
}

// cleanPkgPath strips the " [foo/bar.test]" suffix that packages.Load
// attaches to test-variant packages, returning the underlying import
// path. Used to attribute test-variant files to the same Package as
// their production siblings so a search hit in a *_test.go file is
// still reported under the canonical import path.
func cleanPkgPath(p string) string {
	if before, _, ok := strings.Cut(p, " ["); ok {
		return before
	}
	return p
}
