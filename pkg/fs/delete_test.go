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

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "todelete.txt")
	if err := os.WriteFile(path, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	delIn, _ := json.Marshal(map[string]any{"path": path})
	delOut, err := fs.Delete.Execute(context.Background(), delIn)
	if err != nil {
		t.Fatalf("fs.delete: %v", err)
	}
	do, ok := delOut.(fs.DeleteOutput)
	if !ok {
		t.Fatalf("fs.delete returned unexpected type %T", delOut)
	}
	if !do.Success {
		t.Error("fs.delete: Success should be true")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("fs.delete: file should not exist after deletion")
	}
}

func TestDelete_Recursive(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	delIn, _ := json.Marshal(map[string]any{"path": subdir, "recursive": true})
	delOut, err := fs.Delete.Execute(context.Background(), delIn)
	if err != nil {
		t.Fatalf("fs.delete (recursive): %v", err)
	}
	do := delOut.(fs.DeleteOutput)
	if !do.Success {
		t.Error("fs.delete (recursive): Success should be true")
	}
	if _, err := os.Stat(subdir); !os.IsNotExist(err) {
		t.Error("fs.delete (recursive): directory should not exist after deletion")
	}
}

func TestDelete_Force_NonExistent(t *testing.T) {
	delIn, _ := json.Marshal(map[string]any{"path": "/nonexistent/path/file.txt", "force": true})
	delOut, err := fs.Delete.Execute(context.Background(), delIn)
	if err != nil {
		t.Fatalf("fs.delete (force, nonexistent): %v", err)
	}
	do := delOut.(fs.DeleteOutput)
	if !do.Success {
		t.Error("fs.delete (force): Success should be true even for nonexistent path")
	}
}
