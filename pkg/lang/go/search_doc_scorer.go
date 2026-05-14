// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"fmt"
	"go/ast"
	"go/token"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/tools/go/packages"

	"go.thesmos.sh/techne/pkg/lang"
)

// stopWords is the set of high-frequency function words stripped from
// query tokenization so they do not artificially inflate the doc-scorer
// match count. Tuned for natural-language queries like "how is X
// handled?" — the agent should not have its intent diluted by matches
// on "the" or "of". The list is deliberately short; aggressive stop-word
// filtering would also strip meaningful terms like "new" or "key".
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "for": true,
	"how": true, "what": true, "in": true, "of": true, "to": true, "and": true,
}

// runDocScorer scores every exported and unexported declaration in the
// loaded packages against the query by word-overlap on symbol names,
// docblocks, and package-name context. Sorted by score descending with
// a stable tie-break on symbol name, capped to maxResults.
//
// Methods are returned as "Receiver.Name" (the same form gopls emits)
// so the downstream merger in mergeSignals can dedup hits that appear
// in both backends. Returns nil for an empty query (after stop-word
// stripping) so the caller falls through to gopls-only results.
//
// Is not thread-safe with respect to pkgs slice mutation — callers must
// not concurrently modify the passed-in packages. It is safe to invoke
// from a goroutine alongside queryGopls because they share no state.
func runDocScorer(pkgs []*packages.Package, query string, maxResults int) []lang.SearchResult {
	queryWords := tokenizeQuery(query)
	if len(queryWords) == 0 {
		return nil
	}

	var results []lang.SearchResult
	for _, p := range pkgs {
		if !shouldScorePackage(p) {
			continue
		}
		results = append(results, scorePackage(p, queryWords)...)
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Symbol < results[j].Symbol
	})

	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results
}

// shouldScorePackage filters out packages the doc-scorer cannot or
// should not process: those without a FileSet (loaded with
// metadata-only modes that strip syntax info) and synthetic test-variant
// packages ("foo/bar [foo/bar.test]") whose declarations are duplicated
// from their regular counterparts.
func shouldScorePackage(p *packages.Package) bool {
	return p.Fset != nil && !strings.Contains(p.PkgPath, " [")
}

// scorePackage scores every top-level declaration in a single package
// against queryWords and returns the subset whose score is greater than
// zero. The package-name word list is computed once and reused across
// all declarations to amortize the camel-case splitting cost.
func scorePackage(p *packages.Package, queryWords []string) []lang.SearchResult {
	pkgWords := packageNameWords(p)

	var out []lang.SearchResult
	for i, file := range p.Syntax {
		filePath := ""
		if i < len(p.CompiledGoFiles) {
			filePath = p.CompiledGoFiles[i]
		}
		for _, decl := range file.Decls {
			out = append(out, scoreFileDecl(decl, filePath, p.PkgPath, pkgWords, queryWords, p.Fset)...)
		}
	}
	return out
}

// packageNameWords extracts the lower-cased word tokens that contribute
// to the package-context portion of the doc scorer's weighting. Falls
// back to the last segment of the import path when Package.Name is
// empty (which happens for some metadata-only loads).
func packageNameWords(p *packages.Package) []string {
	name := p.Name
	if name == "" {
		parts := strings.Split(p.PkgPath, "/")
		name = parts[len(parts)-1]
	}
	return splitCamelCase(name)
}

// scoreFileDecl dispatches one top-level declaration to the matching
// scorer (function or general declaration). A single GenDecl can
// produce multiple results because it may contain several TypeSpecs, so
// the return is a slice rather than a single value.
func scoreFileDecl(
	decl ast.Decl,
	filePath, pkgPath string,
	pkgWords, queryWords []string,
	fset *token.FileSet,
) []lang.SearchResult {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return scoreFuncDecl(d, filePath, pkgPath, pkgWords, queryWords, fset)
	case *ast.GenDecl:
		return scoreGenDecl(d, filePath, pkgPath, pkgWords, queryWords, fset)
	}
	return nil
}

// scoreFuncDecl scores a single function or method declaration. Returns
// nil when the score is zero so the caller does not accumulate empty
// results. The receiver-qualified naming applied by funcDeclName makes
// the returned Symbol match the form emitted by gopls workspace_symbol
// for the same declaration.
func scoreFuncDecl(
	d *ast.FuncDecl,
	filePath, pkgPath string,
	pkgWords, queryWords []string,
	fset *token.FileSet,
) []lang.SearchResult {
	name, kind := funcDeclName(d)
	r := scoreDeclAsSearchResult(name, kind, funcDeclDoc(d), filePath, pkgPath, pkgWords, queryWords, fset, d.Pos())
	if r.Score == 0 {
		return nil
	}
	return []lang.SearchResult{r}
}

