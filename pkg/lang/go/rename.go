// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// Rename is the lang.go.rename tool. It renames a Go symbol —
// package-level identifier, type, method, struct field, or constant — and
// rewrites every reference across the workspace, including references in
// sibling modules wired up under a go.work setup.
//
// The rename is type-checked via go/types: method resolution, interface
// satisfaction, and identifier shadowing all behave correctly without the
// false positives a regex-driven rename would produce. After the AST
// rewrite stages every file, the build gate runs go vet + go build on the
// affected packages; on any failure the transaction rolls every file back
// to its original bytes, so a failed rename never leaves the workspace
// half-broken. When AutoVerify is set the dispatcher also runs lint and
// optionally tests as diagnostic signals (see [runRefactorAction]).
//
// What is NOT rewritten: references hidden in string literals such as
// reflect.ValueOf(x).MethodByName("Old"), struct-tag references, and
// incidental name mentions in non-godoc comments. Godoc bracket-link
// references to the renamed symbol ARE rewritten so reference
// documentation stays in sync. After renaming an exported method or
// field, manually grep for MethodByName / FieldByName call sites that
// still point at the old name — no static refactor can see those.
//
// Prefer this over Grep + Edit for any rename touching more than a single
// file. A mechanical search misses method dispatches via interfaces,
// embedding, and cross-package wrappers, and the build gate will not
// catch a typo'd identifier until the next compile — frequently several
// turns away. One call here replaces five-to-eight turns of manual
// search-and-edit and removes a class of silent regression.
//
// The input mirrors [lang.RenameInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var Rename = tool.New[lang.RenameInput, refactor.Output](
	"lang.go.rename",
	"PREFER OVER Edit + Grep workflow for renaming a Go symbol. One call updates every reference project-wide (Edit only changes one file and silently breaks call sites). Type-checked, workspace-aware (handles go.work), and atomic — on build failure, all changes are rolled back. A typical Edit+Grep rename takes 5+ turns and may still miss method-shadowed references; this tool does it in 1 turn safely.",
	func(ctx context.Context, in lang.RenameInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionRename,
			Symbol:       in.Symbol,
			NewName:      in.NewName,
			Package:      in.Package,
			File:         in.File,
			Line:         in.Line,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
	tool.WithShortDescription("Rename a Go symbol project-wide, type-checked and atomic"),
)
