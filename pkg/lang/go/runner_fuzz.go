// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"go.thesmos.sh/techne/pkg/lang"
)

// fuzzRunner implements lang.SuiteRunner for `go test -fuzz`. It
// executes a fuzzing run with a fixed 10-second budget, converts
// crashes and timeouts into a single lang.Issue identifying the
// offending fuzz target, and returns a structured lang.SuiteReport.
//
// The runner emits StatusPass when no FAIL marker appears in the
// output, StatusFail when a fuzz target produced a crash, and
// StatusError when go test failed to start. The 10-second budget is
// deliberately short — in a verify call the goal is regression
// detection, not exhaustive corpus generation.
type fuzzRunner struct{}

// Name returns the suite identifier (lang.SuiteFuzz) reported in
// lang.Issue.Linter and verification output. Used by the verify engine
// to route this runner's output into the fuzz key of the SuiteReports
// map.
func (*fuzzRunner) Name() string { return lang.SuiteFuzz }

var (
	// fuzzTargetRegex extracts the name of the failing Fuzz* target from
	// the go test output. The first match wins — fuzz runs only one
	// target per invocation, so the first FuzzXxx token is necessarily
	// the one that crashed.
	fuzzTargetRegex = regexp.MustCompile(`(Fuzz\w+)`)
	// fuzzCrashRegex extracts the panic reason from a fuzz crash. The
	// non-greedy capture stops at the first newline or end of string so
	// the issue message contains the immediate panic message rather than
	// the trailing goroutine dump (which would dominate the agent's
	// context window).
	fuzzCrashRegex = regexp.MustCompile(`panic:\s*(.*?)(?:\n|$)`)
)

// Run executes `go test -fuzz=<pattern> -fuzztime=10s` against the
// resolved targets and converts a crash or timeout into a single
// lang.Issue identifying the offending fuzz target.
//
// The fuzz pattern defaults to "." (match all fuzz targets) when
// Focus is empty. The fixed 10-second budget is short by design —
// verify is for regression detection, not corpus expansion. Agents
// that want a longer fuzzing run should invoke go test directly.
//
// Status disambiguation: "FAIL" appearing anywhere in the output is
// StatusFail (regardless of exit code, because fuzzing can also
// report a non-fatal failure); ExitError without "FAIL" is
// StatusError (build or setup failed); a clean run is StatusPass.
//
// Spawns go test as a subprocess; honors context cancellation via
// exec.CommandContext.
func (*fuzzRunner) Run(
	ctx context.Context,
	input lang.VerifyInput,
	logDir string,
) (lang.SuiteReport, []lang.SuggestedPatch, error) {
	workDir, targets := resolveTargets(input.Targets)

	fuzzPattern := "."
	if input.Focus != "" {
		fuzzPattern = input.Focus
	}

	args := []string{"test", "-fuzz=" + fuzzPattern, "-fuzztime=10s"}
	args = append(args, targets...)

	cmd := exec.CommandContext(ctx, "go", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, runErr := cmd.CombinedOutput()

	_ = os.WriteFile(filepath.Join(logDir, "fuzz.log"), out, 0o644)

	reproCmd := lang.BuildReproCommand("go", args)

	// startup error (not ExitError) → report failure; ExitError just means
	// fuzzing failed and we still want to parse the output.
	if runErr != nil {
		exitError := &exec.ExitError{}
		if !errors.As(runErr, &exitError) {
			return lang.StartupErrorReport(
				lang.SuiteFuzz,
				fmt.Sprintf("go test -fuzz failed to start: %v", runErr),
				reproCmd,
			), nil, nil
		}
	}

	outStr := string(out)
	status := lang.StatusPass

	var issues []lang.Issue

	if strings.Contains(outStr, "FAIL") {
		status = lang.StatusFail

		target := ""
		if m := fuzzTargetRegex.FindStringSubmatch(outStr); len(m) > 1 {
			target = m[1]
		}

		crashReason := ""
		if m := fuzzCrashRegex.FindStringSubmatch(outStr); len(m) > 1 {
			crashReason = strings.TrimSpace(m[1])
		}
		if crashReason == "" {
			crashReason = "fuzz target failed or timed out"
		}

		msg := crashReason
		if target != "" {
			msg = fmt.Sprintf("%s: %s", target, crashReason)
		}

		issues = append(issues, lang.Issue{
			Severity:     lang.SeverityError,
			Linter:       lang.SuiteFuzz,
			Message:      msg,
			SymbolName:   target,
			ReproCommand: reproCmd,
		})
	} else if runErr != nil {
		status = lang.StatusError
		issues = append(issues, lang.Issue{
			Severity:     lang.SeverityError,
			Linter:       lang.SuiteFuzz,
			Message:      "Fuzz suite failed to run",
			ReproCommand: reproCmd,
		})
	}

	summary := "No fuzz failures detected"
	switch status {
	case lang.StatusFail:
		summary = fmt.Sprintf("Fuzz target failed: %d issue(s)", len(issues))
	case lang.StatusError:
		summary = "Fuzz suite failed to run"
	}

	return lang.SuiteReport{
		Status:  status,
		Summary: summary,
		Issues:  issues,
	}, nil, nil
}
