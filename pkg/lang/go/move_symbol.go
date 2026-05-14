// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// MoveSymbol is the lang.go.move_symbol tool. It moves a single Go
// symbol — a top-level function, method, type, var, or const — from
// its current source file to a different file in the same package,
// preserving the symbol's doc comment, surrounding blank-line spacing,
// and any `//go:` directives attached to the declaration.
//
// Moving a struct type carries its receiver methods along
// automatically: the tool finds every method declared on the type (in
// any file of the package) and moves them as a group to the target so
// the definition and its methods stay collocated. Moving a function or
// var moves only that declaration. Imports are rebalanced — imports
// used only by the moved symbol are added to the target file, and
// imports left unused in the source are removed via goimports. Because
// the move stays within one package, no reference rewriting is
// required.
//
// Edits stage into a single transaction with the standard build gate
// and rollback. AutoVerify adds lint and tests as diagnostic signals
// — see [runRefactorAction]. The target file is created if it does
// not exist; the package clause is inherited from the source file.
//
// Manual symbol moves routinely forget receiver methods, mis-handle
// blank lines around the moved declaration, and either duplicate or
// orphan imports. Prefer this tool whenever a symbol moves between
// files, especially for types whose method set spans multiple files.
// For moves across packages use [MoveFile] (whole file) or first move
// the symbol within the package then move the file.
//
// The input mirrors [lang.MoveSymbolInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var MoveSymbol = tool.New[lang.MoveSymbolInput, refactor.Output](
	"lang.go.move_symbol",
	"PREFER OVER Read + Edit when moving a Go symbol between files in the same package. One call removes the symbol (and its receiver methods, if any) from the source file and adds it to the target with doc comments preserved. Manual move misses receiver methods and breaks blank-line spacing.",
	func(ctx context.Context, in lang.MoveSymbolInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionMoveSymbol,
			Symbol:       in.Symbol,
			TargetFile:   in.TargetFile,
			Package:      in.Package,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
	tool.WithShortDescription("Move a Go symbol (with receiver methods) between files in the same package"),
)
