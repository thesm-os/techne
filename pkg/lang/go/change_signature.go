// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// ChangeSignature is the lang.go.change_signature tool. It modifies a
// Go function or method signature — adding parameters, adding return
// values, or removing parameters — and rewrites every call site
// workspace-wide so the build stays green throughout.
//
// For each added parameter the caller supplies a DefaultValue expression
// that is spliced into every call site as the new argument; for each
// added return value the tool inserts a matching zero/blank receiver at
// callers that discard returns and a named binding at callers that
// already destructure. Removed parameters are deleted from the
// signature AND stripped from every call site argument list. Type
// resolution is done through go/types: method dispatches via interfaces,
// embedded promotion, and function-typed values are all updated
// correctly without the false positives a regex rewrite would produce.
//
// The edits stage into a single transaction; the build gate runs go vet
// + go build on the affected packages after staging, and any failure
// rolls every file back to its original content. Pre-edit failures
// (symbol not found, unresolved type in DefaultValue, etc.) are returned
// as [refactor.StatusFailure] without touching the disk. There is no
// generic alternative — gopls cannot perform cross-file signature
// changes and Edit can only mutate one file at a time, so any manual
// attempt breaks every caller as its first step.
//
// What is NOT updated: argument lists hidden inside reflect.Call,
// function-typed values passed around as values where the signature is
// implicit (e.g., a callback stored in a struct), and string literals
// that reproduce the signature for documentation. Audit those manually
// after a major signature change.
//
// The input mirrors [lang.ChangeSignatureInput]; the output is the
// shared [refactor.Output] surfaced by every refactor tool.
var ChangeSignature = tool.New[lang.ChangeSignatureInput, refactor.Output](
	"lang.go.change_signature",
	"PREFER OVER manual Edit when changing a Go function signature. One call adds/removes parameters and return values AND rewrites every call site with the supplied default values. Edit alone changes the signature and immediately breaks every caller; this tool keeps the build green. No generic alternative — gopls cannot do cross-file signature changes.",
	func(ctx context.Context, in lang.ChangeSignatureInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionChangeSignature,
			Symbol:       in.Symbol,
			AddParams:    convertAddParams(in.AddParams),
			AddReturns:   convertAddReturns(in.AddReturns),
			RemoveParams: in.RemoveParams,
			Package:      in.Package,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
)

// convertAddParams maps the public [lang.AddParameter] schema onto the
// internal [refactor.AddParameter] type. The two packages keep parallel
// types so the public tool schema (consumed by MCP, CLI, and TUI agents)
// stays decoupled from the internal refactor engine — the engine can
// evolve its own struct shape without breaking external JSON contracts,
// and the public schema can carry presenter-specific tags without
// polluting the engine.
func convertAddParams(in []lang.AddParameter) []refactor.AddParameter {
	out := make([]refactor.AddParameter, len(in))
	for i, p := range in {
		out[i] = refactor.AddParameter{Name: p.Name, Type: p.Type, DefaultValue: p.DefaultValue}
	}
	return out
}

// convertAddReturns maps [lang.AddReturn] entries onto the internal
// [refactor.AddReturn] type. Same rationale as [convertAddParams]:
// parallel types keep the wire-facing schema decoupled from the engine.
func convertAddReturns(in []lang.AddReturn) []refactor.AddReturn {
	out := make([]refactor.AddReturn, len(in))
	for i, r := range in {
		out[i] = refactor.AddReturn{Type: r.Type, DefaultValue: r.DefaultValue}
	}
	return out
}
