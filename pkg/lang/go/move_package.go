// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// MovePackage is the lang.go.move_package tool. It relocates a whole
// Go package — every source file in the directory — to a new module
// path and rewrites every import statement project-wide that referenced
// the old path.
//
// When the basename changes (for example `foo/bar` → `foo/baz`), the
// tool adds an explicit import alias in each rewritten importer if the
// imported package's name does not match the new basename, so call
// sites that referenced the old package selector continue to compile
// without further edits. When the basename is preserved, no alias is
// needed and the rewrite reduces to a clean path change. The move
// respects go.work boundaries: a package moved within one module only
// touches that module's importers; a package moved across modules
// rewrites importers in every workspace sibling.
//
// The filesystem rename and every import-statement edit stage into a
// single transaction. The build gate runs go vet + go build on the
// affected packages after staging; any failure rolls the directory
// back to its original location AND restores every importer's source
// bytes, leaving the workspace exactly as it was. AutoVerify runs lint
// and optionally tests as diagnostic signals — see [runRefactorAction].
//
// Manual moves (mv + Edit) leave dangling imports across the workspace
// and frequently miss test files or sibling-module references; the
// build then breaks several turns later and the trail back to the move
// is already lost. Use this tool whenever a package needs to be
// relocated, even within the same module.
//
// The input mirrors [lang.MovePackageInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var MovePackage = tool.New[lang.MovePackageInput, refactor.Output](
	"lang.go.move_package",
	"PREFER OVER Bash + Edit when relocating a Go package. One call moves the directory AND rewrites every import statement project-wide; manual moves leave dangling imports. Adds explicit aliases when the basename changes so call sites continue to compile. Atomic with rollback.",
	func(ctx context.Context, in lang.MovePackageInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:        refactor.ActionMovePackage,
			SourcePackage: in.SourcePackage,
			DestPackage:   in.DestPackage,
			DryRun:        in.DryRun,
			AutoVerify:    in.AutoVerify,
			VerifySuites:  in.VerifySuites,
			Detail:        in.Detail,
		})
	},
)
