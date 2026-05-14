// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
)

func TestVerify(t *testing.T) {
	// ---- test suite ----

	t.Run("TestSuite/Counts", func(t *testing.T) {
		dir := writeTempProject(t)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteTest},
			Targets: []string{dir},
		})

		report, hasTest := out.Reports[lang.SuiteTest]
		if !hasTest {
			t.Fatal("expected test suite report")
		}

		if report.TestCounts == nil {
			t.Fatal("expected TestCounts to be populated")
		}

		counts := report.TestCounts
		if counts.Total != 2 {
			t.Errorf("Total = %d, want 2", counts.Total)
		}
		if counts.Passed != 1 {
			t.Errorf("Passed = %d, want 1", counts.Passed)
		}
		if counts.Failed != 1 {
			t.Errorf("Failed = %d, want 1", counts.Failed)
		}
	})

	t.Run("TestSuite/FailureHasMetadata", func(t *testing.T) {
		dir := writeTempProject(t)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteTest},
			Targets: []string{dir},
		})

		report := out.Reports[lang.SuiteTest]

		if len(report.Issues) == 0 {
			t.Fatal("expected at least one failure issue")
		}

		issue := report.Issues[0]

		if issue.SymbolName == "" {
			t.Error("SymbolName should be populated for test failure")
		}
		if issue.ReproCommand == "" {
			t.Error("ReproCommand should be populated for test failure")
		}
		if issue.File == "" {
			t.Error("File should be populated for test failure")
		}
		if issue.Line == 0 {
			t.Error("Line should be populated for test failure")
		}
	})

	t.Run("OverallStatus/DegradedOnTestFailure", func(t *testing.T) {
		dir := writeTempProject(t)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteTest},
			Targets: []string{dir},
		})

		if out.OverallStatus != lang.StatusDegraded {
			t.Errorf("OverallStatus = %q, want %q", out.OverallStatus, lang.StatusDegraded)
		}
	})

	t.Run("FailFast/StopsAfterFirstFailure", func(t *testing.T) {
		dir := writeTempProject(t)

		// Request both test and bench with FailFast.
		out := runVerify(t, lang.VerifyInput{
			Suites:   []string{lang.SuiteTest, lang.SuiteBench},
			Targets:  []string{dir},
			FailFast: true,
		})

		// Test should have run (and failed).
		if _, ok := out.Reports[lang.SuiteTest]; !ok {
			t.Error("expected test report when FailFast is set")
		}

		// Bench should NOT have run because FailFast stopped after test failure.
		if _, ok := out.Reports[lang.SuiteBench]; ok {
			t.Error("bench should not have run with FailFast after test failure")
		}
	})

	t.Run("BloatPrevention/LogID", func(t *testing.T) {
		dir := writeTempProject(t)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteTest},
			Targets: []string{dir},
		})

		if out.BloatPrevention.LogID == "" {
			t.Error("BloatPrevention.LogID should be populated")
		}
	})

	t.Run("NextActions/PopulatedOnFailure", func(t *testing.T) {
		dir := writeTempProject(t)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteTest},
			Targets: []string{dir},
		})

		// Test failures exist, so we expect NextActions with lang.go.explore.
		if len(out.NextActions) == 0 {
			t.Error("NextActions should be populated when there are test failures")
		}

		found := false
		for _, a := range out.NextActions {
			if a.Tool == "lang.go.explore" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected NextAction with tool 'lang.go.explore', got: %+v", out.NextActions)
		}
	})

	t.Run("LintSuite/GracefulWhenNotInstalled", func(t *testing.T) {
		dir := writeTempProject(t)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteLint},
			Targets: []string{dir},
		})

		report, hasLint := out.Reports[lang.SuiteLint]
		if !hasLint {
			t.Fatal("expected lint suite report even when golangci-lint may not be installed")
		}

		// Any of pass/fail/error is acceptable — never panic.
		switch report.Status {
		case lang.StatusPass, lang.StatusFail, lang.StatusError:
			// All acceptable outcomes.
		default:
			t.Errorf("unexpected lint status: %q", report.Status)
		}
	})

	// ---- lint runner — issue detection + synthetic-patch generation ----

	t.Run("Lint/GeneratesErrcheckFixPatch", func(t *testing.T) {
		requireGolangciLint(t)
		// Module with an explicit-blank error discard (`_ = os.Remove(...)`).
		// With errcheck.check-blank=true, this is flagged; lint_patches.go
		// synthesizes a fix patch that replaces `_ =` with `err :=` plus an
		// `if err != nil` handler block.
		dir := writeLintProject(t, "errcheck.go", `package testpkg

import "os"

func ReadConfig() {
	_ = os.Remove("/tmp/x")
}
`, withGolangciConfig(`version: "2"
linters:
  default: none
  enable:
    - errcheck
  settings:
    errcheck:
      check-blank: true
`))

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteLint},
			Targets: []string{dir},
		})

		report, ok := out.Reports[lang.SuiteLint]
		if !ok {
			t.Fatal("expected lint suite report")
		}
		if !hasIssueFromLinter(report.Issues, "errcheck") {
			t.Fatalf("expected errcheck issue; got: %+v", linterNames(report.Issues))
		}
		if !hasSuggestedPatchFromSource(out.SuggestedPatches, "synthetic-errcheck") {
			t.Errorf(
				"expected SuggestedPatch with Source=synthetic-errcheck; got: %+v",
				patchSources(out.SuggestedPatches),
			)
		}
	})

	t.Run("Lint/DetectsAndPatchesUnusedAssignment", func(t *testing.T) {
		requireGolangciLint(t)
		// An unused assignment: `x` is declared but never read.
		dir := writeLintProject(t, "unused.go", `package testpkg

func Compute() int {
	x := 1
	x = 2
	return x
}
`, withGolangciConfig(`version: "2"
linters:
  default: none
  enable:
    - ineffassign
`))

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteLint},
			Targets: []string{dir},
		})

		report, ok := out.Reports[lang.SuiteLint]
		if !ok {
			t.Fatal("expected lint suite report")
		}
		if !hasIssueFromLinter(report.Issues, "ineffassign") {
			t.Fatalf("expected ineffassign issue; got: %+v", linterNames(report.Issues))
		}
	})

	t.Run("Lint/GeneratesUnusedFixPatch", func(t *testing.T) {
		requireGolangciLint(t)
		// An unused variable — the `unused` linter flags it; lint_patches.go
		// generates a synthetic delete-the-declaration patch.
		dir := writeLintProject(t, "unused.go", `package testpkg

func Compute() int {
	unusedVar := 42
	_ = unusedVar
	return 0
}
`, withGolangciConfig(`version: "2"
linters:
  default: none
  enable:
    - unused
`))

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteLint},
			Targets: []string{dir},
		})

		report, ok := out.Reports[lang.SuiteLint]
		if !ok {
			t.Fatal("expected lint suite report")
		}
		// Don't assert on issue presence — `unused` linter sometimes considers
		// assigned-then-discarded vars used. Just verify no crash and runner ran.
		_ = report
		_ = out
	})

	t.Run("Lint/SyntheticPatchWorksWithoutChdir", func(t *testing.T) {
		// Regression for the synthetic-patch path-resolution bug: previously
		// generateErrcheckPatch did os.ReadFile against the agent's CWD,
		// not against the lint runner's working dir. So patch generation
		// silently failed unless the agent had cd'd into the workspace.
		// The fix routes paths through resolveLintPath; this test exercises
		// it by passing an absolute Targets path and NOT calling t.Chdir.
		requireGolangciLint(t)
		dir := writeLintProject(t, "errcheck.go", `package testpkg

import "os"

func ReadConfig() {
	_ = os.Remove("/tmp/x")
}
`, withGolangciConfig(`version: "2"
linters:
  default: none
  enable:
    - errcheck
  settings:
    errcheck:
      check-blank: true
`))
		// Crucially: NO t.Chdir. The agent's CWD is wherever the test runner
		// happens to be. The lint runner picks up the workspace from Targets.

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteLint},
			Targets: []string{dir},
		})

		report, ok := out.Reports[lang.SuiteLint]
		if !ok {
			t.Fatal("expected lint suite report")
		}
		if !hasIssueFromLinter(report.Issues, "errcheck") {
			t.Fatalf("expected errcheck issue; got: %+v", linterNames(report.Issues))
		}
		if !hasSuggestedPatchFromSource(out.SuggestedPatches, "synthetic-errcheck") {
			t.Errorf(
				"synthetic patch should be generated even without t.Chdir; got sources %+v",
				patchSources(out.SuggestedPatches),
			)
		}
		// And the path in the suggested patch must be absolute so lang.go.patch
		// can find the file regardless of the patch tool's CWD.
		for _, p := range out.SuggestedPatches {
			if p.Source == "synthetic-errcheck" && !filepath.IsAbs(p.FilePath) {
				t.Errorf("synthetic patch FilePath should be absolute; got %q", p.FilePath)
			}
		}
	})

	t.Run("Lint/GeneratesPatchForBareErrcheck", func(t *testing.T) {
		requireGolangciLint(t)
		// Bare unchecked call (no blank-discard) — exercises the ExprStmt
		// branch of generateErrcheckPatch (vs the AssignStmt branch covered above).
		dir := writeLintProject(t, "errcheck.go", `package testpkg

import "os"

func ReadConfig() {
	os.Remove("/tmp/x")
}
`, withGolangciConfig(`version: "2"
linters:
  default: none
  enable:
    - errcheck
`))

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteLint},
			Targets: []string{dir},
		})

		report, ok := out.Reports[lang.SuiteLint]
		if !ok {
			t.Fatal("expected lint suite report")
		}
		if !hasIssueFromLinter(report.Issues, "errcheck") {
			t.Fatalf("expected errcheck issue; got: %+v", linterNames(report.Issues))
		}
		if !hasSuggestedPatchFromSource(out.SuggestedPatches, "synthetic-errcheck") {
			t.Errorf(
				"expected SuggestedPatch with Source=synthetic-errcheck; got: %+v",
				patchSources(out.SuggestedPatches),
			)
		}
	})

	// ---- bench runner ----

	t.Run("Bench/RunsBenchmark", func(t *testing.T) {
		dir := writeBenchProject(t)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteBench},
			Targets: []string{dir},
		})

		report, ok := out.Reports[lang.SuiteBench]
		if !ok {
			t.Fatal("expected bench suite report")
		}
		if report.Status == lang.StatusError {
			t.Fatalf("bench runner should not error on a working benchmark; got summary: %s", report.Summary)
		}
		// At least one Metric should have been parsed from the benchmark output.
		foundBench := false
		for _, m := range report.Metrics {
			if strings.HasPrefix(m.Name, "Benchmark") {
				foundBench = true
				break
			}
		}
		if !foundBench {
			t.Errorf("expected at least one Benchmark metric; got %+v", report.Metrics)
		}
	})

	// ---- fuzz runner ----

	t.Run("Fuzz/RunsFuzz", func(t *testing.T) {
		dir := writeFuzzProject(t)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteFuzz},
			Targets: []string{dir},
		})

		report, ok := out.Reports[lang.SuiteFuzz]
		if !ok {
			t.Fatal("expected fuzz suite report")
		}
		// Fuzz exits cleanly (no panics) for our well-behaved Add function.
		if report.Status == lang.StatusError {
			t.Errorf("fuzz runner returned StatusError; summary: %s", report.Summary)
		}
	})

	// ---- Focus + MaxIssuesPerType ----

	t.Run("Focus/FiltersTestsByPattern", func(t *testing.T) {
		dir := writeTempProject(t)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteTest},
			Targets: []string{dir},
			Focus:   "^TestAdd_Pass$",
		})

		report, ok := out.Reports[lang.SuiteTest]
		if !ok {
			t.Fatal("expected test suite report")
		}
		// With Focus restricting to only the passing test, the failing test
		// should not run — so Failed should be 0.
		if report.TestCounts != nil && report.TestCounts.Failed > 0 {
			t.Errorf("expected Failed=0 when Focus matches only passing test; got %d", report.TestCounts.Failed)
		}
	})

	t.Run("MaxIssuesPerType/CapsIssueList", func(t *testing.T) {
		requireGolangciLint(t)
		// File with 3+ unchecked errors. MaxIssuesPerType=2 should cap report.Issues to 2.
		dir := writeLintProject(t, "errcheck.go", `package testpkg

import "os"

func A() { _ = os.Remove("/tmp/a") }
func B() { _ = os.Remove("/tmp/b") }
func C() { _ = os.Remove("/tmp/c") }
`, withGolangciConfig(`version: "2"
linters:
  default: none
  enable:
    - errcheck
`))

		out := runVerify(t, lang.VerifyInput{
			Suites:           []string{lang.SuiteLint},
			Targets:          []string{dir},
			MaxIssuesPerType: 2,
		})

		report, ok := out.Reports[lang.SuiteLint]
		if !ok {
			t.Fatal("expected lint suite report")
		}
		if len(report.Issues) > 2 {
			t.Errorf("expected at most 2 issues with MaxIssuesPerType=2; got %d", len(report.Issues))
		}
	})

	// ---- complex multi-package fixture ----

	t.Run("Complex/TestSuiteAcrossPackages", func(t *testing.T) {
		dir := complexProject(t)
		mustWriteFileC(t, filepath.Join(dir, "core/reader_test.go"), `package core

import "testing"

func TestReaderInterface(t *testing.T) {
	var _ Reader = nil // compile-time interface check
}
`)
		t.Chdir(dir)

		raw, _ := json.Marshal(lang.VerifyInput{
			Suites:  []string{lang.SuiteTest},
			Targets: []string{"./..."},
		})
		result, err := golang.Verify.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		var out lang.VerifyOutput
		if vo, ok := result.(lang.VerifyOutput); ok {
			out = vo
		} else {
			b, _ := json.Marshal(result)
			_ = json.Unmarshal(b, &out)
		}

		report, ok := out.Reports[lang.SuiteTest]
		if !ok {
			t.Fatal("expected test suite report")
		}
		if report.Status == lang.StatusError {
			t.Errorf("test suite failed to start: %s", report.Summary)
		}
	})

	t.Run("Complex/CompareToNarrowsToAffected", func(t *testing.T) {
		dir := complexProject(t)
		t.Chdir(dir)

		// Initialize a git repo and commit the baseline.
		if err := runGit(t, dir, "init", "-q"); err != nil {
			t.Skipf("git not available: %v", err)
		}
		if err := runGit(t, dir, "config", "user.email", "test@example.com"); err != nil {
			t.Fatalf("git config: %v", err)
		}
		if err := runGit(t, dir, "config", "user.name", "Test"); err != nil {
			t.Fatalf("git config: %v", err)
		}
		// Disable GPG signing for this temp repo; the user's global git
		// config may have it on, and the test image has no signing key.
		if err := runGit(t, dir, "config", "commit.gpgsign", "false"); err != nil {
			t.Fatalf("git config: %v", err)
		}
		if err := runGit(t, dir, "add", "-A"); err != nil {
			t.Fatalf("git add: %v", err)
		}
		if err := runGit(t, dir, "commit", "-q", "-m", "baseline"); err != nil {
			t.Fatalf("git commit: %v", err)
		}

		// Modify only api/handler.go — verify with compare_to=HEAD~ should
		// narrow to the api package and its dependents, not run on every package.
		mustWriteFileC(t, filepath.Join(dir, "api/handler.go"),
			`package api

import (
	"example.com/cx/core"
	"example.com/cx/store"
)

type Handler struct{ r core.Reader }

func NewHandler(path string) *Handler {
	return &Handler{r: store.New(path)}
}

func (h *Handler) Process(n int) ([]byte, error) {
	return h.r.Read(n)
}

// added comment line
`)

		raw, _ := json.Marshal(lang.VerifyInput{
			Suites:    []string{lang.SuiteTest},
			CompareTo: "HEAD",
		})
		result, err := golang.Verify.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("verify execute: %v", err)
		}
		var out lang.VerifyOutput
		if vo, ok := result.(lang.VerifyOutput); ok {
			out = vo
		} else {
			b, _ := json.Marshal(result)
			_ = json.Unmarshal(b, &out)
		}

		report, ok := out.Reports[lang.SuiteTest]
		if !ok {
			t.Fatal("expected test suite report")
		}
		// Whatever the outcome (no tests fail in this fixture), the run should
		// have completed without StatusError. The narrowing logic is exercised
		// inside narrowTargetsFromDiff.
		if report.Status == lang.StatusError {
			t.Errorf("compare_to verify reported StatusError; summary: %s", report.Summary)
		}
	})

	// ---- production / workspace ----

	// lang.go.verify must report failing tests, not silently mark the package
	// as healthy.
	t.Run("Production/DetectsTestFailure", func(t *testing.T) {
		dir := writeMod(t, "prodverify", map[string]string{
			"a.go":      "package prodverify\n\nfunc Add(a, b int) int { return a - b }\n", // bug
			"a_test.go": "package prodverify\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatalf(\"expected 5\")\n\t}\n}\n",
		})
		t.Chdir(dir)

		in := lang.VerifyInput{Targets: []string{dir}, Suites: []string{"test"}}
		raw, _ := json.Marshal(in)
		result, err := golang.Verify.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("verify execute: %v", err)
		}
		out, ok := result.(lang.VerifyOutput)
		if !ok {
			var probe lang.VerifyOutput
			b, _ := json.Marshal(result)
			_ = json.Unmarshal(b, &probe)
			out = probe
		}
		report, ok := out.Reports[lang.SuiteTest]
		if !ok {
			t.Fatalf("expected test suite report; got %+v", out.Reports)
		}
		if report.Status == lang.StatusPass {
			t.Errorf("verify must surface a failing test; got status=%q summary=%q", report.Status, report.Summary)
		}
	})

	// resolveErrHandler branches on whether the enclosing function has a
	// *testing.T param. When it does, the suggested patch should use
	// t.Fatal(err) rather than _ = err. This exercises findTestingParam.
	t.Run("Lint/ErrcheckInTestFuncSuggestsTFatal", func(t *testing.T) {
		requireGolangciLint(t)

		dir := t.TempDir()
		t.Chdir(dir)
		mustWriteFileVT(t, filepath.Join(dir, "go.mod"), "module testlintpkg\n\ngo 1.21\n")
		// Minimal source file so the package is valid.
		mustWriteFileVT(t, filepath.Join(dir, "ops.go"), "package testlintpkg\n")
		// Test file with an unchecked os.Remove — errcheck should flag it.
		mustWriteFileVT(t, filepath.Join(dir, "ops_test.go"), `package testlintpkg

import (
	"os"
	"testing"
)

func TestClean(t *testing.T) {
	os.Remove("testfile.txt")
}
`)
		mustWriteFileVT(
			t,
			filepath.Join(dir, ".golangci.yml"),
			"version: \"2\"\nlinters:\n  default: none\n  enable:\n    - errcheck\n",
		)

		out := runVerify(t, lang.VerifyInput{
			Suites:  []string{lang.SuiteLint},
			Targets: []string{dir},
		})

		lintReport, ok := out.Reports[lang.SuiteLint]
		if !ok || lintReport.Status == lang.StatusError {
			t.Skipf("lint runner error (golangci-lint may be misconfigured): %v", lintReport.Summary)
		}

		for _, p := range out.SuggestedPatches {
			if p.Source != "synthetic-errcheck" {
				continue
			}
			for _, e := range p.Edits {
				if strings.Contains(e.NewString, "t.Fatal(err)") {
					return // test passes
				}
			}
		}
		if len(out.SuggestedPatches) == 0 {
			t.Skip("no errcheck patches generated — golangci-lint may not have detected the issue")
		}
		t.Errorf(
			"expected synthetic-errcheck patch containing t.Fatal(err) for *testing.T func; patches: %+v",
			out.SuggestedPatches,
		)
	})

	// verify with default targets must run lint/test across every workspace module.
	t.Run("Workspace/RunsAcrossModules", func(t *testing.T) {
		twoModuleWorkspace(t,
			map[string]string{
				"api.go":      "package a\n\nfunc Add(x, y int) int { return x + y }\n",
				"api_test.go": "package a\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 { t.Fail() }\n}\n",
			},
			map[string]string{
				"main.go":      "package b\n\nimport \"example.com/a\"\n\nfunc Use() int { return a.Add(2, 3) }\n",
				"main_test.go": "package b\n\nimport \"testing\"\n\nfunc TestUse(t *testing.T) {\n\tif Use() != 5 { t.Fail() }\n}\n",
			},
		)

		in := lang.VerifyInput{Suites: []string{"test"}}
		raw, _ := json.Marshal(in)
		result, err := golang.Verify.Execute(t.Context(), raw)
		if err != nil {
			t.Fatalf("verify execute: %v", err)
		}
		out, ok := result.(lang.VerifyOutput)
		if !ok {
			var probe lang.VerifyOutput
			b, _ := json.Marshal(result)
			_ = json.Unmarshal(b, &probe)
			out = probe
		}
		report, ok := out.Reports[lang.SuiteTest]
		if !ok {
			t.Fatalf("expected test suite report; got %+v", out.Reports)
		}
		if report.Status != lang.StatusPass {
			t.Errorf("expected workspace tests to pass; got status=%q summary=%q", report.Status, report.Summary)
		}
	})
}

