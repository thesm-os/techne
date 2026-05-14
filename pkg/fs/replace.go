// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"go.thesmos.sh/techne/internal/tool"
)

// ReplaceInput is the wire-format request for fs.replace. Path,
// Pattern, and Replacement are required; flavour modifiers and a
// dry-run toggle round out the schema. The handler operates on a
// single file in one shot — there is no atomic batching here. For
// multi-file or build-gated changes reach for fs.patch.
type ReplaceInput struct {
	// Path is the absolute or workspace-relative path of the file whose
	// contents will be rewritten. The file must exist and be readable;
	// the handler does not create missing files. Permissions are
	// preserved by stat-ing the file before writing back.
	Path string `json:"path" jsonschema:"Path to the file to modify."`
	// Pattern is the regular expression to match against the file body.
	// It uses Go's RE2 syntax (no look-around, no backreferences). The
	// full file is loaded into memory before matching, so the pattern
	// sees the entire body at once rather than line-by-line — a meaningful
	// difference from fs.grep when constructing multiline patterns.
	Pattern string `json:"pattern" jsonschema:"Regular expression pattern to find."`
	// Replacement is the string written in place of each match using
	// Go's regexp.ReplaceAllString syntax: capture group references take
	// the form $1, $2, ${name}; a literal dollar sign must be written as
	// $$. The replacement string is interpreted, not literal — escape any
	// dollar signs that should appear verbatim in the output.
	Replacement string `json:"replacement" jsonschema:"Replacement string (may use capture group references)."`
	// CaseInsensitive, when true, prepends "(?i)" to the pattern so
	// letter casing is ignored during matching. Composes with Multiline.
	CaseInsensitive bool `json:"case_insensitive,omitempty" jsonschema:"Perform a case-insensitive match. Optional, defaults to false."`
	// MaxReplacements caps the number of substitutions performed in the
	// file. Zero (the default) replaces every match. Set to 1 to perform
	// an idempotent first-match replacement, useful when the agent knows
	// the pattern occurs only once but wants belt-and-braces protection
	// against over-replacement.
	MaxReplacements int `json:"max_replacements,omitempty" jsonschema:"Maximum number of replacements to perform. Optional, replaces all occurrences by default."`
	// DryRun, when true, computes the replaced content and returns it in
	// ReplaceOutput.Content without writing to disk. Use this to preview
	// the result of a destructive replacement before committing. The
	// Replacements count is still populated in dry-run mode so the agent
	// can validate that the right number of matches was found.
	DryRun bool `json:"dry_run,omitempty" jsonschema:"Preview changes without modifying the file. Optional, defaults to false."`
	// Multiline, when true, prepends "(?m)" so '^' and '$' match the
	// start and end of every line rather than the entire file body.
	// Does not enable '.' to match newlines — that requires "(?s)" and
	// is not exposed as a separate flag.
	Multiline bool `json:"multiline,omitempty" jsonschema:"Enable multiline mode so ^ and $ match the start and end of each line. Optional, defaults to false."`
}

// ReplaceOutput is the wire-format response for fs.replace. The
// fields together tell the caller what was changed and, in dry-run
// mode, exactly what the result would look like — enough for the
// agent to validate without a follow-up fs.read.
type ReplaceOutput struct {
	// Path echoes the file that was (or would be) modified.
	Path string `json:"path" jsonschema:"Path of the file that was modified."`
	// Replacements is the number of substitutions performed (or, in
	// dry-run mode, the number that would be performed). A value of zero
	// with no error means the pattern matched nothing — typically a
	// signal that the regex needs tuning rather than a hard failure.
	Replacements int `json:"replacements" jsonschema:"Number of replacements made."`
	// DryRun mirrors the input flag, included so a caller piping the
	// output to another tool can branch on it without retaining the
	// request.
	DryRun bool `json:"dry_run" jsonschema:"Whether this was a dry run (no file modified)."`
	// Content holds the post-replacement file body when DryRun is true,
	// so the caller can validate the result before committing. For
	// actual writes Content is left empty — the agent should re-read the
	// file via fs.read or trust the Replacements count.
	Content string `json:"content,omitempty" jsonschema:"Modified content (only included for dry runs)."`
}

