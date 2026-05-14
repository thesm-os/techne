// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.thesmos.sh/techne/pkg/lang"
)

// resolveLintPath turns a golangci-lint reported filename into an
// absolute path. golangci-lint reports filenames relative to its own
// working directory, which is fine for display but breaks any
// downstream consumer (synthetic patch generators, lang.go.patch) that
// performs os.ReadFile against the agent's CWD.
//
// Resolving to absolute up-front decouples the lint runner's CWD from
// the agent's CWD, so a verify in module A can still produce patches
// that lang.go.patch (running with module B's CWD) can apply. Absolute
// paths and empty inputs pass through unchanged.
func resolveLintPath(workDir, file string) string {
	if file == "" || filepath.IsAbs(file) {
		return file
	}
	if workDir == "" {
		return file
	}
	return filepath.Join(workDir, file)
}

// lintRunner implements lang.SuiteRunner for golangci-lint. It executes
// the lint subprocess, parses its JSON report, converts each finding
// into a lang.Issue plus an optional lang.SuggestedPatch (from
// golangci-lint's own auto-fix data or synthetic generators), and
// returns the structured lang.SuiteReport that the verify engine
// aggregates.
//
// Golangci-lint's exit code is interpreted: any ExitError after parsing
// the report is treated as "lint found issues" (StatusFail); a
// startup/IO error before the report can be parsed is StatusError so
// the agent distinguishes "tool failed" from "code has issues".
type lintRunner struct{}

// Name returns the suite identifier (lang.SuiteLint) reported in
// lang.Issue.Linter and verification output, so verification
// result aggregation can route this runner's output to the lint
// key of the SuiteReports map.
func (*lintRunner) Name() string { return lang.SuiteLint }

// linterHints maps common linter names to actionable architectural
// hints that get attached to the corresponding lang.Issue when
// Detail level >= standard. The hints surface project-specific
// advice (e.g. depguard → Hexagonal Architecture boundary; forbidigo
// → inject a Port instead) so the agent gets context that a
// stock golangci-lint message lacks.
//
// Unknown linter names fall through to a generic "Review and address
// the linter finding." hint at the call site.
var linterHints = map[string]string{
	"depguard":    "Boundary violation. Remove this import to maintain Hexagonal Architecture.",
	"wrapcheck":   "Wrap this error with fmt.Errorf(\"...: %w\", err) before returning.",
	"errcheck":    "Check this error properly. Assign it to 'err' and handle it, or explicitly ignore with _.",
	"revive":      "Address the naming or documentation convention violation.",
	"forbidigo":   "You are using a forbidden standard library function. Inject a Port instead.",
	"gocognit":    "Reduce cognitive complexity by extracting helper functions.",
	"cyclop":      "Reduce cyclomatic complexity by breaking this function into smaller pieces.",
	"dupl":        "Extract duplicated code into a shared function to satisfy DRY.",
	"funlen":      "This function is too long. Extract logical sub-steps into named helpers.",
	"godot":       "Add a period at the end of the comment.",
	"gocritic":    "Review the style or correctness suggestion from gocritic.",
	"staticcheck": "Address the staticcheck finding — often a correctness issue.",
	"unused":      "Remove this unused identifier.",
	"ineffassign": "This assignment is immediately overwritten. Remove it.",
	"typecheck":   "Fix the type error so the package compiles.",
}

// golangciReport is the top-level JSON structure emitted by
// golangci-lint when run with --output.json.path. We only need the
// Issues array; fields like Runners, Report metadata, etc. are
// ignored.
type golangciReport struct {
	Issues []golangciIssue `json:"Issues" jsonschema:"List of lint issues reported by golangci-lint"`
}

// golangciIssue is one entry in the Issues array of a golangci-lint
// JSON report. The structure mirrors golangci-lint's own internal
// result.Issue with only the fields we surface to the agent.
type golangciIssue struct {
	// FromLinter is the name of the linter that reported this issue. Maps
	// to lang.Issue.Linter and is used to look up the appropriate hint in
	// linterHints and to route synthetic-patch generation for errcheck,
	// unused, and deadcode.
	FromLinter string `json:"FromLinter" jsonschema:"Name of the linter that reported this issue"`
	// Text is the human-readable description of the issue emitted by the
	// upstream linter.
	Text string `json:"Text" jsonschema:"Human-readable description of the issue"`
	// Severity is one of "error", "warning", or "info". Mapped to the
	// corresponding lang.Severity* constant for the surfaced lang.Issue;
	// unknown values default to warning.
	Severity string `json:"Severity" jsonschema:"Issue severity: error, warning, or info"`
	// Pos is the source file position of the issue — filename, 1-based
	// line, and 1-based column.
	Pos golangciPos `json:"Pos" jsonschema:"Source file position of the issue"`
	// SourceLines is the captured source-line context around the issue,
	// used as the OldString half of a SuggestedPatch when a Replacement
	// is also present.
	SourceLines []string `json:"SourceLines" jsonschema:"Source lines surrounding the issue"`
	// Replacement is the auto-fix payload produced by some linters
	// (staticcheck, gci, etc.). When non-nil and not just a delete, it is
	// converted into a lang.SuggestedPatch so the agent can apply it
	// through lang.go.patch.
	Replacement *golangciReplace `json:"Replacement" jsonschema:"Auto-fix replacement if available"`
}

