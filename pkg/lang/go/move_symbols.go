// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// MoveSymbols is the lang.go.move_symbols tool. It moves a batch of
// Go symbols between files in the same package as a single atomic
// operation — the tool computes all moves, stages every edit, then
// runs one build gate at the end. Compared to N sequential
// [MoveSymbol] calls it cuts build-gate overhead by a factor of N and
// guarantees that the batch commits or rolls back as a unit.
//
// Each move entry names the symbol and the target file; the source
// file is auto-resolved from the symbol's current location unless
// explicitly provided. The same semantics as [MoveSymbol] apply per
// entry: types carry their receiver methods, doc comments and
// attached directives travel with the declaration, and imports are
// rebalanced through goimports. When two moves target the same file
// their output order matches their order in the input slice.
//
// The batch stages into one transaction with the standard build gate.
// A single failure rolls every staged edit back, so a partially
// broken redistribution is impossible. AutoVerify runs lint and
// optionally tests after a successful commit — see [runRefactorAction].
//
// Common use cases: splitting a large monolithic file into
// topic-grouped files, reorganizing test helpers to sit next to the
// code they test, distributing utility functions from a catch-all
// file across the packages that actually use them, and pre-staging
// the layout of a future package extraction. For a single move the
// plain [MoveSymbol] tool is fine; this one earns its keep starting
// at two symbols.
//
// The input mirrors [lang.MoveSymbolsInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var MoveSymbols = tool.New[lang.MoveSymbolsInput, refactor.Output](
	"lang.go.move_symbols",
	"PREFER OVER N sequential lang.go.move_symbol calls when redistributing many symbols across files in the same package. One call stages all moves into a single transaction with one build gate at the end — substantially faster (Nx fewer go-build invocations) and atomic (any failure rolls back the whole batch). Common uses: splitting a large file into smaller ones, reorganizing tests to match a source layout, distributing helpers from a utils file across owners.",
	func(ctx context.Context, in lang.MoveSymbolsInput) (refactor.Output, error) {
		moves := make([]refactor.Move, len(in.Moves))
		for i, m := range in.Moves {
			moves[i] = refactor.Move{Symbol: m.Symbol, TargetFile: m.TargetFile, File: m.File}
		}
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionMoveSymbols,
			Moves:        moves,
			Package:      in.Package,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
	tool.WithShortDescription("Move many Go symbols across files in one atomic batch"),
)
