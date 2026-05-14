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

func TestStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	statIn, _ := json.Marshal(map[string]any{"path": path})
	statOut, err := fs.Stat.Execute(context.Background(), statIn)
	if err != nil {
		t.Fatalf("fs.stat: %v", err)
	}
	so, ok := statOut.(fs.StatOutput)
	if !ok {
		t.Fatalf("fs.stat returned unexpected type %T", statOut)
	}
	if so.Size != 5 {
		t.Errorf("fs.stat: Size got %d, want 5", so.Size)
	}
	if so.IsDir {
		t.Error("fs.stat: IsDir should be false for a file")
	}
}

func TestStat_Directory(t *testing.T) {
	dir := t.TempDir()

	statIn, _ := json.Marshal(map[string]any{"path": dir})
	statOut, err := fs.Stat.Execute(context.Background(), statIn)
	if err != nil {
		t.Fatalf("fs.stat (dir): %v", err)
	}
	so, ok := statOut.(fs.StatOutput)
	if !ok {
		t.Fatalf("fs.stat returned unexpected type %T", statOut)
	}
	if !so.IsDir {
		t.Error("fs.stat: IsDir should be true for a directory")
	}
}

func TestStat_NonExistent(t *testing.T) {
	statIn, _ := json.Marshal(map[string]any{"path": "/nonexistent/path/file.txt"})
	_, err := fs.Stat.Execute(context.Background(), statIn)
	if err == nil {
		t.Error("fs.stat: expected error for nonexistent path, got nil")
	}
}