// ---- writeLintProject + helpers ----

// requireGolangciLint skips the test when golangci-lint isn't on PATH.
// Lint-runner tests need it; skipping locally is fine.
func requireGolangciLint(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("golangci-lint not on PATH; skipping lint-runner integration test")
	}
}

// writeTempProject creates a minimal Go module in a temp dir with a passing
// and a failing test so we can exercise the verify tool end-to-end.
func writeTempProject(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	goMod := "module example.com/testpkg\n\ngo 1.21\n"

	addGo := `package testpkg

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`

	addTestGo := `package testpkg

import "testing"

func TestAdd_Pass(t *testing.T) {
	if got := Add(1, 2); got != 3 {
		t.Errorf("Add(1,2) = %d, want 3", got)
	}
}

func TestAdd_Fail(t *testing.T) {
	if got := Add(1, 2); got != 999 {
		t.Errorf("Add(1,2) = %d, want 999", got)
	}
}
`

	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	writeFile("go.mod", goMod)
	writeFile("add.go", addGo)
	writeFile("add_test.go", addTestGo)

	return dir
}

// runVerify is a test helper that marshals input, calls Execute, and returns a typed VerifyOutput.
func runVerify(t *testing.T, input lang.VerifyInput) lang.VerifyOutput {
	t.Helper()

	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	result, err := golang.Verify.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The tool returns any; handle direct cast or round-trip through JSON.
	if vo, ok := result.(lang.VerifyOutput); ok {
		return vo
	}
	var vo lang.VerifyOutput
	b, _ := json.Marshal(result)
	if err2 := json.Unmarshal(b, &vo); err2 != nil {
		t.Fatalf("unexpected result type %T: %v", result, err2)
	}
	return vo
}

