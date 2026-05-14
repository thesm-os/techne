// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// ChangeType is the lang.go.change_type tool. It replaces a Go type
// definition with a new spec and updates every usage site project-wide
// — variable declarations, parameter types, return types, struct
// fields, type assertions, and conversions — keeping the workspace
// compilable across the swap.
//
// The common motivation is migrating between equivalent shapes: a named
// function type to an interface, a struct to a named alias, a type alias
// to a fresh type, or widening a numeric type. When the new shape
// changes how values are consumed (a function alias becoming an
// interface, for example), the optional MethodMapping rewrites direct
// invocations: the special source key '__call__' maps the bare-callable
// form into a method call, so edge(args) becomes edge.<method>(args) at
// every call site. Other mapping entries rename specific method calls
// in place.
//
// Edits stage into a single transaction; the build gate runs go vet +
// go build on the affected packages and rolls every file back on
// failure. AutoVerify additionally runs lint and optionally tests
// (diagnostic-only via [runRefactorAction]). What is NOT updated:
// references buried in reflection or in code generated outside the
// workspace, and conversions whose target literal is interpolated from
// a string. Audit those manually.
//
// Prefer this over Grep + Edit even for small type swaps — every usage
// site gets type-checked against the new definition, surfacing
// incompatibilities up front instead of in the next build. A manual
// sweep typically misses field embeddings and interface conversions and
// will not detect that a literal initializer became invalid.
//
// The input mirrors [lang.ChangeTypeInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var ChangeType = tool.New[lang.ChangeTypeInput, refactor.Output](
	"lang.go.change_type",
	"PREFER OVER manual Edit + Grep when replacing a Go type definition. Updates every usage site project-wide. Optional method_mapping rewrites direct invocations: '__call__' → method-name turns edge(args) into edge.<method>(args). Atomic build-gate with rollback. Common use: changing a function-type alias to an interface and migrating callers in one operation.",
	func(ctx context.Context, in lang.ChangeTypeInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:            refactor.ActionChangeType,
			Symbol:            in.Symbol,
			NewTypeDefinition: in.NewTypeDefinition,
			MethodMapping:     in.MethodMapping,
			Package:           in.Package,
			DryRun:            in.DryRun,
			AutoVerify:        in.AutoVerify,
			VerifySuites:      in.VerifySuites,
			Detail:            in.Detail,
		})
	},
	tool.WithShortDescription("Replace a Go type definition and migrate every usage site project-wide"),
)
