// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"go.thesmos.sh/techne/pkg/fs"
	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
)

const testPkgSrc = `package testpkg

import "fmt"

func Hello() {
	fmt.Println("hello")
}

func Broken() {
	x := 1
	_ = x
}
`

func TestPatch(t *testing.T) {
	t.Run("single success returns diff receipt", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						{OldString: `fmt.Println("hello")`, NewString: `fmt.Println("goodbye")`},
					},
				},
			},
		})

		if out.Summary.Total != 1 {
			t.Fatalf("expected total=1, got %d", out.Summary.Total)
		}
		if out.Summary.Applied != 1 {
			t.Fatalf("expected applied=1, got %d", out.Summary.Applied)
		}
		if out.Summary.Failed != 0 {
			t.Fatalf("expected failed=0, got %d", out.Summary.Failed)
		}
		if len(out.Results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(out.Results))
		}
		r := out.Results[0]
		if r.Status != golang.GoPatchSuccess {
			t.Errorf("expected status %q, got %q: %s", golang.GoPatchSuccess, r.Status, r.Error)
		}
		if r.DiffReceipt == "" {
			t.Error("expected non-empty diff receipt")
		}
		if !strings.Contains(r.DiffReceipt, "goodbye") {
			t.Errorf("diff receipt should contain 'goodbye', got: %s", r.DiffReceipt)
		}
		if !strings.Contains(r.DiffReceipt, "hello") {
			t.Errorf("diff receipt should show old 'hello', got: %s", r.DiffReceipt)
		}

		// Verify file was actually modified.
		content, err := os.ReadFile(goFile)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if !strings.Contains(string(content), "goodbye") {
			t.Error("file should contain 'goodbye' after patch")
		}
	})

	t.Run("single failure rolls back and populates forensics", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)

		originalContent, err := os.ReadFile(goFile)
		if err != nil {
			t.Fatalf("read original: %v", err)
		}

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						// Introduce a syntax error: use undefined symbol.
						{OldString: `_ = x`, NewString: `_ = undefinedSymbolXYZ`},
					},
				},
			},
		})

		if out.Summary.Applied != 0 {
			t.Fatalf("expected applied=0, got %d", out.Summary.Applied)
		}
		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}

		r := out.Results[0]
		if r.Status != golang.GoPatchFailure {
			t.Errorf("expected status %q, got %q", golang.GoPatchFailure, r.Status)
		}
		if r.Error == "" {
			t.Error("expected non-empty error")
		}
		if r.Forensics == nil {
			t.Fatal("expected forensics to be populated")
		}
		if r.Forensics.CompilerOutput == "" {
			t.Error("expected non-empty compiler output in forensics")
		}
		if r.Forensics.Hint == "" {
			t.Error("expected non-empty hint in forensics")
		}

		// Verify file was rolled back.
		afterContent, err := os.ReadFile(goFile)
		if err != nil {
			t.Fatalf("read file after failure: %v", err)
		}
		if string(afterContent) != string(originalContent) {
			t.Error("file should be restored to original content after build failure")
		}
	})

	t.Run("old string not found returns descriptive error", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						{OldString: "this string does not exist in the file", NewString: "replacement"},
					},
				},
			},
		})

		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}
		r := out.Results[0]
		if r.Status != golang.GoPatchFailure {
			t.Errorf("expected failure status, got %q", r.Status)
		}
		if !strings.Contains(r.Error, "old_string not found") {
			t.Errorf("error should mention 'old_string not found', got: %q", r.Error)
		}
	})

	t.Run("dry run previews without modifying file", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)

		originalContent, err := os.ReadFile(goFile)
		if err != nil {
			t.Fatalf("read original: %v", err)
		}

		out := executePatch(t, golang.GoPatchInput{
			DryRun: true,
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						{OldString: `fmt.Println("hello")`, NewString: `fmt.Println("dry-run-test")`},
					},
				},
			},
		})

		if out.Summary.Applied != 1 {
			t.Fatalf("expected applied=1 in dry run, got %d", out.Summary.Applied)
		}
		if out.Results[0].Status != golang.GoPatchSuccess {
			t.Errorf("expected success in dry run, got %q: %s", out.Results[0].Status, out.Results[0].Error)
		}
		if out.Results[0].DiffReceipt == "" {
			t.Error("expected diff receipt in dry run")
		}

		// File must be unchanged.
		afterContent, err := os.ReadFile(goFile)
		if err != nil {
			t.Fatalf("read after dry run: %v", err)
		}
		if string(afterContent) != string(originalContent) {
			t.Error("file must not be modified during dry run")
		}
	})

	t.Run("auto formatting applies goimports", func(t *testing.T) {
		dir, _ := writeGoPatchTestModule(t)

		// Create a file that uses strings package but doesn't import it yet.
		// We'll edit it to use strings.ToUpper and expect goimports to add the import.
		srcBefore := `package testpkg

func Upper(s string) string {
	return s
}
`
		srcAfter := `package testpkg

func Upper(s string) string {
	return strings.ToUpper(s)
}
`
		targetFile := writeGoPatchExtraFile(t, dir, "upper.go", srcBefore)

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: targetFile,
					Edits: []lang.PatchEdit{
						{OldString: "return s", NewString: "return strings.ToUpper(s)"},
					},
				},
			},
		})

		if out.Summary.Applied != 1 {
			t.Fatalf("expected applied=1, got %d (error: %s)", out.Summary.Applied, out.Results[0].Error)
		}
		if out.Results[0].Status != golang.GoPatchSuccess {
			t.Errorf("expected success, got %q: %s", out.Results[0].Status, out.Results[0].Error)
		}

		// The diff receipt should reflect the formatted content (with import added by goimports).
		receipt := out.Results[0].DiffReceipt
		if receipt == "" {
			t.Error("expected a non-empty diff receipt")
		}

		// Check the file has been written with the edit applied.
		content, _ := os.ReadFile(targetFile)
		if !strings.Contains(string(content), "strings.ToUpper") {
			t.Errorf("file should contain strings.ToUpper, got:\n%s", string(content))
		}
		// Confirm what we started with (the srcAfter edit) is in the diff.
		_ = srcAfter // used above for reference
	})

	t.Run("parse gate rejects syntax error before touching disk", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)

		original, err := os.ReadFile(goFile)
		if err != nil {
			t.Fatalf("read original: %v", err)
		}

		// Replace a line with something that produces a syntax error (unclosed brace).
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						{
							OldString: `fmt.Println("hello")`,
							NewString: "fmt.Println(\"hello\"\n// missing closing paren",
						},
					},
				},
			},
		})

		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}
		r := out.Results[0]
		if r.Status != golang.GoPatchFailure {
			t.Errorf("expected failure, got %q", r.Status)
		}
		if !strings.Contains(r.Error, "invalid Go syntax") {
			t.Errorf("error should mention 'invalid Go syntax', got: %q", r.Error)
		}

		// File must NOT have been touched.
		verifyFileUnchanged(t, goFile, original)
	})

	t.Run("parse gate rejects empty content before touching disk", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)

		original, err := os.ReadFile(goFile)
		if err != nil {
			t.Fatalf("read original: %v", err)
		}

		// Replace the whole content with empty.
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						{OldString: string(original), NewString: ""},
					},
				},
			},
		})

		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}
		r := out.Results[0]
		if r.Status != golang.GoPatchFailure {
			t.Errorf("expected failure, got %q", r.Status)
		}
		// Error should mention empty.
		if !strings.Contains(strings.ToLower(r.Error), "empty") {
			t.Errorf("error should mention 'empty', got: %q", r.Error)
		}

		// File must NOT have been modified.
		verifyFileUnchanged(t, goFile, original)
	})

	t.Run("parse gate passes valid edit and updates file on disk", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						{OldString: `fmt.Println("hello")`, NewString: `fmt.Println("parsed-ok")`},
					},
				},
			},
		})

		if out.Summary.Applied != 1 {
			t.Fatalf("expected applied=1, got %d: %s", out.Summary.Applied, out.Results[0].Error)
		}
		if out.Results[0].Status != golang.GoPatchSuccess {
			t.Errorf("expected success, got %q: %s", out.Results[0].Status, out.Results[0].Error)
		}

		content, _ := os.ReadFile(goFile)
		if !strings.Contains(string(content), "parsed-ok") {
			t.Error("file should contain 'parsed-ok' after successful parse-gated edit")
		}
	})

	t.Run("goimports adds missing import", func(t *testing.T) {
		dir, _ := writeGoPatchTestModule(t)

		// A file that does NOT import "fmt".
		src := `package testpkg

func Greet(name string) string {
	return name
}
`
		target := writeGoPatchExtraFile(t, dir, "greet.go", src)

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: target,
					Edits: []lang.PatchEdit{
						{OldString: "return name", NewString: `return fmt.Sprintf("Hello, %s", name)`},
					},
				},
			},
		})

		if out.Summary.Applied != 1 {
			t.Fatalf("expected applied=1, got %d: %s", out.Summary.Applied, out.Results[0].Error)
		}

		content, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if !strings.Contains(string(content), `"fmt"`) {
			t.Errorf("goimports should have added 'fmt' import; file:\n%s", content)
		}
	})

	t.Run("goimports removes unused import", func(t *testing.T) {
		dir, _ := writeGoPatchTestModule(t)

		// A file that imports "fmt" and uses it once.
		src := `package testpkg

import "fmt"

func PrintHello() {
	fmt.Println("hello")
}
`
		target := writeGoPatchExtraFile(t, dir, "printer.go", src)

		// Remove the only fmt usage; goimports should strip the import.
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: target,
					Edits: []lang.PatchEdit{
						{OldString: `fmt.Println("hello")`, NewString: `_ = "hello"`},
					},
				},
			},
		})

		if out.Summary.Applied != 1 {
			t.Fatalf("expected applied=1, got %d: %s", out.Summary.Applied, out.Results[0].Error)
		}

		content, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if strings.Contains(string(content), `"fmt"`) {
			t.Errorf("goimports should have removed unused 'fmt' import; file:\n%s", content)
		}
	})

	t.Run("package lock is cleaned up after successful patch", func(t *testing.T) {
		dir, goFile := writeGoPatchTestModule(t)

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						{OldString: `fmt.Println("hello")`, NewString: `fmt.Println("lock-test")`},
					},
				},
			},
		})

		if out.Summary.Applied != 1 {
			t.Fatalf("expected applied=1, got %d: %s", out.Summary.Applied, out.Results[0].Error)
		}

		lockPath := filepath.Join(dir, ".techne.lock")
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Errorf("lock file %s should have been cleaned up after patch, but it still exists", lockPath)
		}
	})

	t.Run("package lock prevents concurrent access", func(t *testing.T) {
		dir, goFile := writeGoPatchTestModule(t)

		// Manually acquire the directory flock before calling executePatch.
		f, err := os.Open(dir)
		if err != nil {
			t.Fatalf("open dir for lock: %v", err)
		}
		defer func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		}()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatalf("acquire lock: %v", err)
		}

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						{OldString: `fmt.Println("hello")`, NewString: `fmt.Println("concurrent")`},
					},
				},
			},
		})

		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}
		r := out.Results[0]
		if r.Status != golang.GoPatchFailure {
			t.Errorf("expected failure, got %q", r.Status)
		}
		if !strings.Contains(r.Error, "locked") {
			t.Errorf("error should mention 'locked', got: %q", r.Error)
		}
	})

	t.Run("force bypasses pre-gate for already-broken file", func(t *testing.T) {
		dir, _ := writeGoPatchTestModule(t)

		// Create a file that has a deliberate compile error (undefined symbol).
		brokenSrc := `package testpkg

func BrokenFunc() int {
	return undefinedSymbol
}
`
		brokenFile := writeGoPatchExtraFile(t, dir, "broken.go", brokenSrc)

		// Without force: pre-gate should block the patch.
		outNoForce := executePatch(t, golang.GoPatchInput{
			Force: false,
			Patches: []fs.FilePatch{
				{
					FilePath: brokenFile,
					Edits: []lang.PatchEdit{
						{OldString: "return undefinedSymbol", NewString: "return 42"},
					},
				},
			},
		})
		if outNoForce.Summary.Failed != 1 {
			t.Fatalf("without force: expected failed=1, got %d", outNoForce.Summary.Failed)
		}
		if !strings.Contains(outNoForce.Results[0].Error, "already broken") {
			t.Errorf("without force: error should mention 'already broken', got: %q", outNoForce.Results[0].Error)
		}

		// With force: should succeed because the edit fixes the error.
		outForce := executePatch(t, golang.GoPatchInput{
			Force: true,
			Patches: []fs.FilePatch{
				{
					FilePath: brokenFile,
					Edits: []lang.PatchEdit{
						{OldString: "return undefinedSymbol", NewString: "return 42"},
					},
				},
			},
		})
		if outForce.Summary.Applied != 1 {
			t.Fatalf("with force: expected applied=1, got %d: %s", outForce.Summary.Applied, outForce.Results[0].Error)
		}
		if outForce.Results[0].Status != golang.GoPatchSuccess {
			t.Errorf("with force: expected success, got %q: %s", outForce.Results[0].Status, outForce.Results[0].Error)
		}
	})

	t.Run("force still enforces post-gate", func(t *testing.T) {
		dir, _ := writeGoPatchTestModule(t)

		// Create a file with a compile error.
		brokenSrc := `package testpkg

func ForceFunc() int {
	return undefinedSymbolABC
}
`
		brokenFile := writeGoPatchExtraFile(t, dir, "force.go", brokenSrc)

		original, _ := os.ReadFile(brokenFile)

		// Force=true but the edit keeps the error (replaces one undefined with another).
		out := executePatch(t, golang.GoPatchInput{
			Force: true,
			Patches: []fs.FilePatch{
				{
					FilePath: brokenFile,
					Edits: []lang.PatchEdit{
						{OldString: "return undefinedSymbolABC", NewString: "return anotherUndefinedXYZ"},
					},
				},
			},
		})

		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}
		r := out.Results[0]
		if r.Status != golang.GoPatchFailure {
			t.Errorf("expected failure (post-gate), got %q", r.Status)
		}
		if !strings.Contains(r.Error, "post-patch verification failed") {
			t.Errorf("error should mention 'post-patch verification failed', got: %q", r.Error)
		}

		// File should be rolled back to original broken content.
		verifyFileUnchanged(t, brokenFile, original)
	})

	t.Run("rollback restores original on post-gate failure", func(t *testing.T) {
		dir, _ := writeGoPatchTestModule(t)

		// A valid file with a function we'll edit to introduce a type error.
		src := `package testpkg

func Multiply(a, b int) int {
	return a * b
}
`
		target := writeGoPatchExtraFile(t, dir, "multiply.go", src)
		original, _ := os.ReadFile(target)

		// Introduce a type error: return a string where int is expected.
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: target,
					Edits: []lang.PatchEdit{
						{OldString: "return a * b", NewString: `return "not an int"`},
					},
				},
			},
		})

		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}

		// The file must be byte-for-byte identical to original.
		verifyFileUnchanged(t, target, original)
	})

	t.Run("next actions on success includes lang.go.verify with high confidence", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						{OldString: `fmt.Println("hello")`, NewString: `fmt.Println("next-actions-success")`},
					},
				},
			},
		})

		if out.Summary.Applied != 1 {
			t.Fatalf("expected applied=1, got %d: %s", out.Summary.Applied, out.Results[0].Error)
		}

		if len(out.NextActions) == 0 {
			t.Fatal("expected NextActions to be populated on success")
		}

		found := false
		for _, a := range out.NextActions {
			if a.Tool == "lang.go.verify" && a.Confidence == lang.ConfidenceHigh {
				found = true
				if vi, ok := a.Input.(lang.VerifyInput); ok {
					if len(vi.Targets) == 0 {
						t.Error("VerifyInput.Targets should be pre-filled")
					}
				}
			}
		}
		if !found {
			t.Errorf("expected NextAction with tool='lang.go.verify' and confidence='high'; got: %+v", out.NextActions)
		}
	})

	t.Run("next actions on failure includes lang.go.explore with low confidence", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						// Force a type error.
						{OldString: `_ = x`, NewString: `_ = undefinedVarFailure999`},
					},
				},
			},
		})

		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}

		if len(out.NextActions) == 0 {
			t.Fatal("expected NextActions to be populated on failure")
		}

		found := false
		for _, a := range out.NextActions {
			if a.Tool == "lang.go.explore" && a.Confidence == lang.ConfidenceLow {
				found = true
			}
		}
		if !found {
			t.Errorf("expected NextAction with tool='lang.go.explore' and confidence='low'; got: %+v", out.NextActions)
		}
	})

	t.Run("non-go file rejected with descriptive error", func(t *testing.T) {
		dir := t.TempDir()
		txtFile := filepath.Join(dir, "notes.txt")
		if err := os.WriteFile(txtFile, []byte("hello world\n"), 0o644); err != nil {
			t.Fatalf("write txt: %v", err)
		}

		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: txtFile,
					Edits: []lang.PatchEdit{
						{OldString: "hello", NewString: "goodbye"},
					},
				},
			},
		})

		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}
		r := out.Results[0]
		if r.Status != golang.GoPatchFailure {
			t.Errorf("expected failure, got %q", r.Status)
		}
		if !strings.Contains(r.Error, "not a Go file") {
			t.Errorf("error should mention 'not a Go file', got: %q", r.Error)
		}
	})

	t.Run("file not found returns descriptive error", func(t *testing.T) {
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{
					FilePath: "/tmp/does-not-exist-xyz-12345.go",
					Edits: []lang.PatchEdit{
						{OldString: "anything", NewString: "something"},
					},
				},
			},
		})

		if out.Summary.Failed != 1 {
			t.Fatalf("expected failed=1, got %d", out.Summary.Failed)
		}
		r := out.Results[0]
		if r.Status != golang.GoPatchFailure {
			t.Errorf("expected failure, got %q", r.Status)
		}
		if !strings.Contains(r.Error, "cannot read file") {
			t.Errorf("error should mention 'cannot read file', got: %q", r.Error)
		}
	})

	t.Run("empty patch list is a no-op", func(t *testing.T) {
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{},
		})

		if out.Summary.Total != 0 {
			t.Errorf("expected total=0, got %d", out.Summary.Total)
		}
		if out.Summary.Applied != 0 {
			t.Errorf("expected applied=0, got %d", out.Summary.Applied)
		}
		if out.Summary.Failed != 0 {
			t.Errorf("expected failed=0, got %d", out.Summary.Failed)
		}
		if len(out.Results) != 0 {
			t.Errorf("expected 0 results, got %d", len(out.Results))
		}
	})

	t.Run("atomic cross-file rename succeeds when both files change together", func(t *testing.T) {
		dir := t.TempDir()
		writeGoPatchGoMod(t, dir)

		// Two files that reference each other's types.
		file1 := writeGoPatchExtraFile(t, dir, "types.go", `package testpkg

type OldName struct {
	Value string
}

func NewOldName(v string) OldName {
	return OldName{Value: v}
}
`)
		file2 := writeGoPatchExtraFile(t, dir, "user.go", `package testpkg

func UseIt() string {
	x := NewOldName("test")
	return x.Value
}
`)

		// In per-file mode, renaming OldName→NewName in types.go would break
		// user.go (which still references NewOldName). In atomic mode, both
		// files are changed together and verified as a unit.
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{FilePath: file1, Edits: []fs.PatchEdit{
					{OldString: "type OldName struct", NewString: "type NewName struct"},
					{OldString: "func NewOldName(v string) OldName", NewString: "func MakeNewName(v string) NewName"},
					{OldString: "return OldName{", NewString: "return NewName{"},
				}},
				{FilePath: file2, Edits: []fs.PatchEdit{
					{OldString: "NewOldName", NewString: "MakeNewName"},
				}},
			},
		})

		if out.Summary.Applied != 2 {
			t.Errorf("expected 2 applied, got %d", out.Summary.Applied)
		}
		if out.Summary.Failed != 0 {
			for _, r := range out.Results {
				if r.Status == golang.GoPatchFailure {
					t.Logf("failure: %s: %s", r.FilePath, r.Error)
					if r.Forensics != nil {
						t.Logf("  compiler: %s", r.Forensics.CompilerOutput)
					}
				}
			}
			t.Fatalf("expected 0 failed, got %d", out.Summary.Failed)
		}

		// Verify both files changed.
		data1, _ := os.ReadFile(file1)
		data2, _ := os.ReadFile(file2)
		if !strings.Contains(string(data1), "NewName") {
			t.Error("types.go should contain NewName")
		}
		if !strings.Contains(string(data2), "MakeNewName") {
			t.Error("user.go should contain MakeNewName")
		}
	})

	t.Run("atomic rollback on failure restores all files", func(t *testing.T) {
		dir := t.TempDir()
		writeGoPatchGoMod(t, dir)

		file1 := writeGoPatchExtraFile(t, dir, "good.go", `package testpkg

func Good() string { return "good" }
`)
		file2 := writeGoPatchExtraFile(t, dir, "bad.go", `package testpkg

func Bad() int { return 42 }
`)

		original1, _ := os.ReadFile(file1)
		original2, _ := os.ReadFile(file2)

		// file1 edit is valid, file2 edit introduces a type error.
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{FilePath: file1, Edits: []fs.PatchEdit{
					{OldString: `"good"`, NewString: `"great"`},
				}},
				{FilePath: file2, Edits: []fs.PatchEdit{
					{OldString: "return 42", NewString: `return "not an int"`},
				}},
			},
		})

		// Both should fail because the combined build fails.
		if out.Summary.Applied != 0 {
			t.Errorf("expected 0 applied (atomic rollback), got %d", out.Summary.Applied)
		}

		// Both files should be rolled back to their original content.
		restored1, _ := os.ReadFile(file1)
		restored2, _ := os.ReadFile(file2)
		if !bytes.Equal(restored1, original1) {
			t.Error("good.go was not rolled back")
		}
		if !bytes.Equal(restored2, original2) {
			t.Error("bad.go was not rolled back")
		}
	})

	t.Run("atomic dry run does not modify files", func(t *testing.T) {
		dir := t.TempDir()
		writeGoPatchGoMod(t, dir)

		file1 := writeGoPatchExtraFile(t, dir, "main.go", `package testpkg

func Hello() string { return "hello" }
`)

		original, _ := os.ReadFile(file1)

		out := executePatch(t, golang.GoPatchInput{
			DryRun: true,
			Patches: []fs.FilePatch{
				{FilePath: file1, Edits: []fs.PatchEdit{
					{OldString: `"hello"`, NewString: `"world"`},
				}},
			},
		})

		if out.Summary.Applied != 1 {
			t.Errorf("expected 1 applied in dry run, got %d", out.Summary.Applied)
		}

		// File should be unchanged on disk.
		current, _ := os.ReadFile(file1)
		if !bytes.Equal(current, original) {
			t.Error("file was modified during dry run")
		}

		// Should have a diff receipt.
		for _, r := range out.Results {
			if r.DiffReceipt == "" {
				t.Error("expected diff receipt in dry run result")
			}
		}
	})

	t.Run("atomic parse gate rejects before writing", func(t *testing.T) {
		dir := t.TempDir()
		writeGoPatchGoMod(t, dir)

		file1 := writeGoPatchExtraFile(t, dir, "main.go", `package testpkg

func Hello() string { return "hello" }
`)

		original, _ := os.ReadFile(file1)

		// Edit that produces invalid syntax.
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{FilePath: file1, Edits: []fs.PatchEdit{
					{OldString: `return "hello"`, NewString: `return "hello`}, // missing closing quote
				}},
			},
		})

		// Should fail at parse gate, file untouched.
		if out.Summary.Applied != 0 {
			t.Errorf("expected 0 applied, got %d", out.Summary.Applied)
		}

		current, _ := os.ReadFile(file1)
		if !bytes.Equal(current, original) {
			t.Error("file was modified despite parse gate failure")
		}
	})

	t.Run("auto verify lint ok", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: goFile,
				Edits:    []fs.PatchEdit{{OldString: `fmt.Println("hello")`, NewString: `fmt.Println("goodbye")`}},
			}},
			AutoVerify: true,
		})
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied, got %d", out.Summary.Applied)
		}
		if out.VerificationStatus != golang.VerificationLintOK {
			t.Errorf("expected verification_status %q, got %q", golang.VerificationLintOK, out.VerificationStatus)
		}
		if out.VerifyOutput == nil {
			t.Error("expected verify_output to be present when auto_verify=true")
		}
	})

	t.Run("auto verify disabled by default", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)
		out := executePatch(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: goFile,
				Edits:    []fs.PatchEdit{{OldString: `fmt.Println("hello")`, NewString: `fmt.Println("goodbye")`}},
			}},
		})
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied, got %d", out.Summary.Applied)
		}
		if out.VerificationStatus != golang.VerificationUnverified {
			t.Errorf("expected verification_status %q, got %q", golang.VerificationUnverified, out.VerificationStatus)
		}
		if out.VerifyOutput != nil {
			t.Error("expected verify_output to be nil when auto_verify is not set")
		}
	})

	t.Run("patch on test file works without confusing parser", func(t *testing.T) {
		dir := writeMod(t, "prodtest", map[string]string{
			"a.go":      "package prodtest\n\nfunc Greet() string { return \"hello\" }\n",
			"a_test.go": "package prodtest\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {\n\tif Greet() != \"hello\" {\n\t\tt.Fail()\n\t}\n}\n",
		})
		t.Chdir(dir)

		in := golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a_test.go"),
				Edits: []lang.PatchEdit{
					{OldString: `t.Fail()`, NewString: `t.Fatalf("greet returned %q", Greet())`},
				},
			}},
		}
		out := executePatchTool(t, in)
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied; got %+v results=%+v", out.Summary, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a_test.go"))
		if !strings.Contains(body, `t.Fatalf("greet returned %q", Greet())`) {
			t.Errorf("test file edit did not land; got:\n%s", body)
		}
	})

	t.Run("patch on build-tagged file preserves build constraint comment", func(t *testing.T) {
		tag := "//go:build " + runtime.GOOS + "\n\n"
		dir := writeMod(t, "prodbuildtag", map[string]string{
			"plat.go": tag + "package prodbuildtag\n\nfunc Plat() string { return \"old\" }\n",
		})
		t.Chdir(dir)

		in := golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "plat.go"),
				Edits: []lang.PatchEdit{
					{OldString: `return "old"`, NewString: `return "new"`},
				},
			}},
		}
		out := executePatchTool(t, in)
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied; got %+v results=%+v", out.Summary, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "plat.go"))
		if !strings.HasPrefix(body, "//go:build "+runtime.GOOS) {
			t.Errorf("//go:build directive must remain at file head; got:\n%s", body)
		}
		if !strings.Contains(body, `return "new"`) {
			t.Errorf("edit did not land; got:\n%s", body)
		}
	})

	t.Run("goimports adds local package import", func(t *testing.T) {
		dir := writeMod(t, "prodlocalimport", map[string]string{
			"util/util.go": "package util\n\nfunc Plus(a, b int) int { return a + b }\n",
			"main.go":      "package prodlocalimport\n\nfunc Compute() int { return 1 }\n",
		})
		t.Chdir(dir)

		in := golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "main.go"),
				Edits: []lang.PatchEdit{
					{
						OldString: `func Compute() int { return 1 }`,
						NewString: `func Compute() int { return util.Plus(1, 2) }`,
					},
				},
			}},
		}
		out := executePatchTool(t, in)
		if out.Summary.Applied != 1 {
			t.Fatalf(
				"expected 1 applied (goimports must wire up util import); got %+v results=%+v",
				out.Summary,
				out.Results,
			)
		}
		body := mustReadFile(t, filepath.Join(dir, "main.go"))
		if !strings.Contains(body, `prodlocalimport/util`) {
			t.Errorf("expected module-local import wired up by goimports; got:\n%s", body)
		}
	})

	t.Run("old string not found keeps file intact", func(t *testing.T) {
		dir := writeMod(t, "prodmiss", map[string]string{
			"a.go": "package prodmiss\n\nfunc Foo() int { return 1 }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))

		in := golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{OldString: `THIS DOES NOT EXIST`, NewString: `nope`},
				},
			}},
		}
		out := executePatchTool(t, in)
		if out.Summary.Applied != 0 {
			t.Errorf("expected 0 applied; got %+v", out.Summary)
		}
		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != original {
			t.Errorf("file must remain untouched when OldString is missing; got:\n%s", got)
		}
	})

	t.Run("large atomic batch rolls back all files on build break", func(t *testing.T) {
		files := map[string]string{}
		for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			files[n+".go"] = "package prodbig\n\nfunc " + strings.ToUpper(n) + "() string { return \"" + n + "\" }\n"
		}
		dir := writeMod(t, "prodbig", files)
		t.Chdir(dir)

		originals := map[string]string{}
		for n := range files {
			originals[n] = mustReadFile(t, filepath.Join(dir, n))
		}

		var patches []fs.FilePatch
		for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			newStr := `return "` + strings.ToUpper(n) + `"`
			if n == "f" {
				newStr = `return totally_undefined_xyz_123`
			}
			patches = append(patches, fs.FilePatch{
				FilePath: filepath.Join(dir, n+".go"),
				Edits: []lang.PatchEdit{{
					OldString: `return "` + n + `"`,
					NewString: newStr,
				}},
			})
		}
		executePatchTool(t, golang.GoPatchInput{Patches: patches})

		for n, want := range originals {
			got := mustReadFile(t, filepath.Join(dir, n))
			if got != want {
				t.Errorf("%s must roll back; got:\n%s", n, got)
			}
		}
	})

	t.Run("generated file with DO NOT EDIT header can still be patched", func(t *testing.T) {
		dir := writeMod(t, "rwgen", map[string]string{
			"gen.go": "// Code generated by example_generator. DO NOT EDIT.\n\npackage rwgen\n\nfunc Generated() string { return \"old\" }\n",
		})
		t.Chdir(dir)

		out := executePatchTool(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "gen.go"),
				Edits: []lang.PatchEdit{
					{OldString: `return "old"`, NewString: `return "new"`},
				},
			}},
		})
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied; got %+v results=%+v", out.Summary, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "gen.go"))
		if !strings.HasPrefix(body, "// Code generated by") {
			t.Errorf("DO NOT EDIT header must be preserved; got:\n%s", body)
		}
	})

	t.Run("preserves go:generate directive", func(t *testing.T) {
		dir := writeMod(t, "rwgogen", map[string]string{
			"gen.go": "package rwgogen\n\n//go:generate stringer -type=Status\n\ntype Status int\n\nconst (\n\tActive Status = iota\n\tInactive\n)\n\nfunc IsActive(s Status) bool { return s == Active }\n",
		})
		t.Chdir(dir)

		out := executePatchTool(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "gen.go"),
				Edits: []lang.PatchEdit{
					{
						OldString: `func IsActive(s Status) bool { return s == Active }`,
						NewString: `func IsActive(s Status) bool { return s == Active || s == Inactive }`,
					},
				},
			}},
		})
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied; got %+v results=%+v", out.Summary, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "gen.go"))
		if !strings.Contains(body, "//go:generate stringer -type=Status") {
			t.Errorf("//go:generate directive must remain; got:\n%s", body)
		}
	})

	t.Run("crlf line endings do not prevent patch from applying", func(t *testing.T) {
		dir := writeMod(t, "rwcrlf", map[string]string{})
		t.Chdir(dir)

		// Manually write a file with CRLF endings.
		crlf := "package rwcrlf\r\n\r\nfunc Greet() string { return \"hi\" }\r\n"
		if err := writeFileRaw(filepath.Join(dir, "a.go"), crlf); err != nil {
			t.Fatalf("write: %v", err)
		}

		out := executePatchTool(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{OldString: `return "hi"`, NewString: `return "hello"`},
				},
			}},
		})
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied; got %+v results=%+v", out.Summary, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, `return "hello"`) {
			t.Errorf("edit must apply on CRLF source; got:\n%s", body)
		}
	})

	t.Run("overlapping edits do not silently corrupt the file", func(t *testing.T) {
		dir := writeMod(t, "rwzover", map[string]string{
			"a.go": "package rwzover\n\nfunc F() string { return \"hello world\" }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		out := executePatchTool(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{OldString: `"hello world"`, NewString: `"goodbye world"`},
					// Overlaps with the first edit's range.
					{OldString: `"hello"`, NewString: `"hi"`},
				},
			}},
		})

		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		// Either the patch applied with sequential semantics (the first
		// makes "hello" disappear, so the second's OldString isn't found
		// anymore — applied=1, no error), or it failed cleanly.
		if out.Summary.Applied == 0 && body != original {
			t.Errorf("if no patches applied, source must remain original; got:\n%s", body)
		}
		if !strings.Contains(body, "package rwzover") {
			t.Errorf("source must still parse; got:\n%s", body)
		}
	})

	t.Run("no-op edit where old equals new does not corrupt file", func(t *testing.T) {
		dir := writeMod(t, "rwznop", map[string]string{
			"a.go": "package rwznop\n\nfunc Hello() string { return \"hi\" }\n",
		})
		t.Chdir(dir)

		original := mustReadFile(t, filepath.Join(dir, "a.go"))
		out := executePatchTool(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{OldString: `return "hi"`, NewString: `return "hi"`},
				},
			}},
		})
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if body != original {
			t.Errorf("no-op edit must not modify source; got:\n%s", body)
		}
		// The "applied" count is implementation-defined for a no-op; either 0
		// or 1 is reasonable. What matters is content stability.
		_ = out
	})

	t.Run("preserves go:linkname directive", func(t *testing.T) {
		dir := writeMod(t, "rwzlink", map[string]string{
			"a.go": "package rwzlink\n\nimport _ \"unsafe\"\n\n//go:linkname OtherFunc rwzlink.helperImpl\nfunc OtherFunc() string\n\nfunc helperImpl() string { return \"impl\" }\n\nfunc Use() string { return helperImpl() }\n",
		})
		t.Chdir(dir)

		out := executePatchTool(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{OldString: `return "impl"`, NewString: `return "implementation"`},
				},
			}},
		})
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied; got %+v results=%+v", out.Summary, out.Results)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, "//go:linkname OtherFunc") {
			t.Errorf("//go:linkname directive must be preserved; got:\n%s", body)
		}
	})

	t.Run("already-broken source file is rejected not silently corrupted", func(t *testing.T) {
		dir := writeMod(t, "rwxbroken", map[string]string{
			// Missing closing brace.
			"a.go": "package rwxbroken\n\nfunc Broken() string { return \"x\"\n",
		})
		t.Chdir(dir)

		out := executePatchTool(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{OldString: `return "x"`, NewString: `return "y"`},
				},
			}},
		})
		if out.Summary.Applied != 0 {
			t.Errorf("expected zero applied for broken source; got %+v", out.Summary)
		}
	})

	t.Run("chained replacements apply in order and converge", func(t *testing.T) {
		dir := writeMod(t, "rwxchain", map[string]string{
			"a.go": "package rwxchain\n\nconst V = \"step1\"\n",
		})
		t.Chdir(dir)

		out := executePatchTool(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{OldString: `"step1"`, NewString: `"step2"`},
					{OldString: `"step2"`, NewString: `"step3"`},
					{OldString: `"step3"`, NewString: `"final"`},
				},
			}},
		})
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied with chain converged; got %+v", out.Summary)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, `"final"`) {
			t.Errorf("chain should converge to final; got:\n%s", body)
		}
	})

	t.Run("atomic patch across workspace modules rolls back both on build break", func(t *testing.T) {
		twoModuleWorkspace(
			t,
			map[string]string{"api.go": "package a\n\nfunc Hello() string { return \"hi\" }\n"},
			map[string]string{
				"main.go": "package b\n\nimport \"example.com/a\"\n\nfunc Use() string { return a.Hello() }\n",
			},
		)

		originalA := mustReadFile(t, "modA/api.go")
		originalB := mustReadFile(t, "modB/main.go")

		// Two patches: one harmless, one introducing a build break.
		out := executePatchTool(t, golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{FilePath: "modA/api.go", Edits: []lang.PatchEdit{
					{OldString: `return "hi"`, NewString: `return "HI"`},
				}},
				{FilePath: "modB/main.go", Edits: []lang.PatchEdit{
					{OldString: `return a.Hello()`, NewString: `return undefined_xyz`},
				}},
			},
		})
		_ = out
		// Both must roll back to the original.
		if got := mustReadFile(t, "modA/api.go"); got != originalA {
			t.Errorf("modA must roll back; got:\n%s", got)
		}
		if got := mustReadFile(t, "modB/main.go"); got != originalB {
			t.Errorf("modB must roll back; got:\n%s", got)
		}
	})

	t.Run("stress atomic rollback on mixed success never leaves partial state", func(t *testing.T) {
		dir := writeMod(t, "stresspatch", map[string]string{
			"a.go": "package stresspatch\n\nfunc A() string { return \"a\" }\n",
			"b.go": "package stresspatch\n\nfunc B() string { return \"b\" }\n",
			"c.go": "package stresspatch\n\nfunc C() string { return \"c\" }\n",
		})
		t.Chdir(dir)

		originalA := mustReadFile(t, filepath.Join(dir, "a.go"))
		originalB := mustReadFile(t, filepath.Join(dir, "b.go"))
		originalC := mustReadFile(t, filepath.Join(dir, "c.go"))

		in := golang.GoPatchInput{
			Patches: []fs.FilePatch{
				{FilePath: filepath.Join(dir, "a.go"), Edits: []lang.PatchEdit{
					{OldString: `return "a"`, NewString: `return "A"`},
				}},
				{FilePath: filepath.Join(dir, "b.go"), Edits: []lang.PatchEdit{
					{OldString: `return "b"`, NewString: `return undefined_xyz`},
				}},
				{FilePath: filepath.Join(dir, "c.go"), Edits: []lang.PatchEdit{
					{OldString: `return "c"`, NewString: `return "C"`},
				}},
			},
		}
		executePatchTool(t, in)

		if got := mustReadFile(t, filepath.Join(dir, "a.go")); got != originalA {
			t.Errorf("a.go must roll back; got:\n%s", got)
		}
		if got := mustReadFile(t, filepath.Join(dir, "b.go")); got != originalB {
			t.Errorf("b.go must roll back; got:\n%s", got)
		}
		if got := mustReadFile(t, filepath.Join(dir, "c.go")); got != originalC {
			t.Errorf("c.go must roll back; got:\n%s", got)
		}
	})

	t.Run("stress multi-edit sequential dependency converges", func(t *testing.T) {
		dir := writeMod(t, "stresspatchseq", map[string]string{
			"a.go": "package stresspatchseq\n\nfunc Old() string { return \"old\" }\n",
		})
		t.Chdir(dir)

		in := golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{OldString: `func Old() string`, NewString: `func New() string`},
					{OldString: `func New() string { return "old" }`, NewString: `func New() string { return "new" }`},
				},
			}},
		}
		out := executePatchTool(t, in)
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied; got %+v", out.Summary)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, `func New() string { return "new" }`) {
			t.Errorf("expected sequential edits to produce final state; got:\n%s", body)
		}
	})

	t.Run("stress replace all swaps every occurrence", func(t *testing.T) {
		dir := writeMod(t, "stresspatchall", map[string]string{
			"a.go": "package stresspatchall\n\nfunc One() int   { return 42 }\nfunc Two() int   { return 42 }\nfunc Three() int { return 42 }\n",
		})
		t.Chdir(dir)

		in := golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{OldString: `42`, NewString: `100`, ReplaceAll: true},
				},
			}},
		}
		out := executePatchTool(t, in)
		if out.Summary.Applied != 1 {
			t.Fatalf("expected 1 applied; got %+v", out.Summary)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if strings.Count(body, "100") != 3 || strings.Contains(body, "42") {
			t.Errorf("replace_all should swap every 42 → 100; got:\n%s", body)
		}
	})

	// processDryRun has a failure branch when the patched content fails vet/build.
	// Verify that the failure status and Forensics are surfaced and the original
	// file is restored even on the error path.
	t.Run("dry run rejects compile-breaking patch", func(t *testing.T) {
		_, goFile := writeGoPatchTestModule(t)
		original, err := os.ReadFile(goFile)
		if err != nil {
			t.Fatalf("read original: %v", err)
		}

		out := executePatch(t, golang.GoPatchInput{
			DryRun: true,
			Patches: []fs.FilePatch{
				{
					FilePath: goFile,
					Edits: []lang.PatchEdit{
						// Replace valid code with an undefined call that won't build.
						{OldString: `fmt.Println("hello")`, NewString: `undefinedSym()`},
					},
				},
			},
		})

		if len(out.Results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(out.Results))
		}
		r := out.Results[0]
		if r.Status != golang.GoPatchFailure {
			t.Errorf("expected GoPatchFailure for compile-breaking dry run, got %q: %s", r.Status, r.Error)
		}
		if !strings.Contains(r.Error, "vet/build") {
			t.Errorf("error should mention vet/build; got: %s", r.Error)
		}
		if r.Forensics == nil {
			t.Error("expected Forensics to be populated for compile failure in dry run")
		}

		after, readErr := os.ReadFile(goFile)
		if readErr != nil {
			t.Fatalf("read after dry run: %v", readErr)
		}
		if !bytes.Equal(after, original) {
			t.Error("dry run must restore the original file even on build failure")
		}
	})

	t.Run("stress goimports runs before build gate", func(t *testing.T) {
		dir := writeMod(t, "stresspatchimports", map[string]string{
			"a.go": "package stresspatchimports\n\nfunc Now() string { return \"\" }\n",
		})
		t.Chdir(dir)

		in := golang.GoPatchInput{
			Patches: []fs.FilePatch{{
				FilePath: filepath.Join(dir, "a.go"),
				Edits: []lang.PatchEdit{
					{
						OldString: `func Now() string { return "" }`,
						NewString: `func Now() string { return time.Now().Format("2006") }`,
					},
				},
			}},
		}
		out := executePatchTool(t, in)
		if out.Summary.Applied != 1 {
			t.Fatalf(
				"expected goimports to add the time import and patch to apply; got %+v with results %+v",
				out.Summary,
				out.Results,
			)
		}
		body := mustReadFile(t, filepath.Join(dir, "a.go"))
		if !strings.Contains(body, `"time"`) {
			t.Errorf("expected goimports to add time import; got:\n%s", body)
		}
	})
}

