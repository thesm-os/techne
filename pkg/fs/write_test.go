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

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")

	writeIn, _ := json.Marshal(map[string]any{
		"path":    path,
		"content": "line1\nline2\nline3\n",
	})
	writeOut, err := fs.Write.Execute(context.Background(), writeIn)
	if err != nil {
		t.Fatalf("fs.write: %v", err)
	}
	wo, ok := writeOut.(fs.WriteOutput)
	if !ok {
		t.Fatalf("fs.write returned unexpected type %T", writeOut)
	}
	if wo.BytesWritten == 0 {
		t.Error("fs.write: BytesWritten should be > 0")
	}
	if wo.Path != path {
		t.Errorf("fs.write: Path got %q, want %q", wo.Path, path)
	}
}

func TestWrite_Append(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "append.txt")

	writeIn, _ := json.Marshal(map[string]any{"path": path, "content": "first\n"})
	if _, err := fs.Write.Execute(context.Background(), writeIn); err != nil {
		t.Fatalf("fs.write initial: %v", err)
	}

	appendIn, _ := json.Marshal(map[string]any{"path": path, "content": "second\n", "append": true})
	if _, err := fs.Write.Execute(context.Background(), appendIn); err != nil {
		t.Fatalf("fs.write append: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "first\nsecond\n" {
		t.Errorf("fs.write append: got %q, want %q", string(data), "first\nsecond\n")
	}
}

func TestWrite_CreateDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "file.txt")

	writeIn, _ := json.Marshal(map[string]any{
		"path":        path,
		"content":     "hello",
		"create_dirs": true,
	})
	_, err := fs.Write.Execute(context.Background(), writeIn)
	if err != nil {
		t.Fatalf("fs.write (create_dirs): %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("fs.write (create_dirs): file not created: %v", err)
	}
}
