// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// InlineConstant is the lang.go.inline_constant tool. It replaces every
// use site of a named Go constant with the constant's literal value and
// removes the declaration once no usages remain. Useful for retiring a
// constant whose meaning has been absorbed by its single value, for
// flattening a generated-code indirection, or for unblocking a downstream
// refactor that needs the literal in place.
//
// Resolution is type-checked through go/types so identifiers with the
// same spelling in unrelated scopes are NOT rewritten — only references
// that actually resolve to the target constant get inlined. The
// replacement value is the constant's source expression rendered through
// go/printer, which preserves integer base (hex stays hex), string
// quoting, and operator parenthesization where required. When the
// literal was created via untyped arithmetic, the result is reproduced
// without adding a redundant type conversion.
//
// Edits stage into a transaction; the build gate runs go vet + go build
// on the affected packages and rolls back on any failure (for example,
// an inlined large integer that no longer fits a typed parameter).
// AutoVerify runs lint and optionally tests as diagnostic signals — see
// [runRefactorAction]. Only constants are supported here; for inlining
// functions or variables use gopls.
//
// The input mirrors [lang.InlineConstantInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var InlineConstant = tool.New[lang.InlineConstantInput, refactor.Output](
	"lang.go.inline_constant",
	"PREFER OVER Grep + Edit when inlining a Go constant. Replaces every use site with the literal value (type-checked — no false positives from same-named identifiers in unrelated scopes). Only constants are supported; for function inlining use gopls.",
	func(ctx context.Context, in lang.InlineConstantInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionInline,
			Symbol:       in.Symbol,
			Package:      in.Package,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
)
