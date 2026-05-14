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

func TestFind(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "util.go"), []byte("package sub"), 0o644); err != nil {
		t.Fatal(err)
	}

	findIn, _ := json.Marshal(map[string]any{"root": dir, "pattern": "*.go"})
	findOut, err := fs.Find.Execute(context.Background(), findIn)
	if err != nil {
		t.Fatalf("fs.find: %v", err)
	}
	fo, ok := findOut.(fs.FindOutput)
	if !ok {
		t.Fatalf("fs.find returned unexpected type %T", findOut)
	}
	if fo.Count != 2 {
		t.Errorf("fs.find: Count got %d, want 2", fo.Count)
	}
}

func TestFind_TypeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	findIn, _ := json.Marshal(map[string]any{"root": dir, "pattern": "*", "type": "file"})
	findOut, err := fs.Find.Execute(context.Background(), findIn)
	if err != nil {
		t.Fatalf("fs.find (type=file): %v", err)
	}
	fo := findOut.(fs.FindOutput)
	if fo.Count != 1 {
		t.Errorf("fs.find (type=file): Count got %d, want 1", fo.Count)
	}
}

func TestFind_MaxDepth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "top.go"), []byte("top"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "deep.go"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}

	// max_depth=0 restricts to the root level only (depth 0 = direct children of root)
	findIn, _ := json.Marshal(map[string]any{"root": dir, "pattern": "*.go", "max_depth": 0})
	findOut, err := fs.Find.Execute(context.Background(), findIn)
	if err != nil {
		t.Fatalf("fs.find (max_depth=0): %v", err)
	}
	fo := findOut.(fs.FindOutput)
	// max_depth=0 means unlimited (disabled), so both files are found
	if fo.Count != 2 {
		t.Errorf("fs.find (max_depth=0/unlimited): Count got %d, want 2", fo.Count)
	}

	// max_depth=1 allows depth 0 (top.go) and depth 1 (sub/deep.go), so both match
	findIn2, _ := json.Marshal(map[string]any{"root": dir, "pattern": "*.go", "max_depth": 1})
	findOut2, err := fs.Find.Execute(context.Background(), findIn2)
	if err != nil {
		t.Fatalf("fs.find (max_depth=1): %v", err)
	}
	fo2 := findOut2.(fs.FindOutput)
	if fo2.Count != 2 {
		t.Errorf("fs.find (max_depth=1): Count got %d, want 2", fo2.Count)
	}
}

func TestFind_TypeDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir2"), 0o755); err != nil {
		t.Fatal(err)
	}

	findIn, _ := json.Marshal(map[string]any{"root": dir, "pattern": "*", "type": "dir"})
	findOut, err := fs.Find.Execute(context.Background(), findIn)
	if err != nil {
		t.Fatalf("fs.find (type=dir): %v", err)
	}
	fo := findOut.(fs.FindOutput)
	if fo.Count != 2 {
		t.Errorf("fs.find (type=dir): Count got %d, want 2", fo.Count)
	}
}

func TestFind_MaxResults(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		name := "file" + string(rune('0'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	findIn, _ := json.Marshal(map[string]any{"root": dir, "pattern": "*.txt", "max_results": 3})
	findOut, err := fs.Find.Execute(context.Background(), findIn)
	if err != nil {
		t.Fatalf("fs.find (max_results): %v", err)
	}
	fo := findOut.(fs.FindOutput)
	if fo.Count != 3 {
		t.Errorf("fs.find (max_results): Count got %d, want 3", fo.Count)
	}
}
