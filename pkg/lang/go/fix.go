// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"
	"fmt"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/fs"
	"go.thesmos.sh/techne/pkg/lang"
)

// Fix is the lang.go.fix tool. It runs lang.go.verify to surface lint
// issues with SuggestedPatches, applies those patches through
// lang.go.patch (which has the build-gate and atomic-rollback
// machinery), and then re-verifies so the agent sees the post-patch
// state.
//
// This is the canonical lint-and-fix workflow. The combined call
// collapses what would otherwise be a three-turn loop (verify, decide
// what to patch, re-verify) into one tool invocation. Critically,
// the patch application routes through lang.go.patch — patches that
// break the build are rolled back atomically and the agent never sees
// corrupted source.
//
// Use after a refactor when a quick lint-clean is needed, or after the
// first verify of a session when an agent wants "fix what is safely
// fixable and tell me what is left". Pass DryRun=true to preview
// edits without writing them.
var Fix = tool.New[lang.FixInput, lang.FixOutput](
	"lang.go.fix",
	"Lint, apply suggested fix patches, and re-verify in one call. Replaces the verify→patch→verify chain — typically saves 2 turns. Patches go through lang.go.patch (build-gated, rollback-on-failure); the agent never sees corrupted source. Use after a refactor or when a quick lint-clean is needed.",
	fixHandler,
)

// fixHandler implements the lang.go.fix RPC as the three-phase
// workflow:
//
//  1. Initial verify with Detail=Standard so SuggestedPatches are
//     populated (the lint runners gate patch generation on this flag).
//  2. Convert each SuggestedPatch into an fs.FilePatch and submit the
//     batch to goPatchHandler. The patch tool's per-file vs atomic-
//     batch dispatch picks the right transaction model automatically.
//  3. Re-verify so the caller sees the post-fix status — skipped when
//     no patches were applied (input was already clean) or on dry-run.
//
// Each phase's output is attached to the FixOutput regardless of
// success so the agent can inspect partial work after a failure. Wraps
// errors with the phase name (initial verify / apply patches /
// re-verify) so the failure mode is immediately clear from the error
// message.
func fixHandler(ctx context.Context, input lang.FixInput) (lang.FixOutput, error) {
	out := lang.FixOutput{}

	// 1. Run verify with suggested patches enabled (default Detail does this).
	verifyOut, err := verifyHandler(ctx, lang.VerifyInput{
		Suites:           defaultSuites(input.Suites),
		Targets:          input.Targets,
		MaxIssuesPerType: input.MaxIssuesPerType,
		FailFast:         input.FailFast,
		CompareTo:        input.CompareTo,
		Detail:           lang.DetailStandard,
	})
	if err != nil {
		out.InitialVerify = &verifyOut
		return out, fmt.Errorf("initial verify: %w", err)
	}
	out.InitialVerify = &verifyOut

	// 2. No patches → nothing to do, return as-is.
	if len(verifyOut.SuggestedPatches) == 0 {
		return out, nil
	}

	// 3. Convert SuggestedPatches → fs.FilePatch and apply via lang.go.patch.
	patches := make([]fs.FilePatch, 0, len(verifyOut.SuggestedPatches))
	for _, p := range verifyOut.SuggestedPatches {
		patches = append(patches, fs.FilePatch{
			FilePath: p.FilePath,
			Edits:    p.Edits,
		})
	}
	patchOut, err := goPatchHandler(ctx, GoPatchInput{
		Patches: patches,
		DryRun:  input.DryRun,
	})
	if err != nil {
		out.PatchOutput = &patchOut
		return out, fmt.Errorf("apply patches: %w", err)
	}
	out.PatchOutput = &patchOut
	out.Applied = patchOut.Summary.Applied
	out.Failed = patchOut.Summary.Failed

	// 4. Re-verify (skipped on dry-run or if no patches were actually applied).
	if input.DryRun || patchOut.Summary.Applied == 0 {
		return out, nil
	}
	finalVerify, err := verifyHandler(ctx, lang.VerifyInput{
		Suites:           defaultSuites(input.Suites),
		Targets:          input.Targets,
		MaxIssuesPerType: input.MaxIssuesPerType,
		FailFast:         input.FailFast,
		Detail:           lang.DetailStandard,
	})
	if err != nil {
		out.FinalVerify = &finalVerify
		return out, fmt.Errorf("re-verify: %w", err)
	}
	out.FinalVerify = &finalVerify
	return out, nil
}

// defaultSuites returns [lang.SuiteLint] when the caller does not
// specify suites — lint is what lang.go.fix is for, and most patch
// generators (formatters, errcheck, unused) only run as part of the
// lint suite. An agent that wants test-driven fixes typically calls
// verify directly.
func defaultSuites(s []string) []string {
	if len(s) == 0 {
		return []string{lang.SuiteLint}
	}
	return s
}