// Replace is the fs.replace tool entry point. It applies a regex-based
// find-and-replace to a single file and writes the result back in one
// shot, optionally previewing the change via DryRun.
//
// When to use it: simple, scoped textual rewrites where the agent has
// already confirmed the pattern is right. For Go symbol renames, prefer
// lang.go.rename — it is AST-aware and updates every reference
// project-wide rather than just one file. For multi-file edits with
// build verification and rollback, prefer fs.patch.
//
// Important edge cases: the entire file is loaded into memory; a
// mid-write failure (rare with os.WriteFile) leaves the file in an
// indeterminate state with no rollback; capture-group references in
// Replacement use $1/$2 syntax (a literal $ must be written $$); a
// zero MaxReplacements is the default and replaces all matches; and
// file permissions are preserved by stat-ing the original before
// writing the new contents. Failures during the initial read or final
// write surface as handler errors with the fs.replace prefix.
var Replace = tool.New[ReplaceInput, ReplaceOutput](
	"fs.replace",
	"Find-and-replace within a single file. For multi-file atomic edits, use fs.patch instead.",
	replaceHandler,
)

// replaceHandler implements fs.replace. It composes the pattern with
// Multiline and CaseInsensitive inline flags, compiles it, reads the
// full file, performs either a bounded (MaxReplacements) or unbounded
// replacement, then either returns the result in dry-run mode or
// writes back to disk with the original file mode.
func replaceHandler(_ context.Context, input ReplaceInput) (ReplaceOutput, error) {
	if input.Path == "" {
		return ReplaceOutput{}, fmt.Errorf("fs.replace: path is required")
	}
	if input.Pattern == "" {
		return ReplaceOutput{}, fmt.Errorf("fs.replace: pattern is required")
	}

	patStr := ""
	if input.Multiline {
		patStr += "(?m)"
	}
	if input.CaseInsensitive {
		patStr += "(?i)"
	}
	patStr += input.Pattern

	re, err := regexp.Compile(patStr)
	if err != nil {
		return ReplaceOutput{}, fmt.Errorf("fs.replace: invalid pattern %q: %w", input.Pattern, err)
	}

	data, err := os.ReadFile(input.Path)
	if err != nil {
		return ReplaceOutput{}, fmt.Errorf("fs.replace: reading %q: %w", input.Path, err)
	}

	original := string(data)
	count := 0

	var result string
	if input.MaxReplacements > 0 {
		result = replaceN(re, original, input.Replacement, input.MaxReplacements, &count)
	} else {
		// Count matches first
		count = len(re.FindAllString(original, -1))
		result = re.ReplaceAllString(original, input.Replacement)
	}

	if input.DryRun {
		return ReplaceOutput{
			Path:         input.Path,
			Replacements: count,
			DryRun:       true,
			Content:      result,
		}, nil
	}

	// Get original file mode
	info, err := os.Stat(input.Path)
	if err != nil {
		return ReplaceOutput{}, fmt.Errorf("fs.replace: stat %q: %w", input.Path, err)
	}

	if err := os.WriteFile(input.Path, []byte(result), info.Mode()); err != nil {
		return ReplaceOutput{}, fmt.Errorf("fs.replace: writing %q: %w", input.Path, err)
	}

	return ReplaceOutput{
		Path:         input.Path,
		Replacements: count,
		DryRun:       false,
	}, nil
}

// replaceN performs at most n regex replacements against src and
// records the actual count via the count pointer. Uses
// ReplaceAllStringFunc to short-circuit after the nth match — once
// the counter trips, subsequent matches are passed through
// unchanged. Counting is done via the pointer so the caller can
// report the precise number of substitutions even when fewer than n
// matches existed.
func replaceN(re *regexp.Regexp, src, repl string, n int, count *int) string {
	*count = 0
	result := re.ReplaceAllStringFunc(src, func(match string) string {
		if *count >= n {
			return match
		}
		*count++
		return re.ReplaceAllString(match, repl)
	})
	return result
}
