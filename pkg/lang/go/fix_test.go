// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
)

func TestFix(t *testing.T) {
	t.Run("no issues returns clean result with no patches applied", func(t *testing.T) {
		if _, err := exec.LookPath("golangci-lint"); err != nil {
			t.Skip("golangci-lint not on PATH")
		}
		dir := writeLintProject(t, "clean.go", `package testpkg

func A() int { return 1 }
`, withGolangciConfig(`version: "2"
linters:
  default: none
  enable:
    - errcheck
`))
		out := executeFix(t, lang.FixInput{Targets: []string{dir}})

		if out.InitialVerify == nil {
			t.Fatal("expected InitialVerify populated")
		}
		if out.Applied != 0 {
			t.Errorf("expected 0 applied (no issues); got %d", out.Applied)
		}
		if out.FinalVerify != nil {
			t.Errorf("expected no FinalVerify when no patches were applied; got %+v", out.FinalVerify)
		}
	})

	t.Run("applies suggested patch and re-verifies", func(t *testing.T) {
		if _, err := exec.LookPath("golangci-lint"); err != nil {
			t.Skip("golangci-lint not on PATH")
		}
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
		out := executeFix(t, lang.FixInput{Targets: []string{dir}})

		if out.InitialVerify == nil {
			t.Fatal("expected InitialVerify populated")
		}
		report, ok := out.InitialVerify.Reports[lang.SuiteLint]
		if !ok || len(report.Issues) == 0 {
			t.Fatalf("expected initial verify to detect lint issues; got %+v", out.InitialVerify)
		}
		if out.Applied+out.Failed == 0 {
			t.Fatalf("expected at least one patch attempted; applied=%d failed=%d", out.Applied, out.Failed)
		}
	})

	t.Run("dry run skips re-verify and leaves disk untouched", func(t *testing.T) {
		if _, err := exec.LookPath("golangci-lint"); err != nil {
			t.Skip("golangci-lint not on PATH")
		}
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
		out := executeFix(t, lang.FixInput{
			Targets: []string{dir},
			DryRun:  true,
		})

		if out.InitialVerify == nil {
			t.Fatal("expected InitialVerify populated even in dry-run")
		}
		if out.FinalVerify != nil {
			t.Errorf("expected no FinalVerify when DryRun=true; got %+v", out.FinalVerify)
		}
		body := mustReadFile(t, filepath.Join(dir, "errcheck.go"))
		if !strings.Contains(body, `_ = os.Remove`) {
			t.Errorf("dry-run should not modify source; got:\n%s", body)
		}
	})

	// Regression: synthetic errcheck patch was preserving `=` instead of
	// upgrading to `:=`, producing `err = os.Remove(...)` with an undeclared
	// identifier. The fix in buildErrcheckPatchAssign upgrades to `:=` when
	// err isn't in scope yet.
	t.Run("errcheck synthetic patch produces compiling code with := declaration", func(t *testing.T) {
		if _, err := exec.LookPath("golangci-lint"); err != nil {
			t.Skip("golangci-lint not on PATH")
		}
		dir := writeLintProject(t, "errcheck.go", `package testpkg

import "os"

func WriteConfig() {
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

		out := executeFix(t, lang.FixInput{Targets: []string{dir}})

		if out.Applied == 0 {
			t.Fatalf("expected at least 1 applied patch; got applied=%d failed=%d", out.Applied, out.Failed)
		}
		if out.FinalVerify == nil {
			t.Fatal("expected FinalVerify populated after applied patches")
		}
		finalReport, ok := out.FinalVerify.Reports[lang.SuiteLint]
		if !ok {
			t.Fatal("expected lint report in FinalVerify")
		}
		if finalReport.Status != lang.StatusPass {
			t.Errorf(
				"expected post-fix lint to pass; got status=%q summary=%q",
				finalReport.Status,
				finalReport.Summary,
			)
		}
		body := mustReadFile(t, filepath.Join(dir, "errcheck.go"))
		if !strings.Contains(body, "err :=") {
			t.Errorf("expected synthetic patch to use `err :=` (declaration); got:\n%s", body)
		}
		if strings.Contains(body, "err = os.Remove") {
			t.Errorf("regression: synthetic patch produced `err = os.Remove` (undeclared err); got:\n%s", body)
		}
	})
}

// ---- helpers ----

func executeFix(t *testing.T, in lang.FixInput) lang.FixOutput {
	t.Helper()
	raw, _ := json.Marshal(in)
	result, err := golang.Fix.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("fix execute: %v", err)
	}
	if v, ok := result.(lang.FixOutput); ok {
		return v
	}
	var out lang.FixOutput
	b, _ := json.Marshal(result)
	_ = json.Unmarshal(b, &out)
	return out
}
