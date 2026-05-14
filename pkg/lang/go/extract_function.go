// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// ExtractFunction is the lang.go.extract_function tool. It extracts a
// contiguous range of statements out of an enclosing Go function and
// replaces the range with a call to the newly-created function. The
// type checker infers the extracted function's parameter list (every
// free variable read inside the range), its return list (every variable
// written inside the range and used after, plus any short-circuit
// returns), and the correct flow for break/continue/return statements
// so the extraction is behaviourally equivalent.
//
// Extraction targets are line-addressed (StartLine, EndLine) in a single
// source file; the range must be a sequence of complete statements
// inside one function body. The extracted function may be written to
// the same file or to a TargetFile in the same package; pass Receiver
// to extract as a method bound to a struct value or pointer, otherwise
// it is generated as a plain function. When the range references
// identifiers from packages not yet imported in the target file,
// goimports adds the missing imports.
//
// Edits stage into a single transaction; the build gate runs go vet +
// go build on the affected packages and rolls every file back on
// failure, so an unextractable range (one containing labelled jumps
// leaving the range, complex defer interactions, or named-return
// shadowing) reports failure without modifying the disk. AutoVerify
// runs lint and optionally tests as diagnostic signals — see
// [runRefactorAction].
//
// Manual extraction silently picks the wrong types for free variables,
// forgets that a value escapes via closure, and routinely misroutes
// return paths when the extracted code contains an early return. The
// type-checker-driven version removes those traps.
//
// The input mirrors [lang.ExtractFunctionInput]; the output is the
// shared [refactor.Output] surfaced by every refactor tool.
var ExtractFunction = tool.New[lang.ExtractFunctionInput, refactor.Output](
	"lang.go.extract_function",
	"PREFER OVER manual Edit when extracting a code range from a Go function. The type checker infers correct parameters and return values; manual extraction silently picks wrong types and forgets references. Optionally bind to a receiver to extract as a method.",
	func(ctx context.Context, in lang.ExtractFunctionInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionExtractFunction,
			File:         in.File,
			StartLine:    in.StartLine,
			EndLine:      in.EndLine,
			NewFuncName:  in.NewFuncName,
			Receiver:     in.Receiver,
			TargetFile:   in.TargetFile,
			Package:      in.Package,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
)
