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

func TestList(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}

	listIn, _ := json.Marshal(map[string]any{"path": dir})
	listOut, err := fs.List.Execute(context.Background(), listIn)
	if err != nil {
		t.Fatalf("fs.list: %v", err)
	}
	lo, ok := listOut.(fs.ListOutput)
	if !ok {
		t.Fatalf("fs.list returned unexpected type %T", listOut)
	}
	if lo.Count != 2 {
		t.Errorf("fs.list: Count got %d, want 2", lo.Count)
	}
}

func TestList_Hidden(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}

	listIn, _ := json.Marshal(map[string]any{"path": dir, "hidden": true})
	listOut, err := fs.List.Execute(context.Background(), listIn)
	if err != nil {
		t.Fatalf("fs.list (hidden): %v", err)
	}
	lo := listOut.(fs.ListOutput)
	if lo.Count != 3 {
		t.Errorf("fs.list (hidden): Count got %d, want 3", lo.Count)
	}
}

func TestList_Pattern(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}

	listIn, _ := json.Marshal(map[string]any{"path": dir, "pattern": "*.go"})
	listOut, err := fs.List.Execute(context.Background(), listIn)
	if err != nil {
		t.Fatalf("fs.list (pattern): %v", err)
	}
	lo := listOut.(fs.ListOutput)
	if lo.Count != 1 {
		t.Errorf("fs.list (pattern): Count got %d, want 1", lo.Count)
	}
}

func TestList_NonExistentDir(t *testing.T) {
	listIn, _ := json.Marshal(map[string]any{"path": "/nonexistent/directory"})
	_, err := fs.List.Execute(context.Background(), listIn)
	if err == nil {
		t.Error("fs.list: expected error for nonexistent directory, got nil")
	}
}