// scoreGenDecl scores the TypeSpecs inside a general declaration
// (type, const, or var block). Only TypeSpec entries are scored — const
// and var declarations are intentionally excluded from the doc scorer
// because they rarely carry meaningful identifier-word content ("x =
// 1") and would dilute the result quality.
func scoreGenDecl(
	d *ast.GenDecl,
	filePath, pkgPath string,
	pkgWords, queryWords []string,
	fset *token.FileSet,
) []lang.SearchResult {
	var out []lang.SearchResult
	for _, spec := range d.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		r := scoreDeclAsSearchResult(
			ts.Name.Name,
			typeSpecKind(ts),
			typeSpecDoc(ts, d),
			filePath,
			pkgPath,
			pkgWords,
			queryWords,
			fset,
			ts.Pos(),
		)
		if r.Score > 0 {
			out = append(out, r)
		}
	}
	return out
}

// typeSpecDoc returns the trimmed doc comment for a TypeSpec, falling
// back to the enclosing GenDecl's doc when the spec has none of its
// own. Mirrors how godoc resolves the docblock for grouped type
// declarations — the doc on the outer block applies to every spec
// inside it unless overridden.
func typeSpecDoc(ts *ast.TypeSpec, d *ast.GenDecl) string {
	if ts.Doc != nil {
		return strings.TrimSpace(ts.Doc.Text())
	}
	if d.Doc != nil {
		return strings.TrimSpace(d.Doc.Text())
	}
	return ""
}

// scoreDeclAsSearchResult computes the doc-scorer's per-declaration
// score and returns a populated lang.SearchResult. Returns the zero
// value (Score=0) when no query word matches any signal so the caller
// can drop the result with a single check.
//
// Scoring weights:
//   - name match: +2.0 per query word (highest signal: a user querying
//     "foo" almost always wants a symbol literally named "Foo").
//   - docblock match: +1.0 per query word.
//   - package-context match: +0.5 per query word (weakest — nearly
//     every symbol in a "queue" package matches "queue").
//
// The raw score is normalized by dividing by the theoretical maximum
// (len(queryWords) * 3.5) so two-word queries do not dominate one-word
// queries in a mixed result set, and clamped to 1.0 for robustness
// against future weight tweaks. The Location field matches gopls's
// "absolute-path:line" format so agents can pipe it directly into
// Read or Edit.
func scoreDeclAsSearchResult(
	name, kind, docText, filePath, pkgPath string,
	pkgWords, queryWords []string,
	fset *token.FileSet,
	pos token.Pos,
) lang.SearchResult {
	nameWords := splitCamelCase(name)
	docWords := tokenizeWords(docText)

	nameWordSet := wordSet(nameWords)
	docWordSet := wordSet(docWords)
	pkgWordSet := wordSet(pkgWords)

	var rawScore float64
	var matchedOnParts []string

	for _, qw := range queryWords {
		nameHits := 0
		if nameWordSet[qw] {
			nameHits++
			rawScore += 2.0 // 2x weight for symbol name match
		}
		if docWordSet[qw] {
			rawScore += 1.0
		}
		if pkgWordSet[qw] {
			rawScore += 0.5
		}
		if nameHits > 0 && !slices.Contains(matchedOnParts, "symbol_name") {
			matchedOnParts = append(matchedOnParts, "symbol_name")
		}
		if docWordSet[qw] && !slices.Contains(matchedOnParts, "docblock") {
			matchedOnParts = append(matchedOnParts, "docblock")
		}
	}

	if rawScore == 0 {
		return lang.SearchResult{}
	}

	// Normalize: maximum possible raw score per query word is 2 (name) + 1 (doc) + 0.5 (pkg) = 3.5
	maxPossible := float64(len(queryWords)) * 3.5
	score := rawScore / maxPossible
	score = math.Min(score, 1.0)

	location := ""
	if fset != nil && pos.IsValid() {
		position := fset.Position(pos)
		loc := position.Filename
		if filePath != "" {
			loc = filePath
		}
		// Match the gopls-fed branch's format: absolute path:line.
		// Consistency lets agents pipe Location verbatim into Read/Edit.
		location = fmt.Sprintf("%s:%d", loc, position.Line)
	}

	matchedOn := strings.Join(matchedOnParts, ",")
	if matchedOn == "" {
		matchedOn = "package_context"
	}

	return lang.SearchResult{
		Symbol:    name,
		Kind:      kind,
		Package:   pkgPath,
		Location:  location,
		Docblock:  capDocblock(docText),
		Score:     math.Round(score*1000) / 1000,
		MatchedOn: matchedOn,
	}
}

// funcDeclName returns ("Receiver.Method", lang.KindMethod) for
// methods or (Name, lang.KindFunc) for plain functions. The
// receiver-qualified form matches gopls's symbol output so merging in
// mergeSignals dedupes hits correctly. Falls back to the bare method
// name when the receiver type cannot be extracted (unrecognized
// shape).
func funcDeclName(d *ast.FuncDecl) (name, kind string) {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name, lang.KindFunc
	}
	recvType := receiverTypeName(d.Recv.List[0].Type)
	if recvType == "" {
		return d.Name.Name, lang.KindMethod
	}
	return recvType + "." + d.Name.Name, lang.KindMethod
}

