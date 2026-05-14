// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// DeleteFile is the lang.go.delete_file tool. It removes a Go source
// file from the workspace, gated by the same atomicity and build-check
// that every other refactor enjoys — a deletion that would leave
// dangling references is rejected up front rather than left for a
// failed compile to find later.
//
// The pre-check loads the file's package through go/types and verifies
// that no exported (or unexported, in the same package) symbol
// declared in the file is still referenced after the deletion. If a
// reference exists the tool returns [refactor.StatusFailure] with the
// offending site reported in [refactor.Output] — the disk is not
// touched. When the file is reference-free the tool removes it
// inside a transaction with the standard build gate; a build failure
// (for example because of a residual generated artefact elsewhere)
// rolls the file back. AutoVerify runs lint and optionally tests as
// diagnostic signals — see [runRefactorAction]. Workspace-aware: a
// file in a submodule reachable through go.work is handled like any
// other.
//
// Common use cases: removing dead code after a successful migration,
// clearing generated files whose generator was retired, cleaning up
// empty stub files left from a partial refactor. Prefer this over
// `rm` from a shell: rm has no awareness of the surrounding Go
// workspace and will quietly leave a half-broken module that surfaces
// as an unrelated compile error several turns later.
//
// The input mirrors [lang.DeleteFileInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var DeleteFile = tool.New[lang.DeleteFileInput, refactor.Output](
	"lang.go.delete_file",
	"PREFER OVER `rm` from a shell when removing a Go source file. Workspace-aware (handles go.work), atomic (rollback on build failure), and surfaces a structured FileResult so failures point at the offending reference rather than leaving a half-broken module. Use cases: removing dead code, cleaning up after a successful move, deleting old generated files.",
	func(ctx context.Context, in lang.DeleteFileInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionDeleteFile,
			File:         in.File,
			Package:      in.Package,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
)
