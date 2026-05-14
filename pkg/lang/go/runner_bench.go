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
	"strconv"
	"strings"

	"go.thesmos.sh/techne/pkg/lang"
)

// benchRunner implements lang.SuiteRunner for `go test -bench`. It
// executes the benchmark subprocess with -benchmem, parses the
// standard Go benchmark result lines into lang.Metric entries, and
// attaches escape-analysis information for benchmarks that allocate on
// the heap.
//
// The runner emits StatusPass when at least one metric was parsed,
// StatusError when go test failed to start, and StatusError when go
// test exited non-zero and produced no parseable metrics (typically a
// compile error). Regression detection (StatusDegraded) is reserved
// for future use — currently always StatusPass when metrics exist.
type benchRunner struct{}

// Name returns the suite identifier (lang.SuiteBench) reported in
// lang.Issue.Linter and verification output. Used by the verify
// engine to route this runner's output into the bench key of the
// SuiteReports map.
func (*benchRunner) Name() string { return lang.SuiteBench }

// benchRegex matches a standard Go benchmark result line. Example:
//
//	BenchmarkFoo-8   1000000   1234.5 ns/op   64 B/op   2 allocs/op
//
// Capture groups: benchmark name (without GOMAXPROCS suffix),
// ns/op, B/op, allocs/op. The iteration count and any GOMAXPROCS
// suffix are consumed but not captured — the metrics struct only
// records per-op values, which is what an agent typically compares
// across runs.
var benchRegex = regexp.MustCompile(
	`(Benchmark\w+)[-\d]*\s+\d+\s+([\d.]+)\s+ns/op\s+(\d+)\s+B/op\s+(\d+)\s+allocs/op`,
)

// Run executes `go test -bench -benchmem` against the resolved
// targets, parses the textual benchmark output into lang.Metric
// entries, and enriches metrics that allocate with escape-analysis
// information from RunEscapeAnalysis.
//
// The benchmark pattern defaults to "." (match all benchmarks) when
// Focus is empty, mirroring `go test -bench=.`. -run=^$ is set to
// prevent regular tests from running alongside the benchmarks, which
// would pollute timing measurements.
//
// Zero-value benchmark results (0 ns/op, 0 B/op, 0 allocs/op) are
// skipped to save tokens — they typically come from skipped or
// empty benchmarks and convey no useful information.
//
// Spawns go test as a subprocess; honors context cancellation via
// exec.CommandContext.
func (*benchRunner) Run(
	ctx context.Context,
	input lang.VerifyInput,
	logDir string,
) (lang.SuiteReport, []lang.SuggestedPatch, error) {
	workDir, targets := resolveTargets(input.Targets)

	benchPattern := "."
	if input.Focus != "" {
		benchPattern = input.Focus
	}

	args := []string{"test", "-bench=" + benchPattern, "-run=^$", "-benchmem"}
	args = append(args, targets...)

	cmd := exec.CommandContext(ctx, "go", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, runErr := cmd.CombinedOutput()

	_ = os.WriteFile(filepath.Join(logDir, "bench.log"), out, 0o644)

	reproCmd := lang.BuildReproCommand("go", args)

	// startup error (not ExitError) → report failure; ExitError just means
	// tests failed and we still want to parse the output.
	if runErr != nil {
		exitError := &exec.ExitError{}
		if !errors.As(runErr, &exitError) {
			return lang.StartupErrorReport(
				lang.SuiteBench,
				fmt.Sprintf("go test -bench failed to start: %v", runErr),
				reproCmd,
			), nil, nil
		}
	}

	status := lang.StatusPass
	var metrics []lang.Metric

	// Run escape analysis on the target packages to enrich metrics.
	var escapesByFunc map[string][]lang.EscapeInfo
	if len(targets) > 0 {
		escapesByFunc = buildEscapeMap(targets[0], workDir)
	}

	matches := benchRegex.FindAllStringSubmatch(string(out), -1)
	for _, m := range matches {
		nsPerOp, _ := strconv.ParseFloat(m[2], 64)
		bytesPerOp, _ := strconv.Atoi(m[3])
		allocsPerOp, _ := strconv.Atoi(m[4])

		// Skip zero-value results to save context.
		if nsPerOp == 0 && allocsPerOp == 0 && bytesPerOp == 0 {
			continue
		}

		metric := lang.Metric{
			Name:        m[1],
			NsPerOp:     nsPerOp,
			BytesPerOp:  bytesPerOp,
			AllocsPerOp: allocsPerOp,
		}

		// Attach escape analysis for benchmarks with heap allocations.
		if allocsPerOp > 0 && escapesByFunc != nil {
			if escapes, ok := escapesByFunc["*"]; ok {
				// Filter escapes to those mentioning the benchmark function name
				// (strip "Benchmark" prefix to match the tested function).
				funcName := strings.TrimPrefix(m[1], "Benchmark")
				var relevant []lang.EscapeInfo
				for _, e := range escapes {
					if strings.Contains(e.Variable, funcName) || strings.Contains(e.Cause, funcName) || funcName == "" {
						relevant = append(relevant, e)
					}
				}
				if len(relevant) == 0 {
					// If no function-specific matches, include all escapes from the package.
					metric.Escapes = escapes
				} else {
					metric.Escapes = relevant
				}
			}
		}

		metrics = append(metrics, metric)
	}

	if runErr != nil && status == lang.StatusPass && len(metrics) == 0 {
		status = lang.StatusError
	}

	summary := fmt.Sprintf("%d benchmark(s) measured", len(metrics))
	switch status {
	case lang.StatusDegraded:
		summary += " — regressions detected"
	case lang.StatusError:
		summary = "Benchmark suite failed to run"
	}

	return lang.SuiteReport{
		Status:  status,
		Summary: summary,
		Metrics: metrics,
	}, nil, nil
}

// buildEscapeMap runs escape analysis on the benchmark package and
// returns the escapes under a wildcard key ("*"). The caller is
// responsible for filtering by benchmark function name.
//
// The wildcard structure exists because RunEscapeAnalysis returns all
// package-level escapes without a function attribution — mapping each
// escape to its parent benchmark would require deeper AST analysis
// than the current implementation performs. The caller in Run
// performs name-substring matching as a best-effort attribution.
func buildEscapeMap(pkg, workDir string) map[string][]lang.EscapeInfo {
	target := pkg
	if workDir != "" {
		target = workDir
	}

	escapes, err := RunEscapeAnalysis(target, "")
	if err != nil || len(escapes) == 0 {
		return nil
	}

	// RunEscapeAnalysis returns all escapes — we can't directly map them to
	// specific Benchmark* functions without deeper analysis. Return all escapes
	// for any benchmark that has allocations; the function-level filtering
	// happens at the caller based on the benchmark name.
	result := make(map[string][]lang.EscapeInfo)
	// Store all escapes under a wildcard key — the caller can match by name.
	result["*"] = escapes
	return result
}
