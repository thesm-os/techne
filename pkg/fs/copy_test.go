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

func TestCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "dest.txt")
	if err := os.WriteFile(src, []byte("copy me"), 0o644); err != nil {
		t.Fatal(err)
	}

	copyIn, _ := json.Marshal(map[string]any{"src": src, "dst": dst})
	copyOut, err := fs.Copy.Execute(context.Background(), copyIn)
	if err != nil {
		t.Fatalf("fs.copy: %v", err)
	}
	co, ok := copyOut.(fs.CopyOutput)
	if !ok {
		t.Fatalf("fs.copy returned unexpected type %T", copyOut)
	}
	if co.BytesCopied == 0 {
		t.Error("fs.copy: BytesCopied should be > 0")
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("fs.copy: source should still exist: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("fs.copy: destination should exist: %v", err)
	}
}

func TestCopy_Recursive(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "srcdir")
	dstDir := filepath.Join(dir, "dstdir")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("file1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "file2.txt"), []byte("file2"), 0o644); err != nil {
		t.Fatal(err)
	}

	copyIn, _ := json.Marshal(map[string]any{"src": srcDir, "dst": dstDir, "recursive": true})
	_, err := fs.Copy.Execute(context.Background(), copyIn)
	if err != nil {
		t.Fatalf("fs.copy (recursive): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "file1.txt")); err != nil {
		t.Errorf("fs.copy (recursive): file1.txt should exist in destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "file2.txt")); err != nil {
		t.Errorf("fs.copy (recursive): file2.txt should exist in destination: %v", err)
	}
}

func TestCopy_NonExistentSrc(t *testing.T) {
	dir := t.TempDir()
	copyIn, _ := json.Marshal(
		map[string]any{"src": filepath.Join(dir, "nosuchfile.txt"), "dst": filepath.Join(dir, "dst.txt")},
	)
	_, err := fs.Copy.Execute(context.Background(), copyIn)
	if err == nil {
		t.Error("fs.copy: expected error for nonexistent source, got nil")
	}
}
