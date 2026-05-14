// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.thesmos.sh/techne/internal/tool"
	"go.thesmos.sh/techne/pkg/fs"
	"go.thesmos.sh/techne/pkg/lang"
	"go.thesmos.sh/techne/pkg/lang/go/internal/workspace"
)

// Verify is the lang.go.verify tool. It runs the requested verification
// suites (lint, test, bench, fuzz) against the workspace and returns a
// structured per-suite report — status, issues, metrics, and (for lint)
// ready-to-apply SuggestedPatches that feed directly into
// lang.go.patch.
//
// Prefer this over chains of `go test`, `go vet`, `go build`, and
// `golangci-lint`. A single call drives every runner in the engine and
// aggregates the output into one OverallStatus. The verify→fix→re-
// verify workflow drops from five turns (run lint, parse output, hand-
// edit, run tests, parse output) to two (verify, fix) when used with
// lang.go.fix.
//
// Defaults are tuned for the zero-arg call: Suites defaults to [lint,
// test], Targets to ./... in single-module mode or every use directive
// in go.work mode. CompareTo narrows targets to packages affected by
// the diff against a git ref via blast_radius, turning whole-module
// verification (~10s) into affected-only verification (sub-100ms).
//
// The NextActions field is populated by buildNextActions and pre-fills
// lang.go.patch inputs for formatting and lint auto-fixes — the agent
// can apply them in a single follow-up turn. Workspace-aware: handles
// go.work transparently and expands ./... per use directive.
var Verify = tool.New[lang.VerifyInput, lang.VerifyOutput](
	"lang.go.verify",
	"PREFER OVER Bash chains of `go test`/`go vet`/`go build`/`golangci-lint`. One call runs the requested suites and returns structured per-suite reports (status, issues, metrics) plus pre-filled SuggestedPatches that feed directly into lang.go.patch — chaining a lint→fix→re-verify workflow drops from 5 turns to 2. Workspace-aware (handles go.work). Pass compare_to=<git ref> to narrow targets to packages affected by the diff.",
	verifyHandler,
	tool.Enum("suites", lang.SuiteLint, lang.SuiteTest, lang.SuiteBench, lang.SuiteFuzz),
)

// defaultVerifyTargets returns the package patterns to use when the
// agent passes no Targets. In single-module mode this is just
// ["./..."]. In go.work mode, "./..." from a workspace root matches
// nothing because the workspace root has no go.mod — the patterns are
// expanded to one per use directive so every module is covered.
//
// Gracefully degrades to ["./..."] on any error from getwd or
// workspace.Discover; the worst-case is that the agent gets an empty
// result set in a go.work setup, which is preferable to crashing.
func defaultVerifyTargets() []string {
	cwd, err := os.Getwd()
	if err != nil {
		return []string{"./..."}
	}
	w, err := workspace.Discover(cwd)
	if err != nil || !w.IsGoWork() {
		return []string{"./..."}
	}
	var targets []string
	for _, m := range w.Modules() {
		rel, relErr := filepath.Rel(w.Root(), m.Dir)
		if relErr != nil || rel == "." {
			targets = append(targets, "./...")
			continue
		}
		targets = append(targets, "./"+filepath.ToSlash(rel)+"/...")
	}
	if len(targets) == 0 {
		return []string{"./..."}
	}
	return targets
}

