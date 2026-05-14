// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// Document is the lang.go.document tool. It sets or replaces godoc
// comments on multiple exported (or unexported) symbols in one or many
// Go files in a single atomic call, walking each file's AST to locate
// every declaration by symbol name and splicing in the supplied doc
// comment with correct godoc formatting.
//
// The tool resolves four declaration shapes by name: top-level
// functions (`Foo`), methods (`Type.Method`), grouped const/var/type
// declarations (`Name` inside a `()` block), and standalone
// type/var/const declarations. Each supplied doc body is reformatted
// into godoc style — prose paragraphs are wrapped at MaxLineLength,
// code-block lines (already indented or beginning with `//`) are left
// as-is, and every line is prefixed with `// `. When StrictPrefix is
// set, each doc must start with the symbol's bare name to honour the
// godoc convention; the tool rejects the batch up front rather than
// producing non-conforming output.
//
// Mode controls collision behaviour: `replace` (default) overwrites the
// existing doc, `skip_existing` leaves a symbol's doc untouched when one
// is already present. Use ListMissing for a dry-run that just reports
// which exported symbols in the target file(s) are still undocumented —
// useful as a quality gate before generating docs in bulk.
//
// Edits stage into a single transaction with the standard build gate.
// Doc comments are syntactically invisible to the compiler, so a build
// failure here almost always means the tool inserted a comment in a
// position that broke an adjacent declaration (a `//go:build` line, an
// embedded directive); the rollback restores every file. AutoVerify is
// diagnostic-only because comments do not affect runtime behaviour.
//
// Prefer this over a hand-written sequence of Edit calls. For each
// symbol the tool computes the exact byte range of the existing doc
// block (which may span many lines and end on the line above the
// declaration), so the rewrite is one byte-range swap instead of a
// fragile multi-line OldString match — and the batch commits or rolls
// back as a unit so a partial run is impossible.
//
// The input mirrors [lang.DocumentInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var Document = tool.New[lang.DocumentInput, refactor.Output](
	"lang.go.document",
	"PREFER OVER multiple Edit calls when adding or refreshing doc comments on a Go file. One call sets doc comments for many symbols by name (top-level funcs, methods, types, vars, consts) — the tool finds each declaration via AST, formats your text into godoc style (`// ` per line), and applies all edits atomically through the build gate. Edit alone forces you to write each OldString / NewString pair, get indentation right, and risk corrupting the file on bulk edits.",
	func(ctx context.Context, in lang.DocumentInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:        refactor.ActionDocument,
			File:          in.File,
			Comments:      convertSymbolDocs(in.Comments),
			DocumentFiles: convertFileDocs(in.Files),
			Mode:          in.Mode,
			MaxLineLength: in.MaxLineLength,
			NoWrap:        in.NoWrap,
			StrictPrefix:  in.StrictPrefix,
			ListMissing:   in.ListMissing,
			DryRun:        in.DryRun,
			AutoVerify:    in.AutoVerify,
			VerifySuites:  in.VerifySuites,
			Detail:        in.Detail,
		})
	},
)

// convertSymbolDocs maps the public [lang.SymbolDoc] schema onto the
// internal [refactor.DocumentComment] type. The two packages keep
// parallel types so the wire-facing JSON contract stays independent of
// the engine's internal layout — either side can evolve without
// breaking the other.
func convertSymbolDocs(in []lang.SymbolDoc) []refactor.DocumentComment {
	out := make([]refactor.DocumentComment, len(in))
	for i, c := range in {
		out[i] = refactor.DocumentComment{Symbol: c.Symbol, Doc: c.Doc}
	}
	return out
}

// convertFileDocs maps the public [lang.FileDocs] multi-file batch onto
// the internal [refactor.DocumentFile] type, recursively converting
// each entry's symbol-doc list via [convertSymbolDocs]. Same separation
// of concerns as [convertSymbolDocs] — wire-facing schema decoupled
// from engine internals.
func convertFileDocs(in []lang.FileDocs) []refactor.DocumentFile {
	out := make([]refactor.DocumentFile, len(in))
	for i, f := range in {
		out[i] = refactor.DocumentFile{File: f.File, Comments: convertSymbolDocs(f.Comments)}
	}
	return out
}
