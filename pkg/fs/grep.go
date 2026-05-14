// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.thesmos.sh/techne/internal/tool"
)

// GrepInput is the wire-format request for fs.grep. Path and Pattern
// are required; every other field is a modifier on the regex flavour
// or the search scope. The handler auto-detects whether Path is a
// file or directory and dispatches accordingly — there is no separate
// "recursive" flag.
//
// The regex flavour is Go's RE2 (package regexp), not PCRE: features
// like look-around assertions and backreferences are not supported.
// This is a deliberate trade-off for linear-time guarantees on
// adversarial inputs.
type GrepInput struct {
	// Path is the file or directory to search. The handler stats Path and
	// dispatches to a single-file scanner or a directory walker
	// accordingly, so callers do not need to know the target's type ahead
	// of time. A non-existent Path is a hard error.
	Path string `json:"path" jsonschema:"File or directory path to search. Auto-detects file vs directory. Example: '/var/home/user/project' or '/var/home/user/project/main.go'."`
	// Pattern is the regular expression to match against each line, using
	// Go's RE2 syntax (see the regexp/syntax package). For literal
	// substring searches, prefer escaping with regexp.QuoteMeta or use
	// fs.replace with a literal pattern. Invalid expressions return a
	// compile error immediately rather than during the walk.
	Pattern string `json:"pattern" jsonschema:"Regular expression pattern to search for. Example: 'func.*Handler' or 'TODO:'."`
	// CaseInsensitive, when true, wraps the pattern in "(?i)" so letter
	// casing is ignored during matching. The flag composes with the
	// other flavour modifiers (WordMatch, Multiline) by stacking their
	// respective inline flags onto the pattern before compilation.
	CaseInsensitive bool `json:"case_insensitive,omitempty" jsonschema:"Perform a case-insensitive match. Optional, defaults to false."`
	// ContextLines is the number of lines of surrounding context to
	// attach to each match in the Before and After fields of GrepMatch.
	// Zero (the default) returns matches alone. Context windows on
	// adjacent matches overlap if both fall within ContextLines of each
	// other; the handler does not coalesce overlapping windows.
	ContextLines int `json:"context_lines,omitempty" jsonschema:"Number of lines to show before and after each matching line. Optional, defaults to 0."`
	// LineNumbers is retained for schema compatibility but is not
	// consulted by the current handler — GrepMatch.LineNumber is
	// always populated. The flag's presence in the input schema is
	// harmless.
	LineNumbers bool `json:"line_numbers,omitempty" jsonschema:"Prefix each matching line with its line number. Optional, defaults to false."`
	// MaxMatches caps the number of matches returned per file. Zero (the
	// default) means unlimited. When MaxResults is also set, the
	// effective per-file cap is min(MaxMatches, remaining MaxResults
	// budget). Useful to prevent a single noisy file from drowning out
	// results from elsewhere in the tree.
	MaxMatches int `json:"max_matches,omitempty" jsonschema:"Stop after returning this many matches per file. Optional, unlimited by default."`
	// Glob filters which files are searched when Path is a directory.
	// Uses Go's filepath.Match (no '**') against each entry's base name,
	// so a pattern like "*.go" works regardless of nesting depth. Empty
	// (the default) means search every readable file. The flag is
	// ignored when Path is a single file.
	Glob string `json:"glob,omitempty" jsonschema:"Glob pattern to filter which files are searched when path is a directory. Example: '*.go'. Optional."`
	// Hidden, when true, descends into hidden directories and matches
	// against hidden files during a directory walk. When false (the
	// default) entries whose base name starts with a dot are skipped and
	// hidden subtrees are pruned via filepath.SkipDir. The root Path is
	// exempt from this filter so a hidden directory can still be the
	// explicit target of a search.
	Hidden bool `json:"hidden,omitempty" jsonschema:"Include hidden files and directories when searching a directory. Optional, defaults to false."`
	// WordMatch, when true, wraps the pattern in word boundaries
	// ("\b...\b") so it only matches whole tokens. Useful for symbol-name
	// searches where a substring match would catch unrelated
	// identifiers. Composes with CaseInsensitive and Multiline.
	WordMatch bool `json:"word_match,omitempty" jsonschema:"Only match whole words. Optional, defaults to false."`
	// MaxResults caps the total number of matches across all files when
	// searching a directory. Zero (the default) means unlimited. Once the
	// cap is reached the walk terminates via filepath.SkipAll. Combine
	// with MaxMatches to balance breadth vs depth: MaxResults bounds the
	// entire response, MaxMatches bounds any single file's contribution.
	MaxResults int `json:"max_results,omitempty" jsonschema:"Cap total matches across all files when searching a directory. Optional, unlimited by default."`
	// Multiline, when true, prepends "(?s)" so '.' matches '\n', letting
	// patterns span line boundaries. Note: the handler still scans the
	// file line-by-line with bufio.Scanner, so a true cross-line match
	// requires the agent to express the boundaries explicitly or to pull
	// the file contents and search them another way. The flag is included
	// for pattern-flavour completeness, not as a guarantee of cross-line
	// matching.
	Multiline bool `json:"multiline,omitempty" jsonschema:"Enable multiline matching (dot matches newline). Optional, defaults to false."`
}