// verifyHandler implements the lang.go.verify RPC. Applies high-impact
// defaults (Suites=[lint,test], Targets via defaultVerifyTargets),
// optionally narrows targets via narrowTargetsFromDiff when CompareTo
// is set, then executes the suite engine and post-processes its output
// to populate NextActions and the scoped OverallStatus.
//
// The diff-narrowing path is transparent: when targets are narrowed, a
// human-readable hint is appended to BloatPrevention.Hint and the
// resolved targets are exposed via ResolvedTargets so the agent can
// verify what was actually checked. When OverallStatus is
// lang.StatusPass but only lint was run, the status is upgraded to
// StatusLintOK so the agent does not mistake a lint-only run for a
// fully test-verified pass.
func verifyHandler(ctx context.Context, input lang.VerifyInput) (lang.VerifyOutput, error) {
	// Optimization 1: high-impact defaults — agent can call verify() with zero args.
	if len(input.Suites) == 0 {
		input.Suites = []string{lang.SuiteLint, lang.SuiteTest}
	}
	if len(input.Targets) == 0 {
		input.Targets = defaultVerifyTargets()
	}

	// Diff-isolated verification: when CompareTo is set, narrow targets to only
	// the packages affected by changes since that git ref. This is O(affected
	// packages) instead of O(all packages) — sub-100ms instead of 10 seconds.
	var diffNarrowHint string
	if input.CompareTo != "" {
		narrowed, err := narrowTargetsFromDiff(ctx, input.CompareTo, input)
		if err == nil && narrowed != nil {
			input.Targets = narrowed.Targets
			if input.Focus == "" && narrowed.Focus != "" {
				input.Focus = narrowed.Focus
			}
			diffNarrowHint = fmt.Sprintf(
				"Targets narrowed to %d affected package(s) via blast_radius diff against %s: %s",
				len(narrowed.Targets),
				input.CompareTo,
				strings.Join(narrowed.Targets, ", "),
			)
		}
	}

	engine := lang.NewEngine(
		&lintRunner{},
		&testRunner{},
		&benchRunner{},
		&fuzzRunner{},
	)

	out, err := engine.Execute(ctx, input)
	if err != nil {
		return out, err
	}

	// Append diff-narrowing context to BloatPrevention.Hint so agents know
	// that targets were pre-filtered by blast_radius.
	if diffNarrowHint != "" {
		out.BloatPrevention.Hint = diffNarrowHint + ". " + out.BloatPrevention.Hint
	}

	// AutoFix was dropped from VerifyInput. Agents that want the
	// verify→patch→re-verify chain should call lang.go.fix instead.

	// Add NextActions — the Engine deliberately leaves these empty;
	// it is the language-specific tool's responsibility to populate them.
	out.NextActions = buildNextActions(out)

	// Status scoping: if only lint was run and it passed, use "lint_ok"
	// to distinguish from a full test-verified "pass".
	if out.OverallStatus == lang.StatusPass {
		lintOnly := len(input.Suites) == 1 && input.Suites[0] == lang.SuiteLint
		if lintOnly {
			out.OverallStatus = lang.StatusLintOK
		}
	}

	// Strategy transparency: when diff-narrowing was used, expose the resolved targets.
	if diffNarrowHint != "" {
		out.ResolvedTargets = input.Targets
	}

	return out, nil
}

// narrowResult holds the narrowed targets and optional focus filter
// returned by narrowTargetsFromDiff. Focus is a "|"-joined list of
// critical test names ready to be passed as the go test -run pattern.
type narrowResult struct {
	Targets []string
	Focus   string
}

// narrowTargetsFromDiff uses git diff and blastRadiusHandler to narrow
// verification to only the packages affected by changes since the
// given git ref. This replaces the "run ./... and filter output"
// pattern with "run only affected packages" — typically three to five
// packages instead of the whole module.
//
// Edge cases:
//   - git not available or diff fails: returns (nil, err) so the caller
//     falls through to its original targets.
//   - no files changed: returns (nil, nil); the caller should
//     short-circuit to StatusPass.
//   - blast_radius returns no affected packages: returns (nil, nil);
//     fall through.
//   - caller already specified explicit Targets: the intersection of
//     caller-Targets and affected-packages is used so an agent's narrowing
//     intent is honoured but cannot accidentally drop everything
//     (empty intersection falls through to the caller's original list).
//
// Spawns `git diff --name-only <ref>` as a subprocess and honors
// context cancellation via exec.CommandContext.
func narrowTargetsFromDiff(ctx context.Context, compareTo string, input lang.VerifyInput) (*narrowResult, error) {
	// Run git diff to get changed files since compareTo.
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", compareTo)
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s: %w", compareTo, err)
	}

	// Parse the output into a list of file paths.
	var changedFiles []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if f := strings.TrimSpace(line); f != "" {
			changedFiles = append(changedFiles, f)
		}
	}
	if len(changedFiles) == 0 {
		// Nothing changed — nothing to verify.
		return nil, nil
	}

	// Delegate to blast_radius to determine affected packages and critical tests.
	brOut, err := blastRadiusHandler(ctx, lang.BlastRadiusInput{
		Files:             changedFiles,
		IncludeTransitive: true,
		MaxDepth:          3,
	})
	if err != nil {
		return nil, fmt.Errorf("blast_radius: %w", err)
	}
	if len(brOut.AffectedPackages) == 0 {
		return nil, nil
	}

	targets := brOut.AffectedPackages

	// If the caller already specified explicit targets (not the default ./...),
	// intersect: only verify packages that are both affected AND in their list.
	callerHasExplicitTargets := len(input.Targets) > 0 &&
		(len(input.Targets) != 1 || input.Targets[0] != "./...")
	if callerHasExplicitTargets {
		callerSet := make(map[string]bool, len(input.Targets))
		for _, t := range input.Targets {
			callerSet[t] = true
		}
		var intersection []string
		for _, t := range targets {
			if callerSet[t] {
				intersection = append(intersection, t)
			}
		}
		// If the intersection is empty, fall through to the caller's original
		// targets — don't silently drop everything.
		if len(intersection) == 0 {
			return nil, nil
		}
		targets = intersection
	}

	result := &narrowResult{
		Targets: targets,
		Focus:   strings.Join(brOut.CriticalTests, "|"),
	}
	return result, nil
}

