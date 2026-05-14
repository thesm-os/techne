// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/pkg/fs"
	"go.thesmos.sh/techne/pkg/lang"
)

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// helper: marshal input, call Patch.Execute, unmarshal output.
func callPatch(t *testing.T, input fs.PatchInput) fs.PatchOutput {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	res, err := fs.Patch.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out fs.PatchOutput
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	return out
}

// helper: write a temp file and return its path.
func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTempFile %q: %v", path, err)
	}
	return path
}

// helper: read file content.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile %q: %v", path, err)
	}
	return string(data)
}

// TestPatch_SingleFileOneEdit verifies a single file with one edit is applied and diff is generated.
func TestPatch_SingleFileOneEdit(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "a.txt", "hello world\n")

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: path, Edits: []fs.PatchEdit{{OldString: "world", NewString: "Go"}}},
		},
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusSuccess)
	}
	if len(out.Results) != 1 {
		t.Fatalf("results len = %d; want 1", len(out.Results))
	}
	if out.Results[0].Status != fs.PatchFileSuccess {
		t.Errorf("file status = %q; want %q", out.Results[0].Status, fs.PatchFileSuccess)
	}
	got := readFile(t, path)
	if got != "hello Go\n" {
		t.Errorf("file content = %q; want %q", got, "hello Go\n")
	}
	if !strings.Contains(out.Results[0].DiffReceipt, "-hello world") {
		t.Errorf("diff receipt missing removal line; got:\n%s", out.Results[0].DiffReceipt)
	}
	if !strings.Contains(out.Results[0].DiffReceipt, "+hello Go") {
		t.Errorf("diff receipt missing addition line; got:\n%s", out.Results[0].DiffReceipt)
	}
}

// TestPatch_SingleFileMultipleEdits verifies multiple edits applied sequentially.
func TestPatch_SingleFileMultipleEdits(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "b.txt", "foo bar baz\n")

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: path, Edits: []fs.PatchEdit{
				{OldString: "foo", NewString: "one"},
				{OldString: "bar", NewString: "two"},
			}},
		},
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusSuccess)
	}
	got := readFile(t, path)
	if got != "one two baz\n" {
		t.Errorf("file content = %q; want %q", got, "one two baz\n")
	}
}

// TestPatch_MultiFileAtomic verifies two files are both modified on success.
func TestPatch_MultiFileAtomic(t *testing.T) {
	dir := t.TempDir()
	pathA := writeTempFile(t, dir, "a.txt", "alpha\n")
	pathB := writeTempFile(t, dir, "b.txt", "beta\n")

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: pathA, Edits: []fs.PatchEdit{{OldString: "alpha", NewString: "ALPHA"}}},
			{FilePath: pathB, Edits: []fs.PatchEdit{{OldString: "beta", NewString: "BETA"}}},
		},
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusSuccess)
	}
	if readFile(t, pathA) != "ALPHA\n" {
		t.Errorf("pathA not updated")
	}
	if readFile(t, pathB) != "BETA\n" {
		t.Errorf("pathB not updated")
	}
}

// TestPatch_RollbackOnEditFailure verifies that when the second file has a bad edit, neither file is written.
func TestPatch_RollbackOnEditFailure(t *testing.T) {
	dir := t.TempDir()
	pathA := writeTempFile(t, dir, "a.txt", "alpha\n")
	pathB := writeTempFile(t, dir, "b.txt", "beta\n")

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: pathA, Edits: []fs.PatchEdit{{OldString: "alpha", NewString: "ALPHA"}}},
			{FilePath: pathB, Edits: []fs.PatchEdit{{OldString: "NOTHERE", NewString: "X"}}},
		},
	})

	if out.Status != fs.PatchStatusPartialFailure {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusPartialFailure)
	}
	// Neither file should be modified.
	if readFile(t, pathA) != "alpha\n" {
		t.Errorf("pathA was modified despite failure")
	}
	if readFile(t, pathB) != "beta\n" {
		t.Errorf("pathB was modified despite failure")
	}
	// The failing result should have error info.
	found := false
	for _, r := range out.Results {
		if r.Status == fs.PatchFileFailure {
			found = true
			if r.Error == "" {
				t.Errorf("failure result has no error message")
			}
		}
	}
	if !found {
		t.Errorf("no failure result in output")
	}
}

