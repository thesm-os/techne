// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// ExtractVariable is the lang.go.extract_variable tool. It lifts a Go
// expression out of its enclosing statement and binds it to a freshly
// declared local variable, replacing the original occurrence with a
// reference to the new name. Useful when an expression has grown
// unwieldy, when the same subexpression appears more than once and
// should share a name, or when introducing a debugging seam.
//
// The expression is located by line number plus optional column bounds.
// When StartCol and EndCol are omitted, the tool auto-picks the most
// likely expression on that line using a fixed priority order — the
// if-condition of an if statement, the value of a return, the
// right-hand side of an assignment, then the innermost compound
// expression — which is correct in the overwhelmingly common case of
// "the only complex expression on this line". When two candidates are
// plausible, supply explicit column bounds for an exact range.
//
// The new declaration is inserted on the preceding line with the
// enclosing function's indentation; the declaration's type is inferred
// through go/types so type-incompatible extractions (an untyped
// constant promoted into an interface, for example) fail fast with a
// typechecker error rather than producing wrong code. Edits stage into
// a transaction with the standard build gate and rollback; AutoVerify
// adds lint and tests as diagnostic signals — see [runRefactorAction].
//
// The input mirrors [lang.ExtractVariableInput]; the output is the
// shared [refactor.Output] surfaced by every refactor tool.
var ExtractVariable = tool.New[lang.ExtractVariableInput, refactor.Output](
	"lang.go.extract_variable",
	"PREFER OVER manual Edit when extracting a Go expression into a local variable. AST-aware — locates the expression by line (with optional column bounds) and inserts the new declaration with correct indentation. If start_col/end_col are omitted, the tool auto-picks the most likely expression on that line (if-condition, return value, assignment RHS).",
	func(ctx context.Context, in lang.ExtractVariableInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionExtractVariable,
			File:         in.File,
			Line:         in.Line,
			VariableName: in.VariableName,
			StartCol:     in.StartCol,
			EndCol:       in.EndCol,
			Package:      in.Package,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
	tool.WithShortDescription("Extract a Go expression into a local variable with inferred type"),
)
