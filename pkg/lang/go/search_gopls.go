// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// goplsHit is a single match parsed from `gopls workspace_symbol`
// output. The position fields describe the symbol's identifier range in
// the source file, not the full declaration extent — gopls reports the
// bounds of the name token itself so the IDE can highlight it.
//
// SymbolName uses receiver-qualified naming for methods ("Greeter.Hello")
// and bare names for free functions; this matches the doc scorer's
// output so the two streams can be merged by key in mergeSignals.
//
// Kind is the raw string emitted by gopls ("Function", "Method",
// "Struct", …) before normalization. Use normalizeGoplsKind to map it
// to the lang.Kind* constants used throughout the rest of the package.
type goplsHit struct {
	File       string
	Line       int
	ColStart   int
	ColEnd     int
	SymbolName string // e.g. "Greeter.Hello" or "BackpressureQueue"
	Kind       string // gopls's symbol kind: Function, Method, Struct, Interface, etc.
}

// goplsLineRegex parses the line-oriented output of
// `gopls workspace_symbol`. Each line has the shape:
//
//	/path/to/file.go:LINE:COL_START-COL_END SymbolName Kind
//
// File paths may contain spaces or colons (on Windows or unusual
// checkouts) and a SymbolName may itself contain dots
// ("Receiver.Method"), so the regex locks onto the trailing
// `:line:colStart-colEnd <symbol> <kind>` shape with greedy file-path
// matching at the front. Lines that do not match this shape are
// dropped silently — gopls occasionally emits diagnostic lines that are
// not symbol matches.
var goplsLineRegex = regexp.MustCompile(`^(.+):(\d+):(\d+)-(\d+)\s+(\S+)\s+(\S+)$`)

// queryGopls invokes `gopls workspace_symbol -matcher fuzzy <query>` in
// the given workspace directory and returns parsed hits in gopls's
// original rank order (preserved by parseGoplsOutput).
//
// Returns (nil, nil) when gopls is not on PATH so the caller can
// degrade gracefully to the doc-scorer-only path. Any other failure
// (non-zero exit, IO error) yields (nil, err) with stderr included —
// the lang.go.search handler treats this as non-fatal and falls back
// to the doc scorer.
//
// Spawns a subprocess. The provided context's cancellation is honored
// via exec.CommandContext, so request-scoped cancellation reaches the
// gopls process.
func queryGopls(ctx context.Context, dir, query string) ([]goplsHit, error) {
	if _, err := exec.LookPath("gopls"); err != nil {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "gopls", "workspace_symbol", "-matcher", "fuzzy", query)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gopls workspace_symbol: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return parseGoplsOutput(stdout.String()), nil
}

// parseGoplsOutput parses the stdout of `gopls workspace_symbol`,
// dropping malformed lines silently so a single bad output line does
// not break the entire result set. The slice order is preserved exactly
// as gopls emits it, which is significant: gopls returns matches in
// descending score order, and that ordering is what combinedScore's
// rank-decay weighting consumes.
//
// A blank line terminates parsing implicitly (the regex won't match)
// so the function handles both newline-trailed and bare output
// formats.
func parseGoplsOutput(s string) []goplsHit {
	var hits []goplsHit
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := goplsLineRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lineNum, err1 := strconv.Atoi(m[2])
		colStart, err2 := strconv.Atoi(m[3])
		colEnd, err3 := strconv.Atoi(m[4])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		hits = append(hits, goplsHit{
			File:       m[1],
			Line:       lineNum,
			ColStart:   colStart,
			ColEnd:     colEnd,
			SymbolName: m[5],
			Kind:       m[6],
		})
	}
	return hits
}

// normalizeGoplsKind maps gopls's symbol-kind strings to the lang.Kind*
// constants used throughout the package. Returns the empty string for
// kinds we deliberately do not surface (Field, TypeParameter, Module,
// Namespace, …) so the caller can filter them with a single check.
//
// Note that gopls emits "Class" for some struct-like types; the
// function folds it into KindStruct to keep the surfaced taxonomy
// consistent with the doc scorer.
func normalizeGoplsKind(k string) string {
	switch k {
	case "Function":
		return "func"
	case "Method":
		return "method"
	case "Struct", "Class":
		return "struct"
	case "Interface":
		return "interface"
	case "TypeAlias":
		return "type"
	case "Variable":
		return "var"
	case "Constant":
		return "const"
	default:
		return ""
	}
}
