// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"go.thesmos.sh/techne/internal/tool"
)

// ReadInput is the wire-format request for fs.read. All fields except
// Path are optional; the zero value reads the entire file with no line
// numbers.
//
// The Offset / Limit window is expressed in lines, not bytes, so agents
// can address ranges using the same coordinates they see in compiler
// errors, lint output, or fs.grep results without having to translate.
type ReadInput struct {
	// Path is the absolute or workspace-relative path to the file to read.
	// Relative paths resolve against the host process's current working
	// directory at handler invocation; pass absolute paths whenever a
	// session may have moved Chdir state. A symbolic link is followed
	// transparently — use [Stat] with FollowSymlinks=false to inspect the
	// link itself.
	Path string `json:"path" jsonschema:"Absolute or relative path to the file to read. Example: '/var/home/user/project/main.go'."`
	// Offset is the 0-based line index at which the returned content
	// begins. Values below zero clamp to 0; values above the file's line
	// count clamp to that count, in which case the call returns an empty
	// string with TotalLines populated so the agent can adjust. Pair with
	// Limit to page through a file that is too large to return in one call.
	Offset int `json:"offset,omitempty" jsonschema:"Line number to start reading from (0-based). Use with limit to read specific sections instead of loading the entire file. Optional."`
	// Limit caps the number of lines returned starting at Offset. Zero (the
	// default) means "read through end of file". This is the right knob for
	// targeted reads against a known function or hunk — prefer a tight
	// Limit over loading an entire large file, since the response size
	// directly affects context budget.
	Limit int `json:"limit,omitempty" jsonschema:"Maximum number of lines to return. Use with offset for targeted reads. Optional."`
	// LineNumbers, when true, prefixes every returned line with its
	// absolute 1-based line number followed by a tab, matching the format
	// emitted by lang.go.explore and other techne tools. Enable this when
	// the output will be quoted back to the agent or pasted into an issue
	// — it lets later searches and edits reference exact line positions
	// without a follow-up call.
	LineNumbers bool `json:"line_numbers,omitempty" jsonschema:"Prefix each output line with its line number. Optional, defaults to false."`
}

// ReadOutput is the wire-format response for fs.read. Content is
// always terminated with a trailing newline for every line returned,
// even when the file on disk lacks a final newline, so downstream
// rendering is uniform.
//
// Callers should consult TotalLines (not LinesRead) when deciding
// whether more of the file remains — LinesRead reflects only the
// window that was actually returned and may be capped by Limit.
type ReadOutput struct {
	// Content holds the slice of file body corresponding to the requested
	// Offset/Limit window. Each line is terminated with a single '\n' and
	// is prefixed with its line number when LineNumbers was set. Binary
	// content will scan but is likely to be truncated by the underlying
	// bufio.Scanner default token size and should be read with a
	// binary-aware tool instead.
	Content string `json:"content" jsonschema:"The file content."`
	// LinesRead is the count of lines actually present in Content. It will
	// be min(Limit, TotalLines-Offset) when both Offset and Limit are
	// supplied, and equals TotalLines when Offset is zero and Limit is
	// zero. Use it to confirm the window matched expectations — a value of
	// zero with TotalLines>0 means the supplied Offset was past end-of-
	// file.
	LinesRead int `json:"lines_read" jsonschema:"Number of lines returned."`
	// TotalLines is the line count of the underlying file, independent of
	// Offset and Limit. The handler reads the file end-to-end to count
	// lines, so this value is always exact — use it as the upper bound
	// when planning subsequent paginated reads.
	TotalLines int `json:"total_lines" jsonschema:"Total number of lines in the file. Use this to plan follow-up offset+limit reads for large files."`
}

// Read is the fs.read tool entry point. It returns a window of lines
// from a single file, optionally annotated with line numbers, and is
// the basic primitive every presenter (MCP, CLI, TUI) calls when an
// agent asks to inspect a file.
//
// Design rationale: file reads dominate context usage in agent loops,
// so the input schema is built around line-addressed paging (Offset +
// Limit) rather than byte ranges. Agents already think in lines because
// compiler errors, lint findings, and search results are line-keyed;
// asking them to compute byte offsets would be wasteful.
//
// For Go source code, prefer lang.go.explore over Read. Explore returns
// structured symbols (signatures, doc comments, AST-aware bodies) and
// is typically an order of magnitude cheaper in tokens than reading
// the whole file. Reach for fs.read when the file is not Go, when you
// need a literal byte-for-byte view (e.g. a generated artifact, lock
// file, or YAML config), or when you already know the exact line range
// and want to skip the symbol indirection.
//
// The handler reads the entire file to count lines even when only a
// window is returned, so TotalLines is always exact at the cost of
// one scan per call. Files larger than the configured MaxFileSizeMB
// should be rejected by an upstream guard; the handler itself does not
// enforce a size cap.
var Read = tool.New[ReadInput, ReadOutput](
	"fs.read",
	"Reads file contents with line-level precision. Use offset+limit to read specific sections instead of loading entire files. Prefer lang.go.explore for Go source — it returns structured symbols instead of raw text.",
	readHandler,
)

// readHandler implements fs.read. It opens the file, scans every line
// into memory (so TotalLines is exact), then slices the requested
// window and assembles Content with optional line-number prefixes.
//
// The full-file scan is intentional: bufio.Scanner forward-only nature
// makes a single pass cheaper than the two-pass alternative (count then
// seek) for files small enough to be of interest to an agent, and it
// lets the handler return TotalLines without a follow-up. Files that
// contain lines longer than bufio.Scanner's default token size
// (64 KiB) will surface a scanning error — that is the failure mode
// for pathological inputs and is preferred over silently truncating.
func readHandler(_ context.Context, input ReadInput) (ReadOutput, error) {
	if input.Path == "" {
		return ReadOutput{}, fmt.Errorf("fs.read: path is required")
	}

	f, err := os.Open(input.Path)
	if err != nil {
		return ReadOutput{}, fmt.Errorf("fs.read: %w", err)
	}
	defer f.Close()

	var allLines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return ReadOutput{}, fmt.Errorf("fs.read: scanning %q: %w", input.Path, err)
	}

	totalLines := len(allLines)

	// Apply offset
	start := min(max(input.Offset, 0), totalLines)

	selected := allLines[start:]

	// Apply limit
	if input.Limit > 0 && len(selected) > input.Limit {
		selected = selected[:input.Limit]
	}

	var sb strings.Builder
	for i, line := range selected {
		if input.LineNumbers {
			fmt.Fprintf(&sb, "%d\t%s\n", start+i+1, line)
		} else {
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}

	return ReadOutput{
		Content:    sb.String(),
		LinesRead:  len(selected),
		TotalLines: totalLines,
	}, nil
}
