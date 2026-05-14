// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package lang

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BuildReproCommand reconstructs the exact shell command that was used to
// invoke an external tool from its program name and argument vector. The
// result is plain space-joined text suitable for placing in the
// ReproCommand field of an [Issue] or [SymbolMetadata] so an agent can
// copy-paste it into a shell to reproduce the diagnostic.
//
// The output is intentionally simple: arguments are joined with spaces
// and are NOT quoted, escaped, or shell-validated. Callers that pass
// arguments containing whitespace, quotes, or shell metacharacters will
// produce strings that no longer round-trip through /bin/sh. In practice
// the verify-suite runners pass tool flags only ("-run", "^TestFoo")
// which do not need escaping. If that invariant ever changes the
// function should be upgraded to shellquote rather than fixed at call
// sites.
//
// Allocates a single slice of cap len(args)+1 and one final string; safe
// for concurrent use.
func BuildReproCommand(executable string, args []string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, executable)
	parts = append(parts, args...)
	return strings.Join(parts, " ")
}

// ExtractBuildError returns the last 15 lines of a captured command's
// combined stdout/stderr. The Go build toolchain, `go test`, and most
// linters emit their actionable diagnostics at the very end of their
// output — leading lines are typically package-discovery noise — so
// trimming to the tail gives the agent the highest-signal slice of the
// run without forcing a fetch of the full log.
//
// The input is trimmed of trailing whitespace before splitting so that a
// stray final newline does not eat a useful line. When the output has
// fewer than 15 lines the entire output is returned. Lines are joined
// back with `\n`; the result has no trailing newline.
//
// The 15-line window is empirical: Go build errors run 1–4 lines per
// failing package, and 15 comfortably covers two or three concurrent
// failures while keeping the agent's context lean. Callers that need
// more should retrieve the full log via [BloatPrevention] instead.
func ExtractBuildError(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	start := max(len(lines)-15, 0)
	return strings.Join(lines[start:], "\n")
}

// ExtractLinesFromBytes returns a contiguous slice of source lines
// starting at lineNum (1-based, inclusive) and continuing for up to
// extraLines additional lines below — a total of extraLines+1 lines when
// the file is long enough.
//
// Designed for snippet extraction in lint and test reports: given the
// bytes of a source file and a defect's line number, it produces the
// minimal context a reader needs to interpret the diagnostic without
// opening the file. The returned string has no trailing newline so it
// slots cleanly into JSON payloads.
//
// Returns the empty string when src is empty, lineNum is non-positive,
// or lineNum exceeds the line count of src — i.e. callers do not need to
// bounds-check before calling. The function splits on `\n` only and does
// not normalise `\r\n`; on CRLF files the returned snippet will retain
// the carriage returns, which downstream renderers should strip if
// needed.
//
// Not suitable for very large files: the entire src is split into a
// string slice each call. Acceptable for source files (typically <50KB)
// but not for log buffers.
func ExtractLinesFromBytes(src []byte, lineNum, extraLines int) string {
	if len(src) == 0 || lineNum <= 0 {
		return ""
	}
	lines := strings.Split(string(src), "\n")
	if lineNum > len(lines) {
		return ""
	}
	end := min(lineNum+extraLines, len(lines))
	return strings.Join(lines[lineNum-1:end], "\n")
}

// LastNLines returns the last n lines of s, joined with `\n`. A trailing
// newline on the input is stripped before counting so that a file with a
// final blank line still yields n non-empty lines.
//
// When s contains fewer than n lines the entire string is returned
// (minus the trailing newline). When n is zero or negative the result is
// the empty string for negative n and a single empty line for n=0 — the
// caller is responsible for ensuring n is positive.
//
// Like [ExtractBuildError], the function exists to surface the
// highest-signal portion of long tool output. Unlike ExtractBuildError
// it takes the cutoff as a parameter and accepts a string rather than
// byte slice.
func LastNLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// StartupErrorReport builds a synthetic [SuiteReport] for a verification
// suite that failed to start at all — for example, the underlying
// toolchain binary is missing from $PATH, a flag was rejected before any
// tests ran, or the suite's working directory could not be created.
// These failures are distinct from `exec.ExitError` (the tool ran and
// reported failures), which the suite runners surface as ordinary
// Issues.
//
// The returned report carries Status=[StatusError] and a single Issue
// with Severity=[SeverityError], so the engine rolls the overall verify
// result up to [StatusDegraded] without misreporting which suite did
// what. ReproCommand is included so the agent can attempt the failing
// command manually to confirm the environment problem.
//
// suiteName should be one of the [SuiteLint], [SuiteTest], [SuiteBench],
// or [SuiteFuzz] constants — it lands in the Issue's Linter field where
// the assembler later uses it for grouping.
func StartupErrorReport(suiteName, msg, reproCmd string) SuiteReport {
	return SuiteReport{
		Status:  StatusError,
		Summary: msg,
		Issues: []Issue{{
			Severity:     SeverityError,
			Linter:       suiteName,
			Message:      msg,
			ReproCommand: reproCmd,
		}},
	}
}