// TestPatch_CreateFiles verifies new files are created with correct content and permissions.
func TestPatch_CreateFiles(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "sub", "new.txt")

	out := callPatch(t, fs.PatchInput{
		CreateFiles: []fs.CreateFile{
			{Path: newPath, Content: "brand new\n", Mode: "0644"},
		},
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusSuccess)
	}
	got := readFile(t, newPath)
	if got != "brand new\n" {
		t.Errorf("new file content = %q; want %q", got, "brand new\n")
	}
	info, err := os.Stat(newPath)
	if err != nil {
		t.Fatalf("stat new file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o; want 0644", info.Mode().Perm())
	}
	// Check result entry.
	found := false
	for _, r := range out.Results {
		if r.FilePath == newPath && r.Status == fs.PatchFileCreated {
			found = true
		}
	}
	if !found {
		t.Errorf("no created result for %q", newPath)
	}
}

// TestPatch_DeleteFiles verifies files are deleted.
func TestPatch_DeleteFiles(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "del.txt", "bye\n")

	out := callPatch(t, fs.PatchInput{
		DeleteFiles: []string{path},
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusSuccess)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after delete")
	}
	found := false
	for _, r := range out.Results {
		if r.FilePath == path && r.Status == fs.PatchFileDeleted {
			found = true
		}
	}
	if !found {
		t.Errorf("no deleted result for %q", path)
	}
}

// TestPatch_DryRun verifies edits are validated and diff generated but files unchanged.
func TestPatch_DryRun(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "dry.txt", "original content\n")

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: path, Edits: []fs.PatchEdit{{OldString: "original", NewString: "replaced"}}},
		},
		DryRun: true,
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusSuccess)
	}
	// File must be unchanged.
	if readFile(t, path) != "original content\n" {
		t.Errorf("file was modified during dry run")
	}
	// Diff receipt must be populated.
	if len(out.Results) == 0 || out.Results[0].DiffReceipt == "" {
		t.Errorf("dry run produced no diff receipt")
	}
}

// TestPatch_FormatCommand verifies FormatCommand is run on modified files.
func TestPatch_FormatCommand(t *testing.T) {
	// Locate gofmt.
	gofmt, err := exec.LookPath("gofmt")
	if err != nil {
		t.Skip("gofmt not available, skipping format test")
	}

	dir := t.TempDir()
	// Write a Go file with bad formatting.
	ugly := `package main
func main() {x:=1;_=x}
`
	path := writeTempFile(t, dir, "ugly.go", ugly)

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			// No-op edit to include the file in modified set.
			{FilePath: path, Edits: []fs.PatchEdit{{OldString: "x:=1", NewString: "x := 1"}}},
		},
		FormatCommand: gofmt + " -w",
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusSuccess)
	}
	formatted := readFile(t, path)
	// gofmt should add proper whitespace.
	if strings.Contains(formatted, "x:=1") {
		t.Errorf("file not formatted; still contains unformatted code")
	}
}

// TestPatch_VerifyCommandSucceeds verifies that a passing verify command keeps changes.
func TestPatch_VerifyCommandSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "v.txt", "before\n")

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: path, Edits: []fs.PatchEdit{{OldString: "before", NewString: "after"}}},
		},
		VerifyCommand: "true",
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusSuccess)
	}
	if readFile(t, path) != "after\n" {
		t.Errorf("file not updated despite verify passing")
	}
	if out.VerifyResult == nil {
		t.Fatalf("verify_result is nil")
	}
	if !out.VerifyResult.Passed {
		t.Errorf("verify_result.passed = false; want true")
	}
	if out.VerifyResult.ExitCode != 0 {
		t.Errorf("verify_result.exit_code = %d; want 0", out.VerifyResult.ExitCode)
	}
}

