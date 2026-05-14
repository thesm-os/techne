// Copyright Thesmos B.V. 2026
// SPDX-License-Identifier: MIT

package golang_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.thesmos.sh/techne/internal/tool"
)

// ---- Rename ----

// ---- ChangeSignature ----

// ---- ImplementInterface ----

// ---- ExtractFunction ----

// ---- ExtractInterface ----

// ---- ExtractVariable ----

// ---- InlineConstant ----

// ---- MovePackage ----

// ---- MoveSymbol ----

// ---- ChangeType ----

// ---- shared dry-run check ----

// ---- helpers ----

// writeMod creates a temp Go module with the given files and returns the
// module root. Files are addressed by path relative to the root; intermediate
// directories are created automatically.
func writeMod(t *testing.T, modulePath string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte("module "+modulePath+"\n\ngo 1.21\n"),
		0o644,
	); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return dir
}

// executeRefactorRaw invokes a refactor tool's Execute and returns the raw
// result for callers that need to inspect the error explicitly.
func executeRefactorRaw(t *testing.T, tl tool.Tool, input any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return tl.Execute(t.Context(), raw)
}

// executeRefactor invokes a refactor tool and decodes the result into Out,
// fataling on transport errors. Callers still inspect Output.Status to
// distinguish refactor success from refactor failure (which is not a
// transport error).
func executeRefactor[Out any](t *testing.T, tl tool.Tool, input any) Out {
	t.Helper()
	result, err := executeRefactorRaw(t, tl, input)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := result.(Out)
	if ok {
		return out
	}
	// Round-trip through JSON for cases where the framework returns a
	// pointer or compatible-but-different type.
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unexpected result type %T: %v", result, err)
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func mustContain(t *testing.T, path, want string) {
	t.Helper()
	body := readFile(t, path)
	if !strings.Contains(body, want) {
		t.Errorf("file %s missing %q; got:\n%s", path, want, body)
	}
}
