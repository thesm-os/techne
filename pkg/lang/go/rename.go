// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// Rename is the lang.go.rename tool. It renames any Go identifier —
// package-level symbol, type, method, struct field, constant, local
// variable, or function/method parameter — and rewrites every reference
// across the workspace, including references in sibling modules wired up
// under a go.work setup.
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
// LOCAL / PARAMETER RENAMES. A bare name like "kind" or "i" resolves to
// many different types.Object instances across a workspace, so the
// package-scope lookup that handles top-level symbols cannot
// disambiguate it. For locals and parameters, supply File + Line pointing
// at the *defining* identifier — the line of the `var`/`:=` declaration
// or the line of the FuncDecl whose signature contains the parameter.
// The resolver matches against TypesInfo.Defs (defining idents only), so
// the param's def is selected even when other idents with the same name
// (e.g. an imported-package selector `kind.Kind` on the same line) share
// that source position. This is the right tool for the
// "my parameter shadows an imported package name" fixup that often
// follows a cross-package symbol move.
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
// file, AND over plain Edit even for single-file local renames — Edit
// won't catch closure captures, method-on-receiver shadowing, or godoc
// references. A mechanical search misses method dispatches via
// interfaces, embedding, and cross-package wrappers, and the build gate
// will not catch a typo'd identifier until the next compile — frequently
// several turns away. One call here replaces five-to-eight turns of
// manual search-and-edit and removes a class of silent regression.
//
// The input mirrors [lang.RenameInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var Rename = tool.New[lang.RenameInput, refactor.Output](
	"lang.go.rename",
	"PREFER OVER Edit + Grep for renaming ANY Go identifier — package-level symbols, methods, fields, AND local variables / function or method parameters (pass file + line of the defining identifier to disambiguate). One call updates every reference project-wide (Edit only changes one file and silently breaks call sites). Type-checked, workspace-aware (handles go.work), and atomic — on build failure, all changes are rolled back. A typical Edit+Grep rename takes 5+ turns and may still miss method-shadowed references; this tool does it in 1 turn safely. The local/param mode is the right tool for fixing parameter shadowing (e.g. `func F(kind kind.Kind)` → rename param `kind` to `k`) that crops up after a cross-package move.",
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