// GrepMatch is one row in a fs.grep response, representing a single
// matching line plus optional surrounding context. The File field is
// populated only when the search ran against a directory — single-
// file searches omit it because the caller already knows the path.
type GrepMatch struct {
	// File is the path of the file the match came from. It is set when
	// grep ran against a directory; for single-file searches it is left
	// empty because the caller already knows the path.
	File string `json:"file,omitempty" jsonschema:"File path containing the match (set when searching a directory)."`
	// LineNumber is the 1-based line number of the match within the
	// file. Always populated regardless of GrepInput.LineNumbers.
	LineNumber int `json:"line_number" jsonschema:"Line number of the match (1-based)."`
	// Line is the full text of the matching line, without its trailing
	// newline. Leading and trailing whitespace are preserved.
	Line string `json:"line" jsonschema:"Content of the matching line."`
	// Before is the slice of lines immediately preceding the match, in
	// order, of length min(ContextLines, LineNumber-1). Empty when
	// ContextLines is zero or the match is on the first line.
	Before []string `json:"before,omitempty" jsonschema:"Lines before the match (context)."`
	// After is the slice of lines immediately following the match, in
	// order, of length min(ContextLines, totalLines-LineNumber). Empty
	// when ContextLines is zero or the match is on the last line.
	After []string `json:"after,omitempty" jsonschema:"Lines after the match (context)."`
}

// GrepOutput is the wire-format response for fs.grep. Matches is in
// discovery order: scan order within a single file, and walk order
// (depth-first, alphabetical within each directory) when searching a
// directory. The handler never returns a nil Matches slice — an empty
// result is rendered as "[]".
type GrepOutput struct {
	// Matches is the ordered list of matches, capped by MaxMatches per
	// file and by MaxResults across the whole search. Order is scan
	// order; the handler does not re-sort.
	Matches []GrepMatch `json:"matches" jsonschema:"List of matches found."`
	// Count is len(Matches), surfaced for quick emptiness checks.
	Count int `json:"count" jsonschema:"Number of matches found."`
}

// Grep is the fs.grep tool entry point. It searches a single file or a
// directory tree for lines matching a Go RE2 regular expression and
// returns each match with optional surrounding context.
//
// Use Grep when the question is genuinely textual: a literal token,
// a comment marker ("TODO:"), a config value, a string literal that
// the Go type system cannot see. For Go symbol discovery (functions,
// types, methods, references) prefer lang.go.search and friends —
// those tools are AST-aware, ignore strings and comments, and return
// structured metadata an order of magnitude cheaper to reason over.
//
// The handler auto-detects whether Path is a file or directory and
// dispatches without a separate recursive flag. Flavour modifiers
// (CaseInsensitive, WordMatch, Multiline) compose by stacking inline
// flags onto the supplied pattern before compilation, so a single
// regexp.Compile call sees the final form. MaxMatches caps per file,
// MaxResults caps the whole walk; both terminate early via
// filepath.SkipAll, so cost scales with the cap rather than the tree
// size once the cap is hit.
//
// The scanner uses bufio.Scanner with its default 64 KiB token size,
// so pathologically long lines surface a scanning error. Unreadable
// files encountered during a walk are silently skipped — do not rely
// on Grep to surface permission errors as failures.
var Grep = tool.New[GrepInput, GrepOutput](
	"fs.grep",
	"Searches for regex patterns in files or directory trees. Auto-detects file vs directory. For Go symbol discovery, prefer lang.go.search which returns structured metadata.",
	grepHandler,
	tool.WithShortDescription("Search files or directory trees for a regex with structured match output"),
)

