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

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("hello world\nhello go\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	replaceIn, _ := json.Marshal(map[string]any{
		"path":        path,
		"pattern":     "hello",
		"replacement": "hi",
	})
	replaceOut, err := fs.Replace.Execute(context.Background(), replaceIn)
	if err != nil {
		t.Fatalf("fs.replace: %v", err)
	}
	ro, ok := replaceOut.(fs.ReplaceOutput)
	if !ok {
		t.Fatalf("fs.replace returned unexpected type %T", replaceOut)
	}
	if ro.Replacements != 2 {
		t.Errorf("fs.replace: Replacements got %d, want 2", ro.Replacements)
	}
}

func TestReplace_DryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	original := "hello world\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	replaceIn, _ := json.Marshal(map[string]any{
		"path":        path,
		"pattern":     "hello",
		"replacement": "hi",
		"dry_run":     true,
	})
	replaceOut, err := fs.Replace.Execute(context.Background(), replaceIn)
	if err != nil {
		t.Fatalf("fs.replace (dry_run): %v", err)
	}
	ro := replaceOut.(fs.ReplaceOutput)
	if !ro.DryRun {
		t.Error("fs.replace (dry_run): DryRun should be true")
	}
	// File should be unchanged
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Errorf("fs.replace (dry_run): file modified when dry_run=true")
	}
}

func TestReplace_MaxReplacements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("a-a-a-a"), 0o644); err != nil {
		t.Fatal(err)
	}

	replaceIn, _ := json.Marshal(map[string]any{
		"path":             path,
		"pattern":          "a",
		"replacement":      "b",
		"max_replacements": 2,
	})
	replaceOut, err := fs.Replace.Execute(context.Background(), replaceIn)
	if err != nil {
		t.Fatalf("fs.replace (max_replacements): %v", err)
	}
	ro := replaceOut.(fs.ReplaceOutput)
	if ro.Replacements != 2 {
		t.Errorf("fs.replace (max_replacements): Replacements got %d, want 2", ro.Replacements)
	}
}
