// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"os"
	"path/filepath"
	"testing"

	"go.thesmos.sh/techne/pkg/lang"
	golang "go.thesmos.sh/techne/pkg/lang/go"
	"go.thesmos.sh/techne/pkg/lang/go/refactor"
)

func TestDeleteFile(t *testing.T) {
	t.Run("removes unreferenced file and workspace still builds", func(t *testing.T) {
		dir := writeMod(t, "deleteok", map[string]string{
			"a.go": "package deleteok\n\nfunc Used() string { return \"a\" }\n",
			"b.go": "package deleteok\n\nfunc Unused() string { return \"b\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.DeleteFile, lang.DeleteFileInput{
			File: filepath.Join(dir, "b.go"),
		})
		if out.Status != refactor.StatusSuccess {
			t.Fatalf("expected success; got %s: %+v", out.Status, out.Results)
		}
		if _, err := os.Stat(filepath.Join(dir, "b.go")); err == nil {
			t.Errorf("b.go must be removed")
		}
		if _, err := os.Stat(filepath.Join(dir, "a.go")); err != nil {
			t.Errorf("a.go must remain: %v", err)
		}
	})

	t.Run("rolls back when file symbols are still referenced", func(t *testing.T) {
		dir := writeMod(t, "deletefail", map[string]string{
			"a.go": "package deletefail\n\nfunc StillUsed() string { return \"a\" }\n",
			"b.go": "package deletefail\n\nfunc Caller() string { return StillUsed() }\n",
		})
		t.Chdir(dir)

		_, err := executeRefactorRaw(t, golang.DeleteFile, lang.DeleteFileInput{
			File: filepath.Join(dir, "a.go"),
		})
		if err == nil {
			t.Fatal("expected build-gate rollback error; got nil")
		}
		if _, err := os.Stat(filepath.Join(dir, "a.go")); err != nil {
			t.Errorf("a.go must be restored after rollback; got %v", err)
		}
	})

	t.Run("rejects missing path with clean error", func(t *testing.T) {
		dir := writeMod(t, "deletemissing", map[string]string{
			"a.go": "package deletemissing\n\nfunc A() {}\n",
		})
		t.Chdir(dir)

		_, err := executeRefactorRaw(t, golang.DeleteFile, lang.DeleteFileInput{
			File: filepath.Join(dir, "does_not_exist.go"),
		})
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("rejects non-Go file to prevent misuse as generic remover", func(t *testing.T) {
		dir := writeMod(t, "deletenotgo", map[string]string{
			"a.go":  "package deletenotgo\n\nfunc A() {}\n",
			"x.txt": "not go\n",
		})
		t.Chdir(dir)

		_, err := executeRefactorRaw(t, golang.DeleteFile, lang.DeleteFileInput{
			File: filepath.Join(dir, "x.txt"),
		})
		if err == nil {
			t.Fatal("expected error for non-Go file")
		}
	})
}