// grepHandler implements fs.grep. It validates input, composes the
// pattern (CaseInsensitive, WordMatch, Multiline modifiers) into a
// single regexp string, then dispatches to grepSingleFile or
// grepDirectory based on the stat result for Path.
func grepHandler(_ context.Context, input GrepInput) (GrepOutput, error) {
	if input.Path == "" {
		return GrepOutput{}, fmt.Errorf("fs.grep: path is required")
	}
	if input.Pattern == "" {
		return GrepOutput{}, fmt.Errorf("fs.grep: pattern is required")
	}

	patStr := input.Pattern
	if input.WordMatch {
		patStr = `\b` + patStr + `\b`
	}
	if input.CaseInsensitive {
		patStr = "(?i)" + patStr
	}
	if input.Multiline {
		patStr = "(?s)" + patStr
	}
	re, err := regexp.Compile(patStr)
	if err != nil {
		return GrepOutput{}, fmt.Errorf("fs.grep: invalid pattern %q: %w", input.Pattern, err)
	}

	info, err := os.Stat(input.Path)
	if err != nil {
		return GrepOutput{}, fmt.Errorf("fs.grep: %w", err)
	}

	if !info.IsDir() {
		return grepSingleFile(input, re)
	}
	return grepDirectory(input, re)
}

// grepSingleFile drives the single-file search path. It delegates to
// grepFile with the caller's ContextLines and MaxMatches and wraps
// any scan error with the fs.grep prefix.
func grepSingleFile(input GrepInput, re *regexp.Regexp) (GrepOutput, error) {
	matches, err := grepFile(input.Path, re, input.ContextLines, input.MaxMatches)
	if err != nil {
		return GrepOutput{}, fmt.Errorf("fs.grep: %w", err)
	}
	return GrepOutput{Matches: matches, Count: len(matches)}, nil
}

// grepDirectory walks a directory tree, applies the Hidden and Glob
// filters to each visited entry, and runs grepFile against every
// accepted regular file. Maintains a global MaxResults budget so the
// walk short-circuits via filepath.SkipAll once the total cap is hit
// and the per-file cap is reduced as the budget shrinks.
func grepDirectory(input GrepInput, re *regexp.Regexp) (GrepOutput, error) {
	var matches []GrepMatch
	done := false

	err := filepath.WalkDir(input.Path, func(p string, d fs.DirEntry, err error) error {
		if done {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}

		name := d.Name()

		// Skip hidden entries
		if !input.Hidden && strings.HasPrefix(name, ".") && p != input.Path {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		// Filter by glob
		if input.Glob != "" {
			matched, _ := filepath.Match(input.Glob, name)
			if !matched {
				return nil
			}
		}

		remaining := 0
		if input.MaxResults > 0 {
			remaining = input.MaxResults - len(matches)
			if remaining <= 0 {
				done = true
				return filepath.SkipAll
			}
		}

		perFile := input.MaxMatches
		if input.MaxResults > 0 && (perFile == 0 || perFile > remaining) {
			perFile = remaining
		}

		fileMatches, err := grepFile(p, re, input.ContextLines, perFile)
		if err != nil {
			return nil // skip unreadable files
		}
		matches = append(matches, fileMatches...)

		if input.MaxResults > 0 && len(matches) >= input.MaxResults {
			done = true
		}
		return nil
	})
	if err != nil {
		return GrepOutput{}, fmt.Errorf("fs.grep: walking %q: %w", input.Path, err)
	}

	if matches == nil {
		matches = []GrepMatch{}
	}

	return GrepOutput{Matches: matches, Count: len(matches)}, nil
}

// grepFile opens path, scans it line by line, and returns every
// GrepMatch that satisfies re. The remaining parameter caps the
// number of results (zero means unlimited) so a noisy file does not
// exceed its per-file allowance. Surrounding context lines are
// attached when contextLines > 0 by slicing the previously buffered
// lines.
func grepFile(path string, re *regexp.Regexp, contextLines, remaining int) ([]GrepMatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	var matches []GrepMatch
	for i, line := range lines {
		if !re.MatchString(line) {
			continue
		}

		m := GrepMatch{
			File:       path,
			LineNumber: i + 1,
			Line:       line,
		}

		if contextLines > 0 {
			start := max(i-contextLines, 0)
			if start < i {
				m.Before = lines[start:i]
			}
			end := min(i+1+contextLines, len(lines))
			if i+1 < end {
				m.After = lines[i+1 : end]
			}
		}

		matches = append(matches, m)

		if remaining > 0 && len(matches) >= remaining {
			break
		}
	}
	return matches, nil
}
