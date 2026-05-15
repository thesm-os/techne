// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// MoveFile is the lang.go.move_file tool. It relocates a single Go
// source file to a different package directory and rewrites every
// reference workspace-wide so the build stays green across the move.
//
// Unlike a filesystem rename, MoveFile updates the file's `package`
// clause to match its new directory, rewrites every importer of the
// old package to qualify references through the new package selector
// (`srcpkg.X` becomes `dstpkg.X`), and — when the moved file references
// siblings still in the source package — inserts the appropriate
// import so it can refer to them. Tests for the moved file are
// rewritten alongside, and `goimports` finalizes the import lists in
// each touched file.
//
// The whole operation stages into a single transaction with the
// standard build gate. Any failure rolls the file back to its
// original directory AND restores the source bytes of every touched
// importer, so a failed move never leaves dangling imports or split
// definitions. AutoVerify additionally runs lint and optionally tests
// as diagnostic signals — see [runRefactorAction].
//
// Prefer this over fs.move (filesystem-only — leaves the workspace
// with a stale `package` clause and dozens of broken imports the
// agent then has to chase down) AND over [MoveSymbol] for any
// cross-package move: [MoveSymbol] refuses cross-package operations
// outright. When the source file already contains exactly the
// symbol(s) destined for the new package, MoveFile is the equivalent
// operation done correctly — every importer is rewritten in one
// atomic pass. When the source file contains other symbols you want
// to keep in place, the established pattern is two calls: [MoveSymbol]
// first (extract the target symbol into its own file inside the source
// package), then MoveFile (relocate that new file to the destination
// package).
//
// What is NOT supported: moving multiple files at once (use
// [MovePackage] when an entire package moves), and moving a single
// file out of a package whose remaining files transitively depend on
// the moved declarations — those would form an import cycle and the
// build gate will reject the move.
//
// The input mirrors [lang.MoveFileInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var MoveFile = tool.New[lang.MoveFileInput, refactor.Output](
	"lang.go.move_file",
	"PREFER OVER fs.move (filesystem-only, leaves imports broken) AND OVER lang.go.move_symbol when moving across packages (move_symbol refuses cross-package). One call rewrites every importer (srcpkg.X → dstpkg.X), updates the package clause, qualifies references to siblings left behind, and runs goimports — atomic with build-gate rollback. For a cross-package single-symbol move where the source file contains other symbols you want to leave in place, extract via move_symbol first then call move_file.",
	func(ctx context.Context, in lang.MoveFileInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionMoveFile,
			File:         in.File,
			TargetFile:   in.TargetFile,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
	tool.WithShortDescription("Move a Go source file to another package, rewriting imports atomically"),
)