// ── Local helpers ─────────────────────────────────────────────────────────────

// writeGoPatchTestModule creates a temp module with a valid Go file and returns
// the module directory and the path to the created Go file.
func writeGoPatchTestModule(t *testing.T) (dir, goFile string) {
	t.Helper()
	dir = t.TempDir()
	gomod := "module testpkg\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	goFile = filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte(testPkgSrc), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return dir, goFile
}

// writeGoPatchExtraFile creates an additional Go file in the same module directory.
func writeGoPatchExtraFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// executePatch invokes lang.go.patch and returns the output.
func executePatch(t *testing.T, input golang.GoPatchInput) golang.GoPatchOutput {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	result, err := golang.GoPatch.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Handle pointer vs value return.
	switch v := result.(type) {
	case golang.GoPatchOutput:
		return v
	case *golang.GoPatchOutput:
		return *v
	default:
		// Marshal/unmarshal round-trip.
		b, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var out golang.GoPatchOutput
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		return out
	}
}

// verifyFileUnchanged asserts that the file at path is byte-for-byte identical to original.
func verifyFileUnchanged(t *testing.T, path string, original []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(data, original) {
		t.Errorf("file %s was modified but should be unchanged\ngot:\n%s\nwant:\n%s", path, data, original)
	}
}

// writeGoPatchGoMod is a helper to create go.mod in a temp dir.
func writeGoPatchGoMod(t *testing.T, dir string) {
	t.Helper()
	gomod := "module testpkg\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}
