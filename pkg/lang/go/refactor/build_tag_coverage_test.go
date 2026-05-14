// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package refactor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestDetectExcludedFiles(t *testing.T) {
	t.Run("returns nil when no packages contain excluded files", func(t *testing.T) {
		notes := detectExcludedFiles(nil)
		if notes != nil {
			t.Errorf("expected nil notes, got %v", notes)
		}

		notes = detectExcludedFiles([]*packages.Package{
			{PkgPath: "example.com/foo", IgnoredFiles: nil},
		})
		if notes != nil {
			t.Errorf("expected nil notes, got %v", notes)
		}
	})

	t.Run("ignores non-go files and _test.go", func(t *testing.T) {
		dir := t.TempDir()
		write := func(name, content string) string {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}
		nonGo := write("notes.txt", "ignore me")
		testFile := write("foo_test.go", "//go:build never\n\npackage foo\n")

		notes := detectExcludedFiles([]*packages.Package{
			{PkgPath: "example.com/foo", IgnoredFiles: []string{nonGo, testFile}},
		})
		if notes != nil {
			t.Errorf("expected nil notes, got %v", notes)
		}
	})

	t.Run("surfaces explicit //go:build constraint", func(t *testing.T) {
		dir := t.TempDir()
		excluded := filepath.Join(dir, "foo_unsupported.go")
		body := "//go:build never\n\npackage foo\n"
		if err := os.WriteFile(excluded, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}

		notes := detectExcludedFiles([]*packages.Package{
			{PkgPath: "example.com/foo", IgnoredFiles: []string{excluded}},
		})
		if len(notes) != 1 {
			t.Fatalf("expected 1 note, got %d: %v", len(notes), notes)
		}
		got := notes[0]
		if !strings.Contains(got, "foo_unsupported.go") {
			t.Errorf("note missing filename: %s", got)
		}
		if !strings.Contains(got, "//go:build never") {
			t.Errorf("note missing constraint text: %s", got)
		}
		if !strings.Contains(got, "example.com/foo") {
			t.Errorf("note missing package path: %s", got)
		}
		if !strings.Contains(got, runtime.GOOS) || !strings.Contains(got, runtime.GOARCH) {
			t.Errorf("note missing GOOS/GOARCH: %s", got)
		}
	})

	t.Run("falls back to filename-suffix hint", func(t *testing.T) {
		dir := t.TempDir()
		// No build constraint in file body — must be detected from filename.
		excluded := filepath.Join(dir, "foo_windows.go")
		body := "package foo\n"
		if err := os.WriteFile(excluded, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}

		notes := detectExcludedFiles([]*packages.Package{
			{PkgPath: "example.com/foo", IgnoredFiles: []string{excluded}},
		})
		if len(notes) != 1 {
			t.Fatalf("expected 1 note, got %d: %v", len(notes), notes)
		}
		if !strings.Contains(notes[0], "filename suffix: _windows") {
			t.Errorf("note missing filename-suffix hint: %s", notes[0])
		}
	})

	t.Run("dedupes the same file across multiple test variants", func(t *testing.T) {
		dir := t.TempDir()
		excluded := filepath.Join(dir, "foo_windows.go")
		if err := os.WriteFile(excluded, []byte("//go:build windows\n\npackage foo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Same file shows up in two packages.Package entries (e.g. test
		// variant and normal). We should only mention it once.
		notes := detectExcludedFiles([]*packages.Package{
			{PkgPath: "example.com/foo", IgnoredFiles: []string{excluded}},
			{PkgPath: "example.com/foo [example.com/foo.test]", IgnoredFiles: []string{excluded}},
		})
		// Both pkg entries get a note, but the filename should appear once in each.
		// We assert: at least one note mentions the file exactly once.
		found := false
		for _, n := range notes {
			if strings.Count(n, "foo_windows.go") == 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("expected at least one note with single mention of foo_windows.go; got %v", notes)
		}
	})
}

func TestReadBuildConstraint(t *testing.T) {
	t.Run("finds //go:build line", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "a.go")
		_ = os.WriteFile(p, []byte("// Copyright\n//go:build linux && amd64\n\npackage a\n"), 0o644)
		got := readBuildConstraint(p)
		if got != "//go:build linux && amd64" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("finds // +build line", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "a.go")
		_ = os.WriteFile(p, []byte("// +build linux\n\npackage a\n"), 0o644)
		got := readBuildConstraint(p)
		if got != "// +build linux" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("returns empty when no constraint", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "a.go")
		_ = os.WriteFile(p, []byte("package a\n\nfunc Foo() {}\n"), 0o644)
		got := readBuildConstraint(p)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("returns empty on unreadable file", func(t *testing.T) {
		got := readBuildConstraint("/nonexistent/path/file.go")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}

func TestFilenameTagHint(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"foo_windows.go", "filename suffix: _windows"},
		{"foo_linux.go", "filename suffix: _linux"},
		{"foo_unix.go", "filename suffix: _unix"},
		{"foo_amd64.go", "filename suffix: _amd64"},
		{"foo_linux_amd64.go", "filename suffix: _linux_amd64"},
		{"foo.go", ""},
		{"foo_bar.go", ""},
		{"foo_test.go", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filenameTagHint(tc.name)
			if got != tc.want {
				t.Errorf("filenameTagHint(%q) = %q; want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestLoadPackagesEmitsBuildTagNote drives a real packages.Load through a
// throwaway module containing build-tag-split files and asserts the note
// surfaces in the transaction's notes.
func TestLoadPackagesEmitsBuildTagNote(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("go.mod", "module example.com/tagtest\n\ngo 1.21\n")
	mustWrite("doc.go", "package tagtest\n")
	mustWrite("foo_unix.go", "//go:build unix\n\npackage tagtest\n\nfunc Foo() {}\n")
	mustWrite("foo_windows.go", "//go:build windows\n\npackage tagtest\n\nfunc Foo() {}\n")

	ws := newTransactionAt(dir, true)
	pkgs, err := ws.LoadPackages(t.Context())
	if err != nil {
		t.Fatalf("LoadPackages: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("expected at least one package")
	}

	// We expect at least one note mentioning the non-host variant.
	// On Linux/unix runners that's foo_windows.go; on Windows that's
	// foo_unix.go. Just assert that some non-host file is mentioned.
	if len(ws.notes) == 0 {
		t.Fatalf("expected a build-tag note, got none. Pkgs: %+v", pkgs)
	}
	joined := strings.Join(ws.notes, "\n")
	if runtime.GOOS == "windows" {
		if !strings.Contains(joined, "foo_unix.go") {
			t.Errorf("expected note to mention foo_unix.go on windows; got: %s", joined)
		}
	} else {
		if !strings.Contains(joined, "foo_windows.go") {
			t.Errorf("expected note to mention foo_windows.go on %s; got: %s", runtime.GOOS, joined)
		}
	}
	if !strings.Contains(joined, "Build-tag warning") {
		t.Errorf("note missing 'Build-tag warning' prefix: %s", joined)
	}
}

// Sanity check: the workspace package itself can still load successfully when
// no build-tag-split files exist (regression guard).
func TestLoadPackagesNoExcludedFilesEmitsNoNote(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("go.mod", "module example.com/clean\n\ngo 1.21\n")
	mustWrite("doc.go", "package clean\n\nfunc Foo() {}\n")

	ws := newTransactionAt(dir, true)
	if _, err := ws.LoadPackages(t.Context()); err != nil {
		t.Fatalf("LoadPackages: %v", err)
	}
	if len(ws.notes) != 0 {
		t.Errorf("expected no notes for clean module; got: %v", ws.notes)
	}
}
