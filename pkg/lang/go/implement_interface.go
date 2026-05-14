// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// ImplementInterface is the lang.go.implement_interface tool. It
// generates Go method stubs on a target struct so that the struct
// satisfies a given interface, resolving every signature through the
// type checker so parameter names, parameter types, return types, and
// required imports are all correct.
//
// The interface may live in the same package, a different module-local
// package, a workspace sibling, or the standard library. Embedded
// interfaces are flattened — every transitively-required method gets a
// stub. Methods the struct already implements are skipped, so the tool
// is idempotent: re-running after partial implementation only generates
// the remaining stubs. By default each stub body is
// panic("not implemented"); pass StubBody to substitute a different
// template (for example return a zero value, or call into a wrapped
// implementation).
//
// The new methods are appended to the file containing the struct
// definition; missing imports are added through goimports. Edits stage
// into a transaction and the build gate confirms the struct now
// satisfies the interface before committing — any failure rolls back.
// AutoVerify additionally runs lint and optionally tests as diagnostic
// signals (see [runRefactorAction]).
//
// Manual writing of stubs silently picks wrong types when an interface
// uses unexported parameter names, generic type parameters, or types
// from an unfamiliar package, and it is easy to forget receiver pointer
// semantics. This tool removes that class of error: the stubs match the
// interface verbatim or the call fails.
//
// The input mirrors [lang.ImplementInterfaceInput]; the output is the
// shared [refactor.Output] surfaced by every refactor tool.
var ImplementInterface = tool.New[lang.ImplementInterfaceInput, refactor.Output](
	"lang.go.implement_interface",
	"PREFER OVER manual Edit when generating method stubs for a Go interface. One call generates correctly-typed stubs (with imports, parameter names, and return types resolved via the type checker) for every missing method; manual writing silently picks wrong types or forgets receivers. Supports local, cross-package, and stdlib interfaces. Stubs default to panic(\"not implemented\"); customize via stub_body.",
	func(ctx context.Context, in lang.ImplementInterfaceInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionImplementInterface,
			TargetStruct: in.TargetStruct,
			Interface:    in.Interface,
			StubBody:     in.StubBody,
			Package:      in.Package,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
)
