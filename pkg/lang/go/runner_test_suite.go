// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.thesmos.sh/techne/pkg/lang"
)

// testRunner implements lang.SuiteRunner for go test. It executes
// `go test -json` against the resolved targets, parses the event
// stream into test counts and per-failure lang.Issue entries, and
// returns a structured lang.SuiteReport.
//
// The runner distinguishes three outcomes: StatusPass when no tests
// failed, StatusFail when one or more assertion failures appear in the
// event stream, and StatusError when go test could not start or built
// but produced no parseable events (typically a compile error in the
// tested code).
type testRunner struct{}

// Name returns the suite identifier (lang.SuiteTest) reported in
// lang.Issue.Linter and verification output. Used by the verify engine
// to route this runner's output into the test key of the SuiteReports
// map.
func (*testRunner) Name() string { return lang.SuiteTest }

// testEvent is a single line of `go test -json` output. The JSON
// schema is documented at
// https://pkg.go.dev/cmd/test2json. We only consume the four fields
// below; positions, elapsed time, and import path are computed
// separately or ignored.
type testEvent struct {
	// Action is the event kind: run, pass, fail, skip, output, pause, or
	// cont. Only run/pass/fail/skip/output influence the surfaced report;
	// pause and cont are no-ops here.
	Action string `json:"Action" jsonschema:"Event action: run, pass, fail, skip, output, or pause"`
	// Package is the package import path being tested. Used to build a
	// relative path for the failing file (test output gives a base name;
	// we prepend the package directory to form a path the agent can
	// open).
	Package string `json:"Package" jsonschema:"Package import path being tested"`
	// Test is the test function name (possibly with subtest path appended
	// as Test/Subtest). Empty for package-level events (build errors,
	// overall PASS/FAIL summaries); the runner skips those.
	Test string `json:"Test" jsonschema:"Test function name; empty for package-level events"`
	// Output is the captured output line for output events. Buffered per
	// test into the outputs map until a fail event triggers post-
	// processing.
	Output string `json:"Output" jsonschema:"Captured output line for output events"`
}

