// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// ExtractInterface is the lang.go.extract_interface tool. It walks the
// exported method set of a target struct (including methods on the
// pointer receiver) and emits an interface that exactly matches that
// method set, writing the declaration to TargetFile in the same package
// or to a sibling package as configured.
//
// Signatures are resolved through go/types so generic type parameters,
// parameters whose types live in third-party packages, and named return
// values are reproduced verbatim. When the target file lives in a
// different package than the struct, the interface methods are
// qualified with the struct's package and missing imports are added
// through goimports.
//
// Edits stage into a single transaction; the build gate runs go vet +
// go build on the affected packages and rolls back on any failure.
// AutoVerify additionally runs lint and optionally tests as diagnostic
// signals (see [runRefactorAction]). The output indicates which file
// received the new declaration along with its godoc header so the agent
// can immediately reference it in subsequent edits.
//
// Manual extraction tends to miss promoted methods from embedded
// structs and reproduces parameter types as raw strings, which break
// silently when the third-party package renames a type. Use this tool
// whenever an interface needs to mirror a concrete struct — for
// dependency injection scaffolding, test doubles, or breaking import
// cycles.
//
// The input mirrors [lang.ExtractInterfaceInput]; the output is the
// shared [refactor.Output] surfaced by every refactor tool.
var ExtractInterface = tool.New[lang.ExtractInterfaceInput, refactor.Output](
	"lang.go.extract_interface",
	"PREFER OVER manual Edit when generating an interface from a Go struct. Walks the struct's exported method set (including pointer-receiver methods), generates correctly-typed signatures via the type checker, and writes to the target file. Manual writing forgets methods or picks wrong types.",
	func(ctx context.Context, in lang.ExtractInterfaceInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:           refactor.ActionExtractInterface,
			TargetStruct:     in.TargetStruct,
			NewInterfaceName: in.NewInterfaceName,
			TargetFile:       in.TargetFile,
			Package:          in.Package,
			DryRun:           in.DryRun,
			AutoVerify:       in.AutoVerify,
			VerifySuites:     in.VerifySuites,
			Detail:           in.Detail,
		})
	},
	tool.WithShortDescription("Generate a Go interface mirroring a struct's exported method set"),
)
