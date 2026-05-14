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

func TestMove(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "dest.txt")
	if err := os.WriteFile(src, []byte("move me"), 0o644); err != nil {
		t.Fatal(err)
	}

	moveIn, _ := json.Marshal(map[string]any{"src": src, "dst": dst})
	moveOut, err := fs.Move.Execute(context.Background(), moveIn)
	if err != nil {
		t.Fatalf("fs.move: %v", err)
	}
	mo, ok := moveOut.(fs.MoveOutput)
	if !ok {
		t.Fatalf("fs.move returned unexpected type %T", moveOut)
	}
	if !mo.Success {
		t.Error("fs.move: Success should be true")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("fs.move: source file should not exist after move")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("fs.move: destination file should exist: %v", err)
	}
}

func TestMove_ForceOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "dest.txt")
	if err := os.WriteFile(src, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	moveIn, _ := json.Marshal(map[string]any{"src": src, "dst": dst, "force": true})
	moveOut, err := fs.Move.Execute(context.Background(), moveIn)
	if err != nil {
		t.Fatalf("fs.move (force): %v", err)
	}
	mo := moveOut.(fs.MoveOutput)
	if !mo.Success {
		t.Error("fs.move (force): Success should be true")
	}
}

func TestMove_NonExistentSrc(t *testing.T) {
	dir := t.TempDir()
	moveIn, _ := json.Marshal(
		map[string]any{"src": filepath.Join(dir, "nosuchfile.txt"), "dst": filepath.Join(dir, "dst.txt")},
	)
	_, err := fs.Move.Execute(context.Background(), moveIn)
	if err == nil {
		t.Error("fs.move: expected error for nonexistent source, got nil")
	}
}