// buildNextActions inspects the verify output and recommends follow-up
// tool calls with confidence labels suitable for direct execution by
// the agent.
//
// The NextActions cover three scenarios:
//   - Formatting patches (gofmt, goimports): pre-filled as a
//     lang.go.patch input with ConfidenceDeterministic — these never
//     change semantics and are safe to apply without review.
//   - Lint auto-fix patches (everything else from golangci-lint):
//     pre-filled as a lang.go.patch input with ConfidenceHigh — review
//     recommended but the patches come with golangci-lint's blessing.
//   - Lint failure with no auto-fix: suggests an AST-style patch
//     (action: inject_error_check) at ConfidenceMedium and warns that
//     AST edits modify code structure.
//   - Test failure: suggests lang.go.explore on the first failing
//     symbol at ConfidenceLow with a risk note about treating tests as
//     symptoms not root causes.
//
// Results are capped via lang.CapNextActions; passing runs skip the
// failure-mode actions entirely.
func buildNextActions(out lang.VerifyOutput) []lang.NextAction {
	var actions []lang.NextAction

	// Partition suggested patches into formatting (gofmt/goimports) and lint categories.
	var formattingPatches, lintPatches []lang.SuggestedPatch
	for _, p := range out.SuggestedPatches {
		switch p.Source {
		case "gofmt", "goimports", "formatter":
			formattingPatches = append(formattingPatches, p)
		default:
			lintPatches = append(lintPatches, p)
		}
	}

	// V1: formatting patches — deterministically safe, pre-filled as lang.go.patch input.
	if len(formattingPatches) > 0 {
		filePatches := make([]fs.FilePatch, 0, len(formattingPatches))
		for _, p := range formattingPatches {
			filePatches = append(filePatches, fs.FilePatch{
				FilePath: p.FilePath,
				Edits:    p.Edits,
			})
		}
		actions = append(actions, lang.NextAction{
			Tool:       "lang.go.patch",
			Reason:     "Apply formatting fixes (gofmt/goimports) — deterministically safe",
			Confidence: lang.ConfidenceDeterministic,
			Input: GoPatchInput{
				Patches: filePatches,
			},
		})
	}

	// V2: lint auto-fix patches — high confidence, pre-filled as lang.go.patch input.
	if len(lintPatches) > 0 {
		filePatches := make([]fs.FilePatch, 0, len(lintPatches))
		for _, p := range lintPatches {
			filePatches = append(filePatches, fs.FilePatch{
				FilePath: p.FilePath,
				Edits:    p.Edits,
			})
		}
		actions = append(actions, lang.NextAction{
			Tool:       "lang.go.patch",
			Reason:     "Apply lint auto-fixes from golangci-lint",
			Confidence: lang.ConfidenceHigh,
			Input: GoPatchInput{
				Patches: filePatches,
			},
		})
	}

	// When lint failed but no suggested patches exist, advise manual AST edits.
	lintReport, hasLint := out.Reports[lang.SuiteLint]
	if hasLint && (lintReport.Status == lang.StatusFail || lintReport.Status == lang.StatusError) &&
		len(out.SuggestedPatches) == 0 {
		actions = append(actions, lang.NextAction{
			Tool:            "lang.go.patch",
			Confidence:      lang.ConfidenceMedium,
			Reason:          "Lint issues found with no auto-fix available. Use AST edits (action: inject_error_check) for errcheck remediation.",
			RiskDescription: "AST edits modify code structure — review the generated patches before applying.",
		})
	}

	// If there are test failures, suggest exploration.
	testReport, hasTest := out.Reports[lang.SuiteTest]
	if hasTest && (testReport.Status == lang.StatusFail || testReport.Status == lang.StatusError) {
		var failingSymbol string
		if len(testReport.Issues) > 0 {
			failingSymbol = testReport.Issues[0].SymbolName
		}

		reason := "Explore failing symbol to understand the root cause"
		if failingSymbol != "" {
			reason = "Explore " + failingSymbol + " to understand the failure"
		}

		actions = append(actions, lang.NextAction{
			Tool:            "lang.go.explore",
			Reason:          reason,
			Confidence:      lang.ConfidenceLow,
			RiskDescription: "Test failure may be a symptom of a deeper design issue — explore before patching",
		})
	}

	isPass := out.OverallStatus == lang.StatusPass || out.OverallStatus == lang.StatusLintOK
	return lang.CapNextActions(actions, isPass)
}
