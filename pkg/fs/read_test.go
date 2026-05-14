// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package fs_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"go.thesmos.sh/techne/pkg/fs"
)

func TestRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")

	writeIn, _ := json.Marshal(map[string]any{
		"path":    path,
		"content": "line1\nline2\nline3\n",
	})
	if _, err := fs.Write.Execute(context.Background(), writeIn); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	// Read all lines
	readIn, _ := json.Marshal(map[string]any{"path": path})
	readOut, err := fs.Read.Execute(context.Background(), readIn)
	if err != nil {
		t.Fatalf("fs.read: %v", err)
	}
	ro, ok := readOut.(fs.ReadOutput)
	if !ok {
		t.Fatalf("fs.read returned unexpected type %T", readOut)
	}
	if ro.TotalLines != 3 {
		t.Errorf("fs.read: TotalLines got %d, want 3", ro.TotalLines)
	}
	if ro.LinesRead != 3 {
		t.Errorf("fs.read: LinesRead got %d, want 3", ro.LinesRead)
	}
}

func TestRead_OffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")

	writeIn, _ := json.Marshal(map[string]any{
		"path":    path,
		"content": "line1\nline2\nline3\n",
	})
	if _, err := fs.Write.Execute(context.Background(), writeIn); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	readIn, _ := json.Marshal(map[string]any{"path": path, "offset": 1, "limit": 1, "line_numbers": true})
	readOut, err := fs.Read.Execute(context.Background(), readIn)
	if err != nil {
		t.Fatalf("fs.read (offset+limit): %v", err)
	}
	ro, ok := readOut.(fs.ReadOutput)
	if !ok {
		t.Fatalf("fs.read returned unexpected type %T", readOut)
	}
	if ro.LinesRead != 1 {
		t.Errorf("fs.read: LinesRead got %d, want 1", ro.LinesRead)
	}
}

func TestRead_NonExistentFile(t *testing.T) {
	readIn, _ := json.Marshal(map[string]any{"path": "/nonexistent/path/file.txt"})
	_, err := fs.Read.Execute(context.Background(), readIn)
	if err == nil {
		t.Error("fs.read: expected error for nonexistent file, got nil")
	}
}
