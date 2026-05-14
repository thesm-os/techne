// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// ---------------------------------------------------------------------------
// FakeTransaction — in-memory Transaction for unit tests
// ---------------------------------------------------------------------------

// FakeTransaction is an in-memory implementation of Transaction for unit
// tests. It records every staged change in slices so tests can assert on
// what an action did without spinning up a real Go module + build gate.
//
// Use it in package-level tests where you want to exercise an action's edit
// logic in microseconds rather than the ~200ms-3s of a real-module test.
type FakeTransaction struct {
	modRoot string
	pkgs    []*packages.Package
	loadErr error

	Changes []FakeChange
	Moves   []FakeMove
	Deletes []FakeDelete
	Skipped []FakeSkipped
	Notes   []string
}

type FakeChange struct {
	FilePath string
	Original []byte
	Modified []byte
	Message  string
}

type FakeMove struct {
	OldPath    string
	NewPath    string
	NewContent []byte
	Message    string
}

type FakeDelete struct {
	FilePath string
	Message  string
}

type FakeSkipped struct {
	FilePath string
	Message  string
}

func NewFakeTransaction(modRoot string) *FakeTransaction {
	return &FakeTransaction{modRoot: modRoot}
}

// SetPackages controls what LoadPackages returns.
func (f *FakeTransaction) SetPackages(pkgs []*packages.Package) { f.pkgs = pkgs }

// SetLoadError makes LoadPackages return err.
func (f *FakeTransaction) SetLoadError(err error) { f.loadErr = err }

func (f *FakeTransaction) AddChange(filePath string, original, modified []byte, message string) error {
	f.Changes = append(f.Changes, FakeChange{
		FilePath: filePath,
		Original: append([]byte(nil), original...),
		Modified: append([]byte(nil), modified...),
		Message:  message,
	})
	return nil
}

func (f *FakeTransaction) AddFileMove(oldPath, newPath string, newContent []byte, message string) error {
	f.Moves = append(f.Moves, FakeMove{
		OldPath:    oldPath,
		NewPath:    newPath,
		NewContent: append([]byte(nil), newContent...),
		Message:    message,
	})
	return nil
}

func (f *FakeTransaction) AddDelete(filePath, message string) error {
	f.Deletes = append(f.Deletes, FakeDelete{FilePath: filePath, Message: message})
	return nil
}

func (f *FakeTransaction) AddSkipped(filePath, message string) {
	f.Skipped = append(f.Skipped, FakeSkipped{FilePath: filePath, Message: message})
}

func (f *FakeTransaction) AddNote(message string) {
	if slices.Contains(f.Notes, message) {
		return
	}
	f.Notes = append(f.Notes, message)
}

func (f *FakeTransaction) LoadPackages(_ context.Context) ([]*packages.Package, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.pkgs, nil
}

func (f *FakeTransaction) ModRoot() string { return f.modRoot }

// Compile-time assertion: FakeTransaction satisfies Transaction.
var _ Transaction = (*FakeTransaction)(nil)

// ---------------------------------------------------------------------------
// Shared helpers local to transaction tests
// ---------------------------------------------------------------------------

const validGoFile = `package example

// Hello returns a greeting.
func Hello() string {
	return "hello"
}
`

const validGoFileModified = `package example

// Hello returns a greeting.
func Hello() string {
	return "world"
}
`

const invalidGoFile = `package example

func BrokenMissingBrace() {
`

func tempModDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	goMod := "module example.com/testmod\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newTransactionAt(dir string, dryRun bool) *WorkspaceTransaction {
	return &WorkspaceTransaction{
		modRoot:   dir,
		dryRun:    dryRun,
		snapshots: make(map[string][]byte),
		modified:  make(map[string][]byte),
		deletions: make(map[string]bool),
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFakeTransaction(t *testing.T) {
	t.Run("accumulates and deduplicates notes", func(t *testing.T) {
		f := NewFakeTransaction("/tmp/example")

		if got := f.ModRoot(); got != "/tmp/example" {
			t.Errorf("ModRoot got %q; want /tmp/example", got)
		}

		if err := f.AddChange("a.go", []byte("old"), []byte("new"), "renamed X"); err != nil {
			t.Fatalf("AddChange: %v", err)
		}
		if err := f.AddFileMove("a.go", "b.go", []byte("body"), "moved"); err != nil {
			t.Fatalf("AddFileMove: %v", err)
		}
		f.AddSkipped("c.go", "already in shape")
		f.AddNote("run go mod tidy")
		f.AddNote("run go mod tidy") // dedup

		if len(f.Changes) != 1 || f.Changes[0].FilePath != "a.go" || string(f.Changes[0].Modified) != "new" {
			t.Errorf("Changes: %+v", f.Changes)
		}
		if len(f.Moves) != 1 || f.Moves[0].NewPath != "b.go" {
			t.Errorf("Moves: %+v", f.Moves)
		}
		if len(f.Skipped) != 1 || f.Skipped[0].FilePath != "c.go" {
			t.Errorf("Skipped: %+v", f.Skipped)
		}
		if len(f.Notes) != 1 || f.Notes[0] != "run go mod tidy" {
			t.Errorf("Notes (expected dedup to 1): %+v", f.Notes)
		}
	})
}

func TestParseFirstBuildError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single error with file:line:col",
			in:   "# example.com/foo\n./bar.go:42:5: undefined: Bar\n",
			want: "./bar.go:42:5: undefined: Bar",
		},
		{
			name: "absolute path file:line",
			in:   "/abs/path/baz.go:10: syntax error: unexpected }\n",
			want: "/abs/path/baz.go:10: syntax error: unexpected }",
		},
		{
			name: "multiple errors returns first",
			in:   "# pkg\n./a.go:1:1: first error\n./b.go:2:2: second error\n",
			want: "./a.go:1:1: first error",
		},
		{
			name: "fallback when no compiler diagnostic line found",
			in:   "# pkg\n\nsome generic failure\n",
			want: "some generic failure",
		},
		{
			name: "fully empty output",
			in:   "",
			want: "",
		},
		{
			name: "skips package header line",
			in:   "# example.com/foo\n",
			want: "# example.com/foo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFirstBuildError([]byte(tc.in))
			if got != tc.want {
				t.Errorf("parseFirstBuildError(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAddChange(t *testing.T) {
	t.Run("validates and formats", func(t *testing.T) {
		dir := tempModDir(t)
		ws := newTransactionAt(dir, false)

		filePath := filepath.Join(dir, "hello.go")
		original := []byte(validGoFile)
		modified := []byte(validGoFileModified)

		if err := ws.AddChange(filePath, original, modified, "test change"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ws.modified) != 1 {
			t.Fatalf("expected 1 staged file, got %d", len(ws.modified))
		}
		if len(ws.results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(ws.results))
		}
		if ws.results[0].Status != StatusSuccess {
			t.Errorf("expected success status, got %s", ws.results[0].Status)
		}
	})

	t.Run("rejects empty content", func(t *testing.T) {
		dir := tempModDir(t)
		ws := newTransactionAt(dir, false)

		err := ws.AddChange(filepath.Join(dir, "empty.go"), nil, []byte{}, "empty")
		if err == nil {
			t.Fatal("expected error for empty content, got nil")
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("expected 'empty' in error, got: %v", err)
		}
	})

	t.Run("rejects unparseable", func(t *testing.T) {
		dir := tempModDir(t)
		ws := newTransactionAt(dir, false)

		err := ws.AddChange(filepath.Join(dir, "bad.go"), nil, []byte(invalidGoFile), "bad syntax")
		if err == nil {
			t.Fatal("expected error for broken Go, got nil")
		}
		if !strings.Contains(err.Error(), "validation failed") {
			t.Errorf("expected 'validation failed' in error, got: %v", err)
		}
	})

	t.Run("snapshots original only on first call", func(t *testing.T) {
		dir := tempModDir(t)
		ws := newTransactionAt(dir, false)

		filePath := filepath.Join(dir, "multi.go")
		original := []byte(validGoFile)
		first := []byte(validGoFileModified)
		second := []byte("package example\n\nfunc Hello() string { return \"second\" }\n")

		_ = ws.AddChange(filePath, original, first, "first")
		_ = ws.AddChange(filePath, []byte("should not overwrite snapshot"), second, "second")

		if string(ws.snapshots[filePath]) != string(original) {
			t.Error("snapshot was overwritten on second AddChange")
		}
	})
}

func TestDryRun(t *testing.T) {
	t.Run("does not write files to disk", func(t *testing.T) {
		dir := tempModDir(t)
		ws := newTransactionAt(dir, true)

		filePath := filepath.Join(dir, "dry.go")
		_ = ws.AddChange(filePath, []byte(validGoFile), []byte(validGoFileModified), "dry run change")

		out, err := ws.Commit(t.Context())
		if err != nil {
			t.Fatalf("dry run commit error: %v", err)
		}
		if _, statErr := os.Stat(filePath); !os.IsNotExist(statErr) {
			t.Error("dry run should not write files to disk")
		}
		if out.Status != StatusSuccess {
			t.Errorf("expected success, got %s", out.Status)
		}
	})
}

func TestCommit(t *testing.T) {
	t.Run("writes all staged files", func(t *testing.T) {
		dir := tempModDir(t)

		filePath := filepath.Join(dir, "hello.go")
		original := []byte(validGoFile)
		if err := os.WriteFile(filePath, original, 0o644); err != nil {
			t.Fatal(err)
		}

		ws := newTransactionAt(dir, false)
		modified := []byte(validGoFileModified)
		if err := ws.AddChange(filePath, original, modified, "commit test"); err != nil {
			t.Fatalf("AddChange: %v", err)
		}

		out, err := ws.Commit(t.Context())
		if err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if out.Status != StatusSuccess {
			t.Errorf("expected success, got %s: %+v", out.Status, out)
		}

		written, readErr := os.ReadFile(filePath)
		if readErr != nil {
			t.Fatalf("ReadFile after commit: %v", readErr)
		}
		if !strings.Contains(string(written), "world") {
			t.Errorf("expected modified content on disk, got: %s", written)
		}
	})

	t.Run("rolls back on build failure", func(t *testing.T) {
		dir := tempModDir(t)

		filePath := filepath.Join(dir, "hello.go")
		original := []byte(validGoFile)
		if err := os.WriteFile(filePath, original, 0o644); err != nil {
			t.Fatal(err)
		}

		ws := newTransactionAt(dir, false)
		broken := []byte("package example\n\nfunc Hello() string {\n\treturn undefinedIdentifier\n}\n")
		if err := ws.AddChange(filePath, original, broken, "intentionally broken"); err != nil {
			t.Fatalf("AddChange: %v", err)
		}

		_, commitErr := ws.Commit(t.Context())
		if commitErr == nil {
			t.Fatal("expected build failure error, got nil")
		}

		msg := commitErr.Error()
		if !strings.Contains(msg, "rolled back") {
			t.Errorf("error must mention rollback; got: %s", msg)
		}
		if !strings.Contains(msg, "undefined") {
			t.Errorf("error must surface the compiler diagnostic; got: %s", msg)
		}
		if lineCount := strings.Count(msg, "\n"); lineCount > 1 {
			t.Errorf("expected a single-line narrowed error, got %d newlines:\n%s", lineCount, msg)
		}

		onDisk, _ := os.ReadFile(filePath)
		if string(onDisk) != string(original) {
			t.Errorf("expected rollback to original; got: %s", onDisk)
		}
	})

	t.Run("restores go.mod on rollback", func(t *testing.T) {
		dir := tempModDir(t)

		modFile := filepath.Join(dir, "go.mod")
		originalMod, _ := os.ReadFile(modFile)

		filePath := filepath.Join(dir, "hello.go")
		original := []byte(validGoFile)
		_ = os.WriteFile(filePath, original, 0o644)

		ws := newTransactionAt(dir, false)
		broken := []byte("package example\n\nfunc Hello() string {\n\treturn undefinedIdentifier\n}\n")
		_ = ws.AddChange(filePath, original, broken, "trigger rollback")

		_, _ = ws.Commit(t.Context())

		restoredMod, _ := os.ReadFile(modFile)
		if string(restoredMod) != string(originalMod) {
			t.Error("go.mod was not restored after rollback")
		}
	})
}

func TestWorkspaceAddNote(t *testing.T) {
	t.Run("deduplicates repeated messages", func(t *testing.T) {
		dir := tempModDir(t)
		ws := newTransactionAt(dir, false)

		ws.AddNote("run go mod tidy")
		ws.AddNote("run go mod tidy") // duplicate — should be ignored
		ws.AddNote("check imports")

		if len(ws.notes) != 2 {
			t.Fatalf("expected 2 notes, got %d: %v", len(ws.notes), ws.notes)
		}
		if ws.notes[0] != "run go mod tidy" {
			t.Errorf("notes[0] = %q; want %q", ws.notes[0], "run go mod tidy")
		}
		if ws.notes[1] != "check imports" {
			t.Errorf("notes[1] = %q; want %q", ws.notes[1], "check imports")
		}
	})
}

func TestAddFileMove(t *testing.T) {
	t.Run("stages deletion of old path and creation of new path", func(t *testing.T) {
		dir := tempModDir(t)

		oldPath := filepath.Join(dir, "old.go")
		newPath := filepath.Join(dir, "new.go")
		content := []byte(validGoFile)
		if err := os.WriteFile(oldPath, content, 0o644); err != nil {
			t.Fatal(err)
		}

		ws := newTransactionAt(dir, false)
		if err := ws.AddFileMove(oldPath, newPath, content, "move file"); err != nil {
			t.Fatalf("AddFileMove: %v", err)
		}

		if !ws.deletions[oldPath] {
			t.Error("old path not staged for deletion")
		}
		if _, ok := ws.modified[newPath]; !ok {
			t.Error("new path not staged for creation")
		}
	})
}