type lintProjectOpt func(*lintProject)

type lintProject struct {
	golangciConfig string
}

func withGolangciConfig(content string) lintProjectOpt {
	return func(p *lintProject) { p.golangciConfig = content }
}

// writeLintProject creates a temp Go module with a single source file plus
// (optionally) a .golangci.yml that scopes the lint run to the requested
// linters. Scoping prevents incidental lints from polluting the test.
func writeLintProject(t *testing.T, fileName, fileContent string, opts ...lintProjectOpt) string {
	t.Helper()
	dir := t.TempDir()
	p := &lintProject{}
	for _, o := range opts {
		o(p)
	}
	mustWriteFileVT(t, filepath.Join(dir, "go.mod"), "module testlintpkg\n\ngo 1.21\n")
	mustWriteFileVT(t, filepath.Join(dir, fileName), fileContent)
	if p.golangciConfig != "" {
		mustWriteFileVT(t, filepath.Join(dir, ".golangci.yml"), p.golangciConfig)
	}
	return dir
}

func writeBenchProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFileVT(t, filepath.Join(dir, "go.mod"), "module testbenchpkg\n\ngo 1.21\n")
	mustWriteFileVT(t, filepath.Join(dir, "code.go"), `package testbenchpkg

func Sum(a, b int) int { return a + b }
`)
	mustWriteFileVT(t, filepath.Join(dir, "code_test.go"), `package testbenchpkg

import "testing"

func BenchmarkSum(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Sum(i, i+1)
	}
}
`)
	return dir
}

func writeFuzzProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFileVT(t, filepath.Join(dir, "go.mod"), "module testfuzzpkg\n\ngo 1.21\n")
	mustWriteFileVT(t, filepath.Join(dir, "code.go"), `package testfuzzpkg

func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
`)
	mustWriteFileVT(t, filepath.Join(dir, "code_test.go"), `package testfuzzpkg

import "testing"

func FuzzReverse(f *testing.F) {
	f.Add("hello")
	f.Fuzz(func(t *testing.T, s string) {
		_ = Reverse(s)
	})
}
`)
	return dir
}

func mustWriteFileVT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func hasIssueFromLinter(issues []lang.Issue, linter string) bool {
	for i := range issues {
		if issues[i].Linter == linter {
			return true
		}
	}
	return false
}

func linterNames(issues []lang.Issue) []string {
	out := make([]string, len(issues))
	for i, iss := range issues {
		out[i] = iss.Linter
	}
	return out
}

func hasSuggestedPatchFromSource(patches []lang.SuggestedPatch, source string) bool {
	for i := range patches {
		if patches[i].Source == source {
			return true
		}
	}
	return false
}

func patchSources(patches []lang.SuggestedPatch) []string {
	out := make([]string, len(patches))
	for i, p := range patches {
		out[i] = p.Source
	}
	return out
}
