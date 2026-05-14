// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// AddTests is the lang.go.add_tests tool. It scaffolds Go test
// functions — top-level `TestXxx` functions and their subtests — in a
// test file, generating boilerplate that is correct, parallel-safe by
// default, and consistent with the project's test conventions.
//
// For each [lang.TestSpec] the tool generates a top-level test function
// with `t.Parallel()` enabled. When the spec lists no subtests, the
// body is a single `t.Skip("TODO: implement")` so the test runs cleanly
// but clearly advertises that it is a stub. When the spec lists
// subtests, the body becomes one `t.Run` per subtest, each with its
// own `t.Parallel()` call and `t.Skip` placeholder so individual
// subtests can be filled in incrementally without disturbing the rest.
// The package name is auto-detected from production `.go` files in the
// same directory and resolves to `<pkg>_test` per project rules.
//
// When the target file does not exist the tool creates it with the
// file header, imports (`testing` plus anything the package needs by
// convention), and the new tests. When the file exists the new
// functions are inserted after the function named in the After field
// or appended at the end. Edits stage into a single transaction with
// the standard build gate; the gate catches any package-name mismatch
// or stale import. AutoVerify runs lint and optionally tests as
// diagnostic signals — see [runRefactorAction].
//
// Prefer this over hand-typing test scaffolds. The boilerplate is
// repetitive and easy to get subtly wrong: omit `t.Parallel()` and the
// test grid serializes, forget `t.Skip` and incomplete bodies look
// like passing tests, fumble the package suffix and the public-API
// rule from the project test conventions silently flips to internal
// testing. The tool removes those failure modes.
//
// The input mirrors [lang.AddTestsInput]; the output is the shared
// [refactor.Output] surfaced by every refactor tool.
var AddTests = tool.New[lang.AddTestsInput, refactor.Output](
	"lang.go.add_tests",
	"Bootstraps Go test scaffolding in a test file. Creates the file if it doesn't exist; appends or inserts after an existing function if it does. Each generated function has t.Parallel() enabled. Functions with no subtests get a t.Skip() stub; functions with subtests get one t.Run per subtest, each with t.Parallel() and t.Skip(). The package name is auto-detected from the directory's production .go files.",
	func(ctx context.Context, in lang.AddTestsInput) (refactor.Output, error) {
		return runRefactorAction(ctx, refactor.Input{
			Action:       refactor.ActionAddTests,
			TargetFile:   in.File,
			Tests:        convertTestSpecs(in.Tests),
			After:        in.After,
			Package:      in.Package,
			DryRun:       in.DryRun,
			AutoVerify:   in.AutoVerify,
			VerifySuites: in.VerifySuites,
			Detail:       in.Detail,
		})
	},
)

// convertTestSpecs maps the public [lang.TestSpec] schema onto the
// internal [refactor.TestSpec] type. The two packages keep parallel
// types so the wire-facing JSON contract (consumed by MCP, CLI, and
// TUI agents) stays decoupled from the engine; each side can evolve
// independently.
func convertTestSpecs(in []lang.TestSpec) []refactor.TestSpec {
	out := make([]refactor.TestSpec, len(in))
	for i, s := range in {
		out[i] = refactor.TestSpec{Name: s.Name, Subtests: s.Subtests}
	}
	return out
}