// golangciPos is the source location of a golangci-lint issue.
type golangciPos struct {
	// Filename is the source file path containing the issue. Reported
	// relative to golangci-lint's working directory; resolveLintPath
	// converts it to an absolute path for downstream patch generators.
	Filename string `json:"Filename" jsonschema:"Source file path containing the issue"`
	// Line is the 1-based line number of the issue.
	Line int `json:"Line" jsonschema:"Line number of the issue (1-based)"`
	// Column is the 1-based column number of the issue.
	Column int `json:"Column" jsonschema:"Column number of the issue (1-based)"`
}

// golangciReplace carries an auto-fix replacement payload from a
// linter that supports --fix. Used to populate lang.SuggestedPatch.
// Edits.
type golangciReplace struct {
	// NeedOnlyDelete is true if the fix is a pure deletion with no
	// replacement lines. Currently skipped in patch generation — the
	// synthetic generators handle deletion cases more carefully and the
	// upstream signal is rarely emitted by linters we run.
	NeedOnlyDelete bool `json:"NeedOnlyDelete" jsonschema:"True if the fix is a deletion with no replacement lines"`
	// NewLines holds the replacement lines that substitute for the
	// original SourceLines. Joined with newlines to form the NewString
	// field of a lang.PatchEdit.
	NewLines []string `json:"NewLines" jsonschema:"Replacement lines to substitute for the original"`
}