// IsFilesystemPath reports whether s looks like a literal filesystem
// path rather than a Go (or Rust) package selector.
//
// Returns true when s begins with `/`, `./`, or `../` AND does not
// contain `...`. The `...` exclusion is the load-bearing check: in Go
// `./pkg/...` is a package pattern (wildcard subtree) even though it
// starts with `./`. Treating it as a filesystem path would cause the
// verifier to look for a literal directory named with an ellipsis and
// fail with a confusing error.
//
// Used by the verify-input normaliser to decide whether to translate a
// user-supplied target through workspace-relative path resolution or
// pass it straight to `go test` / cargo as a package spec. Callers must
// not pass identifiers like `pkg/foo` (no leading dot) — those route
// through the package-spec branch unchanged.
func IsFilesystemPath(s string) bool {
	if strings.Contains(s, "...") {
		return false
	}
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../")
}

// MaxNextActions is the hard cap on the number of [NextAction]
// suggestions any tool response may carry. The limit exists to keep
// follow-up suggestions actionable — three or four hints can become
// decision-paralysing noise — and to bound the token cost the suggestions
// add to a tool reply.
//
// The value is consumed by [CapNextActions]. Tools that emit fewer than
// MaxNextActions suggestions are returned unchanged.
const MaxNextActions = 2

// CapNextActions trims a slice of [NextAction] suggestions to at most
// [MaxNextActions] entries, after re-ordering by confidence so the most
// useful suggestions land at the top.
//
// The sort direction depends on the overall verification status:
//
//   - statusIsPass=true (everything passed): rank by ascending confidence
//     rank — the deterministic and high-confidence "clean up next" hints
//     come first because the agent has no failures to investigate and
//     benefits from polish suggestions.
//   - statusIsPass=false (something failed): rank by descending confidence
//     rank — the lower-confidence hints come first because they typically
//     point to deeper investigation ("explore this caller") while the
//     high-confidence hints are usually "apply this patch", which can
//     wait until the agent understands the failure.
//
// Sorting is stable so ties preserve the caller's authoring order. When
// the input has [MaxNextActions] or fewer entries the slice is returned
// unchanged with no allocation. When the slice is larger it is sorted
// in-place and a sub-slice is returned that shares the underlying array.
func CapNextActions(actions []NextAction, statusIsPass bool) []NextAction {
	if len(actions) <= MaxNextActions {
		return actions
	}
	rank := func(c string) int {
		switch c {
		case ConfidenceDeterministic:
			return 0
		case ConfidenceHigh:
			return 1
		case ConfidenceMedium:
			return 2
		case ConfidenceLow:
			return 3
		default:
			return 4
		}
	}
	if statusIsPass {
		sort.SliceStable(actions, func(i, j int) bool {
			return rank(actions[i].Confidence) < rank(actions[j].Confidence)
		})
	} else {
		sort.SliceStable(actions, func(i, j int) bool {
			return rank(actions[i].Confidence) > rank(actions[j].Confidence)
		})
	}
	return actions[:MaxNextActions]
}

// WorkspaceVersion returns a coarse fingerprint of the workspace's Go
// source state so callers can detect when prior tool results may be
// stale. The format is `mtime:<unix-seconds>`, where the timestamp is the
// most recent modification time across all .go files reachable from the
// current working directory.
//
// The fingerprint is embedded in [ExploreOutput] and [SearchOutput]
// responses so an agent can compare values across turns: if the
// fingerprint changed between a search call and a follow-up explore
// call, the workspace was modified mid-turn and the search results may
// refer to source that has since shifted line numbers or vanished.
//
// The walk skips hidden directories (any directory whose basename starts
// with `.`) and the `vendor` directory; unreadable directories are
// silently skipped rather than aborting the walk. The mtime resolution
// is whole seconds, so two edits inside the same second collapse to a
// single fingerprint — acceptable for staleness detection but not
// suitable as a content hash.
//
// Returns `mtime:0` when no .go file is found. The walk is O(workspace
// size); call cost is non-trivial on very large monorepos but is
// adequate for end-of-tool fingerprint emission. Safe for concurrent use
// — the walk reads but never writes filesystem state.
func WorkspaceVersion() string {
	var maxMtime time.Time
	_ = filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		// Skip hidden dirs, vendor, and non-Go files.
		if info.IsDir() {
			base := filepath.Base(path)
			if base != "." && strings.HasPrefix(base, ".") || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			if info.ModTime().After(maxMtime) {
				maxMtime = info.ModTime()
			}
		}
		return nil
	})
	if maxMtime.IsZero() {
		return "mtime:0"
	}
	return fmt.Sprintf("mtime:%d", maxMtime.Unix())
}

