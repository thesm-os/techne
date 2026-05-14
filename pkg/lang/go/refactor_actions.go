// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"
	"path/filepath"

	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

// Refactor tools — one narrow tool per refactoring action. Each has its own
// input schema (defined in pkg/lang) and dispatches to a shared
// refactor.Action strategy. The strategy registry inside pkg/lang/go/refactor
// remains the single implementation of each transformation.

// runRefactorAction is the shared dispatcher behind every public Go
// refactor tool ([Rename], [ChangeSignature], [ChangeType],
// [ImplementInterface], [ExtractFunction], [ExtractInterface],
// [ExtractVariable], [InlineConstant], [Document], [MoveFile],
// [MovePackage], [MoveSymbol], [MoveSymbols], [DeleteFile],
// [AddTests]). Each tool builds a [refactor.Input] populated from its
// own public schema and hands it here; this function is the single
// place that drives the strategy registry, the build gate, the
// auto-verify step, and the detail-mode post-processing.
//
// Lifecycle. The function calls [refactor.Handle], which loads the
// workspace, dispatches on `Input.Action` into the relevant strategy
// (see pkg/lang/go/refactor/strategy.go), stages all edits into a
// transaction, runs the build gate (go vet + go build), and commits
// or rolls back. The transaction guarantee is provided entirely
// there — by the time control returns to this function the disk is
// either in the new state or exactly as it was before. Any error
// from Handle is returned verbatim after [applyDetailMode] trims the
// response for the caller's requested verbosity.
//
// Auto-verify is diagnostic-only. When the caller sets AutoVerify
// the dispatcher runs lang.go.verify on the [affectedPackageDirs] of
// the successful refactor and folds the result into
// `out.VerifyOutput` plus `out.VerificationStatus`. A failed
// verification does NOT roll back the refactor — the build gate has
// already proven the module compiles, and lint or test failures are
// opinionated signals the caller decides how to act on. On verify
// timeout the function emits a follow-up [lang.NextAction] suggesting
// a manual re-run with no time budget; the timeout is bounded by
// [autoVerifyTimeout]. DryRun and Status != Success short-circuit
// the verify step entirely and set VerificationStatus to
// VerificationUnverified.
//
// VerifySuites defaults to lint only — the cheap signal that catches
// the majority of regressions — unless the caller specifies otherwise.
// Targets are scoped to the directories actually touched by the
// refactor (via [affectedPackageDirs]) so verify runs against a small
// slice of the workspace rather than `./...`, which keeps the
// budget realistic.
//
// Detail mode threading. Whatever [refactor.Output] comes back from
// Handle passes through [applyDetailMode] with the caller's
// Detail string, which trims diff snippets on summary and drops
// rolled-back results on overall failure. The trimmed output is what
// the presenter renders.
func runRefactorAction(ctx context.Context, in refactor.Input) (refactor.Output, error) {
	out, err := refactor.Handle(ctx, in)
	if err != nil {
		return applyDetailMode(out, in.Detail), err
	}

	out = applyDetailMode(out, in.Detail)

	if !in.AutoVerify || out.Status != refactor.StatusSuccess || in.DryRun {
		if out.VerificationStatus == "" {
			out.VerificationStatus = VerificationUnverified
		}
		return out, nil
	}

	suites := in.VerifySuites
	if len(suites) == 0 {
		suites = []string{lang.SuiteLint}
	}
	targets := affectedPackageDirs(&out)

	verifyCtx, cancel := context.WithTimeout(ctx, autoVerifyTimeout)
	defer cancel()

	verifyOut, verifyErr := verifyHandler(verifyCtx, lang.VerifyInput{Suites: suites, Targets: targets})
	if verifyErr != nil {
		if verifyCtx.Err() == context.DeadlineExceeded {
			out.VerificationStatus = VerificationTimeout
			out.NextActions = append([]lang.NextAction{{
				Tool:            "lang.go.verify",
				Confidence:      lang.ConfidenceMedium,
				Reason:          "Auto-verify timed out — run manual verification",
				RiskDescription: "The build or lint took longer than 10s. Run full verification manually.",
				Input:           lang.VerifyInput{Targets: targets},
			}}, out.NextActions...)
			return out, nil
		}
		out.VerificationStatus = VerificationUnverified
		return out, nil
	}

	out.VerifyOutput = &verifyOut
	switch verifyOut.OverallStatus {
	case lang.StatusPass:
		out.VerificationStatus = VerificationTestOK
	case lang.StatusLintOK:
		out.VerificationStatus = VerificationLintOK
	default:
		out.VerificationStatus = VerificationDegraded
		out.NextActions = append([]lang.NextAction{{
			Tool:            "lang.go.explore",
			Confidence:      lang.ConfidenceLow,
			Reason:          "Verification found issues after refactor — investigate root cause",
			RiskDescription: "The refactor was applied but verification failed. Inspect verify_output for details.",
		}}, out.NextActions...)
	}
	return out, nil
}

// applyDetailMode post-processes a [refactor.Output] based on the
// caller's requested detail level. Two effects, in order:
//
// Partial-failure surfacing. When the overall Status is
// [refactor.StatusFailure], FileResults whose status is
// [refactor.StatusSuccess] are dropped from Results before returning.
// Those entries report files the strategy successfully edited
// in-memory before something else failed, and the build gate has
// already rolled them back — they are not state on disk, so keeping
// them in the response just bloats the payload and gives the caller a
// misleading view of what changed. Only the failed entries remain,
// verbatim, so the caller can see exactly which file produced the
// error.
//
// Diff-snippet stripping. When Detail equals [lang.DetailSummary],
// the DiffSnippet field on every remaining FileResult is cleared.
// Summary mode is for "did this work?" checks where the agent only
// needs file paths, status, and a one-line message — the per-file
// unified-diff bodies add tens of thousands of tokens that
// standard-mode and full-mode callers actually want. The standard
// (default) and full levels leave diff bodies intact; full mode adds
// further information populated by the strategy itself.
//
// The function is intentionally pure: it takes Output by value and
// returns a new Output, so callers can apply it multiple times
// without double-trimming.
func applyDetailMode(out refactor.Output, detail string) refactor.Output {
	if out.Status == refactor.StatusFailure {
		kept := out.Results[:0]
		for _, r := range out.Results {
			if r.Status != refactor.StatusSuccess {
				kept = append(kept, r)
			}
		}
		out.Results = kept
	}

	if detail == lang.DetailSummary {
		for i := range out.Results {
			out.Results[i].DiffSnippet = ""
		}
	}
	return out
}

// affectedPackageDirs returns the set of package directories actually
// touched by a successful refactor, suitable for use as
// lang.go.verify Targets. The function iterates over the
// [refactor.Output]'s FileResults, keeps the directories of files
// whose status is [refactor.StatusSuccess], deduplicates, and returns
// the slice.
//
// Falling back to `./...` is intentional: if no successful results
// were recorded the safe choice is to verify the whole workspace
// rather than no-op, so a regression cannot slip through because the
// tool failed to attribute it to a directory. In practice the
// fallback only triggers on dry-runs and outright failures, where
// verify is skipped anyway.
func affectedPackageDirs(out *refactor.Output) []string {
	dirs := make(map[string]bool)
	for _, r := range out.Results {
		if r.Status == refactor.StatusSuccess && r.FilePath != "" {
			dirs[filepath.Dir(r.FilePath)] = true
		}
	}
	if len(dirs) == 0 {
		return []string{"./..."}
	}
	targets := make([]string, 0, len(dirs))
	for d := range dirs {
		targets = append(targets, d)
	}
	return targets
}