// Run executes golangci-lint against the resolved targets and returns
// a structured lang.SuiteReport plus the lang.SuggestedPatch slice
// the verify engine forwards to its NextActions builder.
//
// Key behaviours:
//   - Output goes to a per-invocation JSON file in logDir. Per-invocation
//     isolation also extends to the lint cache: GOLANGCI_LINT_CACHE is
//     pinned to logDir/lint-cache so consecutive runs over different
//     temp directories do not produce stale results.
//   - --max-issues-per-linter=0 and --max-same-issues=0 disable
//     golangci-lint's own caps so the Engine's truncation and clustering
//     operate on the full result set.
//   - Exit status disambiguation: ExitError after a parseable report is
//     StatusFail; ExitError without a parseable report is StatusError
//     ("build broken before lint could run"); a non-ExitError startup
//     failure (binary not found, PATH issue) is also StatusError.
//   - When Detail.SuggestedPatches is set, errcheck and unused issues
//     are passed through generateErrcheckPatch and generateUnusedPatch
//     to produce synthetic auto-fix patches even though golangci-lint
//     does not natively supply them.
//
// Spawns golangci-lint as a subprocess; honors context cancellation
// via exec.CommandContext.
func (*lintRunner) Run(
	ctx context.Context,
	input lang.VerifyInput,
	logDir string,
) (lang.SuiteReport, []lang.SuggestedPatch, error) {
	workDir, targets := resolveTargets(input.Targets)
	flags := lang.ResolveDetail(input.Detail)

	jsonReportPath := filepath.Join(logDir, "lint-report.json")

	args := []string{
		"run",
		"--output.json.path", jsonReportPath,
		"--max-issues-per-linter=0", // disable golangci-lint's own cap — our Engine controls truncation
		"--max-same-issues=0",       // disable dedup — our Engine clusters duplicates
	}
	if input.CompareTo != "" {
		args = append(args, "--new-from-rev="+input.CompareTo)
	}
	args = append(args, targets...)

	cmd := exec.CommandContext(ctx, "golangci-lint", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	// Isolate the lint cache per invocation. golangci-lint's default
	// cache is shared across all invocations, which causes stale results
	// when consecutive lint runs operate on different temp directories
	// that contain identically-named files (a real footgun in our
	// integration tests, and a subtle correctness bug in production
	// when an agent reorganizes a workspace mid-session). Pinning the
	// cache to logDir scopes it to this single Engine.Execute call.
	cmd.Env = append(os.Environ(), "GOLANGCI_LINT_CACHE="+filepath.Join(logDir, "lint-cache"))
	out, runErr := cmd.CombinedOutput()

	// Write combined output to log file (best effort).
	_ = os.WriteFile(filepath.Join(logDir, "lint.log"), out, 0o644)

	reproCmd := lang.BuildReproCommand("golangci-lint", args)

	// If golangci-lint is not installed (i.e. runErr is NOT an ExitError,
	// it's a startup/IO error), return StatusError. An ExitError just means
	// golangci-lint ran and exited non-zero because it found issues — we
	// fall through to parse the report in that case.
	if runErr != nil {
		exitError := &exec.ExitError{}
		if !errors.As(runErr, &exitError) {
			return lang.StartupErrorReport(
				lang.SuiteLint,
				fmt.Sprintf("golangci-lint not found or failed to start: %v", runErr),
				reproCmd,
			), nil, nil
		}
	}

	// Parse JSON report file.
	var report golangciReport
	var parsedOK bool
	if jsonBytes, err := os.ReadFile(jsonReportPath); err == nil && len(jsonBytes) > 0 {
		if json.Unmarshal(jsonBytes, &report) == nil {
			parsedOK = true
		}
	}

	if !parsedOK && runErr != nil {
		return lang.SuiteReport{
			Status:  lang.StatusError,
			Summary: "golangci-lint failed fatally (build error or bad config). Check lint.log.",
			Issues: []lang.Issue{{
				Linter:       "fatal",
				Message:      lang.ExtractBuildError(out),
				Severity:     lang.SeverityError,
				Hint:         "Verify that the project compiles and golangci-lint is configured correctly.",
				ReproCommand: reproCmd,
			}},
		}, nil, nil
	}

	if len(report.Issues) == 0 {
		return lang.SuiteReport{
			Status:  lang.StatusPass,
			Summary: "0 issues found",
		}, nil, nil
	}

	issues := make([]lang.Issue, 0, len(report.Issues))
	var patches []lang.SuggestedPatch

	for _, gi := range report.Issues {
		sev := lang.SeverityWarning
		switch strings.ToLower(gi.Severity) {
		case "error":
			sev = lang.SeverityError
		case "info":
			sev = lang.SeverityInfo
		}

		// golangci-lint reports filenames relative to its own working dir.
		// Resolve to absolute paths so downstream consumers (synthetic-patch
		// generation here, lang.go.patch later) don't need to be cd'd into
		// the workspace to find the file. issue.File stays relative for
		// human-readable display; SuggestedPatch.FilePath goes absolute.
		absFile := resolveLintPath(workDir, gi.Pos.Filename)

		issue := lang.Issue{
			File:         gi.Pos.Filename,
			Line:         gi.Pos.Line,
			Column:       gi.Pos.Column,
			Severity:     sev,
			Linter:       gi.FromLinter,
			Message:      gi.Text,
			ReproCommand: fmt.Sprintf("golangci-lint run --enable %s %s", gi.FromLinter, gi.Pos.Filename),
		}

		if flags.Snippets {
			issue.Snippet = minifySnippet(extractCodeSnippet(absFile, gi.Pos.Line))
		}

		if absFile != "" && gi.Pos.Line > 0 && flags.Forensics {
			issue.SymbolSource = extractEnclosingFunc(absFile, gi.Pos.Line)
		}

		if flags.Hints {
			if hint, ok := linterHints[gi.FromLinter]; ok {
				issue.Hint = hint
			} else {
				issue.Hint = "Review and address the linter finding."
			}
		}

		issues = append(issues, issue)

		// Build SuggestedPatch from replacement data if available.
		if flags.SuggestedPatches && gi.Replacement != nil && absFile != "" && !gi.Replacement.NeedOnlyDelete {
			if len(gi.Replacement.NewLines) > 0 && len(gi.SourceLines) > 0 {
				patches = append(patches, lang.SuggestedPatch{
					FilePath: absFile,
					Edits: []lang.PatchEdit{{
						OldString: strings.Join(gi.SourceLines, "\n"),
						NewString: strings.Join(gi.Replacement.NewLines, "\n"),
					}},
					Reason: fmt.Sprintf("%s: %s", gi.FromLinter, gi.Text),
					Source: gi.FromLinter,
				})
			}
		}
	}

	// Synthetic patch generation for linters that don't support --fix.
	// The synthetic generators read the source file via os.ReadFile, so
	// they need an absolute path — issue.File alone is relative to workDir.
	if flags.SuggestedPatches {
		for _, issue := range issues {
			absFile := resolveLintPath(workDir, issue.File)
			var patch *lang.SuggestedPatch
			switch issue.Linter {
			case "errcheck":
				patch = generateErrcheckPatch(absFile, issue.Line, strings.HasSuffix(issue.File, "_test.go"))
			case "unused", "deadcode":
				patch = generateUnusedPatch(absFile, issue.Line, extractVarName(issue.Message))
			}
			if patch != nil {
				patches = append(patches, *patch)
			}
		}
	}

	summary := fmt.Sprintf("%d issue(s) found", len(report.Issues))
	if input.CompareTo != "" {
		summary += " (filtered by diff)"
	}

	return lang.SuiteReport{
		Status:  lang.StatusFail,
		Summary: summary,
		Issues:  issues,
	}, patches, nil
}
