// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package lang

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const (
	defaultMaxIssuesPerType = 5
	logBaseDir              = "/tmp/techne-reports"
)

// SuiteRunner is the plug-in interface implemented by every verification
// suite (lint, test, bench, fuzz) the [Engine] orchestrates. Each runner
// encapsulates the toolchain invocation, output parsing, and patch
// synthesis for one quality gate.
//
// Name returns the canonical suite identifier — one of [SuiteLint],
// [SuiteTest], [SuiteBench], or [SuiteFuzz] — that callers reference in
// VerifyInput.Suites and that the engine uses as a map key. Names must be
// stable across versions: agents persist suite names in long-running
// workflows and any change is observable as a behaviour break.
//
// Run executes the suite and returns a [SuiteReport] describing what was
// found, plus a flat slice of [SuggestedPatch] entries the engine
// aggregates into VerifyOutput.SuggestedPatches. Implementations must:
//
//   - Honour ctx cancellation (long-running tests/benches will be killed
//     when the agent's turn deadline expires).
//   - Write raw tool output under logDir — the engine reports that path
//     to callers via [BloatPrevention] so the agent can fetch verbose
//     output without inflating context.
//   - Return a non-nil error only for infrastructure failures (toolchain
//     missing, syntax error in flags). Lint findings or test failures must
//     be reported via SuiteReport.Status = [StatusFail] with a nil error
//     so the engine can keep running remaining suites.
//
// Implementations are not expected to be safe for concurrent use; the
// engine invokes each runner sequentially.
type SuiteRunner interface {
	Name() string
	Run(ctx context.Context, input VerifyInput, logDir string) (SuiteReport, []SuggestedPatch, error)
}

// Engine is the verify-tool dispatcher: it owns a registry of
// [SuiteRunner]s keyed by name and drives them in the order requested by
// an agent's VerifyInput.Suites.
//
// The Engine is the language-agnostic core of the lang.<lang>.verify
// family. Each language package (lang/go, lang/py, ...) constructs its own
// Engine with the runners appropriate to that toolchain — gopls + golangci
// for Go, ruff + pytest for Python, and so on — then exposes a thin
// adapter that turns LSP-style or CLI-style verify requests into
// [Engine.Execute] calls.
//
// An Engine is immutable after construction: the runners map is populated
// by [NewEngine] and never written to again, so a single Engine can be
// shared across goroutines and reused for the lifetime of the process.
// Concurrent calls to [Engine.Execute] are safe but each call performs its
// own filesystem I/O (log directory creation), so callers should not
// assume zero side effects.
type Engine struct {
	// runners maps a suite name (e.g. "lint", "test") to the [SuiteRunner]
	// that handles it. The map is populated once by [NewEngine] and never
	// mutated afterwards, which is the basis for [Engine]'s concurrent-safe
	// dispatch guarantee.
	runners map[string]SuiteRunner
}

// NewEngine constructs an [Engine] from the given runners and returns a
// pointer ready for immediate use.
//
// Runners are keyed by their Name() value. If two runners report the same
// name the later registration silently wins — this is the intended
// override hook for tests that want to swap in a fake runner for a
// specific suite without touching the production wiring. The result
// should be treated as immutable; the engine does not expose a way to
// register additional runners post-construction.
//
// Passing zero runners is legal but produces an Engine that silently
// skips every requested suite (see [Engine.Execute]). Callers usually
// catch this at startup by registering at least the lint and test suites.
func NewEngine(runners ...SuiteRunner) *Engine {
	m := make(map[string]SuiteRunner, len(runners))
	for _, r := range runners {
		m[r.Name()] = r
	}
	return &Engine{runners: m}
}