// TestPatch_VerifyCommandFails verifies rollback on verify failure.
func TestPatch_VerifyCommandFails(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "v.txt", "original\n")

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: path, Edits: []fs.PatchEdit{{OldString: "original", NewString: "changed"}}},
		},
		VerifyCommand: "false",
	})

	if out.Status != fs.PatchStatusRolledBack {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusRolledBack)
	}
	// File must be restored.
	if readFile(t, path) != "original\n" {
		t.Errorf("file not rolled back; contains %q", readFile(t, path))
	}
	if out.VerifyResult == nil {
		t.Fatalf("verify_result is nil")
	}
	if out.VerifyResult.Passed {
		t.Errorf("verify_result.passed = true; want false")
	}
	if out.VerifyResult.ExitCode == 0 {
		t.Errorf("verify_result.exit_code = 0; want non-zero")
	}
}

// TestPatch_NextActionsSuccess verifies NextActions populated on success.
func TestPatch_NextActionsSuccess(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "na.txt", "hello\n")

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: path, Edits: []fs.PatchEdit{{OldString: "hello", NewString: "world"}}},
		},
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusSuccess)
	}
	if len(out.NextActions) == 0 {
		t.Fatalf("next_actions is empty on success")
	}
	found := false
	for _, na := range out.NextActions {
		if na.Tool == "lang.go.verify" && na.Confidence == lang.ConfidenceHigh {
			found = true
		}
	}
	if !found {
		t.Errorf("expected lang.go.verify next action with high confidence; got: %+v", out.NextActions)
	}
}

// TestPatch_NextActionsRolledBack verifies NextActions populated on rollback.
func TestPatch_NextActionsRolledBack(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "rb.txt", "hello\n")

	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: path, Edits: []fs.PatchEdit{{OldString: "hello", NewString: "world"}}},
		},
		VerifyCommand: "false",
	})

	if out.Status != fs.PatchStatusRolledBack {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusRolledBack)
	}
	if len(out.NextActions) == 0 {
		t.Fatalf("next_actions is empty on rollback")
	}
	found := false
	for _, na := range out.NextActions {
		if na.Tool == "lang.go.explore" && na.Confidence == lang.ConfidenceLow {
			found = true
		}
	}
	if !found {
		t.Errorf("expected lang.go.explore next action with low confidence; got: %+v", out.NextActions)
	}
}

// TestPatch_OldStringNotFound verifies a descriptive error when old_string is missing.
func TestPatch_OldStringNotFound(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "nf.txt", "actual content\n")

	longSearch := "this string is definitely not in the file and is longer than fifty characters total"
	out := callPatch(t, fs.PatchInput{
		Patches: []fs.FilePatch{
			{FilePath: path, Edits: []fs.PatchEdit{{OldString: longSearch, NewString: "x"}}},
		},
	})

	if out.Status != fs.PatchStatusPartialFailure {
		t.Errorf("status = %q; want %q", out.Status, fs.PatchStatusPartialFailure)
	}
	if len(out.Results) == 0 {
		t.Fatalf("no results")
	}
	r := out.Results[0]
	if r.Status != fs.PatchFileFailure {
		t.Errorf("file status = %q; want %q", r.Status, fs.PatchFileFailure)
	}
	if r.Error == "" {
		t.Errorf("error message is empty")
	}
	// Error should contain snippet (first 50 chars).
	snippet := longSearch[:50]
	if !strings.Contains(r.Error, snippet) {
		t.Errorf("error %q does not contain snippet %q", r.Error, snippet)
	}
}