// PendingSymbol pairs a symbol's map-key name with the [SymbolMetadata]
// gathered for it during an [ExploreOutput] assembly. Used internally by
// the explore collector to thread per-symbol state through the kind
// filter and ordering passes before the map is finalised on the output
// struct.
//
// Not part of the public LLM-facing API surface — it never appears in
// JSON responses — but exported so language-specific assemblers in
// sibling packages can share the assembly helpers without pulling them
// back through an internal/ package.
type PendingSymbol struct {
	// Key is the identifier under which this symbol will be stored in the
	// ExploreOutput.Symbols map. Matches the symbol's exported name for
	// top-level declarations and the `Receiver.Method` form for methods.
	Key string
	// Meta is the metadata gathered for this symbol so far: kind, signature,
	// docblock, and (depending on the explore mode) the symbol's full source.
	// Further passes may attach fields such as ReproCommand or trim
	// Implementation under the token budget before the entry is committed.
	Meta SymbolMetadata
}

// ApplyTokenBudget enforces a soft cap on an [ExploreOutput]'s response
// size by progressively dropping SymbolMetadata.Implementation bodies
// until the estimated payload fits within maxTokens. Signatures,
// docblocks, file lists, and import lists are preserved at all costs
// because they are far more valuable per byte than implementation source
// for an agent doing API discovery.
//
// The estimator uses a fixed 4-characters-per-token ratio. That ratio is
// intentionally conservative: real tokenisation varies from ~3 to ~6
// chars/token depending on the symbol's name density, so the function
// occasionally under-fills the budget but rarely over-fills it. Callers
// that need an exact budget should run the output through a real
// tokeniser after this trim.
//
// Budget enforcement proceeds in two phases:
//
//  1. If the base payload (package metadata + every symbol's signature +
//     docblock + receiver) already exceeds the budget, every
//     Implementation field is cleared. A single [NextAction] suggesting
//     a follow-up call in [ModeSkeleton] is appended so the agent has
//     an obvious recovery path. The output's Truncated flag is set.
//  2. Otherwise the function walks symbols in declaration order
//     (ExploreOutput.SymbolOrder) and keeps each Implementation while
//     it fits; once the remaining budget is exhausted, all subsequent
//     Implementations are dropped. A [NextAction] suggesting a targeted
//     re-fetch of just the truncated symbols in [ModeCode] is appended.
//
// Declaration-order walk is load-bearing: developers typically declare
// the most-important type at the top of a file, so the agent retains
// the symbols it is most likely to need.
//
// The function mutates out in place (Symbols entries are reassigned and
// NextActions is extended). toolName is the dotted tool name to embed in
// the follow-up NextActions — pass "lang.go.explore" for Go,
// "lang.py.explore" for Python, and so on, so the suggestion is callable
// as-is.
func ApplyTokenBudget(out *ExploreOutput, maxTokens int, toolName string) {
	budget := maxTokens * 4 // ~4 chars per token

	// Measure current size of non-implementation fields.
	baseSize := len(out.Package) + len(out.PackageDoc)
	for _, f := range out.Files {
		baseSize += len(f)
	}
	for _, imp := range out.Imports {
		baseSize += len(imp)
	}
	for _, name := range out.SymbolOrder {
		sym := out.Symbols[name]
		baseSize += len(sym.Kind) + len(sym.Location) + len(sym.Docblock) +
			len(sym.Signature) + len(sym.Receiver)
	}

	if baseSize >= budget {
		// Can't fit even the basics; clear all implementations.
		for _, name := range out.SymbolOrder {
			sym := out.Symbols[name]
			sym.Implementation = ""
			out.Symbols[name] = sym
		}
		out.Truncated = true
		out.NextActions = append(out.NextActions, NextAction{
			Tool:       toolName,
			Reason:     "Output was truncated. Explore individual symbols with skeleton mode for details.",
			Confidence: ConfidenceHigh,
			Input: ExploreInput{
				Package: out.Package,
				Mode:    ModeSkeleton,
			},
		})
		return
	}

	remaining := budget - baseSize
	truncated := false

	for _, name := range out.SymbolOrder {
		sym := out.Symbols[name]
		implLen := len(sym.Implementation)
		if implLen == 0 {
			continue
		}
		if remaining >= implLen {
			remaining -= implLen
		} else {
			sym.Implementation = ""
			truncated = true
			out.Symbols[name] = sym
		}
	}

	if truncated {
		out.Truncated = true
		// Collect the names of truncated symbols for a targeted follow-up call.
		var truncatedSymbols []string
		for _, name := range out.SymbolOrder {
			sym := out.Symbols[name]
			if sym.Implementation == "" {
				truncatedSymbols = append(truncatedSymbols, name)
			}
		}
		out.NextActions = append(out.NextActions, NextAction{
			Tool:       toolName,
			Reason:     "Output was truncated due to MaxOutputTokens. Use code mode on specific symbols for full source.",
			Confidence: ConfidenceHigh,
			Input: ExploreInput{
				Package: out.Package,
				Symbols: truncatedSymbols,
				Mode:    ModeCode,
			},
		})
	}
}
