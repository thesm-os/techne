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

	// Regression for B1: dry-run on a referenced deletion used to
	// return `build_status: pass, status: success` because the dry-run
	// path in WorkspaceTransaction.Commit short-circuited before the
	// build gate and buildOutput hardcoded "pass". Honest dry-run
	// simulates the deletion via `go build -overlay` and must report
	// the build failure that the equivalent real run would hit.
	t.Run("dry-run on referenced deletion reports honest build failure", func(t *testing.T) {
		dir := writeMod(t, "deletedryref", map[string]string{
			"a.go": "package deletedryref\n\nfunc StillUsed() string { return \"a\" }\n",
			"b.go": "package deletedryref\n\nfunc Caller() string { return StillUsed() }\n",
		})
		t.Chdir(dir)

		out, err := executeRefactorRaw(t, golang.DeleteFile, lang.DeleteFileInput{
			File:   filepath.Join(dir, "a.go"),
			DryRun: true,
		})
		// The dry-run must surface the build failure — either as an
		// error or as a refactor.Output with Status=Failure and
		// BuildStatus=fail. The pre-B1 behavior returned (out, nil)
		// with BuildStatus="pass", which is the contract violation.
		ro, _ := out.(refactor.Output)
		if err == nil && ro.BuildStatus == "pass" {
			t.Fatalf(
				"dry-run lied: deleting a.go would break b.go's reference to StillUsed, but "+
					"got Status=%s BuildStatus=%s (err=nil). The dry-run contract requires that "+
					"`build_status: pass` implies the real run is guaranteed to compile.",
				ro.Status, ro.BuildStatus,
			)
		}
		// a.go must still exist on disk — dry-run never writes.
		if _, statErr := os.Stat(filepath.Join(dir, "a.go")); statErr != nil {
			t.Errorf("a.go must remain untouched on dry-run; got %v", statErr)
		}
	})

	// Positive companion: dry-run on a safe deletion must keep reporting
	// success — the honest gate doesn't false-positive on benign cases.
	t.Run("dry-run on unreferenced deletion reports honest pass", func(t *testing.T) {
		dir := writeMod(t, "deletedrysafe", map[string]string{
			"a.go": "package deletedrysafe\n\nfunc Used() string { return \"a\" }\n",
			"b.go": "package deletedrysafe\n\nfunc Unused() string { return \"b\" }\n",
		})
		t.Chdir(dir)

		out := executeRefactor[refactor.Output](t, golang.DeleteFile, lang.DeleteFileInput{
			File:   filepath.Join(dir, "b.go"),
			DryRun: true,
		})
		if out.Status != refactor.StatusSuccess {
			t.Errorf("expected success on safe dry-run; got %s: %+v", out.Status, out.Results)
		}
		if out.BuildStatus != "pass" {
			t.Errorf("expected BuildStatus=pass on safe dry-run; got %q", out.BuildStatus)
		}
		// b.go must still exist — dry-run never writes.
		if _, err := os.Stat(filepath.Join(dir, "b.go")); err != nil {
			t.Errorf("b.go must remain untouched on dry-run; got %v", err)
		}
	})
}