// Execute runs each suite listed in input.Suites in the order given and
// assembles a single [VerifyOutput] from the per-suite results.
//
// The call performs the following side effects, in order:
//
//  1. Allocates a fresh random log directory under /tmp/techne-reports/
//     (see [generateLogID]). The path is reported back to the caller via
//     VerifyOutput.BloatPrevention so agents can stream raw output
//     without inflating their context.
//  2. Invokes each registered [SuiteRunner] sequentially with the shared
//     log directory. Unknown suite names in input.Suites are silently
//     skipped — this preserves forward compatibility when an agent asks
//     for a suite the engine version doesn't know about.
//  3. Truncates each report's Issues slice to at most
//     input.MaxIssuesPerType entries (or the default of 5 when unset).
//  4. Runs [clusterIssues] over the truncated set to collapse repeated
//     diagnostics into [IssueCluster] groups.
//  5. Rolls up the overall status: any failing or erroring suite degrades
//     OverallStatus from [StatusPass] to [StatusDegraded]. When
//     input.FailFast is set, the loop short-circuits on the first
//     non-pass suite.
//  6. Strips per-issue detail when [ResolveDetail](input.Detail) reports
//     Issues=false (summary mode) — counts and status remain so callers
//     can decide whether a follow-up drill-in is needed.
//
// Execute returns a non-nil error only for infrastructure failures (log
// directory creation, log ID generation). Per-suite tool errors are
// folded into the corresponding [SuiteReport] with Status=[StatusError]
// and a non-nil VerifyOutput is still returned. Callers should always
// inspect VerifyOutput.OverallStatus rather than relying on the error
// value alone.
//
// Execute does not populate VerifyOutput.NextActions; that is the
// responsibility of the language-specific verify tool wrapper, which
// knows the correct tool name (e.g. "lang.go.explore" vs
// "lang.rust.explore") to suggest. Execute is safe for concurrent use as
// long as the underlying runners are.
func (e *Engine) Execute(ctx context.Context, input VerifyInput) (VerifyOutput, error) {
	// 1. Generate random log ID.
	logID, err := generateLogID()
	if err != nil {
		return VerifyOutput{}, fmt.Errorf("engine: generate log ID: %w", err)
	}

	// 2. Create log directory.
	logDir := filepath.Join(logBaseDir, logID)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return VerifyOutput{}, fmt.Errorf("engine: create log dir %q: %w", logDir, err)
	}

	maxIssues := input.MaxIssuesPerType
	if maxIssues <= 0 {
		maxIssues = defaultMaxIssuesPerType
	}

	reports := make(map[string]SuiteReport, len(input.Suites))
	var allPatches []SuggestedPatch
	overallStatus := StatusPass

	// 3. Iterate requested suites.
	for _, suite := range input.Suites {
		runner, ok := e.runners[suite]
		if !ok {
			// Silently skip unknown suites.
			continue
		}

		report, patches, runErr := runner.Run(ctx, input, logDir)
		if runErr != nil {
			report.Status = StatusError
			if report.Summary == "" {
				report.Summary = runErr.Error()
			}
		}

		// 5. Truncate issues to MaxIssuesPerType.
		if len(report.Issues) > maxIssues {
			report.Issues = report.Issues[:maxIssues]
		}

		// 5b. Cluster repeated issues (same linter+message, 3+ occurrences).
		report = clusterIssues(report)

		reports[suite] = report
		allPatches = append(allPatches, patches...)

		// 6. Roll up overall status.
		if report.Status == StatusFail || report.Status == StatusError {
			overallStatus = StatusDegraded
		}

		// 7. Respect FailFast.
		if input.FailFast && (report.Status == StatusFail || report.Status == StatusError) {
			break
		}
	}

	// 8. Set BloatPrevention.
	bloat := BloatPrevention{
		LogID: logID,
		Hint:  fmt.Sprintf("Full logs written to %s. Use log_id to retrieve details.", logDir),
	}

	// NextActions is left empty — the language-specific verify tool adds
	// actions with correct tool names (e.g. "lang.go.explore" vs "lang.rust.explore").
	out := VerifyOutput{
		OverallStatus:    overallStatus,
		Reports:          reports,
		SuggestedPatches: allPatches,
		BloatPrevention:  bloat,
	}

	// Detail=summary strips per-issue lists across all reports — counts and
	// status remain so the agent can decide whether to drill in.
	if !ResolveDetail(input.Detail).Issues {
		for name, r := range out.Reports {
			r.Issues = nil
			r.Clusters = nil
			out.Reports[name] = r
		}
		out.SuggestedPatches = nil
	}

	return out, nil
}

// generateLogID returns a fresh log identifier of the form
// "et-<8 hex characters>" suitable for use as a per-run subdirectory name
// under /tmp/techne-reports/.
//
// The 32-bit random suffix gives roughly 4 billion distinct IDs, which is
// far more than enough to avoid collisions within a single agent session.
// The "et-" prefix is a stable marker so operators can identify report
// directories on disk by name.
//
// Returns an error only when crypto/rand cannot read from the system
// entropy source — effectively unreachable on supported platforms but
// propagated rather than panicked so the verify call can surface the
// failure to the agent instead of crashing the process.
func generateLogID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "et-" + hex.EncodeToString(b), nil
}

// clusterIssues groups [Issue] entries in a [SuiteReport] that share the
// same (Linter, Message) pair. Groups with three or more members are
// collapsed into [IssueCluster] values and removed from the Issues slice;
// smaller groups are flattened back into Issues unchanged.
//
// This function exists because lint output frequently includes dozens of
// sites of the same defect (e.g. "err113: do not define dynamic errors"
// flagged on every fmt.Errorf in the codebase). Returning all of them
// inflates the agent's context and rarely helps — three representative
// examples plus a count convey the same information at a fraction of the
// token cost.
//
// Grouping iteration order matches the order in which each (Linter,
// Message) pair was first encountered, so the returned report is
// deterministic given a deterministic input. The original Issues slice is
// not mutated; clusterIssues returns a new SuiteReport value with fresh
// Issues and Clusters slices.
//
// The three-occurrence threshold is hard-coded — issues below it are
// considered varied enough to surface individually. The example cap is
// also hard-coded at three; agents that need more occurrences should
// fetch the raw log via [BloatPrevention].
func clusterIssues(report SuiteReport) SuiteReport {
	type key struct{ linter, message string }

	order := []key{}
	groups := map[key][]Issue{}

	for _, issue := range report.Issues {
		k := key{issue.Linter, issue.Message}
		if _, exists := groups[k]; !exists {
			order = append(order, k)
		}
		groups[k] = append(groups[k], issue)
	}

	var unique []Issue
	var clusters []IssueCluster

	for _, k := range order {
		g := groups[k]
		if len(g) >= 3 {
			examples := g
			if len(examples) > 3 {
				examples = examples[:3]
			}
			clusters = append(clusters, IssueCluster{
				Pattern:  k.message,
				Linter:   k.linter,
				Count:    len(g),
				Examples: examples,
			})
		} else {
			unique = append(unique, g...)
		}
	}

	report.Issues = unique
	report.Clusters = clusters
	return report
}