// Run executes `go test -json` against the resolved targets, parses
// the event stream into per-test outcomes, and returns a structured
// lang.SuiteReport plus a nil patches slice (test failures cannot be
// auto-patched).
//
// Key behaviours:
//   - Streaming JSON event parsing: each line is one testEvent.
//     Output is buffered per (Package, Test) until a fail event,
//     at which point the buffer is condensed through cleanTestOutput
//     and surfaced as the issue message.
//   - File path resolution: failures emit "file.go:line:" markers in
//     their output; the runner finds the first one and prepends the
//     package's relative directory so the resulting path opens from
//     the module root.
//   - Status disambiguation: ExitError combined with parsed fail
//     events = StatusFail; ExitError without any fail events =
//     StatusError (build/setup error — the package didn't compile);
//     non-ExitError = StatusError (go binary failed to start).
//   - Subtests are counted in totals but their failures are NOT
//     surfaced as separate issues (the strings.Contains(Test, "/")
//     check) — only top-level test failures appear in the report,
//     avoiding noise when one parent failure cascades to many
//     children.
//
// Spawns `go test` as a subprocess; honors context cancellation via
// exec.CommandContext.
func (*testRunner) Run(
	ctx context.Context,
	input lang.VerifyInput,
	logDir string,
) (lang.SuiteReport, []lang.SuggestedPatch, error) {
	workDir, targets := resolveTargets(input.Targets)
	flags := lang.ResolveDetail(input.Detail)

	// Resolve module name for relative path construction.
	modCmd := exec.Command("go", "list", "-m")
	if workDir != "" {
		modCmd.Dir = workDir
	}
	modBytes, _ := modCmd.Output()
	modName := strings.TrimSpace(string(modBytes))

	args := []string{"test", "-json"}
	args = append(args, targets...)

	if input.Focus != "" {
		args = append(args, "-run", input.Focus)
	}

	cmd := exec.CommandContext(ctx, "go", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, runErr := cmd.CombinedOutput()

	reproCmd := lang.BuildReproCommand("go", args)

	_ = os.WriteFile(filepath.Join(logDir, "test.log"), out, 0o644)

	// If go itself failed to start (i.e. runErr is NOT an ExitError, but
	// some IO/PATH issue), report as error. An ExitError just means tests
	// failed — we fall through to parse the JSON event stream.
	if runErr != nil {
		exitError := &exec.ExitError{}
		if !errors.As(runErr, &exitError) {
			return lang.StartupErrorReport(
				lang.SuiteTest,
				fmt.Sprintf("go test failed to start: %v", runErr),
				reproCmd,
			), nil, nil
		}
	}

	locRegex := regexp.MustCompile(`(\S+\.go):(\d+):`)

	outputs := make(map[string][]string)
	var issues []lang.Issue
	status := lang.StatusPass

	counts := &lang.TestCounts{}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		var ev testEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}

		if ev.Test == "" {
			continue
		}

		key := ev.Package + "/" + ev.Test

		switch ev.Action {
		case "run":
			counts.Total++
		case "pass":
			counts.Passed++
		case "skip":
			counts.Skipped++
		case "fail":
			counts.Failed++
		case "output":
			outputs[key] = append(outputs[key], ev.Output)
		}

		if ev.Action == "fail" && !strings.Contains(ev.Test, "/") {
			status = lang.StatusFail

			logs := outputs[key]

			var file string
			var line int
			for _, outLine := range logs {
				if m := locRegex.FindStringSubmatch(outLine); len(m) == 3 {
					file = m[1]
					line, _ = strconv.Atoi(m[2])

					// Build relative path using module name.
					relPkg := strings.TrimPrefix(ev.Package, modName+"/")
					if relPkg != ev.Package {
						file = filepath.Join(relPkg, file)
					}
					break
				}
			}

			errMsg := cleanTestOutput(logs)

			issue := lang.Issue{
				File:         file,
				Line:         line,
				Severity:     lang.SeverityError,
				Linter:       lang.SuiteTest,
				Message:      errMsg,
				SymbolName:   ev.Test,
				ReproCommand: fmt.Sprintf("go test -run ^%s$ %s -v", ev.Test, ev.Package),
			}

			if file != "" && line > 0 && flags.Snippets {
				issue.Snippet = minifySnippet(extractCodeSnippet(file, line))
			}

			if file != "" && line > 0 && flags.Forensics {
				issue.SymbolSource = extractEnclosingFunc(file, line)
			}

			if flags.Hints {
				issue.Hint = "A test assertion failed. Fix the logic or update the expectation."
			}

			issues = append(issues, issue)
		}
	}

	// If go test failed but no JSON failures were parsed, it's a build/setup error.
	if runErr != nil && status == lang.StatusPass {
		status = lang.StatusError
		issues = append(issues, lang.Issue{
			Severity:     lang.SeverityError,
			Linter:       lang.SuiteTest,
			Message:      lang.ExtractBuildError(out),
			Hint:         "Failed to compile or start tests. Check syntax or dependencies.",
			ReproCommand: reproCmd,
		})
	}

	// coverage is always nil — the public schema has no CoverageThreshold field.
	var coverage *lang.CoverageInfo

	summary := buildTestSummary(counts, status)

	return lang.SuiteReport{
		Status:     status,
		Summary:    summary,
		Issues:     issues,
		TestCounts: counts,
		Coverage:   coverage,
	}, nil, nil
}

// buildTestSummary creates a human-readable one-line summary of a
// test run based on the status and counts. Branches on status
// rather than counts so a passing run with zero tests (which can
// happen when filtering by an empty pattern) still reads naturally
// as "All 0 test(s) passed" rather than something more alarming.
func buildTestSummary(counts *lang.TestCounts, status string) string {
	switch status {
	case lang.StatusPass:
		return fmt.Sprintf("All %d test(s) passed", counts.Total)
	case lang.StatusFail:
		return fmt.Sprintf("%d/%d test(s) failed", counts.Failed, counts.Total)
	case lang.StatusError:
		return "Test suite failed to run (build or setup error)"
	default:
		return fmt.Sprintf("%d total, %d passed, %d failed", counts.Total, counts.Passed, counts.Failed)
	}
}
