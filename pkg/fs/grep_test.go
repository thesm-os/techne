// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.thesmos.sh/techne/pkg/fs"
)

func TestGrep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("foo bar\nbaz foo\nqux\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	grepIn, _ := json.Marshal(map[string]any{"path": path, "pattern": "foo"})
	grepOut, err := fs.Grep.Execute(context.Background(), grepIn)
	if err != nil {
		t.Fatalf("fs.grep: %v", err)
	}
	out, ok := grepOut.(fs.GrepOutput)
	if !ok {
		t.Fatalf("fs.grep returned unexpected type %T", grepOut)
	}
	if out.Count != 2 {
		t.Errorf("fs.grep: Count got %d, want 2", out.Count)
	}
}

func TestGrep_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("Foo\nfoo\nFOO\nbar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	grepIn, _ := json.Marshal(map[string]any{"path": path, "pattern": "foo", "case_insensitive": true})
	grepOut, err := fs.Grep.Execute(context.Background(), grepIn)
	if err != nil {
		t.Fatalf("fs.grep (case_insensitive): %v", err)
	}
	out := grepOut.(fs.GrepOutput)
	if out.Count != 3 {
		t.Errorf("fs.grep (case_insensitive): Count got %d, want 3", out.Count)
	}
}

func TestGrep_NoMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	grepIn, _ := json.Marshal(map[string]any{"path": path, "pattern": "notfound"})
	grepOut, err := fs.Grep.Execute(context.Background(), grepIn)
	if err != nil {
		t.Fatalf("fs.grep (no match): %v", err)
	}
	out := grepOut.(fs.GrepOutput)
	if out.Count != 0 {
		t.Errorf("fs.grep (no match): Count got %d, want 0", out.Count)
	}
}

func TestGrep_Directory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "b.go"),
		[]byte("package main\n\nfunc helper() {}\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("func is a keyword\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	searchIn, _ := json.Marshal(map[string]any{"path": dir, "pattern": "func"})
	searchOut, err := fs.Grep.Execute(context.Background(), searchIn)
	if err != nil {
		t.Fatalf("fs.grep (directory): %v", err)
	}
	so, ok := searchOut.(fs.GrepOutput)
	if !ok {
		t.Fatalf("fs.grep (directory) returned unexpected type %T", searchOut)
	}
	if so.Count != 3 {
		t.Errorf("fs.grep (directory): Count got %d, want 3", so.Count)
	}
}

func TestGrep_DirectoryGlobFilter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("func goFunc() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("func notGo()\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	searchIn, _ := json.Marshal(map[string]any{"path": dir, "pattern": "func", "glob": "*.go"})
	searchOut, err := fs.Grep.Execute(context.Background(), searchIn)
	if err != nil {
		t.Fatalf("fs.grep (directory glob): %v", err)
	}
	so := searchOut.(fs.GrepOutput)
	if so.Count != 1 {
		t.Errorf("fs.grep (directory glob): Count got %d, want 1", so.Count)
	}
}

func TestGrep_DirectoryNoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	searchIn, _ := json.Marshal(map[string]any{"path": dir, "pattern": "notfound"})
	searchOut, err := fs.Grep.Execute(context.Background(), searchIn)
	if err != nil {
		t.Fatalf("fs.grep (directory no match): %v", err)
	}
	so := searchOut.(fs.GrepOutput)
	if so.Count != 0 {
		t.Errorf("fs.grep (directory no match): Count got %d, want 0", so.Count)
	}
}