func TestPatch_PatternEdit_BulkReplace(t *testing.T) {
	dir := t.TempDir()

	// Create 3 test files with the same pattern to fix.
	for _, name := range []string{"a_test.go", "b_test.go", "c_test.go"} {
		content := `package foo

func TestSomething(t *testing.T) {
	result, _ := NewLedger(nil)
	_ = result
}

func TestOther(t *testing.T) {
	val, _ := NewLedger("arg")
	_ = val
}
`
		mustWriteFile(t, filepath.Join(dir, name), content)
	}

	out := callPatch(t, fs.PatchInput{
		PatternEdits: []fs.PatternEdit{
			{
				FileGlob:    filepath.Join(dir, "*_test.go"),
				OldRegex:    `(\w+), _ := NewLedger\((.+?)\)`,
				NewTemplate: `$1, err := NewLedger($2)\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}`,
			},
		},
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Fatalf("expected success, got %s", out.Status)
	}

	// Each file should have 2 replacements (2 calls to NewLedger per file).
	// 3 files × 1 FilePatch per file = 3 results.
	replacedFiles := 0
	for _, r := range out.Results {
		if r.Status == fs.PatchFileSuccess && r.DiffReceipt != "" {
			replacedFiles++
		}
	}
	if replacedFiles != 3 {
		t.Errorf("expected 3 files modified, got %d", replacedFiles)
	}

	// Verify one file's content was actually changed.
	data, err := os.ReadFile(filepath.Join(dir, "a_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, ", _ := NewLedger(") {
		t.Error("pattern should have been replaced but old pattern still found")
	}
	if !strings.Contains(content, "t.Fatal(err)") {
		t.Error("replacement text not found in output")
	}
}

func TestPatch_PatternEdit_MaxReplacements(t *testing.T) {
	dir := t.TempDir()

	content := `package foo

func a() { x, _ := Do(1) }
func b() { y, _ := Do(2) }
func c() { z, _ := Do(3) }
`
	mustWriteFile(t, filepath.Join(dir, "test.go"), content)

	out := callPatch(t, fs.PatchInput{
		PatternEdits: []fs.PatternEdit{
			{
				FileGlob:        filepath.Join(dir, "*.go"),
				OldRegex:        `(\w+), _ := Do\((\d+)\)`,
				NewTemplate:     `$1, err := Do($2)`,
				MaxReplacements: 1,
			},
		},
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Fatalf("expected success, got %s", out.Status)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "test.go"))
	replaced := strings.Count(string(data), ", err := Do(")
	unreplaced := strings.Count(string(data), ", _ := Do(")
	if replaced != 1 {
		t.Errorf("expected 1 replacement, got %d", replaced)
	}
	if unreplaced != 2 {
		t.Errorf("expected 2 unreplaced, got %d", unreplaced)
	}
}

func TestPatch_PatternEdit_DryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	mustWriteFile(t, path, `result, _ := Do(1)`)

	out := callPatch(t, fs.PatchInput{
		PatternEdits: []fs.PatternEdit{
			{
				FileGlob:    filepath.Join(dir, "*.go"),
				OldRegex:    `(\w+), _ := Do\((\d+)\)`,
				NewTemplate: `$1, err := Do($2)`,
			},
		},
		DryRun: true,
	})

	if out.Status != fs.PatchStatusSuccess {
		t.Fatalf("expected success, got %s", out.Status)
	}

	// File should be unchanged on disk.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), ", _ := Do(") {
		t.Error("dry run should not modify files")
	}
}

func TestPatch_PatternEdit_InvalidRegex(t *testing.T) {
	out := callPatch(t, fs.PatchInput{
		PatternEdits: []fs.PatternEdit{
			{
				FileGlob:    "*.go",
				OldRegex:    "[invalid",
				NewTemplate: "x",
			},
		},
	})

	if out.Status != fs.PatchStatusPartialFailure {
		t.Errorf("expected partial_failure for invalid regex, got %s", out.Status)
	}
}