// receiverTypeName extracts the bare type name from a method receiver
// type expression, peeling off pointer wrappers and generic
// instantiations layer by layer. Handles four canonical receiver
// shapes: plain identifier T, pointer *T, single-type-parameter
// generic T of one parameter, and multi-type-parameter generic T of
// two or more parameters. Returns the empty string for shapes outside
// this set, in which case the caller falls back to the unqualified
// method name.
func receiverTypeName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.Ident:
			return e.Name
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		default:
			return ""
		}
	}
}

// typeSpecKind classifies a TypeSpec into one of the lang.Kind*
// constants: KindStruct for struct types, KindInterface for interface
// types, and KindType for everything else (named aliases, generic
// definitions, etc.).
func typeSpecKind(ts *ast.TypeSpec) string {
	switch ts.Type.(type) {
	case *ast.StructType:
		return lang.KindStruct
	case *ast.InterfaceType:
		return lang.KindInterface
	default:
		return lang.KindType
	}
}

// capDocblock truncates a docblock to ~200 runes and at most 3 lines so
// generated-code docblocks (gRPC stubs, FSM definitions, oapi-generated
// client types) do not dominate the token budget when many results are
// returned at once.
//
// The cap is rune-safe — trimming on a byte boundary inside a multi-byte
// UTF-8 sequence would produce invalid output and crash JSON encoders.
// When truncation happens, the cut is moved back to the nearest word
// boundary in the second half of the result so it does not split a
// word mid-character.
func capDocblock(doc string) string {
	const maxRunes = 200
	const maxLines = 3

	// Line cap first.
	lines := strings.SplitN(doc, "\n", maxLines+1)
	if len(lines) > maxLines {
		doc = strings.Join(lines[:maxLines], "\n") + "..."
	}

	// Rune cap.
	runes := []rune(doc)
	if len(runes) <= maxRunes {
		return doc
	}
	// Break at a word boundary in the second half.
	truncated := string(runes[:maxRunes])
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > maxRunes/2 {
		truncated = truncated[:lastSpace]
	}
	return truncated + "..."
}

// funcDeclDoc returns the trimmed doc comment for a FuncDecl, or the
// empty string when no comment is present. Trimming removes the
// trailing newline that ast.CommentGroup.Text always appends, keeping
// the text suitable for direct embedding in JSON.
func funcDeclDoc(d *ast.FuncDecl) string {
	if d.Doc == nil {
		return ""
	}
	return strings.TrimSpace(d.Doc.Text())
}

// tokenizeQuery lowercases the input, splits on whitespace, strips
// common punctuation from each token, and removes stop words. Empty
// result means "all input was stop words" — the caller treats that as
// no query and returns no results rather than matching everything.
func tokenizeQuery(query string) []string {
	raw := strings.Fields(strings.ToLower(query))
	var out []string
	for _, w := range raw {
		w = strings.Trim(w, ".,;:!?\"'")
		if w == "" || stopWords[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// tokenizeWords lowercases the input and splits it into letter+digit
// run words, dropping all other characters. Used on docblock text
// where we want to break on punctuation, parentheses, slashes, and
// anything else that separates natural-language words but is not
// itself a word.
func tokenizeWords(text string) []string {
	text = strings.ToLower(text)
	var words []string
	current := strings.Builder{}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
		} else if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// splitCamelCase splits a camelCase or snake_case identifier into
// lowercase words, handling acronyms like "HTTPRequest" yielding
// "http", "request" correctly.
//
// Examples:
//
//	applyExecutionQuantum -> [apply execution quantum]
//	NewLedgerFromPatches  -> [new ledger from patches]
//	max_allocs_per_op     -> [max allocs per op]
//	HTTPRequest           -> [http request]
//
// Used both on symbol names (to score against the query) and on
// package names (to build package-context word set).
func splitCamelCase(s string) []string {
	// First split on underscores.
	parts := strings.Split(s, "_")
	var words []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		words = append(words, splitCamelPart(part)...)
	}
	return words
}

// splitCamelPart splits a single underscore-free chunk of a camelCase
// identifier into lowercase parts. Implements the acronym-handling
// rules used by splitCamelCase: a run of uppercase letters is treated
// as a single acronym unless the next letter is lowercase, in which
// case the trailing uppercase letter starts a new word.
//
// Returns nil for an empty input so it composes cleanly under
// splitCamelCase's slice append.
func splitCamelPart(s string) []string {
	if s == "" {
		return nil
	}
	var words []string
	current := strings.Builder{}
	runes := []rune(s)

	for i, r := range runes {
		if i == 0 {
			current.WriteRune(unicode.ToLower(r))
			continue
		}
		if unicode.IsUpper(r) {
			// Check for acronym: sequence of uppercase letters followed by lowercase.
			// e.g. "HTTPRequest" → "http", "request"
			prevIsUpper := unicode.IsUpper(runes[i-1])
			nextIsLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if !prevIsUpper || (prevIsUpper && nextIsLower && current.Len() > 1) {
				words = append(words, current.String())
				current.Reset()
			}
			current.WriteRune(unicode.ToLower(r))
		} else {
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// wordSet converts a slice of words into a string-keyed set so the
// scorer can test membership in O(1) instead of paying O(n) per query
// word. Pre-sized to len(words) to avoid map growth during fill.
func wordSet(words []string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
